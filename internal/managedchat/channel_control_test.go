package managedchat

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"testing"
)

func TestChannelReloadRequiresPreAcknowledgmentAndPostIncarnation(t *testing.T) {
	before, want := appliedFixture()
	want.Generation++
	want.ConfigSHA256 = strings.Repeat("b", 64)
	partial := Applied{Incarnation: before.Incarnation, Generation: want.Generation, ConfigSHA256: want.ConfigSHA256}
	if err := partial.MatchesReload(want, before); err != nil {
		t.Fatal("legitimate partial reload acknowledgment rejected", err)
	}
	if err := partial.MatchesIncarnation(want, before); err == nil {
		t.Fatal("partial acknowledgment substituted for full live status")
	}
	after := before
	after.Generation, after.ConfigSHA256 = want.Generation, want.ConfigSHA256
	if err := after.MatchesIncarnation(want, before); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"replacement", "generation", "digest", "missing incarnation"} {
		t.Run(kind, func(t *testing.T) {
			ack := partial
			switch kind {
			case "replacement":
				ack.Incarnation = NewID()
			case "generation":
				ack.Generation--
			case "digest":
				ack.ConfigSHA256 = before.ConfigSHA256
			case "missing incarnation":
				ack.Incarnation = ""
			}
			if err := ack.MatchesReload(want, before); err == nil {
				t.Fatal("different reload proof accepted")
			}
		})
	}
	after.Incarnation = NewID()
	if err := after.Matches(want); err != nil {
		t.Fatal("fixture must demonstrate otherwise valid replacement", err)
	}
	if err := after.MatchesIncarnation(want, before); err == nil {
		t.Fatal("fresh process confirmed previous process's reload")
	}
	if err := partial.MatchesReload(want, Applied{}); err == nil {
		t.Fatal("missing pre-publication proof accepted")
	}
}

func TestChannelPartialReloadWireProof(t *testing.T) {
	before, want := appliedFixture()
	want.Generation++
	c := testControlSocket(t, func(c *net.UnixConn) {
		line, _ := bufio.NewReader(c).ReadString('\n')
		if strings.TrimSpace(line) != `{"op":"reload"}` {
			return
		}
		// Actual AIRC reload's wire shape intentionally omits full inventory.
		_ = json.NewEncoder(c).Encode(map[string]any{"ok": true, "result": map[string]any{
			"incarnation": before.Incarnation, "applied_generation": want.Generation,
			"config_sha256": want.ConfigSHA256, "disconnected": []string{},
			"kicked": []any{}, "kicked_count": 0, "kicked_truncated": false,
		}})
	})
	ack, err := c.Call(t.Context(), "reload")
	if err != nil {
		t.Fatal(err)
	}
	if err := ack.MatchesReload(want, before); err != nil {
		t.Fatal(err)
	}
	if err := ack.MatchesIncarnation(want, before); err == nil {
		t.Fatal("wire acknowledgment was accepted as readiness inventory")
	}
}
