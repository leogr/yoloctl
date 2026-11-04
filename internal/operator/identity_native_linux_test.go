package operator

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestTaggedRegistrationInventoryRetainsAllModesAndKeepEvidence(t *testing.T) {
	root := identityFixtureRoot(t)
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := LegacyRegistrationIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)})
	native := taggedNativeIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("b", 32)})
	foreign := taggedNativeIdentity(Endpoint{Box: "other", Incarnation: strings.Repeat("c", 32)})
	writeTaggedIdentityFixture(t, root, legacy)
	raw := writeTaggedIdentityFixture(t, root, native)
	writeTaggedIdentityFixture(t, root, foreign)
	p, err := observeRegistrationInventory(t.Context(), func() (*os.File, error) { return openEndpointDirectory(unix.AT_FDCWD, root, false) }, os.Geteuid(), os.Getegid(), endpointCurrentOK)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	all, err := p.IdentitiesForBox("box", "")
	if err != nil || len(all) != 2 || all[0] != legacy || all[1] != native || len(p.entries) != 3 {
		t.Fatal("tag or same-box/foreign coverage lost", all, err)
	}
	kept, err := p.IdentitiesForBox("box", native.Incarnation)
	if err != nil || len(kept) != 1 || kept[0] != legacy {
		t.Fatal("typed exact-keep semantics changed", kept, err)
	}
	if _, err := p.IdentitiesForBox("box", foreign.Incarnation); err == nil {
		t.Fatal("foreign keep accepted")
	}
	missingKeep, err := p.IdentitiesForBox("box", strings.Repeat("d", 32))
	if err != nil || len(missingKeep) != 2 {
		t.Fatal("missing keep hid siblings", err)
	}
	for _, keep := range []string{"", native.Incarnation} {
		if _, err := p.ForBox("box", keep); !errors.Is(err, ErrNativeRegistration) {
			t.Fatal("Endpoint-only getter erased native identity", err)
		}
	}
	all[1].Qualification = strings.Repeat("c", 64)
	copy, err := p.IdentitiesForBox("box", "")
	if err != nil || copy[1] != native || !bytes.Equal(p.entries[native.Incarnation].raw, raw) {
		t.Fatal("caller changed original tagged identity or raw anchor", err)
	}
	if err := p.CheckCurrent(t.Context(), endpointCurrentOK); err != nil {
		t.Fatal(err)
	}
	changed := native
	changed.Qualification = strings.Repeat("c", 64)
	changedRaw, _ := json.Marshal(changed)
	writeIdentityFixtureBytes(t, filepath.Join(root, native.Incarnation), changedRaw)
	if err := p.CheckCurrent(t.Context(), endpointCurrentOK); err == nil {
		t.Fatal("native qualification changed without invalidating original inventory")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.IdentitiesForBox("box", ""); err == nil {
		t.Fatal("closed inventory returned identities")
	}
}

func TestTaggedWithdrawalCompleteAndIncompletePreserveIdentityAndEffects(t *testing.T) {
	for _, mode := range []RegistrationMode{RegistrationLegacy, RegistrationOpenShellNative} {
		for _, profile := range []endpointRegistrationProfile{endpointCompleteRegistration, endpointIncompleteRegistration} {
			t.Run(map[RegistrationMode]string{RegistrationLegacy: "legacy", RegistrationOpenShellNative: "native"}[mode]+map[endpointRegistrationProfile]string{endpointCompleteRegistration: "-complete", endpointIncompleteRegistration: "-incomplete"}[profile], func(t *testing.T) {
				root := identityFixtureRoot(t)
				if err := os.Chmod(root, 0o700); err != nil {
					t.Fatal(err)
				}
				e := Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}
				i := LegacyRegistrationIdentity(e)
				if mode == RegistrationOpenShellNative {
					i = taggedNativeIdentity(e)
				}
				raw := writeTaggedIdentityFixture(t, root, i)
				if profile == endpointCompleteRegistration {
					name := filepath.Join(root, e.Incarnation, "client.json")
					if err := os.WriteFile(name, []byte(`{"space":"s","generation":"g"}`), 0o644); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(name, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if mode == RegistrationOpenShellNative {
					if old, err := prepareEndpointProfileAt(t.Context(), root, e, os.Geteuid(), os.Getegid(), endpointCurrentOK, profile); err == nil {
						_ = old.Close()
						t.Fatal("legacy withdrawal erased native mode")
					}
				}
				p, err := prepareRegistrationProfileAt(t.Context(), root, i, os.Geteuid(), os.Getegid(), endpointCurrentOK, profile)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = p.Close() }()
				got, err := p.Identity()
				if err != nil || got != i || !bytes.Equal(p.raw["identity.json"], raw) {
					t.Fatal("prepared withdrawal lost original identity/raw bytes", got, err)
				}
				var effects []string
				p.unlink = func(fd int, name string, flags int) error {
					effects = append(effects, "unlink:"+name)
					return unix.Unlinkat(fd, name, flags)
				}
				p.sync = func(fd int) error { effects = append(effects, "sync"); return unix.Fsync(fd) }
				if err := p.withdraw(t.Context(), endpointCurrentOK); err != nil {
					t.Fatal(err)
				}
				want := "unlink:identity.json,sync,unlink:" + e.Incarnation + ",sync"
				if profile == endpointCompleteRegistration {
					want = "unlink:client.json,sync," + want
				}
				if strings.Join(effects, ",") != want {
					t.Fatal("typed withdrawal changed ACK order", effects)
				}
				before := len(effects)
				if err := p.withdraw(t.Context(), endpointCurrentOK); err == nil || len(effects) != before {
					t.Fatal("typed withdrawal replayed")
				}
				if err := p.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := p.Identity(); err == nil {
					t.Fatal("closed withdrawal returned original identity")
				}
			})
		}
	}
}

func TestNativeWithdrawalRejectsChangedTagRawAndInitialProfile(t *testing.T) {
	for _, change := range []string{"qualification", "raw-only", "client-added", "client-lost"} {
		t.Run(change, func(t *testing.T) {
			root := identityFixtureRoot(t)
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			i := taggedNativeIdentity(Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)})
			raw := writeTaggedIdentityFixture(t, root, i)
			profile := endpointIncompleteRegistration
			client := filepath.Join(root, i.Incarnation, "client.json")
			if change == "client-lost" {
				profile = endpointCompleteRegistration
				if err := os.WriteFile(client, []byte(`{}`), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(client, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			p, err := prepareRegistrationProfileAt(t.Context(), root, i, os.Geteuid(), os.Getegid(), endpointCurrentOK, profile)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = p.Close() }()
			switch change {
			case "qualification":
				changed := i
				changed.Qualification = strings.Repeat("c", 64)
				raw, _ = json.Marshal(changed)
				writeIdentityFixtureBytes(t, filepath.Join(root, i.Incarnation), raw)
			case "raw-only":
				writeIdentityFixtureBytes(t, filepath.Join(root, i.Incarnation), append(raw, '\n'))
			case "client-added":
				if err := os.WriteFile(client, []byte(`{}`), 0o644); err != nil {
					t.Fatal(err)
				}
			case "client-lost":
				if err := os.Remove(client); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			p.unlink = func(int, string, int) error { calls++; return nil }
			if err := p.withdraw(t.Context(), endpointCurrentOK); err == nil || calls != 0 {
				t.Fatal("changed native observation reached first effect", err, calls)
			}
			if err := p.withdraw(t.Context(), endpointCurrentOK); err == nil || calls != 0 {
				t.Fatal("native failure replayed")
			}
		})
	}
}

// Linux ancestor traversal must not demand directory listing permission.
// This exercises the actual no-follow traversal and final fsync publication;
// the final directory still needs its original readable descriptor.
func TestLegacyIdentityPublicationUsesPathOnlyAncestors(t *testing.T) {
	root := identityFixtureRoot(t)
	ancestor := filepath.Join(root, "search-only")
	if err := os.Mkdir(ancestor, 0700); err != nil {
		t.Fatal(err)
	}
	publication := filepath.Join(ancestor, "publication")
	if err := os.Mkdir(publication, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ancestor, 0111); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(ancestor, 0700); err != nil {
			t.Error(err)
		}
	})
	e := Endpoint{Box: "box", Incarnation: strings.Repeat("a", 32)}
	if err := prepareLegacyIdentityAt(publication, e); err != nil {
		t.Fatal("search-only ancestor prevented readable final publication", err)
	}
	raw, err := os.ReadFile(filepath.Join(publication, "identity.json"))
	want, marshalErr := json.Marshal(e)
	if err != nil || marshalErr != nil || !bytes.Equal(raw, want) {
		t.Fatal("final readable publication bytes changed", err, marshalErr)
	}
}
