package main

import (
	"context"
	"maps"
	"slices"

	"github.com/leogr/yoloctl/internal/agentbox"
)

type previousBoxPermissionsKey struct{}

// Snapshot before mutation: config slices and feature options may be edited in
// place. This declaration comparison is only a warning, never runtime evidence
// or a new admission/cleanup decision.
func withPreviousBoxPermissions(ctx context.Context, cfg *agentbox.Config) context.Context {
	previous := *cfg
	previous.Features = slices.Clone(cfg.Features)
	previous.Capabilities = slices.Clone(cfg.Capabilities)
	previous.Mounts = slices.Clone(cfg.Mounts)
	previous.Ports = slices.Clone(cfg.Ports)
	previous.FeatureOptions = maps.Clone(cfg.FeatureOptions)
	for name, options := range previous.FeatureOptions {
		previous.FeatureOptions[name] = maps.Clone(options)
	}
	return context.WithValue(ctx, previousBoxPermissionsKey{}, &previous)
}

func removedDeclarationPermissions(ctx context.Context, next *agentbox.Config) bool {
	previous, _ := ctx.Value(previousBoxPermissionsKey{}).(*agentbox.Config)
	if previous == nil {
		return false
	}
	if previous.Privileged && !next.Privileged || previous.UnconfinedNetwork && !next.UnconfinedNetwork {
		return true
	}
	for _, capability := range previous.Capabilities {
		if !slices.Contains(next.Capabilities, capability) {
			return true
		}
	}
	// Features may carry capabilities or operator authority. A removed feature
	// or changed option can reduce its grants; do not infer the live old policy.
	for _, feature := range previous.Features {
		if !slices.Contains(next.Features, feature) {
			return true
		}
		for key, value := range previous.FeatureOptions[feature] {
			if nextValue, ok := next.FeatureOptions[feature][key]; !ok || nextValue != value {
				return true
			}
		}
	}
	for _, mount := range previous.Mounts {
		if !slices.ContainsFunc(next.Mounts, func(n agentbox.Mount) bool {
			return mount.Source == n.Source && mount.Target == n.Target && (mount.ReadOnly || !n.ReadOnly)
		}) {
			return true
		}
	}
	for _, port := range previous.Ports {
		if !slices.ContainsFunc(next.Ports, func(n agentbox.Port) bool {
			return port.Target == n.Target && port.Published == n.Published && port.ResolvedHostIP() == n.ResolvedHostIP() && port.ResolvedProtocol() == n.ResolvedProtocol()
		}) {
			return true
		}
	}
	if previous.Workdir != next.Workdir {
		return true
	}
	return previous.Account != "" && previous.Account != "none" && previous.Account != next.Account
}
