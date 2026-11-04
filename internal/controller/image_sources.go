package controller

import (
	"bytes"
	"errors"
	"strings"

	"github.com/distribution/reference"
	"github.com/leogr/yoloctl/internal/imagejob"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	spb "github.com/moby/buildkit/sourcepolicy/pb"
	digest "github.com/opencontainers/go-digest"
)

// dockerfileStageContexts uses the frontend's parser and NamedContext key
// normalization, including latest removal. Protect every declared alias: even
// a later stage, or one whose own base is another image, can be overridden.
func dockerfileStageContexts(dockerfile []byte) (map[string]bool, error) {
	parsed, err := parser.Parse(bytes.NewReader(dockerfile))
	if err != nil {
		return nil, err
	}
	stages, _, err := instructions.Parse(parsed.AST, nil)
	if err != nil {
		return nil, err
	}
	contexts := make(map[string]bool)
	for _, stage := range stages {
		if stage.Name == "" {
			continue
		}
		named, err := reference.ParseNormalizedNamed(strings.ToLower(stage.Name))
		if err != nil {
			return nil, err
		}
		contexts[strings.TrimSuffix(reference.FamiliarString(named), ":latest")] = true
	}
	return contexts, nil
}

// Dockerfile bytes remain unchanged. BuildKit applies this policy both during
// metadata resolution and to the final source graph; unknown external images
// fail closed instead of reintroducing a mutable dependency.
func imageSourcePolicy(r ImageRecipe) (*spb.Policy, error) {
	required, err := imagejob.DockerfileImages(r)
	if err != nil {
		return nil, err
	}
	inputs := append([]imagejob.ImageInput(nil), r.Images...)
	base, baseErr := imagejob.CanonicalReference(r.BaseReference)
	if len(inputs) == 0 && len(required) == 1 && baseErr == nil && required[0].Reference == base {
		required[0].Digest = r.BaseDigest
		required[0].Source = r.BaseReference
		inputs = required
	}
	policy := &spb.Policy{Version: 1, Rules: []*spb.Rule{{Action: spb.PolicyAction_DENY, Selector: &spb.Selector{Identifier: "docker-image://*", MatchType: spb.MatchType_WILDCARD}}}}
	bindings := map[string]string{}
	for _, need := range required {
		var input *imagejob.ImageInput
		for i := range inputs {
			if inputs[i].Reference == need.Reference && inputs[i].Platform == need.Platform {
				input = &inputs[i]
				break
			}
		}
		if input == nil {
			return nil, errors.New("external image dependency was not resolved before preparation")
		}
		if err := digest.Digest(input.Digest).Validate(); err != nil {
			return nil, err
		}
		locator := input.Source
		if locator == "" {
			locator = input.Reference
		}
		named, err := reference.ParseNormalizedNamed(locator)
		if err != nil {
			return nil, err
		}
		pinned, err := reference.WithDigest(reference.TagNameOnly(named), digest.Digest(input.Digest))
		if err != nil {
			return nil, err
		}
		original := "docker-image://" + need.Reference
		target := "docker-image://" + pinned.String()
		if prior, ok := bindings[original]; ok && prior != target {
			return nil, errors.New("one external image must use a consistent immutable index across platforms")
		}
		bindings[original] = target
		if original != target {
			policy.Rules = append(policy.Rules, &spb.Rule{Action: spb.PolicyAction_CONVERT, Selector: &spb.Selector{Identifier: original, MatchType: spb.MatchType_EXACT}, Updates: &spb.Update{Identifier: target}})
		}
		for _, source := range []string{locator, input.Reference} {
			n, err := reference.ParseNormalizedNamed(source)
			if err != nil {
				return nil, err
			}
			canonical, err := reference.WithDigest(reference.TrimNamed(n), digest.Digest(input.Digest))
			if err != nil {
				return nil, err
			}
			tagged, err := reference.WithDigest(reference.TagNameOnly(n), digest.Digest(input.Digest))
			if err != nil {
				return nil, err
			}
			for _, id := range []string{canonical.String(), tagged.String()} {
				policy.Rules = append(policy.Rules, &spb.Rule{Action: spb.PolicyAction_ALLOW, Selector: &spb.Selector{Identifier: "docker-image://" + id, MatchType: spb.MatchType_EXACT}})
			}
		}
	}
	return policy, nil
}

func immutableDockerfile(r ImageRecipe) ([]byte, error) {
	if _, err := imagejob.DockerfileImages(r); err != nil {
		return nil, err
	}
	if !strings.Contains(strings.ToUpper(string(r.Dockerfile)), "FROM") {
		return nil, errors.New("recipe has no FROM")
	}
	return append([]byte(nil), r.Dockerfile...), nil
}
