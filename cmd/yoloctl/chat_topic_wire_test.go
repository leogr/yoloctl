package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/chatui"
)

// The human's topic commands cross every translation layer here: the native
// view's command parsing, the host's Action mapping, RunChannels, the relay's
// FilterChannels and the AIRC JSONL wire, then events back into the view. Only
// the controller transport (a byte pipe) and the real AIRC process are
// replaced; the peer is scripted, and the real-AIRC tests cover the server.
func TestChatTopicCommandsReachWireThroughHostAndRelay(t *testing.T) {
	for _, schema2 := range []bool{false, true} {
		t.Run(fmt.Sprint("schema2=", schema2), func(t *testing.T) {
			store, inventory := roomRuntimeFixture(t)
			room, err := ensureManagedRoom(store, chatRuntimeRequest{DefaultHuman: "person"}, inventory)
			if err != nil {
				t.Fatal(err)
			}
			if schema2 {
				if room, err = store.CreateChannel(room.ID, "#build", room.Generation, inventory); err != nil {
					t.Fatal(err)
				}
			}
			header, err := chatstream.RoomHeader(room)
			if err != nil {
				t.Fatal(err)
			}
			self := header.Nick + "!" + header.SelfAccount + "@airc"
			server := "yoloctl-" + header.RoomID[:12]

			hostRead, peerWrite := io.Pipe()
			filterRead, hostWrite := io.Pipe()
			peerRead, filterWrite := io.Pipe()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			humanID := room.Human.ID
			go func() {
				// The relay authorizes each channel-bearing request, then forwards it.
				err := chatstream.FilterChannels(filterRead, filterWrite, func(name string) error {
					return authorizeChatStreamChannel(ctx, store, chatStreamOrigin{RoomID: header.RoomID, DiskID: header.DiskID, HumanID: humanID, Generation: header.Generation}, name)
				}, false)
				_ = filterWrite.CloseWithError(err)
				_ = filterRead.Close()
			}()

			var mu sync.Mutex
			var topics []map[string]json.RawMessage
			var joins []string
			peerDone := make(chan error, 1)
			go func() {
				defer func() { _ = peerRead.Close(); _ = peerWrite.Close() }()
				enc := json.NewEncoder(peerWrite)
				if err := enc.Encode(header); err != nil {
					peerDone <- err
					return
				}
				if err := enc.Encode(map[string]any{"event": "ready", "version": 2, "epoch": "topic-wire", "account": header.SelfAccount, "nick": header.Nick, "multiline": map[string]int{"max_bytes": 65536, "max_lines": 1000}}); err != nil {
					peerDone <- err
					return
				}
				numeric := func(code string, params ...string) map[string]any {
					return map[string]any{"command": code, "prefix": server, "params": append([]string{header.Nick}, params...)}
				}
				// No initial 331/332: the view starts without a known topic, so its
				// first /topic must query the wire.
				queue := []map[string]any{numeric("422", "No MOTD"), {"command": "JOIN", "prefix": self, "params": []string{"#space"}}, numeric("366", "#space", "end")}
				current, cursor, serial := "", 0, 0
				reader := bufio.NewReader(peerRead)
				for {
					line, err := reader.ReadBytes('\n')
					if err != nil {
						peerDone <- nil
						return
					}
					var request chatstream.Request
					if err := json.Unmarshal(line, &request); err != nil {
						peerDone <- err
						return
					}
					var result any = map[string]string{"status": "queued"}
					switch request.Op {
					case "read":
						var page []map[string]any
						if len(queue) > 0 {
							cursor++
							event := queue[0]
							queue = queue[1:]
							event["cursor"] = cursor
							page = append(page, event)
						}
						result = map[string]any{"epoch": "topic-wire", "events": page, "next": cursor, "latest": cursor + len(queue)}
					case "who":
						// Records are handled in order: once the view sees this marker,
						// the driver has finished the WHO setup that blocks controls.
						marker := map[string]any{"command": "PRIVMSG", "prefix": "peer!peer@airc", "params": []string{request.Channel, "setup complete"}, "tags": map[string]string{"msgid": "setup-marker", "account": "peer"}}
						queue = append(queue, numeric("352", request.Channel, header.SelfAccount, "airc", server, header.Nick, "H@", "0 human"), numeric("315", request.Channel, "end"), marker)
					case "join":
						mu.Lock()
						joins = append(joins, request.Channel)
						mu.Unlock()
						queue = append(queue, map[string]any{"command": "JOIN", "prefix": self, "params": []string{request.Channel}},
							numeric("353", "=", request.Channel, "@"+header.Nick), numeric("366", request.Channel, "end"))
					case "history":
						serial++
						batch := fmt.Sprintf("h%d", serial)
						queue = append(queue, map[string]any{"command": "BATCH", "prefix": server, "params": []string{"+" + batch, "chathistory", request.Channel}}, map[string]any{"command": "BATCH", "prefix": server, "params": []string{"-" + batch}})
					case "topic":
						var fields map[string]json.RawMessage
						if err := json.Unmarshal(line, &fields); err != nil {
							peerDone <- err
							return
						}
						mu.Lock()
						topics = append(topics, fields)
						mu.Unlock()
						// AIRC: no text queries; empty text clears; other text sets.
						if raw, ok := fields["text"]; ok {
							if err := json.Unmarshal(raw, &current); err != nil {
								peerDone <- err
								return
							}
							queue = append(queue, map[string]any{"command": "TOPIC", "prefix": self, "params": []string{request.Channel, current}})
						} else if current == "" {
							queue = append(queue, numeric("331", request.Channel, "No topic is set"))
						} else {
							queue = append(queue, numeric("332", request.Channel, current))
						}
					case "send":
						result = map[string]string{"receipt": request.ID, "state": "pending"}
						queue = append(queue, map[string]any{"event": "receipt", "receipt": request.ID, "state": "confirmed", "target": request.Target, "msgid": "new-channel-message"})
					default:
						peerDone <- fmt.Errorf("unexpected op %s", request.Op)
						return
					}
					if err := enc.Encode(map[string]any{"id": request.ID, "ok": true, "result": result}); err != nil {
						peerDone <- nil
						return
					}
				}
			}()

			identities, members := chatIdentities(header)
			s := &chatSession{ctx: ctx, members: members}
			l := &chatLink{outgoing: make(chan chatstream.Send, 1), older: make(chan chatstream.HistoryRequest, 1), actions: make(chan chatstream.Action, 1),
				catalogs: make(chan chatstream.Catalog, 1), cancel: cancel, done: make(chan struct{})}
			s.link = l
			events := make(chan chatstream.Event, 256)
			runDone := make(chan error, 1)
			go func() {
				runDone <- chatstream.RunChannels(ctx, hostRead, hostWrite, l.outgoing, l.older, l.actions, l.catalogs, func(e chatstream.Event) { events <- e })
			}()
			var actions []chatui.Action
			refreshes := 0
			opts := chatui.Options{Space: "dev", Channel: header.Channel, Nick: header.Nick, Account: header.SelfAccount, MaxMessageBytes: chatstream.MaxMessageBytes, Identities: identities}
			opts.Control = func(a chatui.Action) tea.Cmd {
				actions = append(actions, a)
				if msg := s.control(a, func(context.Context) (chatstream.Catalog, error) {
					refreshes++
					latest, err := store.ReadManifestSnapshot()
					if err != nil {
						return chatstream.Catalog{}, err
					}
					header, err := chatstream.RoomHeader(latest)
					return chatstream.Catalog{Header: header, RuntimeApplied: true}, err
				})(); msg != nil { // the real host mapping and queue, with an inert typed-read transport
					t.Fatalf("action was not queued: %+v", msg)
				}
				return nil
			}
			opts.Send = func(out chatui.Outgoing) tea.Cmd {
				if msg := s.send(out)(); msg != nil {
					t.Fatalf("send was not queued: %+v", msg)
				}
				return nil
			}
			m := chatui.New(opts)
			m.Update(tea.WindowSizeMsg{Width: 110, Height: 30})
			// pump delivers driver events to the view, as chatSession.start does.
			pump := func(what string, done func(chatstream.Event) bool) {
				t.Helper()
				for {
					select {
					case e := <-events:
						if e.Kind == "gap" {
							t.Fatalf("unexpected history gap: %v", e.Err)
						}
						if e.Kind == "catalog" && e.Catalog != nil {
							s.adoptCatalogFor(nil, e.Catalog.Header)
						}
						if msg := chatViewEvent(e); msg != nil {
							m.Update(msg)
						}
						if done(e) {
							return
						}
					case err := <-runDone:
						var peerErr error
						select {
						case peerErr = <-peerDone:
						case <-time.After(time.Second):
						}
						t.Fatalf("stream ended while waiting for %s: %v (peer: %v)", what, err, peerErr)
					case <-ctx.Done():
						t.Fatalf("timed out waiting for %s", what)
					}
				}
			}
			pump("#space setup", func(e chatstream.Event) bool { return e.Kind == "message" && e.Message.Text == "setup complete" })

			// A known topic answers /topic locally; only an unknown one queries.
			for _, tc := range []struct {
				command, kind, text string
				hasText             bool
				shown               string
			}{
				{"/topic", "topic-read", "", false, ""}, // the reply only updates the band
				{"/topic set checkpoint-test", "topic-set", "checkpoint-test", true, ""},
				{"/topic", "", "", false, "Topic of #space: checkpoint-test"},
				{"/topic clear", "topic-clear", "", true, ""},
				{"/topic", "", "", false, "No topic is set for #space"},
			} {
				mu.Lock()
				before := len(topics)
				mu.Unlock()
				queued := len(actions)
				m.Update(tea.PasteMsg{Content: tc.command})
				m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
				if tc.kind == "" {
					if len(actions) != queued {
						t.Fatalf("%q with a known topic queued %+v", tc.command, actions[queued:])
					}
				} else {
					if len(actions) != queued+1 || actions[queued].Kind != tc.kind || actions[queued].Channel != "#space" {
						t.Fatalf("%q produced %+v (draft %q)", tc.command, actions[queued:], m.Draft())
					}
					id := actions[queued].ID
					var control chatstream.Event
					pump(tc.command, func(e chatstream.Event) bool {
						if e.Kind == "control" && e.ID == id {
							control = e
							return true
						}
						return false
					})
					if control.State != "confirmed" || control.Err != nil || control.Channel != "#space" || control.Action != tc.kind {
						t.Fatalf("%q was not confirmed end to end: %+v", tc.command, control)
					}
				}
				mu.Lock()
				sent := topics[before:]
				mu.Unlock()
				if want := map[bool]int{true: 1, false: 0}[tc.kind != ""]; len(sent) != want {
					t.Fatalf("%q reached the wire %d times", tc.command, len(sent))
				}
				if len(sent) == 1 {
					wire := sent[0]
					fields := 3 // id, op, channel
					if tc.hasText {
						fields++
					}
					raw, hasText := wire["text"]
					if hasText != tc.hasText || hasText && string(raw) != fmt.Sprintf("%q", tc.text) || string(wire["channel"]) != `"#space"` || len(wire) != fields {
						t.Fatalf("%q wire request: %v", tc.command, wire)
					}
				}
				if m.Draft() != "" {
					t.Fatalf("%q left its draft %q", tc.command, m.Draft())
				}
				view := ansi.Strip(m.View().Content)
				if strings.Contains(view, "failed") || strings.Contains(view, "unexpected") || !strings.Contains(view, tc.shown) {
					t.Fatalf("%q: want %q without failures:\n%s", tc.command, tc.shown, view)
				}
			}
			// The channel is created after the stream opened. Its catalog travels
			// in the original JOIN action; no periodic refresh or retry is needed.
			latest, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateChannel(latest.ID, "#test-channel", latest.Generation, inventory); err != nil {
				t.Fatal(err)
			}
			m.Update(tea.PasteMsg{Content: "/join #test-channel"})
			m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
			pump("new channel setup", func(e chatstream.Event) bool {
				return e.Kind == "message" && e.Channel == "#test-channel" && e.Message.Text == "setup complete"
			})
			if refreshes != 1 {
				t.Fatalf("explicit JOIN refreshed %d times", refreshes)
			}
			mu.Lock()
			joined := append([]string(nil), joins...)
			mu.Unlock()
			if len(joined) != 1 || joined[0] != "#test-channel" {
				t.Fatalf("new channel sent unexpected JOINs: %v", joined)
			}
			m.Update(tea.PasteMsg{Content: "message after join"})
			m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
			pump("send after JOIN", func(e chatstream.Event) bool {
				return e.Kind == "delivery" && e.Channel == "#test-channel" && e.State == "confirmed"
			})
			if m.Draft() != "" {
				t.Fatal("confirmed send after JOIN retained an unchanged draft")
			}
			cancel()
			<-runDone
			if err := <-peerDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}
