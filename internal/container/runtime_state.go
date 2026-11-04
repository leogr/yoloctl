package container

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// RuntimeState is the live identity of a container. PID and IP are only
// meaningful while Running; StartedAt is zero when nerdctl does not report it.
type RuntimeState struct {
	ID        string
	Name      string
	Running   bool
	PID       int
	IP        string
	StartedAt time.Time
	// Details is the same entry as Inspect would return it, for launch-mode
	// evidence; nil when the entry's resource fields are malformed, which
	// leaves the identity above usable.
	Details *Details
}

// RuntimeStates inspects several containers with one nerdctl call. A
// container that vanished between listing and inspection is omitted; a
// failure to inspect any of them is returned.
func (c *Client) RuntimeStates(ctx context.Context, names []string) (map[string]RuntimeState, error) {
	out := map[string]RuntimeState{}
	if len(names) == 0 {
		return out, nil
	}
	var stdout bytes.Buffer
	cmd := c.command(ctx, append([]string{"sudo", "nerdctl", "inspect"}, names...)...)
	cmd.Stdout, cmd.Stderr = &stdout, io.Discard
	runErr := cmd.Run()
	states, err := parseRuntimeStates(stdout.Bytes())
	if err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("inspect runtime state: %w", runErr)
		}
		return nil, err
	}
	if runErr != nil && len(states) == 0 {
		return nil, fmt.Errorf("inspect runtime state: %w", runErr)
	}
	for _, s := range states {
		out[s.Name] = s
	}
	return out, nil
}

func parseRuntimeStates(raw []byte) ([]RuntimeState, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var entries []inspectEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse runtime state: %w", err)
	}
	out := make([]RuntimeState, 0, len(entries))
	for _, e := range entries {
		s := RuntimeState{
			ID:      e.ID,
			Name:    strings.TrimPrefix(e.Name, "/"),
			Running: e.State.Running,
			PID:     e.State.Pid,
			IP:      e.NetworkSettings.IPAddress,
		}
		if d, err := e.details(strings.TrimPrefix(e.Name, "/")); err == nil {
			s.Details = d
		}
		if t, err := time.Parse(time.RFC3339Nano, e.State.StartedAt); err == nil && t.Year() > 1 {
			s.StartedAt = t
		}
		if !s.Running {
			s.PID, s.IP = 0, ""
		}
		out = append(out, s)
	}
	return out, nil
}
