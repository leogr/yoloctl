package imagejob

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/containerd/platforms"
	"github.com/distribution/reference"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
)

// ImageInput records a recipe's external image, its immutable target and the
// private/local locator used to obtain those same bytes. Source is a locator,
// not a content identity; private attempt names never affect deduplication.
type ImageInput struct {
	Reference string `json:"reference"`
	Source    string `json:"source,omitempty"`
	Platform  string `json:"platform"`
	Digest    string `json:"digest,omitempty"`
}

func CanonicalReference(ref string) (string, error) {
	n, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", err
	}
	return reference.TagNameOnly(n).String(), nil
}

// DockerfileImages uses BuildKit's parser and flag/ARG semantics. It retains
// the original Dockerfile bytes and supports independent stages, platform
// flags, global ARGs, the syntax frontend, external COPY and RUN-mount sources. Internal aliases and
// numeric COPY sources are not registry images. BuildKit forbids variable
// expansion in COPY/RUN --from itself; FROM uses its global ARG environment.
func DockerfileImages(r Recipe) ([]ImageInput, error) {
	parsed, err := parser.Parse(bytes.NewReader(r.Dockerfile))
	if err != nil {
		return nil, err
	}
	stages, meta, err := instructions.Parse(parsed.AST, nil)
	if err != nil {
		return nil, err
	}
	target := r.Platform
	if target == "" {
		target = platforms.Format(platforms.DefaultSpec())
	}
	p, err := platforms.Parse(target)
	if err != nil {
		return nil, err
	}
	env := map[string]string{"BUILDPLATFORM": target, "TARGETPLATFORM": target, "BUILDOS": p.OS, "TARGETOS": p.OS, "BUILDARCH": p.Architecture, "TARGETARCH": p.Architecture, "BUILDVARIANT": p.Variant, "TARGETVARIANT": p.Variant}
	lex := shell.NewLex(parsed.EscapeToken)
	expand := func(word string) (string, error) {
		values := make([]string, 0, len(env))
		for k, v := range env {
			values = append(values, k+"="+v)
		}
		v, _, err := lex.ProcessWord(word, shell.EnvsFromSlice(values))
		return v, err
	}
	for _, cmd := range meta {
		for _, arg := range cmd.Args {
			if v, ok := r.Args[arg.Key]; ok {
				env[arg.Key] = v
			} else if arg.Value != nil {
				v, err := expand(*arg.Value)
				if err != nil {
					return nil, err
				}
				env[arg.Key] = v
			}
		}
	}
	aliases := map[string]bool{}
	for _, stage := range stages {
		if stage.Name != "" {
			aliases[strings.ToLower(stage.Name)] = true
		}
	}
	priorAliases := map[string]bool{}
	inputs := map[string]ImageInput{}
	add := func(ref, platform string, indexAllowed bool) error {
		if ref == "" || ref == "scratch" || indexAllowed && aliases[strings.ToLower(ref)] {
			return nil
		}
		if indexAllowed {
			if index, err := strconv.Atoi(ref); err == nil {
				if index < 0 || index >= len(stages) {
					return fmt.Errorf("invalid Dockerfile stage index %q", ref)
				}
				return nil
			}
		}
		ref, err := CanonicalReference(ref)
		if err != nil {
			return err
		}
		parsed, err := platforms.Parse(platform)
		if err != nil {
			return err
		}
		platform = platforms.Format(parsed)
		inputs[ref+"\x00"+platform] = ImageInput{Reference: ref, Source: ref, Platform: platform}
		return nil
	}
	// Match dockerfile.v0's frontend selection, including the documented
	// BUILDKIT_SYNTAX build-arg override. The frontend executes on the native
	// builder platform, independently of a FROM's target platform.
	frontend, _, _, found := parser.DetectSyntax(r.Dockerfile)
	if override, ok := r.Args["BUILDKIT_SYNTAX"]; ok {
		fields := strings.Fields(override)
		if len(fields) == 0 {
			return nil, fmt.Errorf("empty Dockerfile syntax frontend")
		}
		frontend, found = fields[0], true
	}
	if found {
		if err := add(frontend, platforms.Format(platforms.DefaultSpec()), false); err != nil {
			return nil, err
		}
	}
	for _, stage := range stages {
		platform := target
		if stage.Platform != "" {
			platform, err = expand(stage.Platform)
			if err != nil {
				return nil, err
			}
		}
		base, err := expand(stage.BaseName)
		if err != nil {
			return nil, err
		}
		if !priorAliases[strings.ToLower(base)] {
			if err := add(base, platform, false); err != nil {
				return nil, err
			}
		}
		if stage.Name != "" {
			priorAliases[strings.ToLower(stage.Name)] = true
		}
		for _, cmd := range stage.Commands {
			switch cmd := cmd.(type) {
			case *instructions.CopyCommand:
				if err := add(cmd.From, platform, true); err != nil {
					return nil, err
				}
			case *instructions.RunCommand:
				for _, mount := range instructions.GetMounts(cmd) {
					if err := add(mount.From, platform, true); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]ImageInput, 0, len(keys))
	for _, key := range keys {
		out = append(out, inputs[key])
	}
	return out, nil
}
