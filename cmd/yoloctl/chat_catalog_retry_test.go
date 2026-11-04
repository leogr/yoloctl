package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
)

func TestCatalogRetriesOnlyConcurrentChange(t *testing.T) {
	for _, kind := range []string{"once", "twice", "status", "runtime", "cancelled", "deadline", "identity-human", "identity-room", "identity-disk", "regression", "between-identity-human", "between-identity-room", "between-identity-disk", "between-regression", "strict-store", "cancel-during", "revoke", "between-missing"} {
		t.Run(kind, func(t *testing.T) {
			store, inventory, room, cfg := attachObserveFixture(t)
			original := chatObservationSnapshot
			t.Cleanup(func() { chatObservationSnapshot = original })
			reads, calls := 0, 0
			path := filepath.Join(store.Disk, layout.ChatManifest)
			rewrite := func(identity, regress bool) {
				t.Helper()
				r, err := store.ReadManifestSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				if identity {
					switch {
					case strings.HasSuffix(kind, "room"):
						r.ID = managedchat.NewID()
					case strings.HasSuffix(kind, "disk"):
						r.DiskID = managedchat.NewID()
					default:
						r.Human.ID = managedchat.NewID()
					}
				}
				if regress {
					r.Generation--
				} else {
					r.Generation++
				}
				raw, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			chatObservationSnapshot = func(s managedchat.Store) (*managedchat.Room, error) {
				reads++
				if reads == 3 && strings.HasPrefix(kind, "between-") {
					if kind == "between-missing" {
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					} else {
						rewrite(strings.Contains(kind, "identity"), kind == "between-regression")
					}
				}
				return original(s)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if kind == "cancelled" {
				cancel()
			}
			if kind == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 20*time.Millisecond)
				defer stop()
			}
			var deadline time.Time
			newBoxStateResolver = attachObserveRuntime(func(c context.Context) ([]container.Container, error) {
				calls++
				d, ok := c.Deadline()
				if !ok || time.Until(d) > chatObservationTimeout {
					t.Fatal("missing shared five-second bound")
				}
				if calls == 1 {
					deadline = d
				} else if d != deadline {
					t.Fatal("retry renewed its deadline")
				}
				if kind == "deadline" {
					<-c.Done()
				}
				switch kind {
				case "once", "status", "between-identity-human", "between-identity-room", "between-identity-disk", "between-regression", "between-missing":
					if calls > 1 {
						break
					}
					fallthrough
				case "twice", "deadline", "cancel-during":
					current, err := store.ReadManifestSnapshot()
					if err != nil {
						t.Fatal(err)
					}
					if _, err = store.SetHumanNickname("nick"+strings.Repeat("x", calls), current.Generation, inventory); err != nil {
						t.Fatal(err)
					}
				case "identity-human", "identity-room", "identity-disk":
					rewrite(true, false)
				case "regression":
					rewrite(false, true)
				case "revoke":
					if calls == 1 {
						if _, err := store.Revoke(room.Members[0].ID, room.Generation, inventory); err != nil {
							t.Fatal(err)
						}
					}
				}
				if kind == "cancel-during" {
					cancel()
				}
				return []container.Container{{Name: cfg.Name, Status: "Exited (0)"}}, nil
			})
			if kind == "strict-store" {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			observe := observeManagedChatCatalog
			if kind == "status" {
				observe = observeManagedChatRead
			}
			got, err := observe(ctx, "work", store)
			want := 1
			switch kind {
			case "once", "twice", "revoke":
				want = 2
			case "cancelled", "strict-store":
				want = 0
			}
			if calls != want {
				t.Fatalf("attempts=%d want %d (reads=%d, err=%v)", calls, want, reads, err)
			}
			switch kind {
			case "once":
				if err != nil || got.Room == nil || got.Room.Generation != room.Generation+1 || got.Room.Human.Nickname != "nickx" {
					t.Fatalf("second consistent snapshot: %+v %v", got, err)
				}
			case "revoke":
				if err != nil || got.Room == nil || len(got.Room.Members) != 1 || !got.Room.Members[0].Revoked || got.Room.Generation != room.Generation+1 {
					t.Fatalf("revocation not reflected: %+v %v", got, err)
				}
			case "runtime":
				if err != nil || got.Room == nil || got.Room.Generation != room.Generation {
					t.Fatal(got, err)
				}
			case "cancelled", "deadline", "cancel-during":
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			default:
				if err == nil || got.Room != nil {
					t.Fatalf("failure published catalog: %+v %v", got, err)
				}
			}
			if (kind == "twice" || kind == "status") && !errors.Is(err, errChatObservationChanged) {
				t.Fatal("lost concurrent-change classification", err)
			}
			if strings.Contains(kind, "identity") || strings.Contains(kind, "regression") {
				wantReads := 2
				if strings.HasPrefix(kind, "between-") {
					wantReads = 3
				}
				if reads != wantReads {
					t.Fatalf("identity/regression continued observation: reads=%d want=%d", reads, wantReads)
				}
				if errors.Is(err, errChatObservationChanged) {
					t.Fatal("identity or regression was retryable", err)
				}
			}
		})
	}
}

func TestCatalogAuthorityFailureDoesNotObserveOrRetry(t *testing.T) {
	store, _, _, _ := attachObserveFixture(t)
	original := chatObservationSnapshot
	t.Cleanup(func() { chatObservationSnapshot = original })
	reads := 0
	chatObservationSnapshot = func(s managedchat.Store) (*managedchat.Room, error) { reads++; return original(s) }
	ctx := withControllerPrincipal(t.Context(), controller.Principal{Box: "coder"}) // invalid incarnation
	got, err := observeManagedChatCatalog(ctx, "work", store)
	if err == nil || got.Room != nil || reads != 0 {
		t.Fatalf("authority failure reached observation: reads=%d, result=%+v err=%v", reads, got, err)
	}
}
