package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

// AuditLabelObservation is a nonsecret runtime projection, not a declaration or
// proof of a process's confinement. No Env, argv, buffers or arbitrary labels.
type AuditLabelObservation struct {
	ID          string `json:"id"`
	Schema      string `json:"schema"`
	Kind        string `json:"kind"`
	Box         string `json:"box"`
	Space       string `json:"space"`
	Account     string `json:"account"`
	Agent       string `json:"agent"`
	RuntimeMode string `json:"runtime_mode"`
	Network     string `json:"network"`
	Privileged  *bool  `json:"privileged"`
}

const auditProjection = `{"id":{{json .Id}},"schema":{{json (index .Config.Labels "yoloctl.audit.schema")}},"kind":{{json (index .Config.Labels "yoloctl.kind")}},"box":{{json (index .Config.Labels "yoloctl.agentbox")}},"space":{{json (index .Config.Labels "yoloctl.space")}},"account":{{json (index .Config.Labels "yoloctl.audit.account")}},"agent":{{json (index .Config.Labels "yoloctl.agent")}},"runtime_mode":{{json (index .Config.Labels "yoloctl.audit.runtime_mode")}},"network":{{json (index .Config.Labels "yoloctl.audit.network_unconfined")}},"privileged":{{json .HostConfig.Privileged}}}`

// AuditLabels bounds both runtime IO and the complete batch. Incomplete reads
// fail the observation; callers let their old snapshot expire, never mutate or
// adopt runtime state. Unlike destructive inventories this is best effort.
func (c *Client) AuditLabels(ctx context.Context) ([]AuditLabelObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	remaining := 3 << 20
	raw, err := c.runtimeIdentityRead(ctx, &remaining, 4096*65, "ps", "-a", "-q", "--no-trunc", "--filter", "label=yoloctl.kind=agentbox")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(raw))
	if len(ids) > 4096 {
		return nil, errors.New("audit metadata inventory exceeds bound")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !nativeMountCID(id) || seen[id] {
			return nil, errors.New("audit metadata requires unique full container IDs")
		}
		seen[id] = true
	}
	var result []AuditLabelObservation
	for start := 0; start < len(ids); start += 32 {
		selected := ids[start:min(start+32, len(ids))]
		args := append([]string{"inspect", "--mode", "dockercompat", "--format", auditProjection}, selected...)
		raw, err := c.runtimeIdentityRead(ctx, &remaining, 1<<20, args...)
		if err != nil {
			return nil, err
		}
		rows, err := parseAuditLabels(raw, selected)
		if err != nil {
			return nil, err
		}
		result = append(result, rows...)
	}
	return result, nil
}

func parseAuditLabels(raw []byte, ids []string) ([]AuditLabelObservation, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	expected := map[string]bool{}
	for _, id := range ids {
		expected[id] = true
	}
	var result []AuditLabelObservation
	for {
		var row AuditLabelObservation
		err := decoder.Decode(&row)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("invalid audit metadata projection")
		}
		if !expected[row.ID] {
			return nil, errors.New("audit metadata container identity changed")
		}
		delete(expected, row.ID)
		result = append(result, row)
	}
	if len(expected) != 0 {
		return nil, errors.New("incomplete audit metadata observation")
	}
	return result, nil
}
