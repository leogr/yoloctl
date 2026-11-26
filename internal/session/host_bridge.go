package session

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/hostintegration"
	"golang.org/x/crypto/ssh"
)

//go:embed host_bridge.py
var browserBridge string

// Stable within the container, independent of account state and holder lifetime.
// Agents may cache BROWSER/PATH in background processes across session restarts.
const browserDirectory = "/tmp/yoloctl-browser"

var browserBridgeSetupBudget = 5 * time.Second

type browserBridgeSession struct {
	session *ssh.Session
	dir     string
}

// startBrowserBridge uses a second channel of the already authenticated sandbox
// SSH connection. It runs as the same confined user, never via container exec.
// Closing the holder's SSH client closes this pipe and the sandbox helper.
func (h *holder) startBrowserBridge(client *ssh.Client) (*browserBridgeSession, error) {
	ctx, cancel := context.WithTimeout(h.hostCtx, browserBridgeSetupBudget)
	defer cancel()
	s, err := openBrowserSession(ctx, client)
	if err != nil {
		return nil, err
	}
	readyBridge := false
	defer func() {
		if !readyBridge {
			// The optional channel can be stuck awaiting a peer response.
			// Closing it must not wait in the normal launch path.
			go func() { _ = s.Close() }()
		}
	}()
	in, err := s.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := s.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Start waits for an SSH acknowledgement. A silent optional channel
	// must not keep a healthy holder from starting its command.
	started := make(chan error, 1)
	go func() {
		started <- s.Start(shellJoin([]string{"python3", "-u", "-c", browserBridge, browserDirectory}))
	}()
	select {
	case err := <-started:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	r := bufio.NewReaderSize(out, 2*hostintegration.MaxURLBytes+2048)
	line, err := browserReadyLine(ctx, r)
	var ready struct {
		Directory string `json:"directory"`
	}
	if err != nil || ctx.Err() != nil || decodeJSON(line, &ready) != nil || ready.Directory != browserDirectory {
		return nil, errors.New("sandbox browser helper did not become ready")
	}
	readyBridge = true
	go func() {
		defer func() { _ = in.Close(); _ = s.Close() }()
		for {
			line, err := r.ReadSlice('\n')
			if err != nil {
				return
			}
			var request struct {
				URL string `json:"url"`
			}
			if decodeJSON(line, &request) != nil {
				return
			}
			err = h.openBrowser(request.URL)
			result := hostResult{}
			if err != nil {
				result.Error = err.Error()
			}
			if err := json.NewEncoder(in).Encode(result); err != nil {
				return
			}
		}
	}()
	return &browserBridgeSession{session: s, dir: ready.Directory}, nil
}

// Session.Close sends an SSH close request; it does not guarantee that a
// nonresponsive peer wakes the stdout reader. Keep optional startup bounded
// independently of that reply. There is at most one such reader per holder;
// closing the holder's SSH connection retires it even if the peer stays silent.
func browserReadyLine(ctx context.Context, r *bufio.Reader) ([]byte, error) {
	type result struct {
		line []byte
		err  error
	}
	ready := make(chan result, 1)
	go func() {
		line, err := r.ReadSlice('\n')
		ready <- result{line, err}
	}()
	select {
	case got := <-ready:
		return got.line, got.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func browserLaunch(argv []string, directory string) string {
	// An explicit BROWSER survives. Prepending PATH also supports programs that
	// call xdg-open directly. No URL is part of this shell program.
	return "if [ -z \"${BROWSER+x}\" ]; then export BROWSER=" + shellJoin([]string{directory + "/open-url"}) + "; fi; " +
		"export PATH=" + shellJoin([]string{directory}) + ":\"$PATH\"; exec " + shellJoin(argv)
}

// sandboxImages uses the same confined SSH endpoint as the holder. Each
// operation owns its connection, so canceling a silent peer wakes NewSession,
// Start and Wait without disturbing the holder's terminal or browser channel.
type sandboxImages struct {
	socket   string
	endpoint os.FileInfo
	slots    chan struct{}
}

func (images sandboxImages) run(ctx context.Context, input io.Reader, argv []string) error {
	select {
	case images.slots <- struct{}{}:
		defer func() { <-images.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	// The path is reused across box runs. Only the listener observed when
	// the holder attached may receive image commands from this holder.
	if err := images.checkEndpoint(); err != nil {
		return err
	}
	client, err := connectHolderSandbox(ctx, images.socket)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stop()
	if err := images.checkEndpoint(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s, err := client.NewSession()
	if err == nil {
		s.Stdin, s.Stdout, s.Stderr = input, io.Discard, io.Discard
		err = s.Run(shellJoin(argv))
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (images sandboxImages) checkEndpoint() error {
	info, err := os.Stat(images.socket)
	if err != nil {
		return err
	}
	if images.endpoint == nil || info.Mode()&os.ModeSocket == 0 || !os.SameFile(images.endpoint, info) {
		return errors.New("sandbox image endpoint changed since attachment")
	}
	return nil
}

// upload creates a private file with no caller-selected destination. Files
// survive a session exit and disappear with the box.
func (images sandboxImages) upload(ctx context.Context, data []byte) (string, error) {
	if err := hostintegration.ValidateImage(data); err != nil {
		return "", err
	}
	dir := "yoloctl-image-" + rand.Text()
	err := images.run(ctx, bytes.NewReader(data), []string{"python3", "-c", imageUploadScript, dir, fmt.Sprint(len(data))})
	if err != nil {
		return "", fmt.Errorf("save image in sandbox: %w", err)
	}
	return "/tmp/" + dir + "/clipboard.png", nil
}

// workdirImagePath only maps canonical host files below this box's host
// workdir. Other binds and ephemeral workdirs deliberately fall back to upload.
func workdirImagePath(root, source string) string {
	if root == "" || !path.IsAbs(root) || path.Clean(root) != root ||
		!hostintegration.ValidImageFilePath(source) || !strings.HasPrefix(source, strings.TrimSuffix(root, "/")+"/") {
		return ""
	}
	guest := imageGuestPath(source)
	if !hostintegration.ValidImageFilePath(guest) {
		return ""
	}
	return guest
}

// reuse verifies read access and content as the confined sandbox user.
// Neither a manifest mapping nor a host-side stat proves guest accessibility.
func (images sandboxImages) reuse(ctx context.Context, root string, probe imageProbe) (string, error) {
	guest := workdirImagePath(root, probe.Path)
	if guest == "" {
		return "", nil
	}
	guestRoot := path.Clean(imageGuestPath(root))
	err := images.run(ctx, nil, []string{"python3", "-c", imageReuseScript, guestRoot, strings.TrimPrefix(guest, guestRoot+"/"), fmt.Sprint(probe.Size), probe.SHA256})
	if err != nil {
		var exit *ssh.ExitError
		if errors.As(err, &exit) && exit.ExitStatus() == 3 {
			return "", nil
		}
		return "", err
	}
	return guest, nil
}

//go:embed image_reuse.py
var imageReuseScript string

// Browser startup opens at most one channel per holder. The SSH package
// has no cancellable channel open, so a late result is retired independently
// and the regular terminal startup proceeds when the browser deadline ends.
func openBrowserSession(ctx context.Context, client *ssh.Client) (*ssh.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	type opened struct {
		session *ssh.Session
		err     error
	}
	result := make(chan opened)
	go func() {
		s, err := client.NewSession()
		select {
		case result <- opened{s, err}:
		case <-ctx.Done():
			if s != nil {
				_ = s.Close()
			}
		}
	}()
	select {
	case r := <-result:
		return r.session, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

const imageUploadScript = `import os, sys
name, size = sys.argv[1], int(sys.argv[2])
root = os.open('/tmp', os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
os.mkdir(name, 0o700, dir_fd=root)
directory = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=root)
try:
    fd = os.open('clipboard.png', os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=directory)
    with os.fdopen(fd, 'wb') as output:
        while size:
            data = sys.stdin.buffer.read(min(size, 65536))
            if not data:
                raise ValueError('incomplete image')
            output.write(data)
            size -= len(data)
        if sys.stdin.buffer.read(1):
            raise ValueError('excess image data')
        output.flush()
        os.fsync(output.fileno())
    os.fsync(directory)
except BaseException:
    try: os.unlink('clipboard.png', dir_fd=directory)
    except FileNotFoundError: pass
    os.rmdir(name, dir_fd=root)
    raise
finally:
    os.close(directory)
    os.close(root)
`

func validImagePath(path string) bool {
	name, ok := strings.CutPrefix(path, "/tmp/yoloctl-image-")
	if !ok {
		return false
	}
	name, ok = strings.CutSuffix(name, "/clipboard.png")
	return ok && validHostID(name)
}
