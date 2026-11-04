package observe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/safepath"
	"golang.org/x/sys/unix"
)

// Health is the state of one generation's reports as of a read. It is shown
// beside the resume outcome and never changes it.
type Health string

const (
	// HealthOK: event files are present and well-formed.
	HealthOK Health = "ok"
	// HealthNoReport: nothing was written. There may be no hook, interpreter,
	// bind or directory, or every write was dropped; absence never means the
	// agent failed.
	HealthNoReport Health = "no-report"
	// HealthGap: reports were lost to rotation, or a file was rewritten.
	HealthGap Health = "gap"
	// HealthInvalid: an entry is not a plain file, or a line is malformed.
	HealthInvalid Health = "invalid"
	// HealthUnsupported: the container predates the observation bind; set
	// by callers from inspect. Only a normal recreate enables it.
	HealthUnsupported Health = "unsupported"
	// HealthUnavailable: Prepare or Read returned ErrUnavailable.
	HealthUnavailable Health = "unavailable"
)

var healthRank = map[Health]int{HealthNoReport: 0, HealthOK: 1, HealthGap: 2, HealthInvalid: 3}

// Worse is the more severe of two read healths, so a generation's health
// never improves past a gap or invalid report once one was seen.
func Worse(a, b Health) Health {
	if healthRank[b] > healthRank[a] {
		return b
	}
	return a
}

// ValidHealth reports whether h is one this package or its callers report.
func ValidHealth(h Health) bool {
	switch h {
	case HealthOK, HealthNoReport, HealthGap, HealthInvalid, HealthUnsupported, HealthUnavailable:
		return true
	}
	return false
}

// ValidModel, ValidEffort, ValidVersion and Valid (on Context) apply the
// reader's field rules to stored values.
func ValidModel(s string) bool   { return s != "" && printable(s, 128) }
func ValidEffort(s string) bool  { return slices.Contains(efforts, s) }
func ValidVersion(s string) bool { return s != "" && printable(s, 32) }

// ContextTokens bounds a reported context window or token count.
const ContextTokens = 1_000_000_000

// Context is a reported context window: its size in tokens and the used
// percentage, both as the agent reported them, and the used tokens when the
// report carried them (Used < 0 otherwise). An agent that reports only the
// tokens used has an unknown size: Size 0 and Percent -1, with Used known.
type Context struct {
	Size    int64
	Used    int64
	Percent int
}

// Valid reports whether c is within the reader's bounds.
func (c Context) Valid() bool {
	if c.Size == 0 {
		return c.Percent == -1 && c.Used >= 0 && c.Used <= ContextTokens
	}
	return c.Size > 0 && c.Size <= ContextTokens && c.Used >= -1 && c.Used <= ContextTokens && c.Percent >= 0 && c.Percent <= 100
}

// Agent states a report may carry. A waiting state names what the agent
// waits for; failed is an API error the agent reported: one that ended a
// Claude turn, or one Codex model request, which a later completed request
// of the same conversation clears.
const (
	StateWorking           = "working"
	StateIdle              = "idle"
	StateWaitingPermission = "waiting_permission"
	StateWaitingInput      = "waiting_input"
	StateWaitingDialog     = "waiting_dialog"
	StateFailed            = "failed"
)

var states = []string{StateWorking, StateIdle, StateWaitingPermission, StateWaitingInput, StateWaitingDialog, StateFailed}

// ValidState reports whether s is a state the reader accepts.
func ValidState(s string) bool { return slices.Contains(states, s) }

// Waiting reports whether s is one of the waiting states.
func Waiting(s string) bool {
	return s == StateWaitingPermission || s == StateWaitingInput || s == StateWaitingDialog
}

// StatusLine is the event name of a status line report. Hook events keep
// Claude's names; this one is the writer's own.
const StatusLine = "statusline"

// Query is the event name of the Codex writer's daemon read, at hook time or
// at refresh: the model, effort and optionally the state of the thread named
// by its session_id. The ID only guards the values: the fold applies them
// while that thread is the box's current conversation, and a query never
// sets, learns or switches a conversation. An older writer's unlabelled query
// still parses; the fold drops it.
const Query = "query"

// Cursor is the position after the last consumed line of one generation:
// events file K, byte Offset in it, and the SHA-256 of the line ending there
// so a rewritten file is noticed. The zero value starts at the beginning.
type Cursor struct {
	Generation uint64
	K          uint64
	Offset     int64
	Last       [32]byte
}

// MarshalText encodes the cursor as an opaque record field.
func (c Cursor) MarshalText() ([]byte, error) {
	return fmt.Appendf(nil, "%d:%d:%d:%x", c.Generation, c.K, c.Offset, c.Last), nil
}

// UnmarshalText decodes MarshalText's form strictly.
func (c *Cursor) UnmarshalText(text []byte) error {
	parts := strings.Split(string(text), ":")
	if len(parts) != 4 || len(parts[3]) != 2*len(c.Last) {
		return errors.New("invalid observation cursor")
	}
	gen, e1 := strconv.ParseUint(parts[0], 10, 64)
	k, e2 := strconv.ParseUint(parts[1], 10, 64)
	off, e3 := strconv.ParseInt(parts[2], 10, 64)
	last, e4 := hex.DecodeString(parts[3])
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || off < 0 || off > FileBytes {
		return errors.New("invalid observation cursor")
	}
	*c = Cursor{Generation: gen, K: k, Offset: off}
	copy(c.Last[:], last)
	return nil
}

// Observation is one validated, agent-reported event line.
type Observation struct {
	Generation uint64
	// K and Offset place the end of the line: a per-generation sequence.
	K         uint64
	Offset    int64
	Event     string
	SessionID string
	Source    string
	Model     string
	Effort    string
	// Version comes only from status line reports, Context from status
	// line, query and PostModelRequest reports.
	Version string
	Context *Context
	// State is the agent's state as this report gives it, or empty.
	State string
	// Subagent events are kept as evidence and never update box values.
	Subagent bool
	AgentTS  time.Time
	ReadAt   time.Time
}

// Result is what one Read consumed.
type Result struct {
	Observations []Observation
	Cursor       Cursor
	Health       Health
	// Rejected counts malformed or oversized lines that were skipped.
	Rejected int
	// Ignored counts regular files with names the writer never uses.
	Ignored int
}

// Read returns the complete event lines of generation gen after cur; a cursor
// of another generation starts over. It walks below the verified root without
// following links, reads each events file through safepath as a regular,
// singly linked file owned by uid within FileBytes, and validates every line.
// A partial last line waits for the next read. Root failures are
// ErrUnavailable; tampering and loss are reported as Health.
func Read(dir string, gen uint64, cur Cursor, uid int, readAt time.Time) (Result, error) {
	return ReadWithin(context.Background(), dir, gen, cur, uid, readAt)
}

// readEventsFile is replaceable so tests can model a slow filesystem.
var readEventsFile = safepath.ReadFileInfo

// ReadWithin is Read with a cooperative deadline. The context is checked
// before and after every step (the root, the listing, each file, every 256
// lines); once it ends no further step starts and the read returns
// ErrUnavailable with the context's error and no partial result. A step in
// progress is never interrupted, so a read can overrun the deadline by one
// step: on the local tmpfs a no-follow open, stat or listing, or one read of
// at most FileBytes+1 (regular files only, opened O_NONBLOCK). A kernel call
// that hangs is not bounded; nothing here cancels a syscall.
func ReadWithin(ctx context.Context, dir string, gen uint64, cur Cursor, uid int, readAt time.Time) (Result, error) {
	if cur.Generation != gen {
		cur = Cursor{Generation: gen}
	}
	stopped := func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return nil
	}
	if err := stopped(); err != nil {
		return Result{Cursor: cur}, err
	}
	res := Result{Cursor: cur, Health: HealthNoReport}
	root, err := openRoot(dir)
	if err != nil {
		return res, err
	}
	defer func() { _ = unix.Close(root) }()
	consumed := cur.K != 0 || cur.Offset != 0
	gd, err := unix.Openat(root, genName(gen), dirFlags, 0)
	if errors.Is(err, unix.ENOENT) {
		if consumed {
			// Removed after reports were read from it (Prepare's dropped
			// previous generation, or a pruned old one): its tail is lost.
			res.Health = HealthGap
		}
		return res, nil
	}
	if err != nil {
		res.Health = HealthInvalid
		return res, nil
	}
	defer func() { _ = unix.Close(gd) }()
	var st unix.Stat_t
	if unix.Fstat(gd, &st) != nil || st.Uid != uint32(uid) {
		res.Health = HealthInvalid
		return res, nil
	}
	files, ignored, ok := listEvents(gd, uid)
	if err := stopped(); err != nil {
		return Result{Cursor: cur}, err
	}
	res.Ignored = ignored
	if !ok {
		res.Health = HealthInvalid
		return res, nil
	}
	if len(files) == 0 {
		if consumed {
			res.Health = HealthGap
		}
		return res, nil
	}
	start := slices.IndexFunc(files, func(k uint64) bool { return k >= cur.K })
	if start < 0 {
		// Files never go backwards for an honest writer.
		res.Health = HealthInvalid
		return res, nil
	}
	health := HealthOK
	off, last := cur.Offset, cur.Last
	if files[start] != cur.K {
		// The cursor's file was rotated away: its unread tail is lost.
		health, off, last = HealthGap, 0, [32]byte{}
	}
	for i := start; i < len(files); i++ {
		k := files[i]
		if i > start {
			if k != files[i-1]+1 {
				health = Worse(health, HealthGap)
			}
			off, last = 0, [32]byte{}
		}
		rel := filepath.Join(genName(gen), eventsName(k))
		data, info, err := readEventsFile(dir, rel, FileBytes)
		if stop := stopped(); stop != nil {
			return Result{Cursor: cur}, stop
		}
		if err != nil || !singleOwned(info, uid) {
			res.Health = HealthInvalid
			return res, nil
		}
		if off > int64(len(data)) || (off > 0 && sha256.Sum256(data[lineStart(data, off):off]) != last) {
			// Truncated or rewritten since the last read: start the file over.
			health, off = Worse(health, HealthGap), 0
		}
		for n := 1; ; n++ {
			if n%256 == 0 {
				if err := stopped(); err != nil {
					return Result{Cursor: cur}, err
				}
			}
			nl := bytes.IndexByte(data[off:], '\n')
			if nl < 0 {
				break
			}
			end := off + int64(nl) + 1
			line := data[off:end]
			if o, ok := parseLine(line, gen, k, end, readAt); ok {
				res.Observations = append(res.Observations, o)
			} else {
				res.Rejected++
				health = Worse(health, HealthInvalid)
			}
			off, last = end, sha256.Sum256(line)
		}
		if i < len(files)-1 && off < int64(len(data)) {
			// A partial line that the next file will never complete.
			res.Rejected++
			health = Worse(health, HealthInvalid)
		}
		res.Cursor = Cursor{Generation: gen, K: k, Offset: off, Last: last}
	}
	if res.Cursor.K == 0 && res.Cursor.Offset == 0 && health == HealthOK {
		health = HealthNoReport
	}
	res.Health = health
	return res, nil
}

func eventsName(k uint64) string { return "events." + strconv.FormatUint(k, 10) + ".jsonl" }

// listEvents returns the sorted events file indices. Any entry that is not a
// regular file, including the lock, makes the generation invalid.
func listEvents(gd, uid int) (files []uint64, ignored int, ok bool) {
	names, err := readNames(gd, RootInodes)
	if err != nil {
		return nil, 0, false
	}
	for _, name := range names {
		var st unix.Stat_t
		if unix.Fstatat(gd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
			return nil, 0, false
		}
		if k, isEvents := parseEventsName(name); isEvents {
			files = append(files, k)
		} else if name != "events.lock" {
			ignored++
		}
	}
	slices.Sort(files)
	return files, ignored, true
}

func parseEventsName(name string) (uint64, bool) {
	digits, ok := strings.CutPrefix(name, "events.")
	if !ok {
		return 0, false
	}
	if digits, ok = strings.CutSuffix(digits, ".jsonl"); !ok || digits == "" || len(digits) > 19 {
		return 0, false
	}
	k, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || strconv.FormatUint(k, 10) != digits {
		return 0, false
	}
	return k, true
}

func singleOwned(info interface{ Sys() any }, uid int) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink == 1 && st.Uid == uint32(uid)
}

// lineStart is the start of the line ending at end.
func lineStart(data []byte, end int64) int64 {
	return int64(bytes.LastIndexByte(data[:end-1], '\n') + 1)
}

// wireEvent is the writer's line format; see internal/features/agent-observe.py.
// Wrapped, Handover and DurMS are the writer's own diagnostics: validated,
// then dropped.
type wireEvent struct {
	V         int      `json:"v"`
	Event     string   `json:"event"`
	SessionID string   `json:"session_id"`
	Source    string   `json:"source"`
	Model     string   `json:"model"`
	Effort    string   `json:"effort"`
	Subagent  bool     `json:"subagent"`
	PID       int      `json:"pid"`
	TS        string   `json:"ts"`
	DurMS     *float64 `json:"dur_ms"`
	Version   string   `json:"version"`
	CtxSize   *int64   `json:"ctx_size"`
	CtxUsed   *int64   `json:"ctx_used"`
	CtxPct    *int     `json:"ctx_pct"`
	Wrapped   *bool    `json:"wrapped"`
	Handover  string   `json:"handover"`
	State     string   `json:"state"`
}

// statusLineOnly reports whether w carries a field only status line lines may.
func (w wireEvent) statusLineOnly() bool {
	return w.Version != "" || w.Wrapped != nil || w.Handover != ""
}

// hasContext reports whether w carries any context field.
func (w wireEvent) hasContext() bool { return w.CtxSize != nil || w.CtxUsed != nil || w.CtxPct != nil }

// context is the line's context report: nil without one, ok false when it is
// malformed (a percentage without a size, or a field out of bounds). Used
// tokens alone are a context of unknown size.
func (w wireEvent) context() (c *Context, ok bool) {
	if !w.hasContext() {
		return nil, true
	}
	if w.CtxSize == nil && w.CtxPct == nil {
		c = &Context{Used: *w.CtxUsed, Percent: -1}
		return c, c.Valid()
	}
	if w.CtxSize == nil || w.CtxPct == nil {
		return nil, false
	}
	c = &Context{Size: *w.CtxSize, Used: -1, Percent: *w.CtxPct}
	if w.CtxUsed != nil {
		c.Used = *w.CtxUsed
		if c.Used < 0 {
			return nil, false
		}
	}
	return c, c.Valid()
}

var (
	// Claude conversation IDs are canonical lowercase UUIDs (profile id_format).
	sessionIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	events           = []string{"SessionStart", "SessionEnd", "PostModelSwitch", "Stop", "SubagentStop", "SubagentStart", StatusLine, Query,
		"UserPromptSubmit", "Notification", "StopFailure", "PostModelRequest", "PermissionRequest"}
	// stateEvents may carry a state; contextEvents a context.
	stateEvents   = []string{"SessionStart", "SessionEnd", "Stop", StatusLine, Query, "UserPromptSubmit", "Notification", "StopFailure", "PostModelRequest", "PermissionRequest"}
	contextEvents = []string{StatusLine, Query, "PostModelRequest"}
	// stateOnlyEvents carry a state and nothing the agent said: no prompt,
	// message, tool input, model or context.
	stateOnlyEvents = []string{"Notification", "PermissionRequest"}
	handovers       = []string{"too-large", "unavailable"}
	sources         = []string{"startup", "resume", "fork", "clear", "compact"}
	// Claude's levels plus Codex's named ones; a model-defined custom value
	// is never written, so it stays unknown.
	efforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "persistent"}
)

func parseLine(line []byte, gen, k uint64, end int64, readAt time.Time) (Observation, bool) {
	if len(line) > LineBytes {
		return Observation{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var w wireEvent
	if dec.Decode(&w) != nil || dec.InputOffset() != int64(len(line)-1) {
		return Observation{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, w.TS)
	ctx, ctxOK := w.context()
	statusLine := w.Event == StatusLine
	switch {
	case err != nil || len(w.TS) > 40 || w.V != 1 || w.PID < 1 || !slices.Contains(events, w.Event):
		return Observation{}, false
	case w.SessionID != "" && !sessionIDPattern.MatchString(w.SessionID):
		return Observation{}, false
	case w.Source != "" && (w.Event != "SessionStart" || !slices.Contains(sources, w.Source)):
		return Observation{}, false
	case w.Effort != "" && !slices.Contains(efforts, w.Effort):
		return Observation{}, false
	case !printable(w.Model, 128):
		return Observation{}, false
	case w.DurMS != nil && (*w.DurMS < 0 || *w.DurMS > 60_000):
		return Observation{}, false
	case statusLine != (w.Wrapped != nil) || (!statusLine && w.statusLineOnly()) || (statusLine && w.Subagent):
		return Observation{}, false
	case !ctxOK || !printable(w.Version, 32):
		return Observation{}, false
	case w.Event == Query && (w.Model == "" || w.Effort == "" || w.Subagent):
		return Observation{}, false
	case w.Handover != "" && (*w.Wrapped || !slices.Contains(handovers, w.Handover)):
		return Observation{}, false
	case w.hasContext() && !slices.Contains(contextEvents, w.Event):
		return Observation{}, false
	case w.State != "" && (!ValidState(w.State) || !slices.Contains(stateEvents, w.Event) || w.Subagent):
		return Observation{}, false
	case slices.Contains(stateOnlyEvents, w.Event) && (w.State == "" || w.Model != "" || w.Effort != "" || w.Source != ""):
		return Observation{}, false
	}
	return Observation{
		Generation: gen, K: k, Offset: end, Event: w.Event, SessionID: w.SessionID, Source: w.Source,
		Model: w.Model, Effort: w.Effort, Version: w.Version, Context: ctx, State: w.State,
		Subagent: w.Subagent || w.Event == "SubagentStop" || w.Event == "SubagentStart", AgentTS: ts, ReadAt: readAt,
	}, true
}

func printable(s string, limit int) bool {
	if len(s) > limit {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}
