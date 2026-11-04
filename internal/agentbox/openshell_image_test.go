package agentbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
)

func TestNativeImagePreflightRejectsInheritedEntrypointBeforeTransactionEffects(t *testing.T) {
	const image = "local/native:chosen"
	for _, test := range []struct {
		name, config string
		allowed      bool
	}{
		{"default-omitted", `{}`, true},
		{"default-null", `{"Entrypoint":null}`, true},
		{"default-empty", `{"Entrypoint":[]}`, true},
		{"forwarding-wrapper", `{"Entrypoint":["/usr/local/bin/custom-init"],"Env":["PRIVATE_IMAGE_ENV=do-not-print"]}`, false},
		{"shell-wrapper", `{"Entrypoint":["/bin/sh","-c","exec custom-init \"$@\""]}`, false},
		{"empty-executable", `{"Entrypoint":[""]}`, false},
		{"control-display", `{"Entrypoint":["init\n\u001b[31m"]}`, false},
		{"unavailable-config", `null`, false},
		{"malformed-entrypoint", `{"Entrypoint":"custom-init"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := `[{"Id":"sha256:` + strings.Repeat("a", 64) + `","RepoTags":["docker.io/` + image + `"],"Os":"linux","Architecture":"` + runtime.GOARCH + `","Config":` + test.config + `}]`
			client, log := nativeImageClientFixture(t, raw, 0)
			a := LiveApplier{Client: client}
			var events []string
			actions := nativeTransactionFixture(&events, "", nil)
			actions.preflight = func(ctx context.Context) error {
				events = append(events, "image-preflight")
				return a.PreflightNativeImage(ctx, image)
			}
			err := completeNativeContainerCreation(t.Context(), actions)
			if (err == nil) != test.allowed {
				t.Fatal("image preflight returned wrong result", err)
			}
			if !test.allowed {
				var uncertain *NativeEffectsUncertain
				if !slices.Equal(events, []string{"image-preflight"}) || errors.As(err, &uncertain) {
					t.Fatal("known preflight refusal reached reserve/create/cleanup or became uncertain", events, err)
				}
				if strings.Contains(err.Error(), "do-not-print") || strings.ContainsAny(err.Error(), "\n\x1b") {
					t.Fatal("image refusal exposed unrelated ENV or terminal controls")
				}
				var config struct{ Entrypoint []string }
				if json.Unmarshal([]byte(test.config), &config) == nil && len(config.Entrypoint) > 0 {
					for _, want := range []string{image, "ENTRYPOINT", "rebuild the base without ENTRYPOINT", "feature/RUN step"} {
						if !strings.Contains(err.Error(), want) {
							t.Fatal("refusal lacks selected image/ENTRYPOINT/remedy", want, err)
						}
					}
					if test.name == "forwarding-wrapper" && !strings.Contains(err.Error(), "/usr/local/bin/custom-init") {
						t.Fatal("refusal does not name the actual inherited wrapper", err)
					}
				}
			} else if !slices.Equal(events, []string{"image-preflight", "reserve", "create", "inspect", "prepare", "start", "activate", "ready", "forwards"}) {
				t.Fatal("empty ENTRYPOINT changed transaction ordering", events)
			}
			assertNativeImageReadOnlyCommand(t, log, image)
		})
	}
}

func TestNativeImagePreflightRefusesUnavailableOrMismatchedInspection(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		status    int
	}{
		{"missing-config", `[{"Id":"sha256:a","RepoTags":["docker.io/local/native:chosen"]}]`, 0},
		{"wrong-image", `[{"Id":"sha256:a","RepoTags":["docker.io/local/native:other"],"Config":{}}]`, 0},
		{"missing-identity", `[{"RepoTags":["docker.io/local/native:chosen"],"Config":{}}]`, 0},
		{"empty", `[]`, 0},
		{"truncated", `[{`, 0},
		{"failed-command", `[]`, 23},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, log := nativeImageClientFixture(t, test.raw, test.status)
			err := (LiveApplier{Client: client}).PreflightNativeImage(t.Context(), "local/native:chosen")
			if err == nil {
				t.Fatal("incomplete inspection treated as image without ENTRYPOINT")
			}
			if test.status != 0 {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != test.status {
					t.Fatal("inspection command failure lost", err)
				}
			}
			assertNativeImageReadOnlyCommand(t, log, "local/native:chosen")
		})
	}
	if err := (LiveApplier{}).PreflightNativeImage(t.Context(), ""); err == nil {
		t.Fatal("missing selected image/client accepted")
	}
}

func TestNativeImageRefusalRetainsStrictActualLaunchEvidence(t *testing.T) {
	d := openShellDetailsFixture(t)
	d.Command = append([]string{"/usr/local/bin/custom-init"}, d.Command...)
	if _, ok := openShellRuntimeEvidence(d); ok {
		t.Fatal("inherited wrapper accepted as actual selected helper")
	}
}

func TestNativeImagePreflightUsesPreparedTargetDigestAndItsNativeConfiguration(t *testing.T) {
	target := "sha256:" + strings.Repeat("a", 64)
	other := "sha256:" + strings.Repeat("c", 64)
	for _, name := range []string{
		"private-tag-empty", "public-tag-empty", "wrapper", "wrong-target",
		"config-id-is-not-target", "missing-target", "mixed-targets", "malformed-target",
		"foreign-config-platform", "foreign-os", "missing-config", "missing-config-id", "ambiguous-objects",
	} {
		t.Run(name, func(t *testing.T) {
			// nerdctl 2.3.5's Id is the platform configuration digest. The
			// controller hands us the different manifest/index target digest,
			// while RepoTags retain the private/public stored image name.
			image := map[string]any{
				"Id":          "sha256:" + strings.Repeat("b", 64),
				"RepoTags":    []string{"docker.io/yoloctl/prepared:attempt"},
				"RepoDigests": []string{"docker.io/yoloctl/prepared@" + target},
				"Os":          "linux", "Architecture": runtime.GOARCH,
				"Config": map[string]any{},
			}
			allowed := name == "private-tag-empty" || name == "public-tag-empty"
			switch name {
			case "public-tag-empty":
				image["RepoTags"] = []string{"docker.io/yoloctl/composed:desired"}
				image["RepoDigests"] = []string{"docker.io/yoloctl/composed@" + target}
			case "wrapper":
				image["Config"] = map[string]any{"Entrypoint": []string{"/usr/local/bin/custom-init"}}
			case "wrong-target", "config-id-is-not-target":
				image["RepoDigests"] = []string{"docker.io/yoloctl/prepared@" + other}
				if name == "config-id-is-not-target" {
					image["Id"] = target
				}
			case "missing-target":
				delete(image, "RepoDigests")
			case "mixed-targets":
				image["RepoDigests"] = []string{"docker.io/yoloctl/prepared@" + target, "docker.io/yoloctl/composed@" + other}
			case "malformed-target":
				image["RepoDigests"] = []string{target}
			case "foreign-config-platform":
				image["Architecture"] = "foreign"
			case "foreign-os":
				image["Os"] = "foreign"
			case "missing-config":
				delete(image, "Config")
			case "missing-config-id":
				delete(image, "Id")
			}
			objects := []any{image}
			if name == "ambiguous-objects" {
				objects = append(objects, image)
			}
			raw, err := json.Marshal(objects)
			if err != nil {
				t.Fatal(err)
			}
			client, log := nativeImageClientFixture(t, string(raw), 0)
			var effects []string
			actions := nativeTransactionFixture(&effects, "", nil)
			actions.preflight = func(ctx context.Context) error {
				return (LiveApplier{Client: client}).PreflightNativeImage(ctx, target)
			}
			err = completeNativeContainerCreation(t.Context(), actions)
			if (err == nil) != allowed {
				t.Fatal("prepared target/config binding returned wrong result", err)
			}
			if !allowed && len(effects) != 0 {
				t.Fatal("refused target/config/ENTRYPOINT reached effects", effects)
			}
			if name == "wrapper" {
				for _, want := range []string{target, "ENTRYPOINT", "/usr/local/bin/custom-init", "rebuild the base without ENTRYPOINT", "feature/RUN step"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatal("digest-bound wrapper did not receive the B remedy", want, err)
					}
				}
			}
			assertNativeImageReadOnlyCommand(t, log, target)
		})
	}
}

func nativeImageClientFixture(t *testing.T, raw string, status int) (*container.Client, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LIMA_HOME", t.TempDir())
	directory := t.TempDir()
	t.Setenv("YOLOCTL_IMAGE_PREFLIGHT_FIXTURE", directory)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$YOLOCTL_IMAGE_PREFLIGHT_FIXTURE/commands.log"
case "$*" in
 'shell yolo-fixture -- sudo nerdctl image inspect -- local/native:chosen'|'shell yolo-fixture -- sudo nerdctl image inspect -- sha256:'*)
   cat "$YOLOCTL_IMAGE_PREFLIGHT_FIXTURE/image.json"
   exit ` + strconv.Itoa(status) + ` ;;
 *) exit 97 ;;
esac
`
	for name, data := range map[string]string{"image.json": raw, "limactl": script} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(data), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return container.NewWithOutput("fixture", io.Discard, io.Discard), filepath.Join(directory, "commands.log")
}

func assertNativeImageReadOnlyCommand(t *testing.T, log, image string) {
	t.Helper()
	raw, err := os.ReadFile(log)
	if err != nil || string(raw) != "shell yolo-fixture -- sudo nerdctl image inspect -- "+image+"\n" {
		t.Fatal("image refusal pulled/created/started/overrode ENTRYPOINT or retried", string(raw), err)
	}
}
