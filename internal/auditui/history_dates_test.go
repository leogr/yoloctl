package auditui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

func TestHumanAuditDatesDistinguishDaysAndUseUTC(t *testing.T) {
	r := newEventRenderer(EventOptions{})
	for _, tc := range []struct {
		when time.Time
		want string
	}{
		{time.Date(2026, 11, 14, 23, 59, 59, 0, time.UTC), "2026-11-14 23:59:59"},
		{time.Date(2026, 11, 15, 0, 0, 1, 0, time.UTC), "2026-11-15 00:00:01"},
		{time.Date(2026, 11, 15, 0, 30, 0, 0, time.FixedZone("offset", 2*60*60)), "2026-11-14 22:30:00"},
	} {
		event := audit.Event{Time: tc.when, Rule: "same event", Severity: "Warning", Container: "host"}
		if got := r.heading(event); !strings.HasPrefix(got, tc.want+"  ") {
			t.Errorf("heading %q does not identify UTC date %s", got, tc.want)
		}
	}
}

func TestHumanAuditRepeatAcrossMidnightHasAnUnambiguousEndDate(t *testing.T) {
	var out bytes.Buffer
	p := NewLogPrinter(t.Context(), &out, prettylog.Normal, EventOptions{})
	first := time.Date(2026, 11, 14, 23, 59, 59, 0, time.UTC)
	for _, when := range []time.Time{first, first.Add(2 * time.Second)} {
		if err := p.Add(t.Context(), audit.Event{Time: when, Rule: "same event", Severity: "Warning", Container: "host"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2026-11-14 23:59:59", "until 2026-11-15 00:00:01"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %s", want, out.String())
		}
	}
}
