package agentbox

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

func TestFileExportRecordAcceptsHistoricalProtocolWithoutLaunchCompatibility(t *testing.T) {
	current, d, raw, _ := nativeRuntimeFixture(t)
	d.Labels[LabelKind] = KindAgentbox
	for i, arg := range d.Command {
		if arg == "--protocol" {
			d.Command[i+1] = "1"
		}
	}
	d.Labels[openshell.LabelProtocol] = "1"
	r, err := validateFileExportRecord(current, "fixture", d, raw)
	if err != nil {
		t.Fatal(err)
	}
	if r.Launch.MainLauncher {
		t.Fatal("fixture should be older launcher")
	}
	// Current launch observation remains stricter and unchanged.
	if _, err := validateOpenShellRuntime(current, "fixture", d, raw, nil); err == nil {
		t.Fatal("launch admission relaxed")
	}
}
func TestFileExportRecordRejectsMissingOrMismatchedBinding(t *testing.T) {
	for name, edit := range map[string]func(*spaceenv.Context, *container.Details, *[]byte){
		"disk": func(c *spaceenv.Context, _ *container.Details, _ *[]byte) { c.DiskID = "other" },
		"uid":  func(c *spaceenv.Context, _ *container.Details, _ *[]byte) { c.UID++ },
		"control": func(_ *spaceenv.Context, d *container.Details, _ *[]byte) {
			d.Labels[openshell.LabelControlRoot] = "/other/runtime/openshell/containers/" + strings.Repeat("2", 32)
		},
		"record": func(_ *spaceenv.Context, _ *container.Details, b *[]byte) { *b = []byte(`{}`) },
		"duplicate": func(_ *spaceenv.Context, _ *container.Details, b *[]byte) {
			*b = append([]byte(`{"generation":"bad",`), (*b)[1:]...)
		},
		"wrong-generation": func(_ *spaceenv.Context, _ *container.Details, b *[]byte) {
			var r openshell.RuntimeRecord
			_ = json.Unmarshal(*b, &r)
			r.Generation = "g" + strings.Repeat("f", 48)
			*b, _ = json.Marshal(r)
		},
		"bind": func(_ *spaceenv.Context, d *container.Details, _ *[]byte) { d.Mounts = d.Mounts[:len(d.Mounts)-1] },
	} {
		t.Run(name, func(t *testing.T) {
			c, d, b, _ := nativeRuntimeFixture(t)
			d.Labels[LabelKind] = KindAgentbox
			edit(&c, d, &b)
			if _, err := validateFileExportRecord(c, "fixture", d, b); err == nil {
				t.Fatal("accepted mismatch")
			}
		})
	}
}

func TestFileExportRequiresTheSameRetainedGenerationAndNoMissingRecord(t *testing.T) {
	for _, mode := range []string{"same", "changed", "missing"} {
		t.Run(mode, func(t *testing.T) {
			c, d, raw, _ := nativeRuntimeFixture(t)
			d.Labels[LabelKind] = KindAgentbox
			calls := []string{}
			err := verifyFileExportRecords(c, "fixture", d, func(p string) ([]byte, error) {
				calls = append(calls, p)
				if len(calls) == 2 {
					if mode == "missing" {
						return nil, os.ErrNotExist
					}
					if mode == "changed" {
						return append(raw, ' '), nil
					}
				}
				return raw, nil
			})
			if mode == "same" && err != nil || mode != "same" && err == nil {
				t.Fatal(mode, err)
			}
			if len(calls) != 2 || !strings.HasSuffix(calls[0], "/current.json") || !strings.Contains(calls[1], "/generations/g") || !strings.HasSuffix(calls[1], "/record.json") {
				t.Fatal(calls)
			}
		})
	}
}
