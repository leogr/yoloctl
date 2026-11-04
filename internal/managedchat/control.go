package managedchat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"time"
)

const ControlLimit = 16 << 10 // request and feature-probe limit
const ControlResponseLimit = 1 << 20

type Features struct {
	Protocol int      `json:"protocol"`
	Features []string `json:"features"`
}

// Has reports one optional feature of a supported protocol.
func (f Features) Has(name string) bool {
	return f.Protocol == 1 && slices.Contains(f.Features, name)
}

func (f Features) Require(names ...string) error {
	if f.Protocol != 1 {
		return errors.New("unsupported AIRC managed protocol; update the managed server image explicitly")
	}
	for _, name := range names {
		if !slices.Contains(f.Features, name) {
			return fmt.Errorf("packaged AIRC lacks %s; update its reviewed source/image before applying managed chat", name)
		}
	}
	return nil
}

type Applied struct {
	Features
	Incarnation        string                 `json:"incarnation"`
	Generation         uint64                 `json:"applied_generation"`
	ConfigSHA256       string                 `json:"config_sha256"`
	Listeners          Listeners              `json:"listeners"`
	Accounts           map[string]Connections `json:"accounts"`
	ConfiguredAccounts uint64                 `json:"configured_accounts"`
}
type Listeners struct {
	IRC       string `json:"irc"`
	Artifacts string `json:"artifacts"`
}
type Connections struct {
	Connections uint64 `json:"connections"`
}

func (a Applied) Validate() error {
	if err := a.Require("unix-listeners", "control-reload", "acl-revoke", "server-presence"); err != nil {
		return err
	}
	if len(a.Incarnation) < 16 || len(a.Incarnation) > 80 {
		return errors.New("missing bounded AIRC server incarnation")
	}
	for _, c := range a.Incarnation {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
			return errors.New("invalid AIRC server incarnation")
		}
	}
	digest, err := hex.DecodeString(a.ConfigSHA256)
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != a.ConfigSHA256 {
		return errors.New("invalid AIRC applied configuration digest")
	}
	if a.Listeners.IRC != "unix" || a.Listeners.Artifacts != "unix" {
		return errors.New("managed AIRC listeners must both use Unix sockets")
	}
	return nil
}

func (a Applied) Matches(want Rendered) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if a.Generation != want.Generation || a.ConfigSHA256 != want.ConfigSHA256 {
		return errors.New("managed chat desired generation/configuration is not applied")
	}
	if a.ConfiguredAccounts != uint64(len(want.Accounts)) {
		return errors.New("AIRC configured account inventory differs from desired membership")
	}
	for account, connections := range a.Accounts {
		if connections.Connections > 0 && !slices.Contains(want.Accounts, account) {
			return errors.New("revoked AIRC sessions remain connected")
		}
	}
	return nil
}

type Control struct {
	Socket string
	// VerifyPeer must bind the connection to the expected live container and
	// sandbox UID, not merely a socket path or the server's claimed identity.
	VerifyPeer func(*net.UnixConn) error
}

// Call performs exactly one bounded request. A failed/lost reload is uncertain;
// this method never reconnects, resends, unlinks a socket or restarts a server.
func (c Control) Call(ctx context.Context, op string) (Applied, error) {
	var applied Applied
	if op != "status" && op != "reload" {
		return applied, errors.New("invalid AIRC control operation")
	}
	if c.VerifyPeer == nil {
		return applied, errors.New("AIRC control requires live peer verification")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return applied, err
	}
	defer func() { _ = conn.Close() }()
	u, ok := conn.(*net.UnixConn)
	if !ok {
		return applied, errors.New("AIRC control is not a private Unix connection")
	}
	if err := c.VerifyPeer(u); err != nil {
		return applied, err
	}
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return applied, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := json.NewEncoder(conn).Encode(struct {
		Op string `json:"op"`
	}{op}); err != nil {
		return applied, err
	}
	line, err := bufio.NewReaderSize(conn, ControlResponseLimit+1).ReadSlice('\n')
	if err != nil || len(line) > ControlResponseLimit {
		return applied, errors.New("invalid or incomplete bounded AIRC control reply; inspect status before retrying")
	}
	var reply struct {
		OK     bool            `json:"ok"`
		Code   string          `json:"code"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(line), &reply); err != nil {
		return applied, errors.New("invalid AIRC control JSON reply")
	}
	if !reply.OK {
		for _, code := range []string{"GENERATION_STALE", "GENERATION_CONFLICT", "RESTART_REQUIRED"} {
			if reply.Code == code {
				return applied, fmt.Errorf("AIRC control rejected %s; inspect status before retrying", code)
			}
		}
		return applied, errors.New("AIRC control rejected the request; inspect status and server configuration")
	}
	if err := json.Unmarshal(reply.Result, &applied); err != nil {
		return applied, errors.New("invalid AIRC control result")
	}
	return applied, nil
}
