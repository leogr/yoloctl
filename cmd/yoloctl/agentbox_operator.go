package main

import "github.com/spf13/cobra"

// operatorCreatePresetFeatures is the explicit, centrally maintained preset.
// Expand it only in box create: manifests store ordinary features, never a
// role whose authority could grow when this list changes in a later build.
var operatorCreatePresetFeatures = []string{
	"yolo.space.operator",
	"yolo.chat.skill",
}

// applyOperatorCreatePreset runs after agent defaults so explicit extra
// features survive. Workdir selection and authority warnings use the ordinary
// create paths; no special role or chat enrollment is persisted.
func applyOperatorCreatePreset(cmd *cobra.Command, opts *agentboxCreateOptions) {
	var merged []string
	seen := map[string]bool{}
	for _, names := range [][]string{opts.Features, opts.ExtraFeatures, operatorCreatePresetFeatures} {
		for _, name := range names {
			if !seen[name] {
				merged = append(merged, name)
				seen[name] = true
			}
		}
	}
	opts.Features, opts.ExtraFeatures = merged, nil
	if !flagChanged(cmd, "unconfined-network") {
		opts.UnconfinedNetwork = true
	}
}
