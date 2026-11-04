package spacearchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"gopkg.in/yaml.v3"
)

func fixture(t *testing.T) (string, *spaceconfig.Bootstrap) {
	t.Helper()
	disk := t.TempDir()
	c, err := spaceconfig.New("original", "/project", "trial")
	if err != nil {
		t.Fatal(err)
	}
	c.AccountDiskUUID = "fixture-uuid"
	b, s, err := spaceconfig.Split(c, 1000, 1000, "old-generation")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := yaml.Marshal(account.New("original", "trial"))
	bundle := controller.StoreBundle{Schema: 1, Settings: *s, Accounts: map[string][]byte{"trial": raw}, Boxes: map[string][]byte{}}
	if _, err := controller.PublishStore(disk, b, bundle); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(disk, "account-state/trial/claude")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "history"), []byte("private native bytes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1700000000, 0)
	if err := os.Chtimes(filepath.Join(dir, "history"), when, when); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, "history"), filepath.Join(dir, "history-hard")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("history", filepath.Join(dir, "history-link")); err != nil {
		t.Fatal(err)
	}
	return disk, b
}
func stateFile(t *testing.T) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "state-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}
func TestRoundTripPrivateNativeStateAndIndependentIdentity(t *testing.T) {
	source, boot := fixture(t)
	outside := t.TempDir()
	const outsideBytes = "outside content must not be copied into the archive"
	nativeTestFile(t, outside, "file", outsideBytes)
	for _, name := range []string{"skills/yolo-dev", "dangling"} {
		target := outside
		if name == "dangling" {
			target += "/missing"
		}
		nativeTestLink(t, filepath.Join(source, "account-state/trial/claude"), name, target)
	}
	state := stateFile(t)
	if err := WriteState(t.Context(), state, source, boot); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := Write(t.Context(), &archive, *boot, state); err != nil {
		t.Fatal(err)
	}
	restored := stateFile(t)
	m, b, err := Read(t.Context(), bytes.NewReader(archive.Bytes()), restored)
	if err != nil {
		t.Fatal(err)
	}
	if b.Settings.DefaultAccount != "trial" || m.Bootstrap.DiskID != boot.DiskID {
		t.Fatal("declarations lost")
	}
	target := t.TempDir()
	if err := os.Mkdir(filepath.Join(target, "account-state"), 0700); err != nil {
		t.Fatal(err)
	}
	newBoot := *boot
	newBoot.Name = "copy"
	newBoot.DiskID = "11111111111111111111111111111111"
	newBoot.DiskUUID = "new-uuid"
	newBoot.UID = os.Getuid()
	newBoot.GID = os.Getgid()
	_, _ = restored.Seek(0, 0)
	if err := RestoreState(t.Context(), restored, target, &newBoot, m.StateSHA256, m.StateSize); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"skills/yolo-dev": outside, "dangling": outside + "/missing"} {
		for _, root := range []string{source, target} {
			if got, err := os.Readlink(filepath.Join(root, "account-state/trial/claude", name)); err != nil || got != want {
				t.Fatalf("outside symlink %s changed: %q, %v", name, got, err)
			}
		}
	}
	raw, err := os.ReadFile(state.Name())
	if err != nil || bytes.Contains(raw, []byte(outsideBytes)) {
		t.Fatalf("outside target content was copied: %v", err)
	}
	a := filepath.Join(target, "account-state/trial/claude/history")
	link := a + "-hard"
	st, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	other, err := os.Stat(link)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(st, other) {
		t.Fatal("hardlink relationship lost")
	}
	if st.ModTime().Unix() != 1700000000 || st.Mode().Perm() != 0600 {
		t.Fatal("file metadata lost", st)
	}
	if got, err := os.ReadFile(a + "-link"); err != nil || string(got) != "private native bytes\n" {
		t.Fatalf("relative link %q %v", got, err)
	}
	if err := os.WriteFile(a, []byte("new destination state"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = restored.Seek(0, 0)
	if err := RestoreState(t.Context(), restored, target, &newBoot, m.StateSHA256, m.StateSize); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(a); string(got) != "new destination state" {
		t.Fatal("completed import replay replaced new state")
	}
	if got, _ := os.ReadFile(filepath.Join(source, "account-state/trial/claude/history")); string(got) != "private native bytes\n" {
		t.Fatal("source changed")
	}
	corrupt := append([]byte(nil), archive.Bytes()...)
	corrupt[len(corrupt)-5] ^= 0xff
	if _, _, err := Read(t.Context(), bytes.NewReader(corrupt), stateFile(t)); err == nil {
		t.Fatal("corrupt gzip accepted")
	}
}

func TestInterruptedImportAcceptsOnlyIdenticalPartialState(t *testing.T) {
	source, boot := fixture(t)
	boot.UID, boot.GID = os.Getuid(), os.Getgid()
	state := stateFile(t)
	if err := WriteState(t.Context(), state, source, boot); err != nil {
		t.Fatal(err)
	}
	sum, size, err := StateDigest(t.Context(), state)
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []bool{false, true} {
		dest := t.TempDir()
		if err := os.Mkdir(filepath.Join(dest, "account-state"), 0700); err != nil {
			t.Fatal(err)
		}
		_, _ = state.Seek(0, 0)
		if err := RestoreState(t.Context(), state, dest, boot, sum, size); err != nil {
			t.Fatal(err)
		}
		// Model power loss after publishing an account and before the marker.
		if err := os.Remove(filepath.Join(dest, ".controller/imported")); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dest, "account-state/trial/claude/history")
		if changed {
			if err := os.WriteFile(file, []byte("unrelated changes"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		_, _ = state.Seek(0, 0)
		err = RestoreState(t.Context(), state, dest, boot, sum, size)
		if (err != nil) != changed {
			t.Fatalf("changed=%v recovery=%v", changed, err)
		}
		if changed {
			if got, _ := os.ReadFile(file); string(got) != "unrelated changes" {
				t.Fatal("recovery overwrote changed state")
			}
		}
	}
}

func TestRejectUnsafeNativeArchiveBeforePublication(t *testing.T) {
	source, boot := fixture(t)
	bundle, err := readDeclarations(source, boot)
	if err != nil {
		t.Fatal(err)
	}
	decl, _ := json.Marshal(bundle)
	outside := t.TempDir()
	nativeTestFile(t, outside, "file", "unchanged")
	for _, h := range []*tar.Header{
		{Name: "accounts/trial/claude/../../escape", Typeflag: tar.TypeReg},
		{Name: "accounts/unknown", Typeflag: tar.TypeDir},
		{Name: "accounts/trial/claude/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/../passwd"},
		{Name: "accounts/trial/claude/link", Typeflag: tar.TypeSymlink, Linkname: "/home/sandbox/back\\slash"},
		{Name: "accounts/trial/claude/link", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"},
		{Name: "accounts/trial/claude/link", Typeflag: tar.TypeLink, Linkname: "accounts/other/claude/file"},
		{Name: "accounts/trial/claude/link", Typeflag: tar.TypeSymlink, Linkname: outside},
		{Name: "accounts/trial/claude/link", Typeflag: tar.TypeSymlink, Linkname: "../../other/claude/secret"},
		{Name: "accounts/trial/claude/socket", Typeflag: tar.TypeFifo},
		{Name: "accounts/trial/claude/link", Typeflag: tar.TypeLink, Linkname: "accounts/trial/claude/missing"},
		{Name: "accounts/trial", Typeflag: tar.TypeDir},
	} {
		t.Run(h.Name, func(t *testing.T) {
			var data bytes.Buffer
			tw := tar.NewWriter(&data)
			_ = tw.WriteHeader(&tar.Header{Name: "declarations.json", Typeflag: tar.TypeReg, Size: int64(len(decl))})
			_, _ = tw.Write(decl)
			_ = tw.WriteHeader(&tar.Header{Name: "accounts/trial", Typeflag: tar.TypeDir, Mode: 0700})
			_ = tw.WriteHeader(&tar.Header{Name: "accounts/trial/claude", Typeflag: tar.TypeDir, Mode: 0700})
			_ = tw.WriteHeader(h)
			if h.Linkname == outside {
				// Retaining an outside link must not permit subsequent writes through it.
				_ = tw.WriteHeader(&tar.Header{Name: h.Name + "/file", Typeflag: tar.TypeReg, Size: 7})
				_, _ = tw.Write([]byte("changed"))
			}
			_ = tw.Close()
			dest := t.TempDir()
			_ = os.Mkdir(filepath.Join(dest, "account-state"), 0700)
			sum := fmt.Sprintf("%x", sha256.Sum256(data.Bytes()))
			if err := RestoreState(t.Context(), bytes.NewReader(data.Bytes()), dest, boot, sum, int64(data.Len())); err == nil {
				t.Fatal("unsafe member accepted")
			}
			rows, _ := os.ReadDir(filepath.Join(dest, "account-state"))
			if len(rows) != 0 {
				t.Fatal("failed archive published state")
			}
			if got, err := os.ReadFile(filepath.Join(outside, "file")); err != nil || string(got) != "unchanged" {
				t.Fatalf("outside link target modified: %q, %v", got, err)
			}
		})
	}
}

func TestRejectAdditionalOuterMembersAndChecksumMismatch(t *testing.T) {
	source, boot := fixture(t)
	state := stateFile(t)
	if err := WriteState(t.Context(), state, source, boot); err != nil {
		t.Fatal(err)
	}
	sum, size, err := StateDigest(t.Context(), state)
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []bool{false, true} {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		m := Manifest{Version: 1, Bootstrap: *boot, StateSHA256: sum, StateSize: size}
		if !extra {
			m.StateSHA256 = "1111111111111111111111111111111111111111111111111111111111111111"
		}
		raw, _ := json.Marshal(m)
		_ = tw.WriteHeader(&tar.Header{Name: "manifest.json", Typeflag: tar.TypeReg, Size: int64(len(raw))})
		_, _ = tw.Write(raw)
		_, _ = state.Seek(0, 0)
		raw, _ = os.ReadFile(state.Name())
		_ = tw.WriteHeader(&tar.Header{Name: "state.tar", Typeflag: tar.TypeReg, Size: size})
		_, _ = tw.Write(raw)
		if extra {
			_ = tw.WriteHeader(&tar.Header{Name: "../escape", Typeflag: tar.TypeReg})
		}
		_ = tw.Close()
		_ = gz.Close()
		if _, _, err := Read(t.Context(), &buf, stateFile(t)); err == nil {
			t.Fatal("invalid outer archive accepted")
		}
	}
}
