package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Ordinary private filesystem seams ONLY. The fictional manifest is never
// installed/fetched, and no Current Configure/root/native admission is faked.
func nativeHistoricalReleaseFixture(t *testing.T) (string, containerOrigin, []byte) {
	t.Helper()
	d := releaseFixture(t)
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	release, err := LoadQualifiedRelease(raw, sha256Hex(raw))
	if err != nil {
		t.Fatal(err)
	}
	helper, err := release.Helper(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	origin := containerOrigin{Space: "fixture", DiskID: "disk-id", DiskUUID: "disk-uuid", Box: "selected", Incarnation: strings.Repeat("a", 32), Qualification: release.Identity(), HelperSHA256: helper.BinarySHA256}
	root := t.TempDir()
	nativeHistoricalReleaseWriteFixture(t, root, origin, raw)
	// Restore only owned fixture permissions for normal TempDir cleanup.
	t.Cleanup(func() {
		for _, base := range []string{root, root + "-saved"} {
			for _, leaf := range []string{origin.Qualification, origin.Qualification + "-saved"} {
				_ = os.Chmod(filepath.Join(base, leaf), 0700)
			}
			_ = os.Chmod(base, 0700)
		}
	})
	return root, origin, raw
}
func nativeHistoricalReleaseWriteFixture(t *testing.T, root string, origin containerOrigin, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	q := filepath.Join(root, origin.Qualification)
	if err := os.Mkdir(q, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(q, "qualification.json"), raw, 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(q, 0555); err != nil {
		t.Fatal(err)
	}
}
func nativeHistoricalReleaseOpenFixture(root string, origin containerOrigin) func() ([]nativeHistoricalReleaseNode, error) {
	return func() ([]nativeHistoricalReleaseNode, error) {
		return openNativeHistoricalReleaseChain(root, []string{origin.Qualification}, os.Geteuid(), os.Getegid())
	}
}
func nativeHistoricalReleaseBoundaryFixture() error                   { return nil }
func nativeHistoricalReleaseCurrentFixture(ctx context.Context) error { return ctx.Err() }
func TestNativeHistoricalReleaseReadsRecordedPrettyHistoricalPair(t *testing.T) {
	root, origin, raw := nativeHistoricalReleaseFixture(t)
	p, err := captureNativeHistoricalReleaseWith(t.Context(), origin, os.Geteuid(), os.Getegid(), nativeHistoricalReleaseCurrentFixture, nativeHistoricalReleaseBoundaryFixture, nativeHistoricalReleaseOpenFixture(root, origin))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	r, err := p.Release(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if r.Identity() != origin.Qualification || string(r.Manifest()) != string(raw) {
		t.Fatal("recorded historical bytes were reformatted or replaced")
	}
	// Deliberately fictional source digest in releaseFixture is not replaced by
	// CURRENT helper inputs; this primary reader must not call ValidateHelperSource.
	if r.document.Pins.Helper.Inputs.SourceSHA256 != sha256Hex([]byte("helper-source-fixture")) {
		t.Fatal("historical inputs were refreshed")
	}
	manifest := r.Manifest()
	manifest[0] = 'x'
	again, err := p.Release(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if string(again.Manifest()) != string(raw) {
		t.Fatal("caller mutated retained manifest")
	}
}
func TestNativeHistoricalReleaseSameByteOriginalReplacementsStaySticky(t *testing.T) {
	for _, which := range []string{"file", "qualification-dir", "ancestor"} {
		t.Run(which, func(t *testing.T) {
			root, origin, raw := nativeHistoricalReleaseFixture(t)
			p, err := captureNativeHistoricalReleaseWith(t.Context(), origin, os.Geteuid(), os.Getegid(), nativeHistoricalReleaseCurrentFixture, nativeHistoricalReleaseBoundaryFixture, nativeHistoricalReleaseOpenFixture(root, origin))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = p.Close() }()
			q := filepath.Join(root, origin.Qualification)
			manifest := filepath.Join(q, "qualification.json")
			switch which {
			case "file":
				if err := os.Chmod(q, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(manifest, filepath.Join(root, "original-manifest")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(manifest, raw, 0444); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(q, 0555); err != nil {
					t.Fatal(err)
				}
			case "qualification-dir":
				if err := os.Rename(q, q+"-saved"); err != nil {
					t.Fatal(err)
				}
				nativeHistoricalReleaseWriteFixture(t, root, origin, raw)
			case "ancestor":
				if err := os.Rename(root, root+"-saved"); err != nil {
					t.Fatal(err)
				}
				nativeHistoricalReleaseWriteFixture(t, root, origin, raw)
			}
			if err := p.Check(t.Context()); err == nil {
				t.Fatal("same bytes repaired original named mapping")
			}
			// Restore the exact original object mapping. A detected loss still cannot
			// become a successful observation under a new/current matching path.
			switch which {
			case "file":
				if err := os.Chmod(q, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(manifest, filepath.Join(root, "replacement-manifest")); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(root, "original-manifest"), manifest); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(q, 0555); err != nil {
					t.Fatal(err)
				}
			case "qualification-dir":
				if err := os.Rename(q, q+"-replacement"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(q+"-replacement", 0700) })
				if err := os.Rename(q+"-saved", q); err != nil {
					t.Fatal(err)
				}
			case "ancestor":
				if err := os.Rename(root, root+"-replacement"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = os.Chmod(filepath.Join(root+"-replacement", origin.Qualification), 0700)
					_ = os.Chmod(root+"-replacement", 0700)
				})
				if err := os.Rename(root+"-saved", root); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := p.Release(t.Context()); err == nil {
				t.Fatal("original failure repaired by restoration")
			}
		})
	}
}
func TestNativeHistoricalReleaseExternalVerifierPrecedesFinalSourceChecks(t *testing.T) {
	root, origin, raw := nativeHistoricalReleaseFixture(t)
	q := filepath.Join(root, origin.Qualification)
	armed := false
	current := func(ctx context.Context) error {
		if armed {
			armed = false
			if err := os.Chmod(q, 0700); err != nil {
				return err
			}
			if err := os.Rename(filepath.Join(q, "qualification.json"), filepath.Join(root, "old-manifest")); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(q, "qualification.json"), raw, 0444); err != nil {
				return err
			}
			return os.Chmod(q, 0555)
		}
		return ctx.Err()
	}
	p, err := captureNativeHistoricalReleaseWith(t.Context(), origin, os.Geteuid(), os.Getegid(), current, nativeHistoricalReleaseBoundaryFixture, nativeHistoricalReleaseOpenFixture(root, origin))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	armed = true
	if err := p.Check(t.Context()); err == nil {
		t.Fatal("final verifier replacement escaped live member checks")
	}
}
func TestNativeHistoricalReleaseFinalCancellationAndOwnedOnlyClose(t *testing.T) {
	root, origin, _ := nativeHistoricalReleaseFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	armed := false
	current := func(ctx context.Context) error {
		if armed {
			cancel()
			return nil
		}
		return ctx.Err()
	}
	p, err := captureNativeHistoricalReleaseWith(ctx, origin, os.Geteuid(), os.Getegid(), current, nativeHistoricalReleaseBoundaryFixture, nativeHistoricalReleaseOpenFixture(root, origin))
	if err != nil {
		t.Fatal(err)
	}
	var files []*os.File
	for _, node := range p.nodes {
		files = append(files, node.file)
	}
	armed = true
	if err := p.Check(context.WithoutCancel(ctx)); !errors.Is(err, context.Canceled) {
		t.Fatal("original cancellation lost", err)
	}
	if _, err := p.Release(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatal("new context repaired failure", err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatal("owned original descriptor leaked", err)
		}
	}
	// No origin/scope/parent FD was supplied or closed by this independent reader.
}
func TestNativeHistoricalReleaseRefusesBadSourceAndRecordedPair(t *testing.T) {
	for _, which := range []string{"missing", "symlink", "hardlink", "mode", "release-mode", "ancestor-mode", "empty", "oversized", "wrong-helper", "wrong-qualification"} {
		t.Run(which, func(t *testing.T) {
			root, origin, raw := nativeHistoricalReleaseFixture(t)
			q := filepath.Join(root, origin.Qualification)
			file := filepath.Join(q, "qualification.json")
			switch which {
			case "missing":
				if err := os.Chmod(q, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(file, filepath.Join(root, "saved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(q, 0555); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Chmod(q, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(file, filepath.Join(root, "saved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "saved"), file); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(q, 0555); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(file, filepath.Join(root, "other-link")); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(file, 0644); err != nil {
					t.Fatal(err)
				}
			case "release-mode":
				if err := os.Chmod(q, 0755); err != nil {
					t.Fatal(err)
				}
			case "ancestor-mode":
				if err := os.Chmod(root, 0777); err != nil {
					t.Fatal(err)
				}
			case "empty", "oversized":
				replacement := []byte{}
				if which == "oversized" {
					replacement = make([]byte, nativeHistoricalManifestLimit+1)
				}
				if err := os.Chmod(file, 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, replacement, 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(file, 0444); err != nil {
					t.Fatal(err)
				}
			case "wrong-helper":
				origin.HelperSHA256 = strings.Repeat("9", 64)
			case "wrong-qualification":
				origin.Qualification = strings.Repeat("9", 64)
			}
			_ = raw
			p, err := captureNativeHistoricalReleaseWith(t.Context(), origin, os.Geteuid(), os.Getegid(), nativeHistoricalReleaseCurrentFixture, nativeHistoricalReleaseBoundaryFixture, nativeHistoricalReleaseOpenFixture(root, origin))
			if p != nil {
				_ = p.Close()
			}
			if err == nil {
				t.Fatal("bad original historical source/pair accepted")
			}
		})
	}
}
func TestNativeHistoricalReleaseExactManifestFieldsWithoutReformatting(t *testing.T) {
	_, origin, raw := nativeHistoricalReleaseFixture(t)
	compact, err := json.Marshal(releaseFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, encoding := range [][]byte{raw, compact} {
		selected := origin
		selected.Qualification = sha256Hex(encoding)
		if _, err := validateNativeHistoricalManifest(encoding, selected); err != nil {
			t.Fatal("valid reviewed encoding rejected", err)
		}
	}
	for _, pair := range [][2]string{{`"pins":`, `"Pins":`}, {`"helper":`, `"Helper":`}, {`"protocol":"2"`, `"Protocol":"2"`}, {`"member":`, `"Member":`}} {
		changed := []byte(strings.Replace(string(compact), pair[0], pair[1], 1))
		if string(changed) == string(compact) {
			t.Fatal("alias fixture did not change")
		}
		selected := origin
		selected.Qualification = sha256Hex(changed)
		if _, err := validateNativeHistoricalManifest(changed, selected); err == nil {
			t.Fatal("case alias accepted")
		}
	}
	for _, changed := range [][]byte{append(append([]byte(nil), raw...), []byte(" {}")...), []byte("null"), []byte(`{"pins":null}`)} {
		selected := origin
		selected.Qualification = sha256Hex(changed)
		if _, err := validateNativeHistoricalManifest(changed, selected); err == nil {
			t.Fatal("incomplete/trailing/null manifest accepted")
		}
	}
}
func TestNativeHistoricalReleasePartialConstructionClosesAllOwnedHandles(t *testing.T) {
	root, origin, _ := nativeHistoricalReleaseFixture(t)
	var nodes []nativeHistoricalReleaseNode
	expected := errors.New("fixture opener stopped after owned capture")
	opener := func() ([]nativeHistoricalReleaseNode, error) {
		var err error
		nodes, err = openNativeHistoricalReleaseChain(root, []string{origin.Qualification}, os.Geteuid(), os.Getegid())
		if err != nil {
			return nodes, err
		}
		return nodes, expected
	}
	if p, err := captureNativeHistoricalReleaseWith(t.Context(), origin, os.Geteuid(), os.Getegid(), nativeHistoricalReleaseCurrentFixture, nativeHistoricalReleaseBoundaryFixture, opener); p != nil || !errors.Is(err, expected) {
		t.Fatal("partial original error lost", err)
	}
	for _, node := range nodes {
		if _, err := node.file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatal("partial handle leaked", err)
		}
	}
	if _, err := captureNativeHistoricalRelease(t.Context(), nil, origin); err == nil {
		t.Fatal("actual wrapper accepted missing original native boundary")
	}
}

func TestNativeHistoricalReleaseOriginalFinalPhaseRunsNoExternalVerifier(t *testing.T) {
	root, origin, _ := nativeHistoricalReleaseFixture(t)
	armed := false
	calls := 0
	current := func(ctx context.Context) error {
		calls++
		if armed {
			return errors.New("late external callback forbidden")
		}
		return ctx.Err()
	}
	p, err := captureNativeHistoricalReleaseWith(t.Context(), origin, os.Geteuid(), os.Getegid(), current, nativeHistoricalReleaseBoundaryFixture, nativeHistoricalReleaseOpenFixture(root, origin))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	before := calls
	armed = true
	if err := p.CheckOriginal(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != before {
		t.Fatal("callback-free final phase called external verifier")
	}
	if err := p.Check(t.Context()); err == nil {
		t.Fatal("full Check skipped its independent verifier")
	}
	if err := p.CheckOriginal(t.Context()); err == nil {
		t.Fatal("original-only phase repaired failed verifier")
	}
}

func TestNativeHistoricalReleaseOriginalAndCheckCancellationCausesRemainSticky(t *testing.T) {
	for _, which := range []string{"original", "check"} {
		t.Run(which, func(t *testing.T) {
			root, origin, _ := nativeHistoricalReleaseFixture(t)
			lifetime, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			p, err := captureNativeHistoricalReleaseWith(lifetime, origin, os.Geteuid(), os.Getegid(), nativeHistoricalReleaseCurrentFixture, nativeHistoricalReleaseBoundaryFixture, nativeHistoricalReleaseOpenFixture(root, origin))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = p.Close() }()
			reason := errors.New("exact historical observation cancellation cause")
			var check context.Context
			if which == "original" {
				cancel(reason)
				check = context.WithoutCancel(lifetime)
			} else {
				ctx, stop := context.WithCancelCause(t.Context())
				stop(reason)
				check = ctx
			}
			if err := p.Check(check); !errors.Is(err, context.Canceled) || !errors.Is(err, reason) {
				t.Fatal("original cancellation cause lost", err)
			}
			if _, err := p.Release(t.Context()); !errors.Is(err, context.Canceled) || !errors.Is(err, reason) {
				t.Fatal("later context repaired original cancellation failure", err)
			}
		})
	}
}
