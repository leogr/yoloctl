package hostintegration

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func imageFixture(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDesktopPayloadValidation(t *testing.T) {
	for _, s := range []string{"https://example.org/path?a=b#c", "http://localhost:8080/", "http://[::1]:8080/", "https://example.org/?v=$(id)&q='quoted'"} {
		if err := ValidateURL(s); err != nil {
			t.Fatalf("valid URL %q: %v", s, err)
		}
	}
	for _, s := range []string{"", "file:///etc/passwd", "javascript:alert(1)", "--help", "https:///path", "https://user:password@example.org/", "https://example.org/\x1b[2J", "https://example.org/\n", "https://example.org/a b", "https://example.org/\u202eabc", "https://example.org/\xff", "https://example.org/" + strings.Repeat("x", MaxURLBytes)} {
		if err := ValidateURL(s); err == nil {
			t.Fatalf("accepted URL %q", s)
		}
	}
	valid := imageFixture(t)
	if err := ValidateImage(valid); err != nil {
		t.Fatal(err)
	}
	for _, p := range [][]byte{nil, []byte("text"), valid[:len(valid)-4], bytes.Repeat([]byte{0}, MaxImageBytes+1)} {
		if err := ValidateImage(p); err == nil {
			t.Fatal("accepted invalid/oversized image")
		}
	}
	got, err := decodeDarwinImage("«data PNGf" + hex.EncodeToString(valid) + "»")
	if err != nil || !bytes.Equal(got, valid) {
		t.Fatalf("macOS data decode: %v", err)
	}
	for _, s := range []string{"", "«data TIFF0000»", "«data PNGfxx»", "«data PNGf00»"} {
		if _, err := decodeDarwinImage(s); err == nil {
			t.Fatalf("accepted bad macOS response %q", s)
		}
	}
}

func TestHostBrowserPassesOneLiteralArgument(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux opener dispatch; no actual desktop is launched")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "args")
	t.Setenv("PATH", dir)
	t.Setenv("YOLOCTL_TEST_HOST_ARGS", path)
	if err := os.WriteFile(filepath.Join(dir, "xdg-open"), []byte("#!/bin/sh\nprintf '%s\\n' \"$#\" \"$@\" > \"$YOLOCTL_TEST_HOST_ARGS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	value := "https://example.org/?v=$(id)&q='quoted'"
	if err := OpenURL(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "1\n"+value+"\n" {
		t.Fatalf("opener argv=%q: %v", got, err)
	}
}

func TestHostImageReaderRequestsOfferedPNGTarget(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux clipboard dispatch; macOS data decoding is tested separately")
	}
	dir := t.TempDir()
	data := imageFixture(t)
	imagePath, argsPath := filepath.Join(dir, "image.png"), filepath.Join(dir, "args")
	if err := os.WriteFile(imagePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("YOLOCTL_TEST_IMAGE", imagePath)
	t.Setenv("YOLOCTL_TEST_ARGS", argsPath)
	script := []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$YOLOCTL_TEST_ARGS\"\ncase \"$*\" in *--list-types*|*TARGETS*) printf 'image/png\\n';; *) /bin/cat \"$YOLOCTL_TEST_IMAGE\";; esac\n")
	for _, backend := range []string{"wl-paste", "xclip"} {
		if err := os.WriteFile(filepath.Join(dir, backend), script, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ wayland, display, args string }{
		{"wayland-0", ":0", "--no-newline\n--type\nimage/png\n"},
		{"", ":0", "-selection\nclipboard\n-target\nimage/png\n-out\n"},
	} {
		t.Setenv("WAYLAND_DISPLAY", tc.wayland)
		t.Setenv("DISPLAY", tc.display)
		got, err := ReadImage(context.Background())
		if err != nil || got.Path != "" || !bytes.Equal(got.Data, data) {
			t.Fatalf("read image: %v", err)
		}
		args, _ := os.ReadFile(argsPath)
		if string(args) != tc.args {
			t.Fatalf("clipboard request=%q", args)
		}
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	if _, err := ReadImage(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatalf("headless paste: %v", err)
	}
}

func TestHostImageReaderFallsBackToX11WithoutWaylandTool(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux clipboard dispatch; no real desktop")
	}
	dir := t.TempDir()
	data := imageFixture(t)
	imagePath, payloadPath := filepath.Join(dir, "copied image.png"), filepath.Join(dir, "payload")
	if err := os.WriteFile(imagePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir) // Only xclip is installed, even though Wayland is set.
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	t.Setenv("DISPLAY", ":0")
	t.Setenv("YOLOCTL_TEST_PAYLOAD", payloadPath)
	script := []byte("#!/bin/sh\ncase \"$*\" in *TARGETS*) printf '%s\\n' \"$YOLOCTL_TEST_TARGETS\";; *) /bin/cat \"$YOLOCTL_TEST_PAYLOAD\";; esac\n")
	if err := os.WriteFile(filepath.Join(dir, "xclip"), script, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, targets, path string
		payload             []byte
	}{
		{"pixels", "image/png\nTARGETS", "", data},
		{"GNOME copied file", "application/vnd.portal.files\napplication/vnd.portal.filetransfer\ntext/uri-list\ntext/plain;charset=utf-8\nx-special/gnome-copied-files\nUTF8_STRING\nTARGETS\nTIMESTAMP", canonical, []byte((&url.URL{Scheme: "file", Path: imagePath}).String() + "\r\n")},
		{"ordinary text", "text/plain\nUTF8_STRING\nTARGETS", "", []byte("ordinary text")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("YOLOCTL_TEST_TARGETS", tc.targets)
			if err := os.WriteFile(payloadPath, tc.payload, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadImage(context.Background())
			if tc.name == "ordinary text" {
				if !errors.Is(err, ErrNoImage) {
					t.Fatalf("ordinary clipboard must keep Ctrl+V: %v", err)
				}
				return
			}
			if err != nil || got.Path != tc.path || !bytes.Equal(got.Data, data) {
				t.Fatalf("X11 fallback: path=%q err=%v", got.Path, err)
			}
		})
	}
	t.Setenv("DISPLAY", "")
	if _, err := ReadImage(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatalf("no usable display should keep Ctrl+V: %v", err)
	}
}
