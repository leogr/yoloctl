package managedchat

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func appliedFixture() (Applied, Rendered) {
	a := Applied{Features: Features{Protocol: 1, Features: []string{"unix-listeners", "control-reload", "acl-revoke", "server-presence"}}, Incarnation: NewID(), Generation: 7, ConfigSHA256: strings.Repeat("a", 64), Listeners: Listeners{IRC: "unix", Artifacts: "unix"}, Accounts: map[string]Connections{"human": {Connections: 1}}, ConfiguredAccounts: 2}
	return a, Rendered{Generation: 7, ConfigSHA256: a.ConfigSHA256, Accounts: []string{"human", "coder"}}
}

func TestAppliedRequiresExactConfigurationAndRevokedDisconnect(t *testing.T) {
	a, want := appliedFixture()
	if err := a.Matches(want); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Applied){
		"stale":                        func(a *Applied) { a.Generation-- },
		"same generation other config": func(a *Applied) { a.ConfigSHA256 = strings.Repeat("b", 64) },
		"missing incarnation":          func(a *Applied) { a.Incarnation = "" },
		"missing artifacts":            func(a *Applied) { a.Listeners.Artifacts = "" },
		"partial accounts":             func(a *Applied) { a.ConfiguredAccounts = 1 },
		"revoked still connected":      func(a *Applied) { a.Accounts = map[string]Connections{"old": {Connections: 1}} },
		"unsupported server":           func(a *Applied) { a.Features = Features{} },
		"running server lacks presence": func(a *Applied) {
			a.Features.Features = []string{"unix-listeners", "control-reload", "acl-revoke"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			copy := a
			change(&copy)
			if copy.Matches(want) == nil {
				t.Fatal("claimed applied")
			}
		})
	}
}

func testControlSocket(t *testing.T, serve func(*net.UnixConn)) Control {
	t.Helper()
	dir, err := os.MkdirTemp("", "chat-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "control.sock")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := l.AcceptUnix()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		serve(c)
	}()
	t.Cleanup(func() { _ = l.Close(); <-done })
	return Control{Socket: path, VerifyPeer: func(*net.UnixConn) error { return nil }}
}

func TestControlDoesNotSendBeforePeerProof(t *testing.T) {
	read := make(chan int, 1)
	c := testControlSocket(t, func(c *net.UnixConn) { var p [1]byte; n, _ := c.Read(p[:]); read <- n })
	c.VerifyPeer = func(*net.UnixConn) error { return errors.New("wrong incarnation") }
	if _, err := c.Call(t.Context(), "reload"); err == nil {
		t.Fatal("unverified request succeeded")
	}
	if n := <-read; n != 0 {
		t.Fatal("sent request before proof")
	}
}

func TestControlBoundedReplyAndConflict(t *testing.T) {
	a, _ := appliedFixture()
	for _, kind := range []string{"ok", "partial", "large", "conflict"} {
		t.Run(kind, func(t *testing.T) {
			c := testControlSocket(t, func(c *net.UnixConn) {
				_, _ = bufio.NewReader(c).ReadString('\n')
				switch kind {
				case "ok":
					_ = json.NewEncoder(c).Encode(map[string]any{"ok": true, "result": a})
				case "partial":
					_, _ = c.Write([]byte(`{"ok":true}`))
				case "large":
					_, _ = c.Write([]byte(strings.Repeat("x", ControlResponseLimit) + "\n"))
				case "conflict":
					_ = json.NewEncoder(c).Encode(map[string]any{"ok": false, "code": "GENERATION_CONFLICT", "error": "private server diagnostic"})
				}
			})
			got, err := c.Call(t.Context(), "reload")
			if kind == "ok" {
				if err != nil || got.Generation != a.Generation {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("invalid reply succeeded")
			}
			if kind == "conflict" && (!strings.Contains(err.Error(), "GENERATION_CONFLICT") || strings.Contains(err.Error(), "private server")) {
				t.Fatal(err)
			}
		})
	}
}

func TestControlStatusListenerWireContract(t *testing.T) {
	// The public AIRC status response reports listener kinds by endpoint;
	// keep a literal wire fixture rather than marshaling our own Go model.
	const status = `{"ok":true,"result":{"protocol":1,"features":["unix-listeners","control-reload","acl-revoke","server-presence"],"incarnation":"01234567-89ab-cdef-0123-456789abcdef","applied_generation":7,"config_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","listeners":{"irc":"unix","artifacts":"unix"},"configured_accounts":2,"accounts":{"human":{"connections":1}}}}`
	want := Rendered{Generation: 7, ConfigSHA256: strings.Repeat("a", 64), Accounts: []string{"human", "coder"}}
	for _, tc := range []struct {
		name, listeners string
		valid           bool
	}{
		{"unix", `{"irc":"unix","artifacts":"unix"}`, true},
		{"tcp-irc", `{"irc":"tcp","artifacts":"unix"}`, false},
		{"tcp-artifacts", `{"irc":"unix","artifacts":"tcp"}`, false},
		{"missing-artifacts", `{"irc":"unix"}`, false},
		{"disabled-artifacts", `{"irc":"unix","artifacts":null}`, false},
		{"missing-irc", `{"artifacts":"unix"}`, false},
		{"null-listeners", `null`, false},
		{"incorrect-array-shape", `["irc","artifacts"]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := strings.Replace(status, `{"irc":"unix","artifacts":"unix"}`, tc.listeners, 1)
			c := testControlSocket(t, func(c *net.UnixConn) {
				_, _ = bufio.NewReader(c).ReadString('\n')
				_, _ = c.Write([]byte(wire + "\n"))
			})
			got, err := c.Call(t.Context(), "status")
			if err == nil {
				err = got.Matches(want)
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid = %v, error = %v", tc.valid, err)
			}
		})
	}
}
