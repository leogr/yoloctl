// Package audit owns the bounded, privacy-filtered native Falco audit data.
// It grants no workload or host authority and performs no remediation.
package audit

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/layout"
)

const (
	Version      = "0.45.2"
	MaxEvent     = 16 << 10
	SegmentBytes = 1 << 20
	RetainBytes  = 64 << 20
	RetainAge    = 7 * 24 * time.Hour
	MaxArchive   = 64 << 20
)

type Desired struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	Version     string `yaml:"version,omitempty" json:"version,omitempty"`
	RulesSHA256 string `yaml:"rules_sha256,omitempty" json:"rules_sha256,omitempty"`
}

func (d Desired) Validate() error {
	if d.Version != "" && d.Version != Version {
		return errors.New("unsupported audit version; use the selected yoloctl build")
	}
	if d.Enabled && (d.Version != Version || len(d.RulesSHA256) != 64) {
		return errors.New("enabled auditing needs a pinned version and rules digest")
	}
	return nil
}

type Event struct {
	Cursor            uint64            `json:"cursor"`
	Time              time.Time         `json:"time"`
	Rule              string            `json:"rule"`
	Severity          string            `json:"severity"`
	Output            string            `json:"output,omitempty"`
	OutputFields      map[string]string `json:"output_fields,omitempty"`
	Executable        string            `json:"executable,omitempty"`
	Container         string            `json:"container,omitempty"`
	Box               string            `json:"box,omitempty"`
	Space             string            `json:"space,omitempty"`
	Account           string            `json:"account,omitempty"`
	Agent             string            `json:"agent,omitempty"`
	RuntimeMode       string            `json:"runtime_mode,omitempty"`
	NetworkUnconfined string            `json:"network_unconfined,omitempty"`
	MetadataAvailable bool              `json:"metadata_available"`
	Path              string            `json:"path,omitempty"`
	Endpoint          string            `json:"endpoint,omitempty"`
	Result            string            `json:"result,omitempty"`
}

type Status struct {
	ActualVersion string  `json:"actual_version,omitempty"`
	Desired       Desired `json:"desired"`
	State         string  `json:"state"`
	Architecture  string  `json:"architecture"`
	Driver        string  `json:"driver"`
	DriverDrops   *uint64 `json:"driver_drops"`
	Error         string  `json:"error,omitempty"`
	Drops         uint64  `json:"drops"`
	MetadataGaps  uint64  `json:"metadata_gaps"`
}

type Query struct {
	Tail  bool      `json:"tail,omitempty"`
	After uint64    `json:"after,omitempty"`
	Upper uint64    `json:"upper,omitempty"`
	Since time.Time `json:"since,omitempty"`
	Until time.Time `json:"until,omitempty"`
	Box   string    `json:"box,omitempty"`
	Limit int       `json:"limit,omitempty"`
}

type Summary struct {
	First        uint64    `json:"first_available"`
	Upper        uint64    `json:"upper_cursor"`
	Last         uint64    `json:"last_cursor"`
	Count        int       `json:"count"`
	Since        time.Time `json:"since"`
	Until        time.Time `json:"until"`
	Gap          bool      `json:"gap"`
	Drops        uint64    `json:"drops"`
	MetadataGaps uint64    `json:"metadata_gaps"`
}

type Frame struct {
	Type    string   `json:"type"`
	Event   *Event   `json:"event,omitempty"`
	Summary *Summary `json:"summary,omitempty"`
}

// Clean escapes terminal controls before data enters storage or a view. Limits
// are bytes, preserving UTF-8 boundaries. Event output stays filtered; only a
// bounded Falco stderr prefix is exposed through host-only failure health.
func Clean(s string) string {
	s = strings.ToValidUTF8(s, "�")

	var escaped strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			quoted := strconv.QuoteRuneToASCII(r)
			escaped.WriteString(quoted[1 : len(quoted)-1])
		} else {
			escaped.WriteRune(r)
		}
	}
	s = escaped.String()

	if len(s) > 1024 {
		s = s[:1024]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

func ParseEvent(raw []byte) (Event, error) {
	var e Event
	if len(raw) > MaxEvent {
		return e, errors.New("audit event exceeds size limit")
	}
	var input struct {
		Time     string                     `json:"time"`
		Rule     string                     `json:"rule"`
		Priority string                     `json:"priority"`
		Fields   map[string]json.RawMessage `json:"output_fields"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return e, errors.New("invalid audit event JSON")
	}
	when, err := time.Parse(time.RFC3339Nano, input.Time)
	if err != nil || input.Rule == "" || input.Priority == "" {
		return e, errors.New("audit event lacks time, rule or severity")
	}
	get := func(key string) string {
		var s string
		if json.Unmarshal(input.Fields[key], &s) != nil {
			if key == "yolo.metadata_available" || key == "yolo.network_unconfined" {
				var b bool
				if json.Unmarshal(input.Fields[key], &b) == nil {
					return strconv.FormatBool(b)
				}
			}
			return ""
		}
		if s == "<NA>" {
			return ""
		}
		return Clean(s)
	}
	e.Time, e.Rule, e.Severity = when.UTC(), Clean(input.Rule), Clean(input.Priority)
	e.Executable, e.Container = get("proc.exepath"), get("container.id")
	// A present plugin availability field is authoritative, even when false;
	// never replace a stale/missing S3 snapshot with older container label data.
	if _, plugin := input.Fields["yolo.metadata_available"]; plugin {
		e.Box, e.Space, e.Account, e.Agent = get("yolo.box"), get("yolo.space"), get("yolo.account"), get("yolo.agent")
		e.RuntimeMode, e.NetworkUnconfined = get("yolo.runtime_mode"), get("yolo.network_unconfined")
		e.MetadataAvailable = get("yolo.metadata_available") == "true"
	} else {
		label := func(key string) string { return get("container.label[" + key + "]") }
		e.Box, e.Space, e.Agent = label("yoloctl.agentbox"), label("yoloctl.space"), label("yoloctl.agent")
		e.Account, e.RuntimeMode, e.NetworkUnconfined = label("yoloctl.audit.account"), label("yoloctl.audit.runtime_mode"), label("yoloctl.audit.network_unconfined")
		e.MetadataAvailable = label("yoloctl.kind") == "agentbox" && label("yoloctl.audit.schema") == "1"
	}
	e.MetadataAvailable = e.MetadataAvailable && e.Container != "" && e.Container != "host" &&
		layout.ValidateName("agentbox", e.Box) == nil && layout.ValidateName("space", e.Space) == nil &&
		layout.ValidateName("account", e.Account) == nil && layout.ValidateName("agent", e.Agent) == nil &&
		(e.RuntimeMode == "confined" || e.RuntimeMode == "privileged") &&
		(e.NetworkUnconfined == "true" || e.NetworkUnconfined == "false")
	if !e.MetadataAvailable {
		e.Box, e.Space, e.Account, e.Agent, e.RuntimeMode, e.NetworkUnconfined = "", "", "", "", "", ""
	}
	e.Path, e.Endpoint, e.Result = get("fs.path.name"), get("fd.rip"), get("evt.res")
	if e.Path == "" {
		e.Path = get("fs.path.target")
	}
	if e.Path == "" {
		e.Path = get("fs.path.source")
	}
	if e.Path == "" {
		e.Path = get("fd.name")
	}
	e.OutputFields = make(map[string]string)
	metadata := map[string]string{
		"yolo.box": e.Box, "yolo.space": e.Space, "yolo.account": e.Account, "yolo.agent": e.Agent,
		"yolo.runtime_mode": e.RuntimeMode, "yolo.network_unconfined": e.NetworkUnconfined,
	}
	remaining := 4096
	for _, key := range retainedFields {
		value := get(key)
		if key == "user.uid" && value == "" {
			var uid json.Number
			if json.Unmarshal(input.Fields[key], &uid) == nil {
				if _, err := strconv.ParseUint(uid.String(), 10, 32); err == nil {
					value = uid.String()
				}
			}
		}
		if _, present := input.Fields[key]; present && value == "" {
			value = "<NA>"
		}
		if strings.HasPrefix(key, "yolo.") {
			value = metadata[key]
			if value == "" {
				value = "<NA>"
			}
		}
		if key == "yolo.metadata_available" {
			value = strconv.FormatBool(e.MetadataAvailable)
		}
		if value != "" && len(value) <= remaining {
			e.OutputFields[key] = value
			remaining -= len(value)
		}
	}
	e.Output = RenderOutput(e)
	return e, nil
}
