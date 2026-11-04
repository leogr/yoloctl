// Package spacetelemetry carries the live data behind `yoloctl space view`.
//
// The controller worker writes one JSON frame per line on a single long-lived
// read stream: an inventory of declarations and runtime state at a slow
// cadence, and raw resource counters at the sampling interval. The host turns
// counters into rates, so the guest never interprets time or units beyond
// reading the kernel's files. Reads are bounded and never follow symlinks.
package spacetelemetry

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// Version identifies the frame shape. Host and controller always run the same
// build, so a mismatch is a bug to report rather than a compatibility path.
const Version = 1

// MaxFrame bounds one encoded frame. An inventory of hundreds of boxes stays
// far below it; anything larger is refused instead of buffered.
const MaxFrame = 4 << 20

const (
	KindInventory = "inventory"
	KindSample    = "sample"
)

type Frame struct {
	Version   int        `json:"v"`
	Kind      string     `json:"kind"`
	At        time.Time  `json:"at"`
	Inventory *Inventory `json:"inventory,omitempty"`
	Sample    *Sample    `json:"sample,omitempty"`
}

// Inventory is the declaration-first view of the space. An Error field set
// means that part is uncertain and must be shown as unavailable, never as an
// empty list.
type Inventory struct {
	Space          string    `json:"space"`
	DefaultAccount string    `json:"default_account,omitempty"`
	DefaultWorkdir string    `json:"default_workdir,omitempty"`
	Grants         []Grant   `json:"grants"`
	Boxes          []Box     `json:"boxes"`
	BoxesError     string    `json:"boxes_error,omitempty"`
	Accounts       []Account `json:"accounts"`
	AccountsError  string    `json:"accounts_error,omitempty"`
	Rooms          []Room    `json:"rooms"`
	CPUs           int       `json:"cpus"`
}

type Grant struct {
	Path     string `json:"path"`
	Writable bool   `json:"writable"`
}

// Account lists the agents that have native state on the data disk.
type Account struct {
	Name   string   `json:"name"`
	Agents []string `json:"agents"`
}

type Box struct {
	Name     string `json:"name"`
	State    string `json:"state"`   // boxstate display state
	Runtime  string `json:"runtime"` // boxstate runtime state
	Session  string `json:"session"` // boxstate session state
	ExitCode *int   `json:"exit_code,omitempty"`
	// ExitedAt is set with ExitCode when the holder recorded a tombstone.
	ExitedAt *time.Time `json:"exited_at,omitempty"`
	Reasons  []string   `json:"reasons,omitempty"`

	Agent          string   `json:"agent,omitempty"`
	Account        string   `json:"account,omitempty"`
	Image          string   `json:"image,omitempty"`
	WorkdirMode    string   `json:"workdir_mode,omitempty"`
	Workdir        string   `json:"workdir,omitempty"` // canonical host path for host workdirs
	Features       []string `json:"features,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"` // effective set
	Ports          []Port   `json:"ports,omitempty"`
	CPUs           string   `json:"cpus,omitempty"`
	Memory         string   `json:"memory,omitempty"`
	Unconfined     bool     `json:"unconfined_network,omitempty"`
	Privileged     bool     `json:"privileged,omitempty"` // declared privileged: true
	SessionCommand []string `json:"session_command,omitempty"`
	ConfigError    string   `json:"config_error,omitempty"`

	StartedAt *time.Time `json:"started_at,omitempty"`
	// Container is the observed ID of the box's running container; empty
	// when it is not running or the runtime lookup failed. A sample's
	// readings belong to the box only while they name this same container.
	Container string `json:"container,omitempty"`
	// RuntimeMode is the running container's classified launch mode
	// (privileged or confined); empty when the box is not running, the
	// runtime lookup failed or its evidence disagrees, with
	// RuntimeModeReason set in the last case. RuntimePrivileged reports any
	// single privileged signal in that evidence.
	RuntimeMode       string `json:"runtime_mode,omitempty"`
	RuntimeModeReason string `json:"runtime_mode_reason,omitempty"`
	RuntimePrivileged bool   `json:"runtime_privileged,omitempty"`
}

type Port struct {
	HostIP    string `json:"host_ip"`
	Published int    `json:"published"`
	Target    int    `json:"target"`
	Protocol  string `json:"protocol"`
}

// Room is a chat server box and the boxes observed connected to it; declared
// features alone never create a link. A managed room's members come from its
// verified AIRC server's own per-account connection counts, mapped to
// enrolled boxes by the room declaration.
type Room struct {
	Server   string   `json:"server"`
	Ports    []int    `json:"ports"`
	Members  []string `json:"members"`
	External int      `json:"external"` // peers that are not boxes in this space
	Local    int      `json:"local"`    // clients inside the server box (a managed room's human)
	Error    string   `json:"error,omitempty"`

	Managed bool `json:"managed,omitempty"`
	// Participants lists a managed room's active enrolled boxes, connected
	// or not, in declaration order.
	Participants []Participant `json:"participants,omitempty"`
}

// Participant is one enrolled box as the verified server counts it.
// Connections are the account's current AIRC connections, from any client.
type Participant struct {
	Box         string `json:"box"`
	MemberID    string `json:"member_id"`
	Account     string `json:"account"`
	Connections int    `json:"connections"`
}

// Sample holds raw cumulative counters. Rates need two samples.
type Sample struct {
	CPU          CPUTimes   `json:"cpu"`
	Load         [3]float64 `json:"load"`
	MemTotal     uint64     `json:"mem_total"`
	MemAvailable uint64     `json:"mem_available"`
	Uptime       float64    `json:"uptime"`
	Pool         *Pool      `json:"pool,omitempty"`
	PoolError    string     `json:"pool_error,omitempty"`
	Disk         *Usage     `json:"disk,omitempty"`
	// SystemDisk is the VM's root filesystem: guest OS, container layers,
	// images, build cache and every box's writable files. Nil means it could
	// not be read; it is never shown as empty.
	SystemDisk *Usage               `json:"system_disk,omitempty"`
	Boxes      map[string]BoxSample `json:"boxes"`
	Error      string               `json:"error,omitempty"`
}

// CPUTimes is the aggregate /proc/stat line in clock ticks; Idle includes
// iowait.
type CPUTimes struct {
	Total uint64 `json:"total"`
	Idle  uint64 `json:"idle"`
}

// Pool is the shared workload cgroup: every box and build. Current is its
// whole charge, file cache included. Peak and OOM kills cover the pool's
// lifetime and are never attributed to one box. The kill count normally
// includes descendants, so a kill under a box's own limit can count too.
type Pool struct {
	CPUUsec  uint64 `json:"cpu_usec"`
	Current  uint64 `json:"current"`
	Max      uint64 `json:"max"` // 0 means unlimited
	Peak     uint64 `json:"peak"`
	OOMKills uint64 `json:"oom_kills"`
}

type Usage struct {
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

type BoxSample struct {
	CPUUsec    uint64  `json:"cpu_usec"`
	CPULimit   float64 `json:"cpu_limit"` // cores; 0 means unlimited
	MemCurrent uint64  `json:"mem_current"`
	MemMax     uint64  `json:"mem_max"` // 0 means no per-box limit, unless SignalMemMax is unavailable
	Pids       uint64  `json:"pids"`
	IORead     uint64  `json:"io_read"`
	IOWrite    uint64  `json:"io_write"`
	NetRx      uint64  `json:"net_rx"`
	NetTx      uint64  `json:"net_tx"`
	Error      string  `json:"error,omitempty"`
	// MemStat details MemCurrent, the cgroup's whole charge with file cache
	// included. It is nil when memory.stat could not be read
	// (SignalMemStat); MemCurrent stays valid without it.
	MemStat *MemStat `json:"mem_stat,omitempty"`
	// Container is the sampled container's ID. Counters of two different
	// containers with the same box name are never compared.
	Container string `json:"container,omitempty"`
	// Unavailable names the counters that could not be read (SignalIO,
	// SignalNet, SignalMemMax, SignalMemStat); their fields are zero or nil
	// but are not evidence of no traffic or no limit.
	Unavailable []string `json:"unavailable,omitempty"`
	// PTY is the session holder's workload counters, absent when the box has
	// no running session. PTYError means they could not be read: unknown
	// activity, never an idle session.
	PTY      *PTYCounters `json:"pty,omitempty"`
	PTYError string       `json:"pty_error,omitempty"`
}

// Counters a sample can lack.
const (
	SignalIO      = "io"
	SignalNet     = "net"
	SignalMemMax  = "mem_max"
	SignalMemStat = "mem_stat"
)

// MemStat holds the memory.stat keys the view uses. A nil field was absent
// from the file; zero is a real value. The kernel's categories overlap (file
// includes shmem; inactive_file is an LRU list, not a promise that those
// pages can be reclaimed), so they do not partition MemCurrent.
type MemStat struct {
	Anon         *uint64 `json:"anon,omitempty"`
	File         *uint64 `json:"file,omitempty"`
	Shmem        *uint64 `json:"shmem,omitempty"`
	InactiveFile *uint64 `json:"inactive_file,omitempty"`
}

// PTYCounters are cumulative bytes the agent's PTY produced and participant
// input the holder accepted. They restart with each holder; Session tells
// two holders apart, so a difference is only taken within one session.
type PTYCounters struct {
	Session      string `json:"session"`
	OutputBytes  uint64 `json:"output_bytes"`
	InputPackets uint64 `json:"input_packets"`
	InputBytes   uint64 `json:"input_bytes"`
}

// Encoder writes one frame per line.
type Encoder struct{ w io.Writer }

func NewEncoder(w io.Writer) *Encoder { return &Encoder{w: w} }

func (e *Encoder) Encode(f Frame) error {
	f.Version = Version
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(raw) > MaxFrame {
		return errors.New("telemetry frame exceeds size limit")
	}
	_, err = e.w.Write(append(raw, '\n'))
	return err
}

// Decoder reads frames written by Encoder, refusing oversized lines and
// unknown shapes.
type Decoder struct{ s *bufio.Scanner }

func NewDecoder(r io.Reader) *Decoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64<<10), MaxFrame+1)
	return &Decoder{s: s}
}

func (d *Decoder) Decode() (Frame, error) {
	var f Frame
	if !d.s.Scan() {
		if err := d.s.Err(); err != nil {
			return f, err
		}
		return f, io.EOF
	}
	if err := json.Unmarshal(d.s.Bytes(), &f); err != nil {
		return f, fmt.Errorf("decode telemetry frame: %w", err)
	}
	if f.Version != Version {
		return f, fmt.Errorf("telemetry frame version %d, want %d", f.Version, Version)
	}
	switch {
	case f.Kind == KindInventory && f.Inventory != nil && f.Sample == nil:
	case f.Kind == KindSample && f.Sample != nil && f.Inventory == nil:
	default:
		return f, fmt.Errorf("invalid telemetry frame kind %q", f.Kind)
	}
	return f, nil
}
