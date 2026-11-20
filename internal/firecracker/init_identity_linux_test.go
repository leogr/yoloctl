//go:build linux && amd64

package firecracker

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/guestbinary"
)

// The VM init links the assets package, so its build reads box-only files such
// as the agentbox Dockerfile although the program never uses them. Editing one
// must leave the VM image identity unchanged.
func TestBoxOnlyAssetEditKeepsVMImageIdentity(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ldflags := makefileVMInitLDFlags(t, root)
	box := filepath.Join(root, "assets", "containers", "agentbox", "Dockerfile")
	raw, err := os.ReadFile(box)
	if err != nil {
		t.Fatal(err)
	}
	edited := filepath.Join(t.TempDir(), "Dockerfile")
	if err := os.WriteFile(edited, append(raw, "# box-only edit\n"...), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay, err := json.Marshal(map[string]map[string]string{"Replace": {box: edited}})
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(t.TempDir(), "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0o600); err != nil {
		t.Fatal(err)
	}
	identity := func(ldflags string, edit bool) string {
		t.Helper()
		out := filepath.Join(t.TempDir(), "yoloctl-vm-init-linux-amd64")
		args := []string{"build", "-buildvcs=false", "-trimpath", "-ldflags", ldflags, "-o", out}
		if edit {
			args = append(args, "-overlay", overlayPath)
		}
		cmd := exec.CommandContext(t.Context(), "go", append(args, "./cmd/yoloctl-vm-guest")...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOFLAGS=")
		if msg, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build VM init: %v\n%s", err, msg)
		}
		sum, err := guestbinary.SHA256File(out)
		if err != nil {
			t.Fatal(err)
		}
		old := guestbinary.InitAMD64SHA256
		guestbinary.InitAMD64SHA256 = sum
		defer func() { guestbinary.InitAMD64SHA256 = old }()
		recipe, err := defaultRecipe()
		if err != nil {
			t.Fatal(err)
		}
		return recipe
	}
	if identity(ldflags, false) != identity(ldflags, true) {
		t.Fatal("editing a box-only asset changed the VM image identity")
	}
	// With Go's build ID the same edit changes the init: the overlay reaches
	// the build, so the check above is not vacuous.
	var withBuildID []string
	for _, flag := range strings.Fields(ldflags) {
		if flag != "-buildid=" {
			withBuildID = append(withBuildID, flag)
		}
	}
	if identity(strings.Join(withBuildID, " "), false) == identity(strings.Join(withBuildID, " "), true) {
		t.Fatal("box-only edit did not reach the VM init build")
	}
}

// makefileVMInitLDFlags returns the linker flags of the Makefile's VM init build.
func makefileVMInitLDFlags(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	_, rule, ok := strings.Cut(string(raw), "\nbuild-vm-init:")
	if !ok {
		t.Fatal("Makefile has no build-vm-init rule")
	}
	match := regexp.MustCompile(`go build .*-ldflags "([^"]*)" .*\./cmd/yoloctl-vm-guest`).FindStringSubmatch(rule)
	if match == nil {
		t.Fatal("build-vm-init does not build ./cmd/yoloctl-vm-guest with -ldflags")
	}
	return match[1]
}
