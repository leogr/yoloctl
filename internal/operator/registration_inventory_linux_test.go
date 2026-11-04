package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func writeRegistrationIdentityFixture(t *testing.T, root string, e Endpoint) {
	t.Helper()
	dir := filepath.Join(root, e.Incarnation)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "identity.json"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func registrationInventoryFixture(t *testing.T) (string, *RegistrationInventory) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, e := range []Endpoint{{"box", strings.Repeat("a", 32)}, {"box", strings.Repeat("b", 32)}, {"other", strings.Repeat("c", 32)}} {
		writeRegistrationIdentityFixture(t, root, e)
	}
	p, err := observeRegistrationInventory(t.Context(), func() (*os.File, error) { return openEndpointDirectory(unix.AT_FDCWD, root, false) }, os.Geteuid(), os.Getegid(), endpointCurrentOK)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return root, p
}

func TestOperatorRegistrationInventoryPreservesAllSameBoxAndKeepObservations(t *testing.T) {
	root, p := registrationInventoryFixture(t)
	all, err := p.ForBox("box", "")
	if err != nil || len(all) != 2 || all[0].Incarnation != strings.Repeat("a", 32) || all[1].Incarnation != strings.Repeat("b", 32) {
		t.Fatal("missing current label suppressed older registrations", all, err)
	}
	kept, err := p.ForBox("box", strings.Repeat("b", 32))
	if err != nil || len(kept) != 1 || kept[0] != all[0] {
		t.Fatal("same-box keep semantics lost", kept, err)
	}
	missingKeep, err := p.ForBox("box", strings.Repeat("d", 32))
	if err != nil || len(missingKeep) != 2 {
		t.Fatal("absent selected incarnation suppressed siblings", missingKeep, err)
	}
	if _, err := p.ForBox("box", strings.Repeat("c", 32)); err == nil {
		t.Fatal("foreign kept incarnation accepted")
	}
	all[0].Box = "changed"
	copy, err := p.ForBox("box", "")
	if err != nil || len(copy) != 2 || copy[0].Box != "box" {
		t.Fatal("caller changed original identity inventory", copy, err)
	}
	// Identity inventory deliberately does not read or bless client/socket
	// members. Their complete role/readiness/deletion checks are independent.
	if err := os.WriteFile(filepath.Join(root, strings.Repeat("a", 32), "client.json"), []byte("routing body is outside this identity observation"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckCurrent(t.Context(), endpointCurrentOK); err != nil {
		t.Fatal("non-identity publication changed ownership observation", err)
	}
	if len(p.entries) != 3 {
		t.Fatal("complete identity inventory omitted foreign box")
	}
}

func TestOperatorRegistrationInventoryRejectsChangedOriginal(t *testing.T) {
	for _, change := range []string{"new-registration", "missing-identity", "identity-replaced", "identity-body", "root-replaced", "root-mount"} {
		t.Run(change, func(t *testing.T) {
			root, p := registrationInventoryFixture(t)
			identity := filepath.Join(root, strings.Repeat("a", 32), "identity.json")
			switch change {
			case "new-registration":
				writeRegistrationIdentityFixture(t, root, Endpoint{"box", strings.Repeat("d", 32)})
			case "missing-identity":
				if err := os.Remove(identity); err != nil {
					t.Fatal(err)
				}
			case "identity-replaced":
				raw, err := os.ReadFile(identity)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(identity, identity+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(identity, raw, 0o644); err != nil {
					t.Fatal(err)
				}
			case "identity-body":
				raw, _ := json.Marshal(Endpoint{"other", strings.Repeat("a", 32)})
				if err := os.WriteFile(identity, raw, 0o644); err != nil {
					t.Fatal(err)
				}
			case "root-replaced":
				if err := os.Rename(root, root+"-old"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Rename(root+"-old", root) })
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Remove(root) })
			case "root-mount":
				p.rootID.mount++ // comparison only; no mount probe
			}
			if err := p.CheckCurrent(t.Context(), endpointCurrentOK); err == nil {
				t.Fatal("changed original identity inventory accepted")
			}
		})
	}
}

func TestOperatorRegistrationInventoryNeverFabricatesEmptyAfterFailedRead(t *testing.T) {
	for _, invalid := range []string{"missing-root", "missing-identity", "identity-link", "directory-link", "bad-name", "oversized-identity", "foreign-incarnation"} {
		t.Run(invalid, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			e := Endpoint{"box", strings.Repeat("a", 32)}
			writeRegistrationIdentityFixture(t, root, e)
			dir := filepath.Join(root, e.Incarnation)
			identity := filepath.Join(dir, "identity.json")
			switch invalid {
			case "missing-root":
				root = filepath.Join(root, "absent")
			case "missing-identity":
				if err := os.Remove(identity); err != nil {
					t.Fatal(err)
				}
			case "identity-link":
				if err := os.Remove(identity); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("missing", identity); err != nil {
					t.Fatal(err)
				}
			case "directory-link":
				foreign := filepath.Join(t.TempDir(), e.Incarnation)
				if err := os.Rename(dir, foreign); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(foreign, dir); err != nil {
					t.Fatal(err)
				}
			case "bad-name":
				if err := os.Mkdir(filepath.Join(root, "foreign"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "oversized-identity":
				raw, _ := json.Marshal(e)
				if err := os.WriteFile(identity, append(raw, []byte(strings.Repeat(" ", 4097))...), 0o644); err != nil {
					t.Fatal(err)
				}
			case "foreign-incarnation":
				raw, _ := json.Marshal(Endpoint{"box", strings.Repeat("b", 32)})
				if err := os.WriteFile(identity, raw, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			p, err := observeRegistrationInventory(t.Context(), func() (*os.File, error) { return openEndpointDirectory(unix.AT_FDCWD, root, false) }, os.Geteuid(), os.Getegid(), endpointCurrentOK)
			if p != nil {
				_ = p.Close()
			}
			if err == nil || p != nil {
				t.Fatal("failed identity inventory became successful empty/partial observation", err)
			}
		})
	}
}

func TestOperatorRegistrationInventoryStrictIdentityKeys(t *testing.T) {
	id := strings.Repeat("a", 32)
	for name, raw := range map[string]string{
		"duplicate": `{"box":"box","box":"other","incarnation":"` + id + `"}`,
		"folded":    `{"box":"box","Box":"other","incarnation":"` + id + `"}`,
		"missing":   `{"box":"box"}`,
		"null":      `{"box":null,"incarnation":"` + id + `"}`,
		"trailing":  `{"box":"box","incarnation":"` + id + `"}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeRegistrationIdentity([]byte(raw)); err == nil {
				t.Fatal("ambiguous operator identity decoded")
			}
		})
	}
}

func TestOperatorRegistrationInventoryCurrentExecutorAndLifetimeBoundaries(t *testing.T) {
	_, p := registrationInventoryFixture(t)
	var zero RegistrationInventory
	if _, err := zero.ForBox("box", ""); err == nil {
		t.Fatal("zero inventory fabricated empty registration observation")
	}
	if err := zero.CheckCurrent(t.Context(), endpointCurrentOK); err == nil {
		t.Fatal("zero inventory verified current registration state")
	}
	calls := 0
	if got, err := ObserveRegistrationInventory(t.Context(), func(context.Context) error { calls++; return nil }); err == nil || got != nil || calls != 0 {
		t.Fatal("ordinary executor reached native identity inventory")
	}
	boom := errors.New("current storage/kernel proof unavailable")
	if err := p.CheckCurrent(t.Context(), func(context.Context) error { return boom }); !errors.Is(err, boom) {
		t.Fatal("current proof failure suppressed", err)
	}
	if err := p.CheckCurrent(t.Context(), endpointCurrentOK); !errors.Is(err, boom) {
		t.Fatal("new check repaired original failed proof", err)
	}
	_, cancelled := registrationInventoryFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := cancelled.CheckCurrent(ctx, endpointCurrentOK); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled identity inventory accepted", err)
	}
	if fmt.Sprintf("%v %#v", p, p) != "<operator registration identities> <operator registration identities>" {
		t.Fatal("formatter exposed identity observation")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ForBox("box", ""); err == nil {
		t.Fatal("closed identity inventory selected entries")
	}
	if err := p.CheckCurrent(t.Context(), endpointCurrentOK); err == nil {
		t.Fatal("closed identity inventory verified current state")
	}
}

func TestOperatorRegistrationInventoryAcceptsActuallyCapturedEmptyRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	p, err := observeRegistrationInventory(t.Context(), func() (*os.File, error) {
		return openEndpointDirectory(unix.AT_FDCWD, root, false)
	}, os.Geteuid(), os.Getegid(), endpointCurrentOK)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	entries, err := p.ForBox("box", "")
	if err != nil || len(entries) != 0 {
		t.Fatal("genuine empty root confused with uncaptured zero value", entries, err)
	}
}

func TestOperatorRegistrationInventoryFinalVerifierReplacementRefuses(t *testing.T) {
	for _, where := range []string{"constructor", "check"} {
		for _, kind := range []string{"directory", "identity"} {
			t.Run(where+"-"+kind, func(t *testing.T) {
				root := t.TempDir()
				if err := os.Chmod(root, 0700); err != nil {
					t.Fatal(err)
				}
				e := Endpoint{"box", strings.Repeat("a", 32)}
				writeRegistrationIdentityFixture(t, root, e)
				dir := filepath.Join(root, e.Incarnation)
				identity := filepath.Join(dir, "identity.json")
				raw, err := os.ReadFile(identity)
				if err != nil {
					t.Fatal(err)
				}
				saved := filepath.Join(t.TempDir(), "original")
				changed := false
				calls := 0
				at := 3
				if where == "check" {
					at = 2
				}
				current := func(context.Context) error {
					calls++
					if calls == at {
						changed = true
						if kind == "directory" {
							if err := os.Rename(dir, saved); err != nil {
								t.Fatal(err)
							}
							writeRegistrationIdentityFixture(t, root, e)
						} else {
							if err := os.Rename(identity, saved); err != nil {
								t.Fatal(err)
							}
							if err := os.WriteFile(identity, raw, 0644); err != nil {
								t.Fatal(err)
							}
						}
					}
					return nil
				}
				open := func() (*os.File, error) { return openEndpointDirectory(unix.AT_FDCWD, root, false) }
				if where == "constructor" {
					p, err := observeRegistrationInventory(t.Context(), open, os.Geteuid(), os.Getegid(), current)
					if p != nil {
						_ = p.Close()
					}
					if err == nil {
						t.Error("registration final verifier replacement escaped complete member validation")
					}
				} else {
					p, err := observeRegistrationInventory(t.Context(), open, os.Geteuid(), os.Getegid(), endpointCurrentOK)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = p.Close() }()
					if err := p.CheckCurrent(t.Context(), current); err == nil {
						t.Error("registration final verifier replacement escaped complete member validation")
					}
				}
				if !changed {
					t.Fatal("final verifier replacement schedule did not execute")
				}
			})
		}
	}
}

func TestOperatorRegistrationInventoryDetectedLossAndCancellationStaySticky(t *testing.T) {
	for _, which := range []string{"replacement", "cancellation"} {
		t.Run(which, func(t *testing.T) {
			root, p := registrationInventoryFixture(t)
			identity := filepath.Join(root, strings.Repeat("a", 32), "identity.json")
			saved := filepath.Join(t.TempDir(), "saved")
			if which == "replacement" {
				raw, err := os.ReadFile(identity)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(identity, saved); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(identity, raw, 0644); err != nil {
					t.Fatal(err)
				}
				if err := p.CheckCurrent(t.Context(), endpointCurrentOK); err == nil {
					t.Fatal("original identity loss not observed")
				}
				if err := os.Rename(identity, filepath.Join(t.TempDir(), "replacement")); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(saved, identity); err != nil {
					t.Fatal(err)
				}
			} else {
				ctx, cancel := context.WithCancel(t.Context())
				calls := 0
				current := func(context.Context) error {
					calls++
					if calls == 2 {
						cancel()
					}
					return nil
				}
				if err := p.CheckCurrent(ctx, current); !errors.Is(err, context.Canceled) {
					t.Fatal("final cancellation hidden", err)
				}
			}
			if err := p.CheckCurrent(t.Context(), endpointCurrentOK); err == nil {
				t.Fatal("new context/restored original repaired failed observation")
			}
			if _, err := p.IdentitiesForBox("box", ""); err == nil {
				t.Fatal("failed identity inventory still supplied classification metadata")
			}
			if _, err := p.ForBox("box", ""); err == nil {
				t.Fatal("failed identity inventory still supplied legacy metadata")
			}
		})
	}
}
func TestOperatorRegistrationInventoryOriginalCloseFailureIsSticky(t *testing.T) {
	_, p := registrationInventoryFixture(t)
	if err := p.root.Close(); err != nil {
		t.Fatal(err)
	}
	first := p.Close()
	if !errors.Is(first, os.ErrClosed) {
		t.Fatal(first)
	}
	if err := p.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("original Close failure lost", err)
	}
}
