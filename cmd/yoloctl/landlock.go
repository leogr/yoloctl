package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"syscall"
	"unicode"

	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// landlockState is the box show verdict about Landlock for a confined box.
// yoloctl cannot inspect a workload process's Landlock domain from outside
// it, so the verdict combines two external signals: the space VM kernel's
// documented ABI query and the box's retained openshell-sandbox log. Neither
// proves that a particular workload process is confined, so the healthy
// verdict is "available", never "active" or "enforced".
type landlockState struct {
	// KernelSupported reports that the VM kernel answered the Landlock ABI
	// query with a version.
	KernelSupported bool
	// ABI is the highest Landlock ABI version the VM kernel reports.
	ABI int
	// ProbeError explains an inconclusive verdict.
	ProbeError string
	// LogHint is a sanitized openshell-sandbox log line: the first reported
	// Landlock failure when the retained log has one, otherwise the first
	// line mentioning Landlock.
	LogHint string
	// Summary is the verdict: "available", "kernel-unsupported",
	// "kernel-disabled", "sandbox-reported-failure", or "unknown".
	Summary string
}

// Kernel answers to the documented ABI query
// landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION); see
// https://docs.kernel.org/userspace-api/landlock.html#landlock-abi-versions.
const (
	landlockKernelAvailable   = "available"
	landlockKernelUnsupported = "unsupported" // ENOSYS: not built into the kernel
	landlockKernelDisabled    = "disabled"    // EOPNOTSUPP: built in, disabled at boot
	landlockKernelUnknown     = "unknown"
)

type landlockKernelProbe struct {
	Status string
	ABI    int
	Err    error
}

// classifyLandlockABI maps the raw ABI query result. Only ENOSYS and
// EOPNOTSUPP demonstrate a kernel without usable Landlock; any other errno
// or an impossible version leaves the kernel state unknown.
func classifyLandlockABI(ret uintptr, errno syscall.Errno) landlockKernelProbe {
	switch {
	case errno == syscall.ENOSYS:
		return landlockKernelProbe{Status: landlockKernelUnsupported}
	case errno == syscall.EOPNOTSUPP:
		return landlockKernelProbe{Status: landlockKernelDisabled}
	case errno != 0:
		return landlockKernelProbe{Status: landlockKernelUnknown, Err: fmt.Errorf("landlock ABI query failed: %w", errno)}
	case ret < 1 || ret > math.MaxInt32:
		return landlockKernelProbe{Status: landlockKernelUnknown, Err: fmt.Errorf("landlock ABI query returned %d", ret)}
	}
	return landlockKernelProbe{Status: landlockKernelAvailable, ABI: int(ret)}
}

// probeVMLandlockKernel asks the space VM kernel for its Landlock ABI. The
// query runs in the space's native controller, outside any workload seccomp
// filter. Securityfs is not consulted: the Firecracker guest never mounts it,
// and an absent status file says nothing about the syscall API.
func probeVMLandlockKernel(spaceName string) landlockKernelProbe {
	if !spaceenv.Native() {
		return landlockKernelProbe{Status: landlockKernelUnknown, Err: errors.New("the Landlock ABI query runs only in the space controller")}
	}
	if err := spaceenv.RequireSpace(spaceName); err != nil {
		return landlockKernelProbe{Status: landlockKernelUnknown, Err: err}
	}
	return queryLandlockABI()
}

// inspectLandlock probes Landlock state for a box running in spaceName.
// Returns an error only when the box log cannot be read. A log that was not
// read cannot rule out a reported failure, so "available" then becomes
// "unknown"; a demonstrated kernel gap is still reported.
func inspectLandlock(ctx context.Context, spaceName, agentboxName string) (landlockState, error) {
	kernel := probeVMLandlockKernel(spaceName)
	evidence, err := scanOpenshellLandlockLog(ctx, spaceName, agentboxName)
	state := landlockVerdict(kernel, evidence)
	if err != nil && state.Summary == landlockKernelAvailable {
		state.Summary = landlockKernelUnknown
		state.ProbeError = err.Error()
	}
	return state, err
}

// landlockVerdict combines the kernel answer with the retained sandbox log.
// A demonstrated kernel gap wins, then a reported sandbox failure; healthy
// signals only ever yield "available".
func landlockVerdict(kernel landlockKernelProbe, evidence landlockLogEvidence) landlockState {
	state := landlockState{
		KernelSupported: kernel.Status == landlockKernelAvailable,
		ABI:             kernel.ABI,
		LogHint:         evidence.First,
		Summary:         landlockKernelUnknown,
	}
	if kernel.Err != nil {
		state.ProbeError = kernel.Err.Error()
	}
	if evidence.Failure != "" {
		state.LogHint = evidence.Failure
	}
	switch {
	case kernel.Status == landlockKernelUnsupported:
		state.Summary = "kernel-unsupported"
	case kernel.Status == landlockKernelDisabled:
		state.Summary = "kernel-disabled"
	case evidence.Failure != "":
		state.Summary = "sandbox-reported-failure"
	case kernel.Status == landlockKernelAvailable:
		state.Summary = landlockKernelAvailable
	}
	return state
}

// landlockLogFailureMarkers are lower-case fragments of the Landlock failure
// events openshell-sandbox v0.0.72 emits under best_effort compatibility
// (crates/openshell-supervisor-process/src/sandbox/linux/{mod,landlock}.rs).
// Recheck them when the OpenShell pin changes.
var landlockLogFailureMarkers = []string{
	"unavailable",
	"restrict_self failed",
	"enforce-failed",
	"without filesystem restrictions",
}

// maxLandlockLogLine bounds the bytes kept per log line.
const maxLandlockLogLine = 4096

// landlockLogEvidence holds the first line mentioning Landlock and the first
// line reporting a Landlock failure, so a generic early line cannot mask a
// later failure.
type landlockLogEvidence struct {
	First   string
	Failure string
}

// landlockLogCollector gathers evidence from concurrently written streams.
type landlockLogCollector struct {
	mu       sync.Mutex
	evidence landlockLogEvidence
}

func (c *landlockLogCollector) observe(raw []byte) {
	lower := strings.ToLower(string(raw))
	if !strings.Contains(lower, "landlock") {
		return
	}
	line := sanitizeLandlockLogLine(raw)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.evidence.First == "" {
		c.evidence.First = line
	}
	if c.evidence.Failure != "" {
		return
	}
	for _, marker := range landlockLogFailureMarkers {
		if strings.Contains(lower, marker) {
			c.evidence.Failure = line
			return
		}
	}
}

// sanitizeLandlockLogLine keeps a log line printable on a terminal.
func sanitizeLandlockLogLine(raw []byte) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(string(raw), "?")))
}

// landlockLineWriter splits one output stream into lines for evidence,
// keeping at most maxLandlockLogLine bytes of each line in memory.
type landlockLineWriter struct {
	collector *landlockLogCollector
	line      []byte
}

func (w *landlockLineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		chunk := p
		if end >= 0 {
			chunk = p[:end]
		}
		if room := maxLandlockLogLine - len(w.line); room > 0 {
			w.line = append(w.line, chunk[:min(len(chunk), room)]...)
		}
		if end < 0 {
			break
		}
		w.flush()
		p = p[end+1:]
	}
	return n, nil
}

func (w *landlockLineWriter) flush() {
	if len(w.line) > 0 {
		w.collector.observe(w.line)
	}
	w.line = w.line[:0]
}

// scanOpenshellLandlockLog streams the box's retained container log through
// line scanners. Container stdout and stderr keep separate line buffers, and
// memory stays bounded however long the log is.
func scanOpenshellLandlockLog(ctx context.Context, spaceName, agentboxName string) (landlockLogEvidence, error) {
	var collector landlockLogCollector
	stdout := &landlockLineWriter{collector: &collector}
	stderr := &landlockLineWriter{collector: &collector}
	cmd := vmhost.ShellCommand(ctx, spaceName, "sudo", "nerdctl", "logs", agentboxName)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	stdout.flush()
	stderr.flush()
	if err != nil {
		err = fmt.Errorf("read box log: %w", err)
	}
	return collector.evidence, err
}

// landlockSummaryLine renders a landlockState as the one-line
// human-readable form printed by `box show`.
func landlockSummaryLine(ll landlockState) string {
	switch ll.Summary {
	case "available":
		line := fmt.Sprintf("available (VM kernel ABI %d); per-process enforcement is not verified by this view", ll.ABI)
		if ll.LogHint != "" {
			line += fmt.Sprintf(" (sandbox: %s)", ll.LogHint)
		}
		return line
	case "kernel-unsupported":
		return "DEGRADED — the VM kernel does not implement Landlock (ABI query: ENOSYS); best_effort file policy is not enforced"
	case "kernel-disabled":
		return "DEGRADED — Landlock is built into the VM kernel but disabled at boot (ABI query: EOPNOTSUPP); best_effort file policy is not enforced"
	case "sandbox-reported-failure":
		return fmt.Sprintf("DEGRADED — the retained openshell-sandbox log reports a Landlock failure; workloads launched after it may differ (sandbox: %s)", ll.LogHint)
	default:
		if ll.ProbeError != "" {
			return fmt.Sprintf("unknown — %s", ll.ProbeError)
		}
		return "unknown"
	}
}
