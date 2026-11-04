package main

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/ocifeature"
	"github.com/spf13/cobra"
)

var featureMetadataCompletion = ocifeature.MetadataForCompletion

func completionSelectedFeatures(cmd *cobra.Command, args []string) ([]string, []ocifeature.Lock) {
	var names []string
	for _, flag := range []string{"features", "feature"} {
		if f := cmd.Flags().Lookup(flag); f != nil {
			if value, ok := f.Value.(interface{ GetSlice() []string }); ok {
				names = append(names, value.GetSlice()...)
			}
		}
	}
	var locks []ocifeature.Lock
	if len(args) > 0 && (cmd.Name() == "set" || cmd.Name() == "copy") {
		space := completionAccountSpace(cmd)
		if cmd.Name() == "copy" && flagChanged(cmd, "from-space") {
			space, _ = cmd.Flags().GetString("from-space")
		}
		if cfg, err := agentbox.Load(space, args[0]); err == nil {
			locks = cfg.FeatureLocks
			if !cmd.Flags().Changed("features") && !cmd.Flags().Changed("feature") {
				names = append(names, cfg.Features...)
			}
		}
	}
	return names, locks
}
func completeFeatureOptions(cmd *cobra.Command, args []string, prefix string) ([]string, cobra.ShellCompDirective) {
	names, locks := completionSelectedFeatures(cmd, args)
	ctx, cancel := context.WithTimeout(cmd.Context(), 1500*time.Millisecond)
	defer cancel()
	out := []string{}
	for _, ref := range names {
		if !ocifeature.IsReference(ref) {
			continue
		}
		qualifier := ocifeature.Alias(ref)
		count := 0
		for _, other := range names {
			if ocifeature.Alias(other) == qualifier {
				count++
			}
		}
		if count > 1 || strings.HasPrefix(prefix, ref+".") {
			qualifier = ref
		}
		if prefix != "" && !strings.HasPrefix(prefix, qualifier+".") && !strings.HasPrefix(qualifier+".", prefix) {
			continue
		}
		var metadata ocifeature.Metadata
		found := false
		for _, l := range locks {
			if l.Ref == ref {
				m, err := l.Definition()
				if err == nil {
					metadata = m
					found = true
				}
				break
			}
		}
		if !found {
			m, err := featureMetadataCompletion(ctx, ref)
			if err != nil {
				continue
			}
			metadata = m
		}
		for name, option := range metadata.Options {
			key := qualifier + "." + name + "="
			if !strings.Contains(prefix, "=") {
				if strings.HasPrefix(key, prefix) {
					out = append(out, key)
				}
				continue
			}
			if !strings.HasPrefix(prefix, key) {
				continue
			}
			values := append(append([]string(nil), option.Enum...), option.Proposals...)
			if option.Type == "boolean" {
				values = []string{"true", "false"}
			}
			if defaults, err := metadata.Values(nil); err == nil {
				values = append(values, defaults[name])
			}
			for _, value := range values {
				if !strings.ContainsAny(value, "\n\r\t ") && strings.HasPrefix(key+value, prefix) {
					out = append(out, key+value)
				}
			}
		}
	}
	sort.Strings(out)
	out = slices.Compact(out)
	directive := cobra.ShellCompDirectiveNoFileComp
	if !strings.Contains(prefix, "=") {
		directive |= cobra.ShellCompDirectiveNoSpace
	}
	return out, directive
}
