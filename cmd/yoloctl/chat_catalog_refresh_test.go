package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/managedchat"
)

// Exercise actual controller/host builders across their JSON boundary, then
// the real stream loop. This peer is a fixture, not a live AIRC server or VM.
func TestChatChannelHostCatalogShapesThroughRunChannels(t *testing.T) {
	for _, schema2 := range []bool{false, true} {
		t.Run(fmt.Sprint("schema2=", schema2), func(t *testing.T) {
			store, inventory := roomRuntimeFixture(t)
			room, err := ensureManagedRoom(store, chatRuntimeRequest{DefaultHuman: "person"}, inventory)
			if err != nil {
				t.Fatal(err)
			}
			if schema2 {
				room, err = store.CreateChannel(room.ID, "#build", room.Generation, inventory)
				if err != nil {
					t.Fatal(err)
				}
			}
			room, err = store.SetHumanNickname("live-before", room.Generation, inventory)
			if err != nil {
				t.Fatal(err)
			}
			header, err := chatstream.RoomHeader(room)
			if err != nil {
				t.Fatal(err)
			}
			// The stream probes the confined client's capabilities after building
			// RoomHeader. Catalog reads do not carry that connection capability.
			// runControllerChatStream registers the saved nickname itself; a
			// later save changes only the catalog until an explicit reconnect.
			header.Attachments = true
			toCatalog := func(r *managedchat.Room) chatstream.Catalog {
				t.Helper()
				snapshot, err := channelSnapshot(managedchat.Status{Room: r})
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				var decoded chatChannelSnapshot
				if err := json.Unmarshal(raw, &decoded); err != nil {
					t.Fatal(err)
				}
				// This is the exact projection used by readChatChannelCatalog.
				return chatChannelCatalog(decoded)
			}
			equal := toCatalog(room)
			stale := toCatalog(room)
			stale.Header.Generation-- // Still a valid positive generation.
			room, err = store.SetHumanNickname("saved-after", room.Generation, inventory)
			if err != nil {
				t.Fatal(err)
			}
			saved := toCatalog(room)
			catalogs := make(chan chatstream.Catalog, 4)
			for _, catalog := range []chatstream.Catalog{equal, stale, saved, saved} {
				catalogs <- catalog
			}
			hostRead, peerWrite := io.Pipe()
			peerRead, hostWrite := io.Pipe()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			peerDone := make(chan error, 1)
			go func() {
				defer func() { _ = peerRead.Close(); _ = peerWrite.Close() }()
				enc := json.NewEncoder(peerWrite)
				// RunChannels decodes the real RoomHeader's JSON wire shape.
				if err := enc.Encode(header); err != nil {
					peerDone <- err
					return
				}
				if err := enc.Encode(map[string]any{"event": "ready", "version": 2, "epoch": "host-shapes", "account": header.SelfAccount, "nick": header.Nick, "multiline": map[string]int{"max_bytes": 65536, "max_lines": 1000}}); err != nil {
					peerDone <- err
					return
				}
				dec := json.NewDecoder(peerRead)
				for {
					var request chatstream.Request
					if err := dec.Decode(&request); err != nil {
						peerDone <- nil
						return
					}
					if request.Op != "read" {
						peerDone <- fmt.Errorf("catalog refresh issued unsolicited %s", request.Op)
						return
					}
					if err := enc.Encode(map[string]any{"id": request.ID, "ok": true, "result": map[string]any{"epoch": "host-shapes", "events": []any{}, "next": 0, "latest": 0}}); err != nil {
						peerDone <- nil
						return
					}
				}
			}()
			accepted, refused, savedCount := 0, 0, 0
			err = chatstream.RunChannels(ctx, hostRead, hostWrite, make(chan chatstream.Send), nil, nil, catalogs, func(event chatstream.Event) {
				if event.Kind != "catalog" {
					return
				}
				if event.State == "refused" {
					refused++
				} else if event.Catalog != nil {
					if !event.Catalog.Header.Attachments {
						t.Error("catalog refresh dropped the stream's attachment capability")
					}
					if accepted == 0 && event.Catalog.Header.Nick != "live-before" {
						t.Errorf("the stream's own catalog must carry the saved nickname, not %q", event.Catalog.Header.Nick)
					}
					accepted++
					if event.Catalog.Header.Nick == "saved-after" {
						savedCount++
					}
				}
				if accepted == 4 && refused == 1 {
					cancel()
				}
			})
			if accepted != 4 || refused != 1 || savedCount != 2 {
				t.Fatalf("actual header/catalog shape drift: accepted=%d refused=%d saved=%d err=%v", accepted, refused, savedCount, err)
			}
			if err := <-peerDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}
