package hostintegration

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestClipboardFileReferenceIsSingleLocalPNG(t *testing.T) {
	for _, tc := range []struct {
		value string
		list  bool
		want  string
	}{
		{"# files\r\nfile:///home/a%20b.png\r\n", true, "/home/a b.png"},
		{"file://localhost/home/a.png", true, "/home/a.png"},
		{"/home/a b.png\n", false, "/home/a b.png"},
		{"file:///home/a.png\nfile:///home/b.png", true, ""},
		{"file://server/home/a.png", true, ""},
		{"file:///home/a.png?x=1", true, ""},
		{"file:///home/a.png#fragment", true, ""},
		{"file:///home/a%0ab.png", true, ""},
		{"file:///home/a%1bb.png", true, ""},
		{"file:///home/a/../b.png", true, ""},
		{"file:///home/a\u202eb.png", true, ""},
		{"/home/a.png", true, ""},
		{"https://example.org/a.png", true, ""},
		{"~/a.png", false, ""},
		{"relative.png", false, ""},
		{"text mentioning /home/a.png", false, ""},
		{"/home/a.txt", false, ""},
		{"/home/a.png\n/home/b.png", false, ""},
		{"/home/a.png\xff", false, ""},
	} {
		if got := clipboardFilePath(tc.value, tc.list); got != tc.want {
			t.Errorf("parse(%q,%t)=%q want %q", tc.value, tc.list, got, tc.want)
		}
	}
}

func TestCopiedImageFileResolvesAliasAndRejectsSpecialFiles(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "file.png")
	data := imageFixture(t)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.png")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	got, err := readImageFile(alias)
	if err != nil || got.Path != path || !bytes.Equal(got.Data, data) {
		t.Fatalf("copied file=%+v %v", got, err)
	}
	for _, name := range []string{"missing.png", "folder.png", "pipe.png", "bad.png"} {
		candidate := filepath.Join(dir, name)
		switch name {
		case "folder.png":
			err = os.Mkdir(candidate, 0o700)
		case "pipe.png":
			err = unix.Mkfifo(candidate, 0o600)
		case "bad.png":
			err = os.WriteFile(candidate, []byte("not PNG"), 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := readImageFile(candidate); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
}

func TestImagePathPasteIsOneLiteralShellWord(t *testing.T) {
	for _, path := range []string{"/workdir/simple.png", "/workdir/spaced name.png", "/workdir/it's-$(echo NOT_LITERAL);*.png", "/workdir/界.png"} {
		cmd := exec.Command("sh", "-c", "set -- "+ImagePathText(path)+"; printf '%s\\n' \"$#\" \"$1\"")
		got, err := cmd.Output()
		if err != nil || string(got) != "1\n"+path+"\n" {
			t.Fatalf("paste %q -> %q (%v)", path, got, err)
		}
	}
}

func TestClipboardFilesAndOrdinaryTextUseOfferedTargets(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux desktop dispatch; no real clipboard")
	}
	dir := t.TempDir()
	imagePath, payloadPath := filepath.Join(dir, "a copied image.png"), filepath.Join(dir, "clipboard")
	data := imageFixture(t)
	if err := os.WriteFile(imagePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("YOLOCTL_TEST_PAYLOAD", payloadPath)
	script := []byte("#!/bin/sh\ncase \"$*\" in *--list-types*|*TARGETS*) printf '%s\\n' \"$YOLOCTL_TEST_TARGET\";; *) /bin/cat \"$YOLOCTL_TEST_PAYLOAD\";; esac\n")
	for _, backend := range []string{"wl-paste", "xclip"} {
		if err := os.WriteFile(filepath.Join(dir, backend), script, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, wayland := range []string{"wayland-0", ""} {
		t.Setenv("WAYLAND_DISPLAY", wayland)
		t.Setenv("DISPLAY", ":0")
		for _, tc := range []struct {
			target, payload string
			image           bool
		}{
			{"text/uri-list", (&url.URL{Scheme: "file", Path: imagePath}).String() + "\r\n", true},
			{"text/plain", imagePath, true},
			{"text/plain", "ordinary text", false},
			{"text/plain", strings.Repeat("ordinary text", 8192), false},
			{"text/uri-list", "file:///a.png\nfile:///b.png\n", false},
		} {
			t.Setenv("YOLOCTL_TEST_TARGET", tc.target)
			if err := os.WriteFile(payloadPath, []byte(tc.payload), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadImage(context.Background())
			if tc.image {
				if err != nil || got.Path != canonical || !bytes.Equal(got.Data, data) {
					t.Fatalf("file read=%+v %v", got, err)
				}
			} else if !errors.Is(err, ErrNoImage) {
				t.Fatalf("ordinary clipboard should keep key: %v", err)
			}
		}
	}
}
