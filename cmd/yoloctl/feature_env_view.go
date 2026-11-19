package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// inspectFeatureEnv is one declared dev container Feature variable: the
// value the box declaration delivers to new box processes, not an observed
// runtime value. Feature values are registry metadata, never box secrets.
type inspectFeatureEnv struct {
	Key                string `json:"key"`
	Value              string `json:"value,omitempty"`
	From               string `json:"from"`
	Skipped            string `json:"skipped,omitempty"`
	OverriddenByBoxEnv bool   `json:"overridden_by_box_env,omitempty"`
}

func featureEnvViews(cfg *agentbox.Config) []inspectFeatureEnv {
	rt, err := agentbox.FeatureRuntimeOf(cfg)
	if err != nil {
		return nil
	}
	out := make([]inspectFeatureEnv, 0, len(rt.Env))
	for _, e := range rt.Env {
		_, overridden := cfg.Env[e.Key]
		out = append(out, inspectFeatureEnv{Key: e.Key, Value: e.Value, From: e.Ref, Skipped: e.Skip, OverriddenByBoxEnv: overridden && e.Skip == ""})
	}
	return out
}

// terminalText escapes control and other non-printable characters.
func terminalText(s string) string {
	q := strconv.Quote(s)
	return strings.ReplaceAll(q[1:len(q)-1], `\"`, `"`)
}

func (v inspectFeatureEnv) line() string {
	switch {
	case v.Skipped != "":
		return fmt.Sprintf("%s not set (%s; from %s)", terminalText(v.Key), terminalText(v.Skipped), terminalText(v.From))
	case v.OverriddenByBoxEnv:
		return fmt.Sprintf("%s=%s (from %s; overridden by box env)", terminalText(v.Key), terminalText(v.Value), terminalText(v.From))
	default:
		return fmt.Sprintf("%s=%s (from %s)", terminalText(v.Key), terminalText(v.Value), terminalText(v.From))
	}
}

// noticeFeatureRuntime logs, per Feature, the folders it names that get no or
// narrower access and the variables that don't reach box processes.
func noticeFeatureRuntime(cfg *agentbox.Config) {
	rt, err := agentbox.FeatureRuntimeOf(cfg)
	if err != nil {
		return
	}
	lines := map[string][]string{}
	var refs []string
	add := func(ref, line string) {
		if _, ok := lines[ref]; !ok {
			refs = append(refs, ref)
		}
		lines[ref] = append(lines[ref], line)
	}
	for _, g := range rt.Grants {
		switch {
		case g.Skip != "":
			add(g.Ref, fmt.Sprintf("folder not granted (%s): %s=%s", g.Skip, g.Source, g.Path))
		case g.Note != "":
			add(g.Ref, fmt.Sprintf("folder %s=%s %s", g.Source, g.Path, g.Note))
		}
	}
	for _, e := range rt.Env {
		if e.Skip != "" {
			add(e.Ref, fmt.Sprintf("not set at runtime (%s): %s", e.Skip, e.Key))
		}
	}
	for _, ref := range refs {
		cliLogLines(prettylog.LevelNotice, logging.Box, "feature "+ref+": runtime adjustments:", lines[ref])
	}
}
