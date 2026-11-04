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

type browserBridgeSession struct {
	session *ssh.Session
	dir     string
}

// startBrowserBridge uses a second channel of the already authenticated sandbox
// SSH connection. It runs as the same confined user, never via container exec.
// Closing the holder's SSH client closes this pipe and the sandbox helper.
func (h *holder) startBrowserBridge(client *ssh.Client, opening chan struct{}) (*browserBridgeSession, error) {
	ctx, cancel := context.WithTimeout(h.hostCtx, 5*time.Second)
	defer cancel()
	s, err := newSandboxSession(ctx, client, opening)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	in, err := s.StdinPipe()
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	out, err := s.StdoutPipe()
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	// Missing Python or a custom image must not prevent ordinary attachment.
	if err := s.Start(shellJoin([]string{"python3", "-u", "-c", browserBridge, browserDirectory})); err != nil {
		_ = s.Close()
		return nil, err
	}
	r := bufio.NewReaderSize(out, 2*hostintegration.MaxURLBytes+2048)
	line, err := browserReadyLine(ctx, r)
	var ready struct {
		Directory string `json:"directory"`
	}
	if err != nil || decodeJSON(line, &ready) != nil || ready.Directory != browserDirectory {
		_ = s.Close()
		return nil, errors.New("sandbox browser helper did not become ready")
	}
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

// uploadImage creates a private, new sandbox file through the same confined SSH
// service as the agent. There is no caller-selected destination and no root
// filesystem write. Files survive a session exit and disappear with the box.
func uploadImage(ctx context.Context, client *ssh.Client, opening chan struct{}, data []byte) (string, error) {
	if err := hostintegration.ValidateImage(data); err != nil {
		return "", err
	}
	dir := "yoloctl-image-" + rand.Text()
	s, err := newSandboxSession(ctx, client, opening)
	if err != nil {
		return "", err
	}
	defer func() { _ = s.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	s.Stdin = bytes.NewReader(data)
	s.Stdout, s.Stderr = io.Discard, io.Discard
	err = s.Run(shellJoin([]string{"python3", "-c", imageUploadScript, dir, fmt.Sprint(len(data))}))
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

// reuseImage verifies read access and content as the confined sandbox user.
// Neither a manifest mapping nor a host-side stat proves guest accessibility.
func reuseImage(ctx context.Context, client *ssh.Client, opening chan struct{}, root string, probe imageProbe) (string, error) {
	guest := workdirImagePath(root, probe.Path)
	if guest == "" {
		return "", nil
	}
	s, err := newSandboxSession(ctx, client, opening)
	if err != nil {
		return "", err
	}
	defer func() { _ = s.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	s.Stdout, s.Stderr = io.Discard, io.Discard
	guestRoot := path.Clean(imageGuestPath(root))
	err = s.Run(shellJoin([]string{"python3", "-c", imageReuseScript, guestRoot, strings.TrimPrefix(guest, guestRoot+"/"), fmt.Sprint(probe.Size), probe.SHA256}))
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

// SSH channel opens have no context API. A timed-out open is retired as soon as
// it returns, without closing other channels or terminating the agent. A slot
// stays reserved until the underlying open returns, even after a timeout.
func newSandboxSession(ctx context.Context, client *ssh.Client, opening chan struct{}) (*ssh.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case opening <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	type opened struct {
		session *ssh.Session
		err     error
	}
	result := make(chan opened)
	go func() {
		defer func() { <-opening }()
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
