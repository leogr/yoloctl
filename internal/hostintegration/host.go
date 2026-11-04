// Package hostintegration contains the outer host's explicitly invoked desktop
// actions. Workloads can request an HTTP(S) URL; they cannot request clipboard
// reads, choose an executable, or supply a host filesystem path.
package hostintegration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxURLBytes       = 8192
	MaxImageBytes     = 16 << 20
	MaxImagePixels    = 16 << 20
	MaxImagePathBytes = 4096
)

var ErrNoImage = errors.New("no PNG image on the host clipboard")
var errOutputLimit = errors.New("desktop command output exceeds limit")

// ValidateURL deliberately accepts only web URLs, without credentials or
// invisible/control characters that could conceal a destination in the prompt.
func ValidateURL(value string) error {
	if len(value) == 0 || len(value) > MaxURLBytes || !utf8.ValidString(value) {
		return errors.New("browser URL is empty, invalid UTF-8, or too long")
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return errors.New("browser URL contains whitespace or invisible characters")
		}
	}
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return errors.New("browser requests require an absolute HTTP(S) URL without credentials")
	}
	return nil
}

// ValidateImage bounds both compressed input and decoded memory. Decode, not
// just DecodeConfig, rejects truncated images and invalid PNG checksums.
func ValidateImage(p []byte) error {
	if len(p) == 0 || len(p) > MaxImageBytes {
		return errors.New("clipboard image exceeds the 16 MiB limit or is empty")
	}
	c, err := png.DecodeConfig(bytes.NewReader(p))
	if err != nil || c.Width <= 0 || c.Height <= 0 || c.Width > MaxImagePixels/c.Height {
		return errors.New("clipboard image must be a valid PNG of at most 16 megapixels")
	}
	if _, err := png.Decode(bytes.NewReader(p)); err != nil {
		return fmt.Errorf("invalid clipboard PNG: %w", err)
	}
	return nil
}

// OpenURL never invokes a shell or accepts a command from the guest. Desktop
// policy (including the user's default browser) belongs to the host opener.
func OpenURL(ctx context.Context, value string) error {
	if err := ValidateURL(value); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	name, args := "xdg-open", []string{value}
	if runtime.GOOS == "darwin" {
		name, args = "/usr/bin/open", []string{"--", value}
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	// No terminal or command output is inherited by the desktop process.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("open host browser: %w", err)
	}
	return nil
}

// ClipboardImage keeps an optional canonical source path for a copied PNG file.
// Pixels without a file reference have no Path; Data is always a validated PNG.
type ClipboardImage struct {
	Data []byte
	Path string
}

// ReadImage is called only for a local user paste gesture. There is no RPC for
// this operation. A single copied PNG file can be reused in a shared workdir;
// otherwise only image/png is imported. Ordinary text keeps its terminal path.
func ReadImage(ctx context.Context) (ClipboardImage, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if runtime.GOOS == "darwin" {
		cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", "POSIX path of (the clipboard as alias)")
		cmd.WaitDelay = time.Second
		if p, err := limitedOutput(cmd, MaxImagePathBytes+1); err == nil {
			if path := clipboardFilePath(strings.TrimSuffix(string(p), "\n"), false); path != "" {
				return readImageFile(path)
			}
		}
		p, err := readDarwinImage(ctx)
		return ClipboardImage{Data: p}, err
	}
	// Ask only for offered targets: some clipboard owners return their bytes
	// even when the requested MIME type is absent.
	list, err := readClipboardTarget(ctx, "", 32<<10)
	if err != nil {
		return ClipboardImage{}, err
	}
	types := strings.Fields(string(list))
	for _, target := range []string{"text/uri-list", "image/png", "text/plain;charset=utf-8", "text/plain", "UTF8_STRING"} {
		offered := false
		for _, kind := range types {
			if kind == target {
				offered = true
				break
			}
		}
		if !offered {
			continue
		}
		limit := MaxImageBytes
		if target != "image/png" {
			limit = 32 << 10
		}
		p, err := readClipboardTarget(ctx, target, limit)
		if err != nil {
			if target != "image/png" && errors.Is(err, errOutputLimit) {
				return ClipboardImage{}, ErrNoImage
			}
			return ClipboardImage{}, err
		}
		if target == "image/png" {
			if err := ValidateImage(p); err != nil {
				return ClipboardImage{}, err
			}
			return ClipboardImage{Data: p}, nil
		}
		if path := clipboardFilePath(string(p), target == "text/uri-list"); path != "" {
			return readImageFile(path)
		}
		// Multi-file selections and non-image text are not partial imports.
		return ClipboardImage{}, ErrNoImage
	}
	return ClipboardImage{}, ErrNoImage
}

func readClipboardTarget(ctx context.Context, target string, limit int) ([]byte, error) {
	var name string
	var args []string
	// Mixed Wayland/X11 desktops may provide xclip without wl-clipboard.
	// A Wayland environment alone does not make wl-paste available.
	wayland, waylandErr := exec.LookPath("wl-paste")
	switch {
	case os.Getenv("WAYLAND_DISPLAY") != "" && waylandErr == nil:
		name, args = wayland, []string{"--no-newline", "--type", target}
		if target == "" {
			args = []string{"--list-types"}
		}
	case os.Getenv("DISPLAY") != "":
		if target == "" {
			target = "TARGETS"
		}
		name, args = "xclip", []string{"-selection", "clipboard", "-target", target, "-out"}
	default:
		return nil, ErrNoImage
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	p, err := limitedOutput(cmd, limit)
	if err == nil {
		return p, nil
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("read host clipboard: %w", ctx.Err())
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) || errors.Is(err, exec.ErrNotFound) {
		return nil, ErrNoImage
	}
	return nil, fmt.Errorf("read host clipboard: %w", err)
}

// AppleScript's PNG clipboard coercion returns a hex data value. A fixed script
// avoids temporary host files and interpolating data into executable text.
func readDarwinImage(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", "get the clipboard as «class PNGf»")
	cmd.WaitDelay = time.Second
	p, err := limitedOutput(cmd, 2*MaxImageBytes+64)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, ErrNoImage
		}
		return nil, err
	}
	return decodeDarwinImage(strings.TrimSpace(string(p)))
}

type boundedOutput struct {
	bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errOutputLimit
	}
	return b.Buffer.Write(p)
}

func limitedOutput(cmd *exec.Cmd, limit int) ([]byte, error) {
	b := &boundedOutput{limit: limit}
	cmd.Stdout = b
	err := cmd.Run()
	return b.Bytes(), err
}
