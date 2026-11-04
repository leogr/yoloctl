package chatstream

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/managedchat"
)

// Exercise the real multi-channel loop/transport rather than only event helpers.
// This is a synthetic JSONL peer, not live AIRC/VM authority validation.
func TestRunChannelsRoutesConfirmedControlHistoryAndSend(t *testing.T) {
	d, _, _ := channelDriverFixture(t)
	header := d.wire.header
	hostRead, serverWrite := io.Pipe()
	serverRead, hostWrite := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	outgoing := make(chan Send, 1)
	actions := make(chan Action, 1)
	done := make(chan error, 1)
	var spaceWHO, buildWHO atomic.Bool
	go func() {
		defer func() { _ = serverRead.Close(); _ = serverWrite.Close() }()
		enc := json.NewEncoder(serverWrite)
		if err := enc.Encode(header); err != nil {
			done <- err
			return
		}
		if err := enc.Encode(map[string]any{"event": "ready", "version": 2, "epoch": "loop-epoch", "account": header.SelfAccount, "nick": header.Nick, "multiline": map[string]int{"max_bytes": 65536, "max_lines": 1000}}); err != nil {
			done <- err
			return
		}
		queue := []record{selfRecord(d, "JOIN", "#space"), numericRecord(d, "331", "#space", "No topic"), numericRecord(d, "366", "#space", "end")}
		cursor, serial := uint64(0), 0
		reader := bufio.NewReader(serverRead)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				done <- nil
				return
			}
			var request Request
			if err := json.Unmarshal(line, &request); err != nil {
				done <- err
				return
			}
			var result any = map[string]string{"status": "queued"}
			switch request.Op {
			case "read":
				if request.After != cursor {
					done <- fmt.Errorf("cursor jumped from %d to %d", cursor, request.After)
					return
				}
				var page []record
				if len(queue) > 0 {
					cursor++
					event := queue[0]
					queue = queue[1:]
					event.Cursor = cursor
					page = []record{event}
				}
				result = map[string]any{"epoch": "loop-epoch", "events": page, "next": cursor, "latest": cursor + uint64(len(queue))}
			case "who":
				switch request.Channel {
				case "#space":
					spaceWHO.Store(true)
				case "#build":
					buildWHO.Store(true)
				}
				queue = append(queue, numericRecord(d, "315", request.Channel, "end"))
			case "join":
				queue = append(queue, selfRecord(d, "JOIN", request.Channel), numericRecord(d, "331", request.Channel, "No topic"), numericRecord(d, "366", request.Channel, "end"))
			case "history":
				serial++
				batch := fmt.Sprintf("h%d", serial)
				queue = append(queue, record{Command: "BATCH", Prefix: d.serverPrefix(), Params: []string{"+" + batch, "chathistory", request.Channel}}, record{Command: "PRIVMSG", Prefix: "peer!peer@airc", Params: []string{request.Channel, "retained\n\nbody"}, Tags: map[string]string{"batch": batch, "msgid": "old-" + batch, "account": "peer"}}, record{Command: "BATCH", Prefix: d.serverPrefix(), Params: []string{"-" + batch}})
			case "send":
				if request.Target != "#build" || request.Text != "captured build message" {
					done <- fmt.Errorf("wrong send target/body")
					return
				}
				result = map[string]string{"receipt": "sent", "state": "pending"}
				queue = append(queue, record{Event: "receipt", Receipt: "sent", Target: request.Target, State: "confirmed", MsgID: "new"})
			default:
				done <- fmt.Errorf("unexpected op %s", request.Op)
				return
			}
			if err := enc.Encode(map[string]any{"id": request.ID, "ok": true, "result": result}); err != nil {
				done <- nil
				return
			}
		}
	}()
	var events []Event
	joined, sent := false, false
	err := RunChannels(ctx, hostRead, hostWrite, outgoing, nil, actions, nil, func(event Event) {
		events = append(events, event)
		if event.Kind == "members" && event.Channel == "#space" && spaceWHO.Load() && !joined {
			joined = true
			actions <- Action{ID: "join-build", Kind: "join", Channel: "#build"}
		}
		if event.Kind == "members" && event.Channel == "#build" && buildWHO.Load() && !sent {
			sent = true
			outgoing <- Send{ID: "build-message", Channel: "#build", Text: "captured build message"}
		}
		if event.Kind == "delivery" && event.ID == "build-message" && event.State == "confirmed" {
			cancel()
		}
	})
	if err == nil {
		t.Fatal("expected explicit cancellation")
	}
	if peerErr := <-done; peerErr != nil {
		t.Fatal(peerErr)
	}
	confirmed, history := false, false
	for _, event := range events {
		if event.Kind == "delivery" && event.ID == "build-message" && event.Channel == "#build" && event.State == "confirmed" {
			confirmed = true
		}
		if event.Kind == "message" && event.Message.Replay && strings.Contains(event.Message.Text, "\n\n") && event.Channel == event.Message.Channel {
			history = true
		}
	}
	if !confirmed || !history {
		t.Fatalf("loop did not retain channel delivery/history: joined=%v sent=%v confirmed=%v history=%v err=%v", joined, sent, confirmed, history, err)
	}
}

func TestRunChannelsRefreshesEnrollmentAndRevocationWithoutReconnect(t *testing.T) {
	d, _, _ := channelDriverFixture(t)
	initial := normalizedHeader(d.wire.header)
	enrolled := normalizedHeader(initial)
	enrolled.Generation++
	memberID := managedchat.NewID()
	account, err := managedchat.AuthAccount(memberID)
	if err != nil {
		t.Fatal(err)
	}
	enrolled.Identities = append(enrolled.Identities, managedchat.BootstrapIdentity{ID: memberID, Account: account, Name: "new-worker", Nick: "worker", BoxID: managedchat.NewID(), Role: "member"})
	enrolled.Channels[0].Members = append(enrolled.Channels[0].Members, memberID)
	slices.Sort(enrolled.Channels[0].Members)
	conflicting := normalizedHeader(enrolled)
	conflicting.Identities[len(conflicting.Identities)-1].Name = "untrusted rename"
	revoked := normalizedHeader(enrolled)
	revoked.Generation++
	revoked.Identities[len(revoked.Identities)-1].Revoked = true
	revoked.Channels[0].Members = slices.DeleteFunc(revoked.Channels[0].Members, func(id string) bool { return id == memberID })

	hostRead, peerWrite := io.Pipe()
	peerRead, hostWrite := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	peerDone := make(chan error, 1)
	go func() {
		defer func() { _ = peerRead.Close(); _ = peerWrite.Close() }()
		enc := json.NewEncoder(peerWrite)
		if err := enc.Encode(initial); err != nil {
			peerDone <- err
			return
		}
		if err := enc.Encode(map[string]any{"event": "ready", "version": 2, "epoch": "refresh-epoch", "account": initial.SelfAccount, "nick": initial.Nick, "multiline": map[string]int{"max_bytes": 65536, "max_lines": 1000}}); err != nil {
			peerDone <- err
			return
		}
		dec := json.NewDecoder(peerRead)
		for {
			var request Request
			if err := dec.Decode(&request); err != nil {
				peerDone <- nil
				return
			}
			if request.Op != "read" {
				peerDone <- fmt.Errorf("catalog refresh issued unsolicited %s", request.Op)
				return
			}
			if err := enc.Encode(map[string]any{"id": request.ID, "ok": true, "result": map[string]any{"epoch": "refresh-epoch", "events": []record{}, "next": 0, "latest": 0}}); err != nil {
				peerDone <- nil
				return
			}
		}
	}()
	catalogs := make(chan Catalog, 1)
	updates := []Header{enrolled, initial, conflicting, revoked}
	var accepted []Catalog
	connected, refused, step := 0, 0, 0
	err = RunChannels(ctx, hostRead, hostWrite, nil, nil, nil, catalogs, func(event Event) {
		if event.Kind == "connection" && event.Connected {
			connected++
		}
		if event.Kind != "catalog" {
			return
		}
		if event.Catalog != nil {
			accepted = append(accepted, *event.Catalog)
		} else if event.State == "refused" && event.Err != nil {
			refused++
		}
		if step == len(updates) {
			cancel()
			return
		}
		catalogs <- Catalog{Header: updates[step]}
		step++
	})
	if peerErr := <-peerDone; peerErr != nil {
		t.Fatal(peerErr)
	}
	if err == nil || connected != 1 || refused != 2 || len(accepted) != 3 {
		t.Fatalf("refresh did not complete on the open stream: connected=%d refused=%d accepted=%d err=%v", connected, refused, len(accepted), err)
	}
	member := accepted[1].Header.Identities[len(enrolled.Identities)-1]
	if member.ID != memberID || member.Account != account || member.Name != "new-worker" || member.Revoked {
		t.Fatal("new authenticated enrollment was not delivered to the open view")
	}
	member = accepted[2].Header.Identities[len(revoked.Identities)-1]
	if member.ID != memberID || !member.Revoked || slices.Contains(accepted[2].Header.Channels[0].Members, memberID) {
		t.Fatal("revoked identity/access survived the trusted replacement")
	}
}
