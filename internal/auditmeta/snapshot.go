// Package auditmeta defines the nonsecret, observational controller snapshot
// consumed by the Falco extraction plugin. It grants no runtime authority.
package auditmeta

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
)

const MaxBytes = 2 << 20
const MaxRecords = 4096
const MaxAge = 15 * time.Second

type Record struct {
	ID, Box, Space, Account, Agent, RuntimeMode string
	NetworkUnconfined                           bool
}

func (r Record) Validate() error {
	if len(r.ID) != 64 || strings.Trim(r.ID, "0123456789abcdef") != "" {
		return errors.New("audit metadata requires a full canonical container ID")
	}
	for _, field := range []struct{ kind, value string }{{"agentbox", r.Box}, {"space", r.Space}, {"account", r.Account}, {"agent", r.Agent}} {
		if err := layout.ValidateName(field.kind, field.value); err != nil {
			return err
		}
	}
	if r.RuntimeMode != "confined" && r.RuntimeMode != "privileged" {
		return errors.New("audit metadata runtime mode is unavailable")
	}
	return nil
}

// Encode publishes a complete bounded observation, never declarations, secrets,
// argv or authentication data. IDs, not names, join it to live syscall events.
func Encode(generation string, generated time.Time, records []Record) ([]byte, error) {
	if len(generation) != 64 || strings.Trim(generation, "0123456789abcdef") != "" || generated.Unix() <= 0 || len(records) > MaxRecords {
		return nil, errors.New("invalid audit metadata snapshot header")
	}
	rows := append([]Record(nil), records...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	var out bytes.Buffer
	_, _ = fmt.Fprintf(&out, "yoloctl-audit-meta-v1\t%d\t%d\t%s\n", generated.Unix(), len(rows), generation)
	for n, row := range rows {
		if err := row.Validate(); err != nil {
			return nil, err
		}
		if n > 0 && rows[n-1].ID == row.ID {
			return nil, errors.New("duplicate audit metadata container identity")
		}
		_, _ = fmt.Fprintf(&out, "%s\t%s\t%s\t%s\t%s\t%s\t%t\n", row.ID, row.Box, row.Space, row.Account, row.Agent, row.RuntimeMode, row.NetworkUnconfined)
		if out.Len() > MaxBytes {
			return nil, errors.New("audit metadata exceeds size bound")
		}
	}
	return out.Bytes(), nil
}
