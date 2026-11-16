package boxexport

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type member struct {
	name, body, link string
	kind             byte
	mode             int64
}

func archive(t *testing.T, entries ...member) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0644
		}
		n := int64(len(e.body))
		if kind != tar.TypeReg {
			n = 0
		}
		if err := w.WriteHeader(&tar.Header{Name: e.name, Typeflag: kind, Linkname: e.link, Mode: mode, Size: n, Uid: 0, Gid: 0, ModTime: time.Unix(1234, 0)}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func producer(b []byte, failure error) func(context.Context, io.Writer) error {
	return func(_ context.Context, w io.Writer) error { _, err := w.Write(b); return errors.Join(err, failure) }
}
func TestCopyMappingAndNormalizedFiles(t *testing.T) {
	for _, tt := range []struct {
		name               string
		existing, contents bool
		want               string
	}{
		{"file new", false, false, "out"}, {"file into dir", true, false, "out/file"},
		{"dir new", false, false, "out/file"}, {"dir into dir", true, false, "out/tree/file"},
		{"contents new", false, true, "out/file"}, {"contents merge", true, true, "out/file"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dest := filepath.Join(root, "out")
			if tt.existing {
				if err := os.Mkdir(dest, 0700); err != nil {
					t.Fatal(err)
				}
			}
			name := "file"
			if strings.HasPrefix(tt.name, "dir") {
				name = "tree/file"
			}
			if tt.contents {
				name = "./file"
			}
			raw := archive(t, member{name: name, body: "payload", mode: 06755})
			if err := Copy(t.Context(), dest, Options{Contents: tt.contents}, producer(raw, nil)); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, tt.want)
			got, err := os.ReadFile(file)
			if err != nil || string(got) != "payload" {
				t.Fatalf("%q %v", got, err)
			}
			info, _ := os.Stat(file)
			if info.Mode().Perm() != 0755 || info.Mode()&os.ModeSetuid != 0 || info.ModTime().Unix() != 1234 {
				t.Fatal(info)
			}
			names, _ := os.ReadDir(root)
			for _, n := range names {
				if strings.HasPrefix(n.Name(), ".yoloctl-cp-") {
					t.Fatal("staging retained")
				}
			}
		})
	}
}
func TestCopyRejectsWholeArchiveBeforePublication(t *testing.T) {
	for _, tt := range []struct {
		name    string
		entries []member
	}{
		{"absolute", []member{{name: "/escape"}}}, {"traversal", []member{{name: "a/../b"}}},
		{"duplicate", []member{{name: "tree/a"}, {name: "tree/./a"}}},
		{"link parent", []member{{name: "tree/a", kind: tar.TypeSymlink, link: "."}, {name: "tree/a/file"}}},
		{"absolute symlink", []member{{name: "tree/a", kind: tar.TypeSymlink, link: "/tmp"}}},
		{"escaping symlink", []member{{name: "tree/a", kind: tar.TypeSymlink, link: "../../escape"}}},
		{"composed escape", []member{{name: "tree/a", kind: tar.TypeSymlink, link: "."}, {name: "tree/b", kind: tar.TypeSymlink, link: "a/../escape"}}},
		{"hardlink absent", []member{{name: "tree/a", kind: tar.TypeLink, link: "tree/missing"}}},
		{"fifo", []member{{name: "tree/fifo", kind: tar.TypeFifo}}}, {"device", []member{{name: "tree/dev", kind: tar.TypeChar}}},
		{"two roots", []member{{name: "one"}, {name: "two"}}}, {"empty", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dest := filepath.Join(root, "out")
			err := Copy(t.Context(), dest, Options{}, producer(archive(t, tt.entries...), nil))
			if err == nil {
				t.Fatal("accepted unsafe archive")
			}
			if _, err := os.Lstat(dest); !os.IsNotExist(err) {
				t.Fatal("published", err)
			}
		})
	}
}
func TestCopySafeLinksAndForwardHardlink(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "out")
	raw := archive(t, member{name: "tree/hard", kind: tar.TypeLink, link: "tree/file"}, member{name: "tree/link", kind: tar.TypeSymlink, link: "file"}, member{name: "tree/file", body: "safe"})
	if err := Copy(t.Context(), dest, Options{}, producer(raw, nil)); err != nil {
		t.Fatal(err)
	}
	a, _ := os.Stat(filepath.Join(dest, "file"))
	b, _ := os.Stat(filepath.Join(dest, "hard"))
	if !os.SameFile(a, b) {
		t.Fatal("not linked")
	}
	got, err := os.ReadFile(filepath.Join(dest, "link"))
	if err != nil || string(got) != "safe" {
		t.Fatal(string(got), err)
	}
}
func TestCopyLateProducerFailureAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(strings.ToLower(strings.TrimSpace(map[bool]string{false: "failure", true: "cancel"}[cancel])), func(t *testing.T) {
			root := t.TempDir()
			dest := filepath.Join(root, "out")
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			cause := errors.New("producer failed after tar EOF")
			fn := producer(archive(t, member{name: "file", body: "data"}), cause)
			if cancel {
				fn = func(ctx context.Context, w io.Writer) error {
					_, err := w.Write(archive(t, member{name: "file", body: "data"}))
					stop()
					<-ctx.Done()
					return errors.Join(err, ctx.Err())
				}
			}
			if err := Copy(ctx, dest, Options{}, fn); err == nil {
				t.Fatal("failure accepted")
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatal("published")
			}
		})
	}
}
func TestCopyCollisionsMergeAndLinks(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "out")
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dest, "b")
	if err := os.WriteFile(file, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	raw := archive(t, member{name: "./a", body: "prefix"}, member{name: "./b", body: "new"})
	err := Copy(t.Context(), dest, Options{Contents: true}, producer(raw, nil))
	if err == nil || !strings.Contains(err.Error(), "partially published") {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(file)
	if string(b) != "old" {
		t.Fatal("collision replaced")
	}
	if err := Copy(t.Context(), dest, Options{Contents: true, Overwrite: true}, producer(raw, nil)); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("protected"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dest, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := Copy(t.Context(), dest, Options{Contents: true, Overwrite: true}, producer(archive(t, member{name: "./link", body: "bad"}), nil)); err == nil {
		t.Fatal("overwrote link")
	}
	b, _ = os.ReadFile(outside)
	if string(b) != "protected" {
		t.Fatal("followed link")
	}
}
func TestDestinationAuthorityRetainedAndNoReplaceRace(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "parent")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(root, "saved")
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	fn := func(ctx context.Context, w io.Writer) error {
		if err := os.Rename(parent, saved); err != nil {
			return err
		}
		if err := os.Symlink(outside, parent); err != nil {
			return err
		}
		return producer(archive(t, member{name: "file", body: "held"}), nil)(ctx, w)
	}
	if err := Copy(t.Context(), filepath.Join(parent, "out"), Options{}, fn); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(saved, "out")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "out")); !os.IsNotExist(err) {
		t.Fatal("followed changed parent")
	}
	dest := filepath.Join(root, "racing")
	fn = func(ctx context.Context, w io.Writer) error {
		if err := os.WriteFile(dest, []byte("racer"), 0600); err != nil {
			return err
		}
		return producer(archive(t, member{name: "file", body: "ours"}), nil)(ctx, w)
	}
	if err := Copy(t.Context(), dest, Options{}, fn); err == nil {
		t.Fatal("racing destination overwritten")
	}
	b, _ := os.ReadFile(dest)
	if string(b) != "racer" {
		t.Fatal(string(b))
	}
}

func TestCopyStreamsLargeFileAndRefusesDestinationParentLink(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "large")
	const size = 16 << 20
	fn := func(ctx context.Context, w io.Writer) error {
		tw := tar.NewWriter(w)
		if err := tw.WriteHeader(&tar.Header{Name: "large", Mode: 0600, Size: size}); err != nil {
			return err
		}
		block := bytes.Repeat([]byte{'x'}, 32<<10)
		for sent := 0; sent < size; sent += len(block) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := tw.Write(block); err != nil {
				return err
			}
		}
		return tw.Close()
	}
	if err := Copy(t.Context(), dest, Options{}, fn); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dest)
	if err != nil || info.Size() != size {
		t.Fatal(info, err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	called := false
	err = Copy(t.Context(), filepath.Join(link, "out"), Options{}, func(context.Context, io.Writer) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("destination parent link was followed", err)
	}
}

func TestContentsComposedSymlinksStayInsideSelectedRoot(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, safe := range []bool{false, true} {
			name := "new"
			if existing {
				name = "existing"
			}
			if safe {
				name += "/safe"
			} else {
				name += "/escape"
			}
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				dest := filepath.Join(root, "out")
				if err := os.WriteFile(filepath.Join(root, "outside"), []byte("host sibling"), 0600); err != nil {
					t.Fatal(err)
				}
				if existing {
					if err := os.Mkdir(dest, 0700); err != nil {
						t.Fatal(err)
					}
				}
				target := "a/../outside"
				if safe {
					target = "a/file"
				}
				raw := archive(t, member{name: "a", kind: tar.TypeSymlink, link: "."}, member{name: "b", kind: tar.TypeSymlink, link: target}, member{name: "file", body: "in tree"})
				err := Copy(t.Context(), dest, Options{Contents: true}, producer(raw, nil))
				if !safe {
					if err == nil {
						t.Fatal("published composed contents-root escape")
					}
					if existing {
						entries, _ := os.ReadDir(dest)
						if len(entries) != 0 {
							t.Fatal("rejected archive changed destination")
						}
					} else if _, err := os.Lstat(dest); !os.IsNotExist(err) {
						t.Fatal("published rejected archive")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				b, err := os.ReadFile(filepath.Join(dest, "b"))
				if err != nil || string(b) != "in tree" {
					t.Fatal(string(b), err)
				}
			})
		}
	}
}
