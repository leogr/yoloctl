package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"syscall"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
)

// TestLandlockSummaryLine asserts the verdict strings stay stable —
// they're surfaced in box show output and CI/scripts may grep
// for "DEGRADED" to alert on Landlock failure modes.
func TestLandlockSummaryLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      landlockState
		want    []string // substrings
		notWant []string
	}{
		{
			name:    "available_never_claims_enforcement",
			in:      landlockState{Summary: "available", KernelSupported: true, ABI: 10},
			want:    []string{"available (VM kernel ABI 10)", "not verified"},
			notWant: []string{"DEGRADED", "active", "enforced"},
		},
		{
			name: "available_with_log_hint",
			in:   landlockState{Summary: "available", KernelSupported: true, ABI: 10, LogHint: "Landlock ruleset built [rules_applied:17 skipped:0]"},
			want: []string{"sandbox: Landlock ruleset built"},
		},
		{
			name: "kernel_unsupported_says_DEGRADED",
			in:   landlockState{Summary: "kernel-unsupported"},
			want: []string{"DEGRADED", "ENOSYS"},
		},
		{
			name: "kernel_disabled_says_DEGRADED",
			in:   landlockState{Summary: "kernel-disabled"},
			want: []string{"DEGRADED", "EOPNOTSUPP"},
		},
		{
			name: "sandbox_failure_is_labelled_as_retained_log_evidence",
			in:   landlockState{Summary: "sandbox-reported-failure", KernelSupported: true, ABI: 10, LogHint: "Landlock restrict_self failed (best_effort): x"},
			want: []string{"DEGRADED", "retained openshell-sandbox log reports", "may differ", "restrict_self failed"},
		},
		{
			name:    "unknown_keeps_the_probe_error",
			in:      landlockState{Summary: "unknown", ProbeError: "landlock ABI query failed: operation not permitted"},
			want:    []string{"unknown", "operation not permitted"},
			notWant: []string{"DEGRADED"},
		},
		{
			name: "default_says_unknown",
			in:   landlockState{},
			want: []string{"unknown"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := landlockSummaryLine(c.in)
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("landlockSummaryLine(%+v) = %q, want substring %q", c.in, got, want)
				}
			}
			for _, bad := range c.notWant {
				if strings.Contains(got, bad) {
					t.Errorf("landlockSummaryLine(%+v) = %q, must not contain %q", c.in, got, bad)
				}
			}
		})
	}
}

// TestClassifyLandlockABI pins the documented errno meanings: only ENOSYS
// and EOPNOTSUPP demonstrate a kernel without usable Landlock.
func TestClassifyLandlockABI(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		ret     uintptr
		errno   syscall.Errno
		status  string
		abi     int
		wantErr bool
	}{
		{name: "linux7_abi_10", ret: 10, status: landlockKernelAvailable, abi: 10},
		{name: "not_built_enosys", ret: ^uintptr(0), errno: syscall.ENOSYS, status: landlockKernelUnsupported},
		{name: "disabled_at_boot_eopnotsupp", ret: ^uintptr(0), errno: syscall.EOPNOTSUPP, status: landlockKernelDisabled},
		{name: "seccomp_eperm_is_unknown", ret: ^uintptr(0), errno: syscall.EPERM, status: landlockKernelUnknown, wantErr: true},
		{name: "denied_eacces_is_unknown", ret: ^uintptr(0), errno: syscall.EACCES, status: landlockKernelUnknown, wantErr: true},
		{name: "impossible_zero_version", ret: 0, status: landlockKernelUnknown, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyLandlockABI(c.ret, c.errno)
			if got.Status != c.status || got.ABI != c.abi || (got.Err != nil) != c.wantErr {
				t.Errorf("classifyLandlockABI(%d, %v) = %+v, want status %q abi %d err %v", c.ret, c.errno, got, c.status, c.abi, c.wantErr)
			}
		})
	}
}

// TestProbeVMLandlockKernelOutsideController guards the original bug: a
// probe that cannot run in this space's VM (outside the controller, or in a
// controller scoped to another space) is unknown, never a demonstrated
// unsupported kernel.
func TestProbeVMLandlockKernelOutsideController(t *testing.T) {
	t.Parallel()
	got := probeVMLandlockKernel("default")
	if got.Status != landlockKernelUnknown || got.Err == nil {
		t.Fatalf("probe outside the controller = %+v, want unknown with an error", got)
	}
	state := landlockVerdict(got, landlockLogEvidence{})
	if state.Summary != "unknown" || strings.Contains(landlockSummaryLine(state), "DEGRADED") {
		t.Fatalf("verdict = %+v (%q), want unknown without DEGRADED", state, landlockSummaryLine(state))
	}
}

func TestLandlockVerdict(t *testing.T) {
	t.Parallel()
	available := landlockKernelProbe{Status: landlockKernelAvailable, ABI: 10}
	generic := "Landlock filesystem sandbox available [abi:v10 compat:BestEffort ro:11 rw:6]"
	failure := "Landlock restrict_self failed (best_effort): x"
	cases := []struct {
		name     string
		kernel   landlockKernelProbe
		evidence landlockLogEvidence
		summary  string
		hint     string
	}{
		{name: "kernel_support_without_log", kernel: available, summary: "available"},
		{name: "kernel_support_with_generic_log", kernel: available, evidence: landlockLogEvidence{First: generic}, summary: "available", hint: generic},
		{name: "later_failure_not_masked_by_generic_line", kernel: available, evidence: landlockLogEvidence{First: generic, Failure: failure}, summary: "sandbox-reported-failure", hint: failure},
		{name: "kernel_gap_wins", kernel: landlockKernelProbe{Status: landlockKernelUnsupported}, evidence: landlockLogEvidence{First: generic, Failure: failure}, summary: "kernel-unsupported", hint: failure},
		{name: "kernel_disabled", kernel: landlockKernelProbe{Status: landlockKernelDisabled}, summary: "kernel-disabled"},
		{name: "unknown_kernel_without_failure", kernel: landlockKernelProbe{Status: landlockKernelUnknown}, evidence: landlockLogEvidence{First: generic}, summary: "unknown", hint: generic},
		{name: "unknown_kernel_with_failure", kernel: landlockKernelProbe{Status: landlockKernelUnknown}, evidence: landlockLogEvidence{First: generic, Failure: failure}, summary: "sandbox-reported-failure", hint: failure},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := landlockVerdict(c.kernel, c.evidence)
			if got.Summary != c.summary || got.LogHint != c.hint {
				t.Errorf("landlockVerdict = %+v, want summary %q hint %q", got, c.summary, c.hint)
			}
			if got.KernelSupported != (c.kernel.Status == landlockKernelAvailable) || got.ABI != c.kernel.ABI {
				t.Errorf("landlockVerdict kernel fields = %+v, want from %+v", got, c.kernel)
			}
		})
	}
}

// TestLandlockLogScan feeds openshell-sandbox v0.0.72 message texts through
// the bounded line writer, split across writes as a pipe may deliver them.
func TestLandlockLogScan(t *testing.T) {
	t.Parallel()
	healthy := []string{
		"INFO Landlock filesystem sandbox available [abi:v10 compat:BestEffort ro:11 rw:6]",
		"INFO Applying Landlock filesystem sandbox [abi:V2 compat:BestEffort ro:11 rw:6]",
		"WARN Skipping inaccessible Landlock path (best-effort) [path:/app error:Permission denied (os error 13)]",
		"INFO Landlock ruleset built [rules_applied:17 skipped:0]",
	}
	failures := []string{
		"HIGH Landlock filesystem sandbox unavailable (best_effort, degraded): not enabled (Landlock built into kernel but not in active LSM list)",
		"HIGH landlock-unavailable Running WITHOUT filesystem restrictions: Landlock is not implemented",
		"HIGH Landlock restrict_self failed (best_effort): Operation not permitted",
		"HIGH landlock-enforce-failed Landlock restrict_self Failed",
	}
	scan := func(log string) landlockLogEvidence {
		var collector landlockLogCollector
		w := &landlockLineWriter{collector: &collector}
		for i := 0; i < len(log); i += 7 {
			_, _ = w.Write([]byte(log[i:min(i+7, len(log))]))
		}
		w.flush()
		return collector.evidence
	}
	got := scan("unrelated startup line\n" + strings.Join(healthy, "\n") + "\n")
	if got.First != healthy[0] || got.Failure != "" {
		t.Fatalf("healthy log evidence = %+v", got)
	}
	for _, failure := range failures {
		got := scan(strings.Join(healthy, "\n") + "\n" + failure + "\n" + healthy[3])
		if got.First != healthy[0] || got.Failure != failure {
			t.Errorf("failure %q: evidence = %+v", failure, got)
		}
	}
	long := strings.Repeat("x", 3*maxLandlockLogLine) + " landlock unavailable"
	if got := scan("Landlock \x1b[31mred\x1b[0m\n" + long + "\n"); got.First != "Landlock  [31mred [0m" || got.Failure != "" {
		t.Errorf("sanitize/bound evidence = %+v", got)
	}
}

// TestBoxShowLandlockTextJSONAgree keeps the text line and the JSON landlock
// object on the same verdict.
func TestBoxShowLandlockTextJSONAgree(t *testing.T) {
	t.Parallel()
	d := &container.Details{Name: "coder", Image: "img:1", Status: "Up", Running: true}
	cases := []struct {
		state landlockState
		line  string
	}{
		{landlockState{Summary: "available", KernelSupported: true, ABI: 10, LogHint: "Landlock ruleset built [rules_applied:17 skipped:0]"}, "Landlock: available (VM kernel ABI 10)"},
		{landlockState{Summary: "unknown", ProbeError: "the Landlock ABI query runs only in the space controller"}, "Landlock: unknown — the Landlock ABI query runs only"},
		{landlockState{Summary: "kernel-disabled"}, "Landlock: DEGRADED"},
	}
	for _, c := range cases {
		var text, raw bytes.Buffer
		meta := agentboxShowMetadata{AgentName: "claude"}
		if err := writeAgentboxShow(&text, d, "default", c.state, agentStateMount{}, meta, sessionStatus{State: "running"}, nil); err != nil {
			t.Fatal(err)
		}
		if err := writeAgentboxShowJSON(&raw, d, "default", c.state, agentStateMount{}, meta, sessionStatus{State: "running"}, nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text.String(), c.line) {
			t.Errorf("text lacks %q:\n%s", c.line, text.String())
		}
		var doc struct {
			Landlock struct {
				Summary         string `json:"summary"`
				KernelSupported bool   `json:"kernel_supported"`
				ABI             int    `json:"abi"`
				ProbeError      string `json:"probe_error"`
				LogHint         string `json:"log_hint"`
			} `json:"landlock"`
		}
		if err := json.Unmarshal(raw.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		got := doc.Landlock
		if got.Summary != c.state.Summary || got.KernelSupported != c.state.KernelSupported || got.ABI != c.state.ABI ||
			got.ProbeError != c.state.ProbeError || got.LogHint != c.state.LogHint {
			t.Errorf("JSON landlock = %+v, want %+v", got, c.state)
		}
	}
}
