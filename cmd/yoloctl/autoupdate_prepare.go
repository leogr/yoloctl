package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/leogr/yoloctl/internal/agentprofile"
)

type npmProbeEntry struct {
	info    *agentLatestInfo
	failure string
}
type npmProbeNeeded struct {
	key     string
	profile agentprofile.Profile
}

func (e *npmProbeNeeded) Error() string { return "npm version probe required before apply" }

var probeNpmLatest = fetchNpmLatest

func npmProbeKey(p *agentprofile.Profile, snapshot string) string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(append(append(raw, 0), []byte(snapshot)...))
	return hex.EncodeToString(sum[:])
}
func (s *imageWarmState) requireNpmProbe(ctx context.Context, agent string) error {
	if !s.probeEnabled || !hasRealAgent(agent) {
		return nil
	}
	p, err := agentprofile.Lookup(agent)
	if err != nil || p.NativeRelease != "" || p.NpmPackage == "" {
		return nil
	}
	key := npmProbeKey(p, s.snapshot)
	if _, ok := s.probes[key]; ok {
		return nil
	}
	if s.applied {
		return errors.New("npm profile changed after apply began; autoupdate will not use a stale probe")
	}
	return &npmProbeNeeded{key: key, profile: *p}
}
func (s *imageWarmState) prepareNpmProbe(ctx context.Context, p *npmProbeNeeded) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := probeNpmLatest(ctx, p.profile.NpmPackage)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	entry := npmProbeEntry{info: info}
	if err != nil {
		text := strings.Map(func(r rune) rune {
			if r < 32 || r == 127 {
				return ' '
			}
			return r
		}, err.Error())
		if len(text) > 512 {
			text = text[:512]
		}
		entry.failure = text
	}
	if s.probes == nil {
		s.probes = map[string]npmProbeEntry{}
	}
	s.probes[p.key] = entry
	return nil
}
func preparedAgentLatest(ctx context.Context, p *agentprofile.Profile) (*agentLatestInfo, error) {
	state, _ := ctx.Value(warmKey{}).(*imageWarmState)
	if state != nil && state.probeEnabled && p.NativeRelease == "" && p.NpmPackage != "" {
		entry, ok := state.probes[npmProbeKey(p, state.snapshot)]
		if !ok {
			return nil, errors.New("npm probe does not match the current profile/snapshot; autoupdate skipped")
		}
		if entry.failure != "" {
			return nil, errors.New(entry.failure)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return entry.info, nil
	}
	return fetchAgentLatest(ctx, p)
}
func npmProbeMethod(method string) bool {
	switch method {
	case "box.create", "box.copy", "box.start", "box.restart", "box.recreate", "box.attach":
		return true
	}
	return false
}
