//go:build linux

package observe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	sidA = "0f8fad5b-d9cb-469f-a165-70867728950e"
	sidB = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
)

// fakeRoot stands in for the capped tmpfs: an ordinary 0755 directory on the
// test filesystem, owned by the test user, with mountinfo and mount faked.
type fakeRoot struct {
	dir     string
	mounts  int
	umounts int
	info    string
}

func newFakeRoot(t *testing.T) *fakeRoot {
	t.Helper()
	run := t.TempDir()
	f := &fakeRoot{dir: filepath.Join(run, "agent-observe", "dev")}
	var fs unix.Statfs_t
	if err := unix.Statfs(run, &fs); err != nil {
		t.Fatal(err)
	}
	saved := []any{rootFS, rootUID, rootGID, mountTmpfs, unmountRoot, mountInfo}
	t.Cleanup(func() {
		rootFS, rootUID, rootGID = saved[0].(int64), saved[1].(int), saved[2].(int)
		mountTmpfs, unmountRoot, mountInfo = saved[3].(func(string) error), saved[4].(func(string) error), saved[5].(func() ([]byte, error))
	})
	rootFS, rootUID, rootGID = int64(fs.Type), os.Getuid(), os.Getgid()
	mountTmpfs = func(dir string) error {
		f.mounts++
		f.info = mountLine(dir, "tmpfs", "rw,nosuid,nodev,noexec", "rw,size=2048k,nr_inodes=32,mode=755,inode64")
		return os.Chmod(dir, 0o755) // the tmpfs root's mode=0755
	}
	unmountRoot = func(string) error { f.umounts++; f.info = ""; return nil }
	mountInfo = func() ([]byte, error) { return []byte("22 1 0:21 / /run rw - tmpfs tmpfs rw\n" + f.info), nil }
	return f
}

func mountLine(dir, fstype, perMount, super string) string {
	return fmt.Sprintf("99 22 0:55 / %s %s shared:1 - %s yoloctl-observe %s\n", strings.ReplaceAll(dir, " ", `\040`), perMount, fstype, super)
}

// mountedRoot is a prepared fake root, as after EnsureRoot.
func mountedRoot(t *testing.T) *fakeRoot {
	t.Helper()
	f := newFakeRoot(t)
	if err := EnsureRoot(f.dir); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestEnsureRootMountsOnceAndVerifies(t *testing.T) {
	f := newFakeRoot(t)
	if err := EnsureRoot(f.dir); err != nil || f.mounts != 1 {
		t.Fatalf("first EnsureRoot: %v mounts=%d", err, f.mounts)
	}
	if err := EnsureRoot(f.dir); err != nil || f.mounts != 1 {
		t.Fatalf("an existing capped tmpfs is verified, not remounted: %v mounts=%d", err, f.mounts)
	}
	if st, err := os.Stat(filepath.Dir(f.dir)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("the parent stays root-private: %v %v", st.Mode(), err)
	}
	if err := os.Chmod(f.dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := EnsureRoot(f.dir); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a root with another mode is refused: %v", err)
	}
}

func TestEnsureRootRefusesForeignOrUncappedMounts(t *testing.T) {
	for name, line := range map[string][2]string{
		"ext4":       {"ext4", "rw,size=2048k,nr_inodes=32,mode=755"},
		"no size":    {"tmpfs", "rw,nr_inodes=32,mode=755"},
		"big":        {"tmpfs", "rw,size=65536k,nr_inodes=32,mode=755"},
		"no inodes":  {"tmpfs", "rw,size=2048k,mode=755"},
		"other mode": {"tmpfs", "rw,size=2048k,nr_inodes=32,mode=777"},
	} {
		f := newFakeRoot(t)
		f.info = mountLine(f.dir, line[0], "rw,nosuid,nodev,noexec", line[1])
		if err := EnsureRoot(f.dir); err == nil || f.mounts != 0 {
			t.Errorf("%s: adopted: %v", name, err)
		}
		if err := RemoveRoot(f.dir); err == nil || f.umounts != 0 {
			t.Errorf("%s: a foreign mount is never unmounted: %v", name, err)
		}
	}
	f := newFakeRoot(t)
	f.info = mountLine(f.dir, "tmpfs", "rw,nodev,noexec", "rw,size=2048k,nr_inodes=32,mode=755")
	if err := EnsureRoot(f.dir); err == nil {
		t.Error("a tmpfs without nosuid is adopted")
	}
}

func TestRemoveRootUnmountsAndRemovesWithoutFollowing(t *testing.T) {
	f := mountedRoot(t)
	outside := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRoot(f.dir); err != nil || f.umounts != 1 {
		t.Fatalf("RemoveRoot: %v umounts=%d", err, f.umounts)
	}
	if _, err := os.Lstat(f.dir); !os.IsNotExist(err) {
		t.Fatal("the mount point remains")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("removal followed a link out of the root")
	}
	if err := RemoveRoot(f.dir); err != nil {
		t.Fatalf("a missing root is not an error: %v", err)
	}
}

func TestPrepareCreatesPrivateGenerationsAndKeepsTwo(t *testing.T) {
	f := mountedRoot(t)
	uid, gid := os.Getuid(), os.Getgid()
	outside := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for gen := uint64(1); gen <= 3; gen++ {
		if err := prep(f.dir, gen, uid, gid); err != nil {
			t.Fatalf("prep(%d): %v", gen, err)
		}
		g := filepath.Join(f.dir, genName(gen))
		if st, err := os.Lstat(g); err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
			t.Fatalf("gen-%d: %v %v", gen, st, err)
		}
		// A sandbox process fills its directory with links, a FIFO and a deep tree.
		deep := filepath.Join(g, "a", "b", "c")
		if err := os.MkdirAll(deep, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(deep, "out")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Dir(outside), filepath.Join(g, "dir-link")); err != nil {
			t.Fatal(err)
		}
		if err := unix.Mkfifo(filepath.Join(g, "fifo"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(f.dir)
	if err != nil || len(entries) != 2 || entries[0].Name() != "gen-2" || entries[1].Name() != "gen-3" {
		t.Fatalf("only the current and previous generation stay: %v %v", entries, err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("pruning followed a link")
	}
}

func TestPrepareReusesOnlyAnUntouchedGeneration(t *testing.T) {
	f := mountedRoot(t)
	uid, gid := os.Getuid(), os.Getgid()
	if err := prep(f.dir, 4, uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := prep(f.dir, 4, uid, gid); err != nil {
		t.Fatalf("an untouched directory from an earlier attempt is reused: %v", err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "gen-4", "events.0.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prep(f.dir, 4, uid, gid); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a used generation directory is never reused: %v", err)
	}
	if err := os.Chmod(filepath.Join(f.dir, "gen-4"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prep(f.dir, 5, uid, gid); err != nil {
		t.Fatalf("a newer generation still works: %v", err)
	}
	if err := prep(f.dir, 0, uid, gid); !errors.Is(err, ErrUnavailable) {
		t.Fatal("generation 0 is not a launch")
	}
}

func TestPrepareIsUnavailableOnAnUnverifiedOrExhaustedRoot(t *testing.T) {
	f := newFakeRoot(t)
	if err := prep(f.dir, 1, os.Getuid(), os.Getgid()); !errors.Is(err, ErrRootUnverified) {
		t.Fatalf("no root is an unverified root: %v", err)
	}
	g := mountedRoot(t)
	g.info = mountLine(g.dir, "tmpfs", "rw,nosuid,nodev,noexec", "rw,size=65536k,mode=755")
	if err := prep(g.dir, 1, os.Getuid(), os.Getgid()); !errors.Is(err, ErrRootUnverified) {
		t.Fatalf("an uncapped mount over the root is unverified: %v", err)
	}
	if _, err := Read(g.dir, 1, Cursor{}, os.Getuid(), time.Now()); !errors.Is(err, ErrRootUnverified) {
		t.Fatalf("reads verify the root too: %v", err)
	}
	f = mountedRoot(t)
	// More entries than the inode cap allows can only be junk; prune refuses
	// an unbounded walk instead of looping.
	for i := range RootInodes + 1 {
		if err := os.WriteFile(filepath.Join(f.dir, fmt.Sprintf("junk-%d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := prep(f.dir, 1, os.Getuid(), os.Getgid()); !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrRootUnverified) {
		t.Fatalf("an exhausted root is an optional failure, not an unverified one: %v", err)
	}
}

// prep is Prepare for a caller without proof that the previous holder is gone.
func prep(dir string, gen uint64, uid, gid int) error {
	_, err := Prepare(dir, gen, uid, gid, false)
	return err
}

// event renders a writer line.
func event(name, extra string) string {
	return `{"v":1,"event":"` + name + `","pid":42,"ts":"2026-10-22T06:00:00.5Z"` + extra + "}\n"
}

func writeEvents(t *testing.T, f *fakeRoot, gen, k uint64, lines ...string) string {
	t.Helper()
	path := filepath.Join(f.dir, genName(gen), eventsName(k))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(strings.Join(lines, "")); err != nil {
		t.Fatal(err)
	}
	return path
}

func preparedGen(t *testing.T, gen uint64) *fakeRoot {
	t.Helper()
	f := mountedRoot(t)
	if err := prep(f.dir, gen, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	return f
}

func read(t *testing.T, f *fakeRoot, gen uint64, cur Cursor) Result {
	t.Helper()
	res, err := Read(f.dir, gen, cur, os.Getuid(), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestReadConsumesCompleteLinesOnce(t *testing.T) {
	f := preparedGen(t, 1)
	if res := read(t, f, 1, Cursor{}); res.Health != HealthNoReport || len(res.Observations) != 0 {
		t.Fatalf("an empty generation is no report: %+v", res)
	}
	start := event("SessionStart", `,"session_id":"`+sidA+`","source":"resume","model":"claude-fable-5-1"`)
	path := writeEvents(t, f, 1, 0, start, event("Stop", `,"effort":"high"`), `{"v":1,"event":"PostModelSwi`)
	res := read(t, f, 1, Cursor{})
	if res.Health != HealthOK || len(res.Observations) != 2 || res.Rejected != 0 {
		t.Fatalf("two complete lines: %+v", res)
	}
	o := res.Observations[0]
	if o.Event != "SessionStart" || o.SessionID != sidA || o.Source != "resume" || o.Model != "claude-fable-5-1" ||
		o.Generation != 1 || o.ReadAt != time.Unix(100, 0) || o.AgentTS.IsZero() || res.Observations[1].Effort != "high" {
		t.Fatalf("observation: %+v", o)
	}
	again := read(t, f, 1, res.Cursor)
	if len(again.Observations) != 0 || again.Cursor != res.Cursor || again.Health != HealthOK {
		t.Fatalf("a reread changes nothing: %+v", again)
	}
	file, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	_, _ = file.WriteString(`tch","model":"claude-opus-5-5","session_id":"` + sidA + `","pid":42,"ts":"2026-10-22T06:01:00Z"}` + "\n")
	_ = file.Close()
	next := read(t, f, 1, res.Cursor)
	if len(next.Observations) != 1 || next.Observations[0].Model != "claude-opus-5-5" || next.Observations[0].Offset <= res.Cursor.Offset {
		t.Fatalf("the completed line is read once: %+v", next)
	}
	text, _ := next.Cursor.MarshalText()
	var round Cursor
	if err := round.UnmarshalText(text); err != nil || round != next.Cursor {
		t.Fatalf("cursor text round trip: %s %v", text, err)
	}
	for _, bad := range []string{"", "1:0:0", "1:0:-1:" + strings.Repeat("0", 64), "x:0:0:" + strings.Repeat("0", 64), "1:0:0:zz"} {
		if (&Cursor{}).UnmarshalText([]byte(bad)) == nil {
			t.Errorf("cursor %q accepted", bad)
		}
	}
}

func TestReadFollowsRotationAndReportsGaps(t *testing.T) {
	f := preparedGen(t, 2)
	writeEvents(t, f, 2, 0, event("SessionStart", `,"source":"startup","session_id":"`+sidA+`"`))
	first := read(t, f, 2, Cursor{})
	writeEvents(t, f, 2, 0, event("Stop", ""))
	writeEvents(t, f, 2, 1, event("Stop", `,"effort":"max"`))
	res := read(t, f, 2, first.Cursor)
	if res.Health != HealthOK || len(res.Observations) != 2 || res.Cursor.K != 1 {
		t.Fatalf("file 0's tail then file 1: %+v", res)
	}
	// The writer started file 2 and removed file 0: the cursor still sits in
	// file 1, nothing was lost.
	writeEvents(t, f, 2, 2, event("SessionEnd", ""))
	_ = os.Remove(filepath.Join(f.dir, "gen-2", eventsName(0)))
	res = read(t, f, 2, res.Cursor)
	if res.Health != HealthOK || len(res.Observations) != 1 || res.Cursor.K != 2 {
		t.Fatalf("continuing across a rotation: %+v", res)
	}
	// A reader left behind in file 0 after two rotations lost its tail.
	stale := read(t, f, 2, first.Cursor)
	if stale.Health != HealthGap || len(stale.Observations) != 2 {
		t.Fatalf("a rotated-away cursor is a visible gap: %+v", stale)
	}
	// A fresh reader that never saw file 0 also reports the loss.
	if fresh := read(t, f, 2, Cursor{}); fresh.Health != HealthGap {
		t.Fatalf("missing first file: %+v", fresh)
	}
	// Missing indices between files are a gap too.
	g := preparedGen(t, 3)
	writeEvents(t, g, 3, 0, event("Stop", ""))
	writeEvents(t, g, 3, 2, event("Stop", ""))
	if res := read(t, g, 3, Cursor{}); res.Health != HealthGap || len(res.Observations) != 2 {
		t.Fatalf("hole between files: %+v", res)
	}
}

func TestReadNoticesRewrittenFiles(t *testing.T) {
	f := preparedGen(t, 1)
	path := writeEvents(t, f, 1, 0, event("Stop", `,"effort":"low"`))
	res := read(t, f, 1, Cursor{})
	if err := os.WriteFile(path, []byte(event("Stop", `,"effort":"max"`)), 0o600); err != nil {
		t.Fatal(err)
	}
	again := read(t, f, 1, res.Cursor)
	if again.Health != HealthGap || len(again.Observations) != 1 || again.Observations[0].Effort != "max" {
		t.Fatalf("a rewritten line is a gap and the file is reread: %+v", again)
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if res := read(t, f, 1, again.Cursor); res.Health != HealthGap {
		t.Fatalf("a truncated file is a gap: %+v", res)
	}
}

func TestReadRejectsTamperedEntriesWithoutFollowingOrBlocking(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte(event("Stop", "")), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, plant := range map[string]func(g string) error{
		"symlink events": func(g string) error { return os.Symlink(outside, filepath.Join(g, eventsName(0))) },
		"fifo events":    func(g string) error { return unix.Mkfifo(filepath.Join(g, eventsName(0)), 0o600) },
		"fifo lock":      func(g string) error { return unix.Mkfifo(filepath.Join(g, "events.lock"), 0o600) },
		"directory":      func(g string) error { return os.Mkdir(filepath.Join(g, "x"), 0o700) },
		"other link":     func(g string) error { return os.Symlink(outside, filepath.Join(g, "notes")) },
		"hard link": func(g string) error {
			p := filepath.Join(g, eventsName(0))
			if err := os.WriteFile(p, []byte(event("Stop", "")), 0o600); err != nil {
				return err
			}
			return os.Link(p, filepath.Join(g, "copy"))
		},
		"oversized": func(g string) error {
			line := event("Stop", "")
			return os.WriteFile(filepath.Join(g, eventsName(0)), []byte(strings.Repeat(line, FileBytes/len(line)+1)), 0o600)
		},
	} {
		f := preparedGen(t, 1)
		if err := plant(filepath.Join(f.dir, "gen-1")); err != nil {
			t.Fatal(name, err)
		}
		done := make(chan Result, 1)
		go func() {
			res, _ := Read(f.dir, 1, Cursor{}, os.Getuid(), time.Now())
			done <- res
		}()
		select {
		case res := <-done:
			if res.Health != HealthInvalid || len(res.Observations) != 0 {
				t.Errorf("%s: %+v", name, res)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the reader blocked", name)
		}
	}
	f := preparedGen(t, 1)
	writeEvents(t, f, 1, 0, event("Stop", ""))
	if res, err := Read(f.dir, 1, Cursor{}, os.Getuid()+1, time.Now()); err != nil || res.Health != HealthInvalid {
		t.Fatalf("another owner: %+v %v", res, err)
	}
	for _, name := range []string{"README", "7.jsonl", "events.07.jsonl", "events..jsonl", "xevents.1.jsonl"} {
		if err := os.WriteFile(filepath.Join(f.dir, "gen-1", name), []byte(event("Stop", "")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if res := read(t, f, 1, Cursor{}); res.Health != HealthOK || res.Ignored != 5 || len(res.Observations) != 1 {
		t.Fatalf("other regular files are ignored and counted, never read: %+v", res)
	}
}

// A query line may carry the agent CLI's version, read from the agent's own
// server; it is bounded like a status line's.
func TestReadAcceptsAVersionOnAQueryLine(t *testing.T) {
	f := preparedGen(t, 1)
	writeEvents(t, f, 1, 0,
		event(Query, `,"model":"gpt-5.6-luna","effort":"high","session_id":"`+sidA+`","version":"0.166.0"`),
		event(Query, `,"model":"gpt-5.6-luna","effort":"high","session_id":"`+sidA+`"`))
	res := read(t, f, 1, Cursor{})
	if res.Health != HealthOK || res.Rejected != 0 || len(res.Observations) != 2 {
		t.Fatalf("query lines with and without a version are accepted: %+v", res)
	}
	if res.Observations[0].Version != "0.166.0" || res.Observations[1].Version != "" {
		t.Fatalf("versions: %q %q", res.Observations[0].Version, res.Observations[1].Version)
	}
}

func TestReadValidatesEveryLine(t *testing.T) {
	f := preparedGen(t, 1)
	bad := []string{
		`{"v":2,"event":"Stop","pid":42,"ts":"2026-10-22T06:00:00Z"}` + "\n",
		event("Stop", `,"extra":1`),
		event("Unknown", ""),
		event("Stop", `,"session_id":"`+strings.ToUpper(sidA)+`"`),
		event("Stop", `,"source":"resume"`),
		event("SessionStart", `,"source":"other"`),
		event("Stop", `,"effort":"turbo"`),
		event("Stop", `,"model":"`+strings.Repeat("m", 129)+`"`),
		event("Stop", `,"model":"bad\u0007"`),
		`{"v":1,"event":"Stop","pid":0,"ts":"2026-10-22T06:00:00Z"}` + "\n",
		`{"v":1,"event":"Stop","pid":42,"ts":"yesterday"}` + "\n",
		event("Stop", "")[:len(event("Stop", ""))-1] + ` {}` + "\n",
		`{"v":1,"event":"Stop","pid":42,"ts":"2026-10-22T06:00:00Z","model":"` + strings.Repeat("m", LineBytes) + `"}` + "\n",
		"not json\n",
		event(StatusLine, `,"model":"x"`),
		event("Stop", `,"wrapped":false`),
		event("Stop", `,"version":"2.1.311"`),
		event("Stop", `,"ctx_size":200000,"ctx_pct":42`),
		event(StatusLine, `,"wrapped":false,"ctx_pct":42`),
		event(StatusLine, `,"wrapped":false,"ctx_size":200000`),
		event(StatusLine, `,"wrapped":false,"ctx_size":200000,"ctx_pct":101`),
		event(StatusLine, `,"wrapped":false,"ctx_size":0,"ctx_pct":1`),
		event(StatusLine, `,"wrapped":false,"ctx_size":200000,"ctx_pct":1,"ctx_used":-1`),
		event(StatusLine, `,"wrapped":false,"ctx_size":200000,"ctx_pct":1.5`),
		event(StatusLine, `,"wrapped":true,"handover":"too-large"`),
		event(StatusLine, `,"wrapped":false,"handover":"other"`),
		event(StatusLine, `,"wrapped":false,"subagent":true`),
		event(StatusLine, `,"wrapped":false,"version":"`+strings.Repeat("9", 33)+`"`),
		event(StatusLine, `,"wrapped":false,"dur_ms":-1`),
		event(Query, `,"model":"gpt-5.6-luna"`),
		event(Query, `,"effort":"high"`),
		event(Query, `,"model":"gpt-5.6-luna","effort":"high","session_id":"`+strings.ToUpper(sidA)+`"`),
		event(Query, `,"model":"gpt-5.6-luna","effort":"high","session_id":"not-a-uuid"`),
		event(Query, `,"model":"gpt-5.6-luna","effort":"high","subagent":true`),
		event(Query, `,"model":"gpt-5.6-luna","effort":"high","wrapped":false`),
		event(Query, `,"model":"gpt-5.6-luna","effort":"high","version":"`+strings.Repeat("9", 33)+`"`),
		event(Query, `,"model":"gpt-5.6-luna","effort":"high","version":"0.166.0\u001b[31m"`),
		event("PostModelRequest", `,"version":"0.166.0"`),
	}
	good := event("SubagentStop", `,"effort":"low","session_id":"`+sidB+`"`)
	writeEvents(t, f, 1, 0, append(bad, good)...)
	res := read(t, f, 1, Cursor{})
	if res.Health != HealthInvalid || res.Rejected != len(bad) || len(res.Observations) != 1 {
		t.Fatalf("every malformed line is rejected: %+v", res)
	}
	if o := res.Observations[0]; !o.Subagent || o.SessionID != sidB {
		t.Fatalf("subagent events are kept as subagent evidence: %+v", o)
	}
}

func TestReadIgnoresOtherGenerations(t *testing.T) {
	f := preparedGen(t, 1)
	writeEvents(t, f, 1, 0, event("SessionStart", `,"source":"startup","session_id":"`+sidA+`"`))
	old := read(t, f, 1, Cursor{})
	if err := prep(f.dir, 2, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	// A lingering process of launch 1 keeps writing into gen-1.
	writeEvents(t, f, 1, 0, event("Stop", ""))
	res := read(t, f, 2, old.Cursor)
	if res.Health != HealthNoReport || len(res.Observations) != 0 || res.Cursor != (Cursor{Generation: 2}) {
		t.Fatalf("launch 2 starts empty; gen-1 reports stay launch 1's: %+v", res)
	}
	if res, err := Read(f.dir, 3, Cursor{}, os.Getuid(), time.Now()); err != nil || res.Health != HealthNoReport {
		t.Fatalf("a generation without a directory: %+v %v", res, err)
	}
}

func TestStatHelpersSeeTheRealInode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil || !singleOwned(info, os.Getuid()) || singleOwned(info, os.Getuid()+1) {
		t.Fatal("singleOwned")
	}
	if _, ok := info.Sys().(*syscall.Stat_t); !ok {
		t.Fatal("stat type")
	}
}

// fullWhile makes creation fail with ENOSPC while the named entry exists,
// standing in for a tmpfs whose caps that entry's junk exhausted.
func fullWhile(t *testing.T, entry string) *int {
	t.Helper()
	saved := mkdirAt
	t.Cleanup(func() { mkdirAt = saved })
	attempts := 0
	mkdirAt = func(fd int, name string, mode uint32) error {
		attempts++
		var st unix.Stat_t
		if entry == "*" || unix.Fstatat(fd, entry, &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
			return unix.ENOSPC
		}
		return saved(fd, name, mode)
	}
	return &attempts
}

func plantJunk(t *testing.T, dir string) {
	t.Helper()
	for i := range 8 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("junk-%d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPreparePrunesObsoleteGenerationsBeforeCreating(t *testing.T) {
	f := mountedRoot(t)
	uid, gid := os.Getuid(), os.Getgid()
	for gen := uint64(1); gen <= 2; gen++ {
		if err := prep(f.dir, gen, uid, gid); err != nil {
			t.Fatal(err)
		}
	}
	plantJunk(t, filepath.Join(f.dir, "gen-1"))
	fullWhile(t, "gen-1")
	dropped, err := Prepare(f.dir, 3, uid, gid, false)
	if err != nil || dropped {
		t.Fatalf("junk in an obsolete generation must not block the next launch: %v dropped=%v", err, dropped)
	}
	entries, _ := os.ReadDir(f.dir)
	if len(entries) != 2 || entries[0].Name() != "gen-2" || entries[1].Name() != "gen-3" {
		t.Fatalf("only obsolete generations are pruned: %v", entries)
	}
}

func TestPrepareDropsAFullPreviousGenerationOnlyWithProof(t *testing.T) {
	f := mountedRoot(t)
	uid, gid := os.Getuid(), os.Getgid()
	if err := prep(f.dir, 1, uid, gid); err != nil {
		t.Fatal(err)
	}
	writeEvents(t, f, 1, 0, event("Stop", ""))
	read1 := read(t, f, 1, Cursor{})
	plantJunk(t, filepath.Join(f.dir, "gen-1"))
	attempts := fullWhile(t, "gen-1")
	if dropped, err := Prepare(f.dir, 2, uid, gid, false); !errors.Is(err, ErrUnavailable) || dropped || *attempts != 1 {
		t.Fatalf("without proof the old holder is gone, gen-1 stays: %v dropped=%v attempts=%d", err, dropped, *attempts)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "gen-1")); err != nil {
		t.Fatal("gen-1 was removed without proof")
	}
	*attempts = 0
	dropped, err := Prepare(f.dir, 2, uid, gid, true)
	if err != nil || !dropped || *attempts != 2 {
		t.Fatalf("with proof, gen-1 is dropped and creation retried once: %v dropped=%v attempts=%d", err, dropped, *attempts)
	}
	if res := read(t, f, 1, read1.Cursor); res.Health != HealthGap {
		t.Fatalf("the dropped generation's consumed history reads as a gap: %+v", res)
	}
	g := mountedRoot(t)
	if err := prep(g.dir, 1, uid, gid); err != nil {
		t.Fatal(err)
	}
	attempts = fullWhile(t, "*")
	if _, err := Prepare(g.dir, 2, uid, gid, true); !errors.Is(err, ErrUnavailable) || *attempts != 2 {
		t.Fatalf("a still-full root fails after exactly one retry: %v attempts=%d", err, *attempts)
	}
}

func TestPrepareLeavesFutureAndUnknownEntries(t *testing.T) {
	f := mountedRoot(t)
	uid, gid := os.Getuid(), os.Getgid()
	for _, name := range []string{"gen-9", "gen-01", "other", "gen-18446744073709551615"} {
		if err := os.Mkdir(filepath.Join(f.dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := prep(f.dir, 3, uid, gid); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(f.dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "gen-01,gen-18446744073709551615,gen-3,gen-9,other" {
		t.Fatalf("only obsolete canonical generations are evidence Prepare may discard: %v", names)
	}
}

// The Codex writer's lines: SubagentStart is subagent evidence, a query line
// carries the daemon's model and effort and the thread they were read for (an
// older writer's unlabelled query still parses), and Codex's named efforts
// parse.
func TestReadAcceptsCodexLines(t *testing.T) {
	f := preparedGen(t, 1)
	writeEvents(t, f, 1, 0,
		event("SessionStart", `,"source":"startup","session_id":"`+sidA+`","model":"gpt-5.6-luna"`),
		event("SubagentStart", `,"session_id":"`+sidB+`","model":"gpt-5.6-luna"`),
		event(Query, `,"model":"gpt-5.6-luna","effort":"minimal","session_id":"`+sidA+`"`),
		event(Query, `,"model":"gpt-5.6-luna","effort":"persistent"`))
	res := read(t, f, 1, Cursor{})
	if res.Health != HealthOK || len(res.Observations) != 4 {
		t.Fatalf("Codex lines are valid reports: %+v", res)
	}
	if sub := res.Observations[1]; !sub.Subagent || sub.SessionID != sidB {
		t.Fatalf("SubagentStart is subagent evidence: %+v", sub)
	}
	if q := res.Observations[2]; q.Event != Query || q.Model != "gpt-5.6-luna" || q.Effort != "minimal" || q.SessionID != sidA || q.Subagent {
		t.Fatalf("query line: %+v", q)
	}
	if q := res.Observations[3]; q.Event != Query || q.SessionID != "" {
		t.Fatalf("unlabelled query line: %+v", q)
	}
}

func TestReadAcceptsForkedSessionStarts(t *testing.T) {
	f := preparedGen(t, 1)
	writeEvents(t, f, 1, 0, event("SessionStart", `,"source":"fork","session_id":"`+sidB+`"`))
	if res := read(t, f, 1, Cursor{}); res.Health != HealthOK || len(res.Observations) != 1 || res.Observations[0].Source != "fork" {
		t.Fatalf("a forked session's start is a valid report: %+v", res)
	}
}

// The deadline is cooperative: the slow step in flight completes (one step of
// overrun), then no further step starts and no partial result is returned.
// This does not show a blocked syscall being interrupted; none is.
func TestReadWithinStopsAtTheDeadline(t *testing.T) {
	f := preparedGen(t, 1)
	for k := uint64(0); k < 3; k++ {
		writeEvents(t, f, 1, k, event("Stop", ""))
	}
	saved := readEventsFile
	t.Cleanup(func() { readEventsFile = saved })
	reads := 0
	readEventsFile = func(root, rel string, limit int64) ([]byte, os.FileInfo, error) {
		reads++
		time.Sleep(80 * time.Millisecond)
		return saved(root, rel, limit)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	res, err := ReadWithin(ctx, f.dir, 1, Cursor{}, os.Getuid(), time.Now())
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) || len(res.Observations) != 0 || reads != 1 {
		t.Fatalf("read past its deadline: %v reads=%d %+v", err, reads, res)
	}
	if res, err := ReadWithin(t.Context(), f.dir, 1, Cursor{}, os.Getuid(), time.Now()); err != nil || len(res.Observations) != 3 {
		t.Fatalf("an unbounded read still completes: %v %+v", err, res)
	}
}

// The writer's lines for the Claude Code 2.1.311 payloads of the S0 capture
// (scripts/test_agent_observe.py makes the same lines from the payloads).
func TestReadAcceptsTheClaudeVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "claude-2.1.311", "expected.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	f := preparedGen(t, 1)
	writeEvents(t, f, 1, 0, string(raw))
	res := read(t, f, 1, Cursor{})
	if res.Health != HealthOK || res.Rejected != 0 || len(res.Observations) != 6 {
		t.Fatalf("every captured line is accepted: %+v", res)
	}
	var events []string
	for _, o := range res.Observations {
		events = append(events, o.Event)
		if o.Event != StatusLine && (o.Version != "" || o.Context != nil) {
			t.Fatalf("version and context only come from the status line: %+v", o)
		}
	}
	if strings.Join(events, ",") != "SessionStart,Stop,SessionEnd,SessionStart,SessionStart,statusline" {
		t.Fatalf("events: %v", events)
	}
	resume, sl := res.Observations[4], res.Observations[5]
	if resume.Source != "resume" || resume.Model != "" {
		t.Fatalf("a resumed start carries no model: %+v", resume)
	}
	if sl.Model != "claude-opus-5-5" || sl.Effort != "high" || sl.Version != "2.1.311" || sl.Subagent ||
		sl.Context == nil || *sl.Context != (Context{Size: 1000000, Used: 39763, Percent: 4}) {
		t.Fatalf("status line: %+v %+v", sl, sl.Context)
	}
}

// Claude 2.1.312 D6/A+ capture-derived payloads and docs-backed input/dialog/failure fixtures;
// scripts/test_agent_observe.py checks the writer against these same lines.
func TestReadAcceptsTheClaudeStateVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "claude-2.1.312-state", "expected.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	f := preparedGen(t, 1)
	writeEvents(t, f, 1, 0, string(raw))
	res := read(t, f, 1, Cursor{})
	want := []string{StateWorking, StateIdle, StateWorking, StateIdle, StateWorking,
		StateWaitingPermission, StateWaitingPermission, StateWaitingInput, StateWaitingDialog, StateWaitingDialog, StateFailed}
	if res.Health != HealthOK || res.Rejected != 0 || len(res.Observations) != len(want) {
		t.Fatalf("every state vector must be accepted: %+v", res)
	}
	for i, ob := range res.Observations {
		if ob.State != want[i] || ob.Subagent || ob.SessionID != "04255c19-bd66-4b0b-afeb-30636678c393" {
			t.Errorf("vector %d: %+v, want state %s", i, ob, want[i])
		}
		if ob.Event == "Notification" && (ob.Model != "" || ob.Effort != "" || ob.Context != nil || ob.Source != "") {
			t.Errorf("notification must be state-only: %+v", ob)
		}
	}
}

// The writer's lines for the synthetic Codex 0.164 payloads
// (scripts/test_agent_observe.py makes the same lines from them).
func TestReadAcceptsTheCodexVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "codex-0.164-synthetic", "expected.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	f := preparedGen(t, 1)
	writeEvents(t, f, 1, 0, string(raw))
	res := read(t, f, 1, Cursor{})
	if res.Health != HealthOK || res.Rejected != 0 || len(res.Observations) != 8 {
		t.Fatalf("every Codex line is accepted: %+v", res)
	}
	if used := res.Observations[6]; used.Context == nil || used.Context.Used != 172000 || used.Context.Size != 0 || used.State != StateWorking || used.Model != "" {
		t.Fatalf("a completed model request reports the tokens used and working only: %+v", used)
	}
	if source, err := os.ReadFile(filepath.Join("testdata", "codex-0.164-synthetic", "SOURCE.txt")); err != nil || !strings.Contains(string(source), "They are not a capture") {
		t.Fatalf("the Codex vectors must stay labelled synthetic: %v", err)
	}
	if p := res.Observations[7]; p.State != StateWaitingPermission || p.Model != "" {
		t.Fatalf("a permission request is a state-only line: %+v", p)
	}
	if sub := res.Observations[2]; !sub.Subagent || sub.Event != "SubagentStart" {
		t.Fatalf("SubagentStart: %+v", sub)
	}
	if resume := res.Observations[3]; resume.Source != "resume" || resume.Model != "gpt-5.6-luna" {
		t.Fatalf("a Codex resume reports its model: %+v", resume)
	}
}

// The writer's lines for the synthetic agy status line payloads
// (scripts/test_agent_observe.py makes the same lines from them).
func TestReadAcceptsTheAgyVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "agy-docs-synthetic", "expected.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	f := preparedGen(t, 1)
	writeEvents(t, f, 1, 0, string(raw))
	res := read(t, f, 1, Cursor{})
	if res.Health != HealthOK || res.Rejected != 0 || len(res.Observations) != 7 {
		t.Fatalf("every agy line is accepted: %+v", res)
	}
	var states []string
	for _, o := range res.Observations {
		states = append(states, o.State)
	}
	if want := []string{StateIdle, StateWorking, StateWaitingPermission, StateWorking, StateFailed, "", ""}; !slices.Equal(states, want) {
		t.Fatalf("states = %q, want %q", states, want)
	}
	if c := res.Observations[0].Context; c == nil || *c != (Context{Size: 1048576, Used: 149230, Percent: 14}) {
		t.Fatalf("agy context: %+v", c)
	}
	if c := res.Observations[6].Context; c == nil || *c != (Context{Used: 9120, Percent: -1}) {
		t.Fatalf("an unknown agy window keeps the used tokens alone: %+v", c)
	}
}

func TestReadKeepsAContextWithoutUsedTokens(t *testing.T) {
	f := preparedGen(t, 1)
	writeEvents(t, f, 1, 0, event(StatusLine, `,"wrapped":false,"handover":"unavailable","ctx_size":200000,"ctx_pct":0,"dur_ms":0.2`))
	res := read(t, f, 1, Cursor{})
	if len(res.Observations) != 1 || res.Health != HealthOK {
		t.Fatalf("%+v", res)
	}
	if c := res.Observations[0].Context; c == nil || *c != (Context{Size: 200000, Used: -1, Percent: 0}) {
		t.Fatalf("a zero percentage is a value; unknown used tokens are -1: %+v", c)
	}
}

// A state is one of six words on the events that may carry it, never on a
// subagent line; Notification and PermissionRequest carry a state and
// nothing the agent said; used tokens alone are a context of unknown size.
func TestReadStateAndUsedOnlyContext(t *testing.T) {
	f := preparedGen(t, 1)
	good := []string{
		event("UserPromptSubmit", `,"state":"working"`),
		event("Notification", `,"state":"waiting_permission"`),
		event("PermissionRequest", `,"session_id":"40acee27-150e-430e-8d6a-b69fdbfa6b32","state":"waiting_permission"`),
		event("Stop", `,"state":"idle"`),
		event("StopFailure", `,"state":"failed"`),
		event("PostModelRequest", `,"ctx_used":172000`),
		event(Query, `,"model":"m","effort":"high","state":"waiting_input"`),
		event(StatusLine, `,"wrapped":false,"state":"waiting_dialog","ctx_size":200000,"ctx_pct":4`),
	}
	writeEvents(t, f, 1, 0, good...)
	res := read(t, f, 1, Cursor{})
	if res.Health != HealthOK || res.Rejected != 0 || len(res.Observations) != len(good) {
		t.Fatalf("%+v", res)
	}
	if got := res.Observations[5].Context; got == nil || *got != (Context{Size: 0, Used: 172000, Percent: -1}) {
		t.Fatalf("used tokens alone: %+v", got)
	}
	var states []string
	for _, o := range res.Observations {
		states = append(states, o.State)
	}
	if strings.Join(states, ",") != "working,waiting_permission,waiting_permission,idle,failed,,waiting_input,waiting_dialog" {
		t.Fatalf("states: %v", states)
	}
	for name, bad := range map[string]string{
		"unknown state":            event("Stop", `,"state":"busy"`),
		"state on a subagent line": event("SubagentStop", `,"state":"idle"`),
		"state on PostModelSwitch": event("PostModelSwitch", `,"state":"idle"`),
		"stateless Notification":   event("Notification", ``),
		"Notification with model":  event("Notification", `,"state":"waiting_input","model":"m"`),
		"context on a hook":        event("Stop", `,"ctx_used":1`),
		"negative used":            event("PostModelRequest", `,"ctx_used":-1`),
		"used over the bound":      event("PostModelRequest", `,"ctx_used":1000000001`),
		"size without percent":     event("PostModelRequest", `,"ctx_size":1000`),
	} {
		g := preparedGen(t, 1)
		writeEvents(t, g, 1, 0, bad)
		if res := read(t, g, 1, Cursor{}); res.Rejected != 1 || res.Health != HealthInvalid {
			t.Errorf("%s accepted: %+v", name, res)
		}
	}
}
