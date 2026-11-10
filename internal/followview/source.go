// Package followview owns finite file reads and the live file viewer. It never
// boots a VM or grants access; the CLI supplies the current grant authority.
package followview

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const WholeBytes = 2 << 20
const WindowBytes = 1 << 20

//go:embed reader.py
var guestReader string

type Query struct {
	Offset   int64  `json:"offset"`
	Tail     bool   `json:"tail"`
	Previous string `json:"previous"`
}

type Snapshot struct {
	ReadNS        int64  `json:"read_ns"`
	ReceivedBytes int    `json:"-"`
	Size          int64  `json:"size"`
	Start         int64  `json:"start"`
	End           int64  `json:"end"`
	Large         bool   `json:"large"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
	MtimeNS       int64  `json:"mtime_ns"`
	Hash          string `json:"hash"`
	Key           string `json:"key"`
	Changed       bool   `json:"changed"`
	Data          []byte `json:"data,omitempty"`
}

type Reader interface {
	Read(context.Context, Query) (Snapshot, error)
}

// HostReader follows the pinned absolute name, permitting ordinary host
// symlinks while refusing nonregular leaves without blocking on a FIFO.
type HostReader struct{ Path string }

func (r HostReader) Read(ctx context.Context, q Query) (Snapshot, error) {
	started := time.Now()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	f, err := os.OpenFile(r.Path, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return Snapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return Snapshot{}, errors.New("select a readable regular file")
	}
	s := Snapshot{Size: info.Size(), Large: info.Size() > WholeBytes, MtimeNS: info.ModTime().UnixNano()}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		s.Device, s.Inode = uint64(st.Dev), uint64(st.Ino)
	}
	count := WholeBytes
	if s.Large {
		count = WindowBytes
		s.Start = min(max(q.Offset, 0), max(0, s.Size-WindowBytes))
		if q.Tail {
			s.Start = max(0, s.Size-WindowBytes)
		}
	}
	data := make([]byte, min(int64(count), max(0, s.Size-s.Start)))
	n, err := f.ReadAt(data, s.Start)
	if err != nil && err != io.EOF {
		return Snapshot{}, err
	}
	data = data[:n]
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if s.Large {
		data, s.Start = trimWindow(data, s.Start, s.Size)
	}
	s.End = s.Start + int64(len(data))
	sum := sha256.Sum256(data)
	s.Hash = hex.EncodeToString(sum[:])
	s.Key = fmt.Sprintf("%d:%d:%d:%s", s.Start, s.End, boolInt(s.Large), s.Hash)
	s.Changed = s.Key != q.Previous
	if s.Changed {
		s.Data = data
	}
	s.ReadNS = time.Since(started).Nanoseconds()
	return s, nil
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func trimWindow(data []byte, start, total int64) ([]byte, int64) {
	if start > 0 {
		skip := 0
		for skip < min(3, len(data)) && data[skip]&0xc0 == 0x80 {
			skip++
		}
		start += int64(skip)
		data = data[skip:]
	}
	if start+int64(len(data)) < total {
		i := max(0, len(data)-utf8.UTFMax)
		for i < len(data) {
			if !utf8.FullRune(data[i:]) {
				data = data[:i]
				break
			}
			_, n := utf8.DecodeRune(data[i:])
			i += n
		}
	}
	return data, start
}

// Authority is reread before each finite VM read/list. Roots are declared
// grants, not an inventory of anything that happens to remain mounted.
type Authority func(context.Context) ([]string, error)
type Command func(context.Context, ...string) *exec.Cmd

type VMReader struct {
	Path      string
	Authority Authority
	Command   Command
}
type guestRequest struct {
	Query
	Op     string   `json:"op"`
	Path   string   `json:"path"`
	Roots  []string `json:"roots"`
	Prefix string   `json:"prefix,omitempty"`
}
type guestResponse struct {
	Snapshot
	Entries []string `json:"entries"`
	Error   string   `json:"error"`
}

func (r VMReader) Read(ctx context.Context, q Query) (Snapshot, error) {
	result, err := r.call(ctx, guestRequest{Op: "read", Path: r.Path, Query: q})
	if err != nil {
		return Snapshot{}, err
	}
	s := result.Snapshot
	expectedKey := fmt.Sprintf("%d:%d:%d:%s", s.Start, s.End, boolInt(s.Large), s.Hash)
	digest, decodeErr := hex.DecodeString(s.Hash)
	if decodeErr != nil || len(digest) != sha256.Size || s.Key != expectedKey || (!s.Changed && s.Key != q.Previous) {
		return Snapshot{}, errors.New("invalid follow change signature")
	}
	if s.Size < 0 || s.Start < 0 || s.End < s.Start || s.End > s.Size || s.End-s.Start > WholeBytes || (s.Large && s.End-s.Start > WindowBytes) {
		return Snapshot{}, errors.New("invalid follow file bounds")
	}
	if s.Changed {
		sum := sha256.Sum256(s.Data)
		if s.End-s.Start != int64(len(s.Data)) || hex.EncodeToString(sum[:]) != s.Hash {
			return Snapshot{}, errors.New("follow payload hash/bounds mismatch")
		}
	} else if len(s.Data) != 0 {
		return Snapshot{}, errors.New("unchanged follow response carried data")
	}
	return s, nil
}
func (r VMReader) List(ctx context.Context, path, prefix string) ([]string, error) {
	result, err := r.call(ctx, guestRequest{Op: "list", Path: path, Prefix: prefix})
	if err != nil {
		return nil, err
	}
	if len(result.Entries) > 512 {
		return nil, errors.New("too many follow completion entries")
	}
	for _, entry := range result.Entries {
		if entry == "" || strings.ContainsAny(strings.TrimSuffix(entry, "/"), "/\t\r\n") || !strings.HasPrefix(entry, prefix) {
			return nil, errors.New("invalid follow completion entry")
		}
	}
	sort.Strings(result.Entries)
	return result.Entries, nil
}
func (r VMReader) call(ctx context.Context, req guestRequest) (guestResponse, error) {
	var result guestResponse
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	roots, err := r.Authority(ctx)
	if err != nil {
		return result, err
	}
	allowed := false
	for _, root := range roots {
		if Within(req.Path, root) {
			allowed = true
			break
		}
	}
	if !allowed {
		return result, errors.New("path is outside the current space grants")
	}
	req.Roots = roots
	input, err := json.Marshal(req)
	if err != nil {
		return result, err
	}
	if len(input) > 1<<20 {
		return result, errors.New("follow request too large")
	}
	cmd := r.Command(ctx, "python3", "-c", guestReader)
	cmd.Stdin = strings.NewReader(string(input))
	cmd.WaitDelay = time.Second
	// Own the finite shell/SSH client group; cancellation must not leave its
	// reader descendants holding the response pipes after this view closes.
	attr := syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		attr = *cmd.SysProcAttr
	}
	attr.Setpgid, attr.Pgid = true, 0
	cmd.SysProcAttr = &attr
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
		if errors.Is(err, unix.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	output := &boundedBuffer{limit: 3 << 20}
	diagnostic := &boundedBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = output, diagnostic
	err = cmd.Run()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err != nil {
		return result, fmt.Errorf("VM file reader: %w: %s", err, strings.TrimSpace(diagnostic.String()))
	}
	if output.overflow {
		return result, errors.New("VM file reader response too large")
	}
	dec := json.NewDecoder(strings.NewReader(output.String()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		return result, fmt.Errorf("VM file reader response: %w", err)
	}
	result.ReceivedBytes = len(output.text)
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return result, errors.New("extra VM file reader response")
	}
	if result.Error != "" {
		return result, errors.New(result.Error)
	}
	return result, nil
}
func Within(path, root string) bool {
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

type boundedBuffer struct {
	text     []byte
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	left := max(0, b.limit-len(b.text))
	b.text = append(b.text, p[:min(left, n)]...)
	b.overflow = b.overflow || n > left
	return n, nil
}
func (b *boundedBuffer) String() string { return string(b.text) }
