package imagejob

import (
	"strings"
	"testing"
)

func TestDockerfileExternalDependenciesAndStageAliases(t *testing.T) {
	r := Recipe{Builder: "test/1", BaseDigest: "sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64", Dockerfile: []byte("ARG BASE=alpine:3\nFROM --platform=linux/arm64 ${BASE} AS alpine\nCOPY --from=alpine /x /x\nFROM alpine AS final\nCOPY --from=0 /x /x\nRUN --mount=from=busybox:1,target=/src true\n"), Args: map[string]string{"BASE": "ubuntu:24.04"}}
	inputs, err := DockerfileImages(r)
	if err != nil || len(inputs) != 2 {
		t.Fatal(inputs, err)
	}
	found := false
	for _, input := range inputs {
		if strings.Contains(input.Reference, "ubuntu:24.04") {
			found = input.Platform == "linux/arm64"
		}
	}
	if !found {
		t.Fatal("ARG/platform not represented", inputs)
	}
	for i := range inputs {
		inputs[i].Digest = "sha256:" + strings.Repeat("a", 64)
	}
	r.Images = inputs
	first, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	r.Images[0].Source = "one-time/private"
	second, _ := r.Identity()
	if first != second {
		t.Fatal("private locator changed identity")
	}
	r.Images[0].Digest = "sha256:" + strings.Repeat("b", 64)
	third, _ := r.Identity()
	if first == third {
		t.Fatal("external image bytes absent from identity")
	}
}

func TestSyntaxFrontendAndBuildArgOverrideAreInputDependencies(t *testing.T) {
	r := Recipe{Builder: "test/1", Platform: "linux/amd64", BaseReference: "ubuntu:24.04", BaseDigest: "sha256:" + strings.Repeat("a", 64), Dockerfile: []byte("# syntax = docker/dockerfile:1\nFROM ubuntu:24.04\n")}
	for _, override := range []string{"", "docker/dockerfile:1.9"} {
		expected := "docker.io/docker/dockerfile:1"
		if override != "" {
			r.Args = map[string]string{"BUILDKIT_SYNTAX": override}
			expected = "docker.io/docker/dockerfile:1.9"
		}
		inputs, err := DockerfileImages(r)
		if err != nil || len(inputs) != 2 {
			t.Fatal(inputs, err)
		}
		found := false
		for _, input := range inputs {
			found = found || input.Reference == expected
		}
		if !found {
			t.Fatal("selected frontend absent", inputs)
		}
	}
}
