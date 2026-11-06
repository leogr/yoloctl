package audit

import (
	"regexp"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// This allow-list is independent of incoming JSON and excludes argv, environment,
// buffers and aggregate labels. Values share a 4 KiB budget in ParseEvent.
var retainedFields = []string{
	"proc.name", "proc.exepath", "user.name", "user.uid", "container.id", "container.name",
	"fd.name", "fs.path.name", "fs.path.source", "fs.path.target", "fd.rip", "evt.res",
	"yolo.box", "yolo.space", "yolo.account", "yolo.agent", "yolo.runtime_mode",
	"yolo.network_unconfined", "yolo.metadata_available",
}

var outputTemplates = sync.OnceValue(func() map[string]string {
	var entries []struct{ Rule, Output string }
	if err := yaml.Unmarshal([]byte(Rules), &entries); err != nil {
		panic("invalid embedded audit rules: " + err.Error())
	}
	result := make(map[string]string)
	for _, entry := range entries {
		if entry.Rule != "" {
			result[entry.Rule] = entry.Output
		}
	}
	return result
})

var outputField = regexp.MustCompile(`%([a-z][a-z0-9_.]*)`)

// SelectedFields also supports older records, which retained only flattened
// fields. Unavailable metadata is never inferred from a VM process's context.
func SelectedFields(e Event) map[string]string {
	fields := make(map[string]string)
	for _, key := range retainedFields {
		if value := e.OutputFields[key]; value != "" {
			fields[key] = Clean(value)
		}
	}
	for key, value := range map[string]string{
		"proc.exepath": e.Executable, "container.id": e.Container,
		"fd.rip": e.Endpoint, "evt.res": e.Result,
	} {
		if fields[key] == "" && value != "" {
			fields[key] = Clean(value)
		}
	}
	if len(e.OutputFields) == 0 && e.Path != "" {
		fields["fs.path.name"] = Clean(e.Path)
	}
	for key, value := range map[string]string{
		"yolo.box": e.Box, "yolo.space": e.Space, "yolo.account": e.Account,
		"yolo.agent": e.Agent, "yolo.runtime_mode": e.RuntimeMode, "yolo.network_unconfined": e.NetworkUnconfined,
	} {
		delete(fields, key)
		if e.MetadataAvailable && value != "" {
			fields[key] = Clean(value)
		}
	}
	fields["yolo.metadata_available"] = strconv.FormatBool(e.MetadataAvailable)
	return fields
}

// RenderOutput reconstructs the pinned Falco template, never the arbitrary raw
// output property. This retains the familiar message without admitting argv/env
// through an unfiltered second path. Older events show <NA> for unretained fields.
func RenderOutput(e Event) string {
	fields := SelectedFields(e)
	return Clean(outputField.ReplaceAllStringFunc(outputTemplates()[e.Rule], func(token string) string {
		if value := fields[token[1:]]; value != "" {
			return value
		}
		return "<NA>"
	}))
}

// RenderPrettyOutput hides unavailable assignments in human-readable output;
// the stored/JSON message continues to include their explicit <NA> values.
func RenderPrettyOutput(e Event) string {
	return FormatPrettyOutput(e, nil)
}

// FormatPrettyOutput optionally decorates the already sanitized key/value
// assignments. Only pinned template keys reach format; raw output is never used.
func FormatPrettyOutput(e Event, format func(string, string) string) string {
	fields := SelectedFields(e)
	var parts []string
	remaining := 1024
	for _, part := range strings.Fields(outputTemplates()[e.Rule]) {
		missing := false
		part = outputField.ReplaceAllStringFunc(part, func(token string) string {
			value := fields[token[1:]]
			if value == "" || value == "<NA>" {
				missing = true
			}
			return value
		})
		if !missing {
			part = Clean(part)
			if len(parts) != 0 {
				remaining--
			}
			if remaining <= 0 {
				break
			}
			if len(part) > remaining {
				part = strings.ToValidUTF8(part[:remaining], "")
			}
			remaining -= len(part)
			if key, value, ok := strings.Cut(part, "="); ok && format != nil {
				part = format(key, value)
			}
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " ")
}
