package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
)

// Client runs container operations through the selected VM adapter and
// system containerd, or locally inside the native controller.
type Client struct {
	env    string
	stdout io.Writer
	stderr io.Writer
	// Nil preserves runtime budgets; semantic command fixtures may supply a watchdog.
	newTimeout func(context.Context, time.Duration) (context.Context, context.CancelFunc)
}

// New returns a Client targeting the given env VM.
func New(env string) *Client {
	return NewWithOutput(env, os.Stdout, os.Stderr)
}

// NewWithOutput routes subprocess output to explicit writers. Use it when
// process stdout is a structured response rather than a human-facing stream.
// Nil writers discard output. Writers may receive concurrent writes when both
// streams use the same destination.
func NewWithOutput(env string, stdout, stderr io.Writer) *Client {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return &Client{env: env, stdout: stdout, stderr: stderr}
}

// Env returns the space name this Client targets. Read-only;
// the env is set at construction time and never mutated.
func (c *Client) Env() string {
	return c.env
}

// Mount is a `-v` bind mount spec for Run.
type Mount struct {
	Source      string
	Destination string
	ReadOnly    bool
}

// RunOptions describes a container to launch with `nerdctl run`.
type RunOptions struct {
	Image           string
	Name            string
	Labels          map[string]string
	AddCaps         []string
	SeccompProfile  string
	AppArmorProfile string
	User            string
	CPUs            string
	Memory          string
	WorkloadPool    bool // managed boxes share the VM's bounded workload pool
	Mounts          []Mount
	Env             []string
	// Sysctl entries are "key=value" pairs forwarded to `nerdctl run
	// --sysctl`. Each lands as a per-netns/per-container kernel setting
	// at startup. /proc/sys is mounted read-only inside containers by
	// default, so this is the cleanest way to set a sysctl an agentbox
	// needs from boot (e.g., net.ipv4.ip_forward=1 for inner-netns
	// port forwarding).
	Sysctl  []string
	Command []string
	Detach  bool
	// Privileged emits --privileged: every capability, all VM devices and
	// device cgroup access, with new privileges allowed (nerdctl 2.3.5
	// run_security_linux.go privilegedOpts). Private PID/network/mount
	// namespaces and the declared binds are unchanged.
	Privileged bool
}

// Run launches a container with `nerdctl run`.
func (c *Client) Run(ctx context.Context, opts RunOptions) error {
	return c.createOrRun(ctx, opts, true, c.stdout)
}

// Create records an unstarted container. Native OpenShell preparation inspects
// its effective image/container ENV before publishing private bootstrap; no
// PID1, supervisor or workload starts until the separate admitted Start call.
// The returned full ID comes from this command's successful acknowledgement,
// never a later name lookup. Any command/output error returns no usable ID.
// Like Run, the caller owns launch authorization, receipts and cleanup.
func (c *Client) Create(ctx context.Context, opts RunOptions) (string, error) {
	var output createAcknowledgement
	if err := c.createOrRun(ctx, opts, false, &output); err != nil {
		return "", err
	}
	return output.containerID()
}

func (c *Client) createOrRun(ctx context.Context, opts RunOptions, start bool, stdout io.Writer) error {
	if guard, ok := ctx.Value(buildGuardKey{}).(BuildGuard); ok {
		present, err := c.ImagePresent(ctx, opts.Image)
		if err != nil {
			return err
		}
		if !present {
			if err := guard(ctx, []string{opts.Image}); err != nil {
				return err
			}
		}
	}
	action := "create"
	if start {
		action = "run"
	}
	args := []string{action}
	if opts.WorkloadPool {
		systemd, err := c.workloadPool(ctx)
		if err != nil {
			return err
		}
		manager := "cgroupfs"
		if systemd {
			manager = "systemd"
		}
		args = []string{"--cgroup-manager", manager, action, "--cgroup-parent", resources.CgroupParent(systemd)}
	}
	if start && opts.Detach {
		args = append(args, "-d")
	}
	if !start {
		args = append(args, "--quiet")
	}
	if opts.Privileged {
		args = append(args, "--privileged")
	}
	args = append(args, "--name", opts.Name)
	for k, v := range opts.Labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}
	for _, cap := range opts.AddCaps {
		args = append(args, "--cap-add", cap)
	}
	if opts.SeccompProfile != "" {
		args = append(args, "--security-opt", fmt.Sprintf("seccomp=%s", opts.SeccompProfile))
	}
	if opts.AppArmorProfile != "" {
		args = append(args, "--security-opt", fmt.Sprintf("apparmor=%s", opts.AppArmorProfile))
	}
	if opts.User != "" {
		args = append(args, "--user", opts.User)
	}
	if opts.CPUs != "" {
		args = append(args, "--cpus", opts.CPUs)
	}
	memory, err := resources.BoxMemory(opts.Memory)
	if err != nil {
		return err
	}
	if memory > 0 {
		args = append(args, "--memory", strconv.FormatInt(memory, 10))
	}
	for _, m := range opts.Mounts {
		spec := fmt.Sprintf("%s:%s", m.Source, m.Destination)
		if m.ReadOnly {
			spec += ":ro"
		}
		args = append(args, "-v", spec)
	}
	for _, e := range opts.Env {
		args = append(args, "-e", e)
	}
	for _, s := range opts.Sysctl {
		args = append(args, "--sysctl", s)
	}
	args = append(args, opts.Image)
	args = append(args, opts.Command...)
	cmd := c.command(ctx, append([]string{"sudo", "nerdctl"}, args...)...)
	cmd.Stdout = stdout
	return vmhost.RunWithFilteredStderr(cmd, c.stderr)
}

// Start re-launches a stopped container.
func (c *Client) Start(ctx context.Context, name string) error {
	return c.nerdctl(ctx, "start", name)
}

// stopGraceSeconds is how long `nerdctl stop` waits after SIGTERM
// before escalating to SIGKILL. nerdctl's default is 10s; openshell-
// sandbox tears down its netns/veth/proxy state after its child exits,
// and a SIGKILL mid-teardown orphans that state (see
// internal/sandbox.ReapOrphanedNetns). The longer grace makes the
// escalation rare; the reaper covers the cases it can't.
const stopGraceSeconds = "30"

// Stop performs the runtime's bounded stop/escalation. Managed boxes first
// signal their confined children through sandbox.Stop. ctr tasks kill --all
// cannot do that: modern runc ignores --all for SIGTERM and signals only PID 1.
func (c *Client) Stop(ctx context.Context, name string) error {
	return c.nerdctl(ctx, "stop", "--time", stopGraceSeconds, name)
}

// Image is a parsed `nerdctl images --format json` entry.
type Image struct {
	Repository string `json:"Repository"`
	Tag        string `json:"Tag"`
	ID         string `json:"ID"`
	Size       string `json:"Size"`
	CreatedAt  string `json:"CreatedAt"`
}

// ImagesList returns container images in the space's local cache.
// referenceFilter is a `--filter reference=...` glob (e.g.
// "yoloctl/*"); empty means no filter (full cache). The slice is
// returned in nerdctl's order (most-recent first by CreatedAt).
func (c *Client) ImagesList(ctx context.Context, referenceFilter string) ([]Image, error) {
	args := []string{"images", "--format", "json"}
	if referenceFilter != "" {
		args = append(args, "--filter", "reference="+referenceFilter)
	}
	var stdout, stderr bytes.Buffer
	cmd := c.command(ctx, append([]string{"sudo", "nerdctl"}, args...)...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	vmhost.WriteFilteredStderr(c.stderr, stderr.String())
	if err != nil {
		return nil, err
	}
	return parseImages(&stdout)
}

func parseImages(r io.Reader) ([]Image, error) {
	dec := json.NewDecoder(r)
	var out []Image
	for {
		var img Image
		if err := dec.Decode(&img); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		out = append(out, img)
	}
}

// ImagePull pulls a reference into the space's local cache. nerdctl
// streams progress to stderr (preserved through the Client's stderr)
// and prints the final digest to stdout.
func (c *Client) ImagePull(ctx context.Context, ref string) error {
	if err := checkBuildSpace(ctx, []string{ref}); err != nil {
		return err
	}
	return c.nerdctl(ctx, "pull", ref)
}

// ImageRemove deletes ref from the space's local image cache
// (`nerdctl rmi <ref>`). Used by `yoloctl image prune` to reclaim
// feature-composed images no box references.
func (c *Client) ImageRemove(ctx context.Context, ref string) error {
	return c.nerdctl(ctx, "rmi", ref)
}

// ImagePresent reports whether ref already lives in the VM's local
// image cache. Never makes a network call: maps to `sudo nerdctl
// image inspect <ref>` and translates exit-1-with-"no such" into
// (false, nil). Used by the image-ensure path before a `nerdctl run`
// to decide whether the image needs a local build (yoloctl-managed
// refs) or to leave the pull-or-fail to nerdctl run (everything
// else).
func (c *Client) ImagePresent(ctx context.Context, ref string) (bool, error) {
	var stderr bytes.Buffer
	cmd := c.command(ctx, "sudo", "nerdctl", "image", "inspect",
		"--format", "{{.Id}}", ref)
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		stderrText := vmhost.FilterStderrText(stderr.String())
		msg := strings.ToLower(stderrText)
		if strings.Contains(msg, "no such image") ||
			strings.Contains(msg, "not found") {
			return false, nil
		}
		return false, fmt.Errorf("image inspect %s: %w (%s)",
			ref, err, strings.TrimSpace(stderrText))
	}
	return true, nil
}

// BuildOptions configures a single `nerdctl build` invocation against
// a tar-streamed context. Tag is required; everything else is
// optional. Zero-value BuildOptions{} (no args, no extra flags) is the
// same shape Build used to accept before this struct existed.
type BuildOptions struct {
	// Tag is the resulting image reference (repo:tag).
	Tag string
	// InputImages protects explicitly selected FROM images during GC preflight.
	InputImages []string

	// ContextTar is read until EOF and extracted into the VM-side
	// build temp dir before `nerdctl build` runs there. Must yield a
	// tar whose root contains a Dockerfile plus any COPY-referenced
	// siblings.
	ContextTar io.Reader

	// BuildArgs are `--build-arg KEY=VALUE` pairs. Sorted by key
	// before flag emission so two equivalent maps produce identical
	// argv (stable across reconciles / test fixtures).
	BuildArgs map[string]string

	// NoCache passes `--no-cache` to nerdctl build, forcing every
	// layer to rebuild rather than reuse the local cache. It also resets
	// BuildKit cache mounts. Managed recipes normally use a fresh build ARG
	// instead; custom bases/raw Dockerfile features retain a full rebuild.
	NoCache bool
}

// Build streams a tar build context into a fresh temp dir on the
// space VM and runs `sudo nerdctl build -t tag .` there. The
// ContextTar reader must yield a tar archive whose root contains a
// Dockerfile plus every file it COPYs. The temp dir is removed on
// return (success or failure).
//
// Callers include the image-ensure layer in internal/agentboximage, which
// tars the embedded base-image context, and the manual feature-image builder
// in cmd/yoloctl/image.go, which tars a generated Dockerfile. Both produce
// their tar in memory and never write the build context to host disk.
func (c *Client) Build(ctx context.Context, opts BuildOptions) error {
	if opts.Tag == "" {
		return errors.New("Build: Tag must not be empty")
	}
	if opts.ContextTar == nil {
		return errors.New("Build: ContextTar must not be nil")
	}
	if err := checkBuildSpace(ctx, append([]string{opts.Tag}, opts.InputImages...)); err != nil {
		return err
	}
	if err := c.EnsureBuilder(ctx); err != nil {
		return err
	}
	remoteCtx := fmt.Sprintf("/tmp/yoloctl-build-%d-%d",
		os.Getpid(), time.Now().UnixNano())
	if err := c.Shell(ctx, "mkdir", "-p", remoteCtx); err != nil {
		return fmt.Errorf("mkdir %s: %w", remoteCtx, err)
	}
	defer func() {
		// context.Background, not ctx: this cleanup must run even when
		// ctx was cancelled (the reason we're tearing down the build).
		rm := vmhost.ShellCommand(context.Background(), c.env, "rm", "-rf", remoteCtx)
		_ = rm.Run()
	}()
	extract := vmhost.ShellCommandWithWorkdir(ctx, c.env, remoteCtx, "tar", "-xf", "-")
	extract.Stdin = opts.ContextTar
	if err := vmhost.RunWithFilteredStderr(extract, c.stderr); err != nil {
		return fmt.Errorf("extract context to %s: %w", remoteCtx, err)
	}
	buildArgs := []string{"sudo", "nerdctl", "build", "-t", opts.Tag}
	keys := make([]string, 0, len(opts.BuildArgs))
	for k := range opts.BuildArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		buildArgs = append(buildArgs, "--build-arg", fmt.Sprintf("%s=%s", k, opts.BuildArgs[k]))
	}
	if opts.NoCache {
		buildArgs = append(buildArgs, "--no-cache")
	}
	buildArgs = append(buildArgs, ".")
	build := vmhost.ShellCommandWithWorkdir(ctx, c.env, remoteCtx, buildArgs...)
	build.Stdout = c.stdout
	if err := vmhost.RunWithFilteredStderr(build, c.stderr); err != nil {
		return fmt.Errorf("nerdctl build %s: %w", opts.Tag, err)
	}
	return nil
}

// EnsureBuilder starts the microVM's lazy build service or waits for Lima's
// service to be ready, including after a daemon failure. It never retries a
// build. Image pulls and cached container starts do not call this path.
func (c *Client) EnsureBuilder(ctx context.Context) error {
	systemd, err := c.workloadPool(ctx)
	if err != nil {
		return err
	}
	if !systemd {
		return c.Shell(ctx, "sudo", "/sbin/yoloctl-init", "buildkit")
	}
	startCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// Type=notify makes this wait for readiness even while an automatic
	// restart is pending. An already running service is left alone.
	if err := c.Shell(startCtx, "sudo", "systemctl", "start", "buildkit.service"); err != nil {
		return fmt.Errorf("start BuildKit: %w", err)
	}
	if _, err := c.ShellOutputQuiet(startCtx, "sudo", "buildctl", "debug", "workers"); err != nil {
		return fmt.Errorf("BuildKit worker unavailable: %w", err)
	}
	return nil
}

// RequireWorkloadPool is also a pre-teardown check for managed recreation.
func (c *Client) RequireWorkloadPool(ctx context.Context) error {
	_, err := c.workloadPool(ctx)
	return err
}

func (c *Client) workloadPool(ctx context.Context) (bool, error) {
	systemd, err := c.workloadSystemd()
	if err != nil {
		return false, err
	}
	probeCtx, cancel := c.withTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := c.ShellOutputQuiet(probeCtx, append([]string{"cat"}, resources.PoolFiles(systemd)...)...)
	if err == nil {
		err = resources.ValidatePool(raw)
	}
	if err != nil {
		hint := "space start --update-controller"
		if !systemd {
			hint = "space recreate --update-vm-image"
		}
		return false, fmt.Errorf("VM workload memory pool is unavailable: %w; update this space with %s", err, hint)
	}
	return systemd, nil
}

func (c *Client) workloadSystemd() (bool, error) {
	microvm := false
	if current := spaceenv.Current(); current != nil {
		if err := spaceenv.RequireSpace(c.env); err != nil {
			return false, err
		}
		microvm = current.ServiceManager == "microvm"
	} else {
		driver, err := vmhost.ForSpace(c.env)
		if err != nil {
			return false, err
		}
		microvm = driver.Kind() == "firecracker"
	}
	return !microvm, nil
}

// Logs streams `nerdctl logs <name>` (optionally with --follow) to
// stdout / stderr. NewWithOutput selects explicit writers; New uses the
// process stdout/stderr. Returns when nerdctl exits — for --follow this is when the
// container stops or the context is cancelled.
func (c *Client) Logs(ctx context.Context, name string, follow bool) error {
	args := []string{"logs"}
	if follow {
		args = append(args, "--follow")
	}
	args = append(args, name)
	return c.nerdctl(ctx, args...)
}

// Remove deletes the container. force=true also kills it if running, but
// short-circuits openshell-sandbox's cleanup pipeline; prefer Stop first.
func (c *Client) Remove(ctx context.Context, name string, force bool) error {
	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, name)
	if err := c.nerdctl(ctx, args...); err != nil {
		return err
	}
	return operator.RemoveBox(name, "")
}

// Container is a parsed `nerdctl ps --format json` entry.
type Container struct {
	ID     string `json:"ID"`
	Name   string `json:"Names"`
	Image  string `json:"Image"`
	Status string `json:"Status"`
	Labels string `json:"Labels"`
}

// List returns containers that carry the given label key. Empty label
// returns everything.
func (c *Client) List(ctx context.Context, labelKey string) ([]Container, error) {
	args := []string{"ps", "-a", "--format", "json"}
	if labelKey != "" {
		args = append(args, "--filter", "label="+labelKey)
	}
	var stdout, stderr bytes.Buffer
	cmd := c.command(ctx, append([]string{"sudo", "nerdctl"}, args...)...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	vmhost.WriteFilteredStderr(c.stderr, stderr.String())
	if err != nil {
		return nil, err
	}
	return parseContainers(&stdout)
}

// ListQuiet is List with remote stderr discarded — the probe variant for
// best-effort callers (shell/TUI completion) where an unreachable or stopped
// space VM is an expected answer to fall back from, not an error to surface on
// every keystroke.
func (c *Client) ListQuiet(ctx context.Context, labelKey string) ([]Container, error) {
	args := []string{"ps", "-a", "--format", "json"}
	if labelKey != "" {
		args = append(args, "--filter", "label="+labelKey)
	}
	var stdout bytes.Buffer
	cmd := c.command(ctx, append([]string{"sudo", "nerdctl"}, args...)...)
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return parseContainers(&stdout)
}

func parseContainers(r io.Reader) ([]Container, error) {
	dec := json.NewDecoder(r)
	var out []Container
	for {
		var ctr Container
		if err := dec.Decode(&ctr); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		out = append(out, ctr)
	}
}

// Details is a parsed `nerdctl inspect` entry, narrowed to the fields
// `yoloctl box show` needs. The full nerdctl output is much larger
// (HostConfig, NetworkSettings, etc.); add fields here as additional
// callers need them.
//
// NOTE on capabilities: nerdctl's inspect HostConfig does not surface
// CapAdd, even though the container was created with --cap-add. The
// info exists in containerd's runtime spec but isn't reachable via
// nerdctl inspect; if `box show` needs to display caps later,
// we'll have to fall back to parsing the `nerdctl/host-config` label
// or shell out to a different tool.
type Details struct {
	ID        string
	Name      string
	Image     string
	ImageID   string // nerdctl 2.4+: recorded manifest/index digest; 2.3.5: mutable name
	Status    string
	Running   bool
	Created   string // RFC3339
	PID       int    // VM-visible runtime PID; zero means inspect did not supply one
	StartedAt string // observed runtime start timestamp; never inferred from Created
	// EmptyTaskStateObserved records the explicit complete empty-task shape
	// emitted by pinned nerdctl inspection. It is current observation only,
	// never a create/start/stop acknowledgement or proof the task never ran.
	EmptyTaskStateObserved bool `json:"-"`
	// InactiveTaskFieldsObserved requires explicit, unambiguous status,
	// running=false, pid=0 and valid optional-history fields in inspect JSON.
	// It does not acknowledge Stop or infer whether execution ever happened.
	InactiveTaskFieldsObserved bool `json:"-"`
	// AmbiguousTaskStateObserved withholds native cleanup authority when raw
	// State keys do not have one canonical representation. Legacy decoded
	// process fields remain unchanged; this marker grants no positive evidence.
	AmbiguousTaskStateObserved bool `json:"-"`
	Labels                     map[string]string
	Mounts                     []DetailMount
	Env                        []string
	Command                    []string // inspect Path + Args: recorded OCI launch argv
	CPUs                       string   // normalized observed limit; empty means unavailable
	Memory                     string   // observed bytes, including "0" for no per-box limit
	// Privileged is the runtime's record of --privileged, nil when inspect
	// omitted it. nerdctl 2.3.5 derives HostConfig.Privileged from its
	// internal nerdctl/privileged label, which only --privileged sets and
	// which user labels cannot carry (dockercompat.go:665, create.go:670).
	Privileged *bool
}

// DetailMount is one bind mount from a Details struct.
type DetailMount struct {
	Source      string
	Destination string
	ReadOnly    bool
}

// ContainerIP returns the agentbox container's CNI bridge IPv4 address
// (e.g., 10.4.0.11 in nerdctl's default network). Returns an empty
// string + nil error when nerdctl reports an empty address — happens
// when the container isn't running yet. The caller can use that to
// distinguish "not running" from "lookup failed".
func (c *Client) ContainerIP(ctx context.Context, name string) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := c.command(ctx, "sudo", "nerdctl", "inspect", name)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("inspect %s for IP: %w (%s)", name, err, vmhost.FilterStderrText(stderr.String()))
	}
	return parseContainerIP(stdout.Bytes(), name)
}

// parseContainerIP reads the IPv4 address of exactly the requested container
// (see selectInspectEntry); empty while it is not running.
func parseContainerIP(raw []byte, name string) (string, error) {
	var entries []inspectEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return "", fmt.Errorf("parse inspect %s for IP: %w", name, err)
	}
	e, err := selectInspectEntry(entries, name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(e.NetworkSettings.IPAddress), nil
}

// Inspect returns the parsed Details for one container, or an error if
// the container doesn't exist (the underlying nerdctl exits nonzero).
func (c *Client) Inspect(ctx context.Context, name string) (*Details, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := c.command(ctx, "sudo", "nerdctl", "inspect", name)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("inspect %s: %w (%s)", name, err, vmhost.FilterStderrText(stderr.String()))
	}
	return parseInspect(stdout.Bytes(), name)
}

// inspectEntry is one element of the `nerdctl inspect` JSON array, shared by
// single-container Details and the batched RuntimeStates.
type inspectEntry struct {
	emptyTaskStateObserved     bool
	inactiveTaskFieldsObserved bool
	ambiguousTaskStateObserved bool
	ID                         string   `json:"Id"`
	Name                       string   `json:"Name"`
	Path                       string   `json:"Path"`
	Args                       []string `json:"Args"`
	ImageID                    string   `json:"Image"`
	Created                    string   `json:"Created"`
	State                      struct {
		Status    string
		Running   bool
		Pid       int    `json:"Pid"`
		StartedAt string `json:"StartedAt"`
	} `json:"State"`
	Config struct {
		Image  string
		Labels map[string]string
		Env    []string
	} `json:"Config"`
	Mounts []struct {
		Source      string
		Destination string
		Mode        string
		RW          bool
	} `json:"Mounts"`
	HostConfig *struct {
		CPUQuota   int64  `json:"CpuQuota"`
		CPUPeriod  uint64 `json:"CpuPeriod"`
		Memory     int64
		Privileged *bool
	}
	NetworkSettings struct {
		IPAddress string `json:"IPAddress"`
	} `json:"NetworkSettings"`
}

// parseInspect handles the `nerdctl inspect` JSON shape — a top-level array
// — and returns exactly the requested container (see selectInspectEntry).
func parseInspect(raw []byte, name string) (*Details, error) {
	var entries []inspectEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse inspect %s: %w", name, err)
	}
	e, err := selectInspectEntry(entries, name)
	if err != nil {
		return nil, err
	}
	return e.details(name)
}

// nerdctlNameLabel is the label nerdctl records a container's name in.
const nerdctlNameLabel = "nerdctl/name"

// selectInspectEntry picks the one entry that is exactly the request. nerdctl
// resolves an inspect argument by name label OR by ID prefix and returns every
// match, so `inspect c1` also returns any container whose ID starts with
// "c1". An entry matches only by its recorded name (the nerdctl/name label,
// or Name without its leading "/") or by its full ID, for callers that pass a
// fixed container ID. No match is "no such container"; more than one is an
// error. Never the first entry, never an ID prefix.
func selectInspectEntry(entries []inspectEntry, req string) (*inspectEntry, error) {
	var found *inspectEntry
	for i := range entries {
		e := &entries[i]
		if req == "" || e.ID != req && strings.TrimPrefix(e.Name, "/") != req && e.Config.Labels[nerdctlNameLabel] != req {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("inspect %s: more than one container matches exactly", req)
		}
		found = e
	}
	if found == nil {
		return nil, fmt.Errorf("inspect %s: no such container", req)
	}
	return found, nil
}

func (e inspectEntry) details(name string) (*Details, error) {
	// nerdctl prefixes container names with "/" — strip it.
	displayName := e.Name
	if len(displayName) > 0 && displayName[0] == '/' {
		displayName = displayName[1:]
	}
	mounts := make([]DetailMount, 0, len(e.Mounts))
	for _, m := range e.Mounts {
		mounts = append(mounts, DetailMount{
			Source:      m.Source,
			Destination: m.Destination,
			// nerdctl populates either RW (bool) or Mode (e.g. "ro").
			// Treat either as read-only-when-RW-false-or-mode-ro.
			ReadOnly: !m.RW || m.Mode == "ro",
		})
	}
	d := &Details{
		ID:                         e.ID,
		Name:                       displayName,
		Image:                      e.Config.Image,
		ImageID:                    e.ImageID,
		Status:                     e.State.Status,
		Running:                    e.State.Running,
		Created:                    e.Created,
		PID:                        e.State.Pid,
		StartedAt:                  e.State.StartedAt,
		EmptyTaskStateObserved:     e.emptyTaskStateObserved,
		InactiveTaskFieldsObserved: e.inactiveTaskFieldsObserved,
		AmbiguousTaskStateObserved: e.ambiguousTaskStateObserved,
		Labels:                     e.Config.Labels,
		Mounts:                     mounts,
		Env:                        e.Config.Env,
	}
	// nerdctl 2.3.5 leaves container Config.Cmd unset, but always exposes
	// the OCI process argv as Path + Args. Do not confuse image defaults
	// with the command recorded when this container was created.
	if e.Path != "" {
		d.Command = append([]string{e.Path}, e.Args...)
	}
	if e.HostConfig != nil {
		if e.HostConfig.Memory < 0 {
			return nil, fmt.Errorf("inspect %s: invalid memory limit", name)
		}
		d.Memory = strconv.FormatInt(e.HostConfig.Memory, 10)
		d.Privileged = e.HostConfig.Privileged
		d.CPUs = "0"
		if e.HostConfig.CPUQuota > 0 {
			if e.HostConfig.CPUPeriod == 0 {
				return nil, fmt.Errorf("inspect %s: CPU quota without period", name)
			}
			d.CPUs = strconv.FormatFloat(float64(e.HostConfig.CPUQuota)/float64(e.HostConfig.CPUPeriod), 'g', -1, 64)
		}
	}
	return d, nil
}

// WriteFile pipes content into `sudo tee <path>` on the env VM, so
// the file lands with root ownership in /etc/yoloctl/ etc. mode (when
// non-empty) is then applied with `sudo chmod`. Callers should mkdir
// the parent directory first via Shell.
func (c *Client) WriteFile(ctx context.Context, path string, content []byte, mode string) error {
	tee := c.command(ctx, "sudo", "tee", path)
	tee.Stdin = bytes.NewReader(content)
	// tee echoes its input to stdout; discard so the caller doesn't see
	// a copy of the file content on their terminal.
	tee.Stdout = io.Discard
	if err := vmhost.RunWithFilteredStderr(tee, c.stderr); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if mode != "" {
		if err := c.Shell(ctx, "sudo", "chmod", mode, path); err != nil {
			return err
		}
	}
	return nil
}

// ReadFile cats a file on the env VM and returns its contents.
// Equivalent to `limactl shell <env> -- sudo cat <path>`. Returns the
// nerdctl-style error wrapped with the path for clean user-facing
// messages.
func (c *Client) ReadFile(ctx context.Context, path string) ([]byte, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := c.command(ctx, "sudo", "cat", path)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("read %s: %w (%s)", path, err, vmhost.FilterStderrText(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Shell runs an arbitrary command inside the env VM (no container scope).
// Used for setup/cleanup tasks like creating the per-agentbox socket dir.
func (c *Client) Shell(ctx context.Context, args ...string) error {
	cmd := c.command(ctx, args...)
	cmd.Stdout = c.stdout
	return vmhost.RunWithFilteredStderr(cmd, c.stderr)
}

// ShellOutput runs args in the env VM and returns stdout. Stderr is
// preserved through the Client's stderr writer so the caller sees
// any error context. The variant of Shell used when the caller needs
// to parse a command's output (e.g., systemctl list-units).
func (c *Client) ShellOutput(ctx context.Context, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := c.command(ctx, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	vmhost.WriteFilteredStderr(c.stderr, stderr.String())
	if err != nil {
		return "", err
	}
	return stdout.String(), nil
}

// ShellQuiet is Shell with all remote output discarded. For
// best-effort commands whose failure is an expected answer (stop a
// unit that may not exist, remove a file that may be gone) — their
// stderr is noise, not error context the user should see.
func (c *Client) ShellQuiet(ctx context.Context, args ...string) error {
	cmd := c.command(ctx, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// ShellOutputQuiet is ShellOutput with remote stderr discarded — the
// probe variant for "read it if it's there" commands where a missing
// target is an answer, not an error to surface.
func (c *Client) ShellOutputQuiet(ctx context.Context, args ...string) (string, error) {
	var stdout bytes.Buffer
	cmd := c.command(ctx, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

func (c *Client) nerdctl(ctx context.Context, args ...string) error {
	cmd := c.command(ctx, append([]string{"sudo", "nerdctl"}, args...)...)
	cmd.Stdout = c.stdout
	return vmhost.RunWithFilteredStderr(cmd, c.stderr)
}

func (c *Client) command(ctx context.Context, args ...string) *exec.Cmd {
	return vmhost.ShellCommand(ctx, c.env, args...)
}

// withTimeout keeps timeout construction local to this client; no runtime
// configuration changes the production budgets at its call sites.
func (c *Client) withTimeout(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	if c.newTimeout != nil {
		return c.newTimeout(ctx, budget)
	}
	return context.WithTimeout(ctx, budget)
}
