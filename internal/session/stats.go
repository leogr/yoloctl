package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/terminal"
)

// Stats are a holder's cumulative workload counters. They start at zero with
// each holder process; Session changes with it, so a reader never takes a
// difference across two sessions. A reattach, replay, snapshot, terminal query
// reply or stats read changes none of them.
type Stats struct {
	Bundle string `json:"bundle"`
	// Session is a digest of the holder incarnation, comparable but not the
	// identifier views receive.
	Session      string `json:"session"`
	OutputBytes  uint64 `json:"output_bytes"`  // read from the agent's PTY
	InputPackets uint64 `json:"input_packets"` // accepted participant input
	InputBytes   uint64 `json:"input_bytes"`
}

type statsRequest struct {
	Bundle string `json:"bundle"`
	Core   string `json:"core"`
}

const (
	// StatsTimeout bounds one whole stats read. Samplers reading many holders
	// also share one deadline across them.
	StatsTimeout  = 500 * time.Millisecond
	maxStatsReply = 4 << 10
)

// serveStats answers one stats request on a connection that will be closed.
// It admits no viewer and emits nothing to viewers, so sampling cannot change
// the session, its geometry, input or counters. Requests are not logged.
func (h *holder) serveStats(conn net.Conn, p []byte) {
	var req statsRequest
	if decodeJSON(p, &req) != nil {
		return
	}
	if req.Bundle != h.bundle || req.Core != terminal.CoreIdentity() {
		_ = writeFrame(conn, frameError, []byte(holderMismatchReason))
		return
	}
	h.mu.Lock()
	stats := Stats{Bundle: h.bundle, Session: sessionDigest(h.incarnation), OutputBytes: h.ptyOutput.Load(),
		InputPackets: h.activity, InputBytes: h.inputBytes}
	h.mu.Unlock()
	payload, _ := json.Marshal(stats)
	_ = writeFrame(conn, frameStats, payload)
}

func sessionDigest(incarnation string) string {
	sum := sha256.Sum256([]byte("yoloctl session stats\x00" + incarnation))
	return hex.EncodeToString(sum[:8])
}

// ErrBuildMismatch matches the errors of a holder that answered the stats
// handshake explicitly as another build or terminal core: a well-formed HELLO
// with another protocol version, its exact build/core refusal, or a complete
// stats reply naming another bundle. Views of this build cannot attach to that
// session until its box is restarted. A holder that closes, times out or sends
// anything malformed or incomplete is never classified as a mismatch.
var ErrBuildMismatch = errors.New("session holder runs another build or terminal core")

// buildMismatch keeps the handshake's own message while matching
// ErrBuildMismatch.
type buildMismatch struct{ msg string }

func (e buildMismatch) Error() string      { return e.msg }
func (buildMismatch) Is(target error) bool { return target == ErrBuildMismatch }

// holderMismatchReason is the refusal both stats and view admission send to
// a peer of another build or core.
const holderMismatchReason = "session build/core mismatch"

// ReadStats reads one holder's counters through its local attach socket. The
// holder must run this exact build and terminal core; anything else is an
// error, never zero counters.
func ReadStats(ctx context.Context, socket string) (Stats, error) {
	ctx, cancel := context.WithTimeout(ctx, StatsTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return Stats{}, err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	return readStats(conn)
}

func readStats(conn net.Conn) (Stats, error) {
	var stats Stats
	hello, err := readFrame(conn)
	if err != nil {
		return stats, fmt.Errorf("session stats handshake: %w", err)
	}
	// Validity before compatibility: only a well-formed HELLO can name
	// another protocol.
	if hello.Type != frameHello || len(hello.Payload) != 1 {
		return stats, errors.New("invalid session stats handshake")
	}
	if hello.Payload[0] != protocolVersion {
		return stats, buildMismatch{"session protocol differs from this build"}
	}
	bundle, err := controller.ExecutableIdentity()
	if err != nil {
		return stats, err
	}
	req, _ := json.Marshal(statsRequest{Bundle: bundle, Core: terminal.CoreIdentity()})
	if err := writeFrame(conn, frameStats, req); err != nil {
		return stats, err
	}
	f, err := readFrame(conn)
	switch {
	case err != nil:
		// Holders from other builds close on the unknown request.
		return stats, fmt.Errorf("session stats unavailable: %w", err)
	case f.Type == frameError:
		// Only the complete raw refusal names another build; the bounded,
		// sanitized text is for diagnostics and never classifies.
		if string(f.Payload) == holderMismatchReason {
			return stats, buildMismatch{"session stats refused: " + holderMismatchReason}
		}
		reason := strings.ToValidUTF8(string(f.Payload[:min(len(f.Payload), 200)]), "")
		return stats, fmt.Errorf("session stats refused: %s", reason)
	case f.Type != frameStats || len(f.Payload) > maxStatsReply || decodeJSON(f.Payload, &stats) != nil ||
		stats.Bundle == "" || stats.Session == "":
		return Stats{}, errors.New("invalid session stats reply")
	case stats.Bundle != bundle:
		return Stats{}, buildMismatch{"session stats from another build"}
	}
	return stats, nil
}
