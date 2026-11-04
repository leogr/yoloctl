package features

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The installer names the privileged setting, not a capability, in the
// recipe and in every refusal.
func TestPrivilegedInstallerWording(t *testing.T) {
	f, err := Lookup(PrivilegedRuntime)
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := GenerateDockerfile("base", []string{PrivilegedRuntime})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(recipe, "# Feature: "+PrivilegedRuntime+" — sudo for the sandbox user in a privileged box") || strings.Contains(recipe, "yolo.dev.privileged") {
		t.Fatalf("recipe:\n%s", recipe)
	}
	if strings.Count(f.Install.Dockerfile, "echo 'a privileged box needs ") != 3 {
		t.Fatalf("refusals:\n%s", f.Install.Dockerfile)
	}
}

// The installer script runs here against a scratch root with fake package,
// account and sudo tools; mktemp, chmod, mv and grep are the real ones. It
// installs sudo through apt only when the base lacks it, refuses a base it
// cannot complete, and publishes only a visudo-checked 0440 drop-in.
func TestPrivilegedInstallerBehavior(t *testing.T) {
	f, err := Lookup(PrivilegedRuntime)
	if err != nil {
		t.Fatal(err)
	}
	_, script, ok := strings.Cut(f.Install.Dockerfile, "<<'YOLOCTL_PRIVILEGED_SUDO'\n")
	script, _, ok2 := strings.Cut(script, "\nYOLOCTL_PRIVILEGED_SUDO")
	if !ok || !ok2 || !strings.HasPrefix(script, "#!/bin/sh\n") {
		t.Fatal("script must be a shebang heredoc")
	}
	scratch := t.TempDir()
	if !regexp.MustCompile(`^[A-Za-z0-9/_.-]+$`).MatchString(scratch) {
		t.Skipf("the unquoted script paths need a plain scratch root, not %q", scratch)
	}
	tempName := regexp.MustCompile(`\.yoloctl-privileged\.[A-Za-z0-9]{6}`)
	tools := map[string]string{}
	for _, name := range []string{"mktemp", "chmod", "mv", "grep"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		tools[name] = path
	}
	type base struct {
		sudo, apt, user, visudoRejects bool
		sudoers                        string
	}
	run := func(t *testing.T, b base) (root, log, out string, err error) {
		root, err = os.MkdirTemp(scratch, "root")
		if err != nil {
			t.Fatal(err)
		}
		bin, pkg := filepath.Join(root, "bin"), filepath.Join(root, "pkg")
		logPath := filepath.Join(root, "log")
		for _, dir := range []string{bin, pkg, filepath.Join(root, "etc", "sudoers.d")} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for name, path := range tools {
			if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
				t.Fatal(err)
			}
		}
		fake := func(dir, name, body string) {
			raw := "#!/bin/sh\necho \"" + name + " $*\" >> " + logPath + "\n" + body + "\n"
			if err := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		// apt-get installs the sudo package's tools into PATH.
		sudoDir := pkg
		if b.sudo {
			sudoDir = bin
		}
		fake(sudoDir, "sudo", "")
		if b.visudoRejects {
			fake(sudoDir, "visudo", `[ "$1" != -cf ]`)
		} else {
			fake(sudoDir, "visudo", "")
		}
		if b.apt {
			fake(bin, "apt-get", `[ "$1" != install ] || mv `+pkg+`/sudo `+pkg+`/visudo `+bin+`/`)
		}
		if b.user {
			fake(bin, "id", "")
		} else {
			fake(bin, "id", "exit 1")
		}
		for _, name := range []string{"install", "chown", "rm"} {
			fake(bin, name, "")
		}
		if err := os.WriteFile(filepath.Join(root, "etc", "sudoers"), []byte(strings.ReplaceAll(b.sudoers, "ROOT", root)), 0o644); err != nil {
			t.Fatal(err)
		}
		rooted := strings.NewReplacer("/etc/", root+"/etc/", "/var/lib/apt/lists/", root+"/var/lib/apt/lists/").Replace(script)
		cmd := exec.Command("/bin/sh", "-c", rooted)
		cmd.Env = []string{"PATH=" + bin}
		raw, err := cmd.CombinedOutput()
		logged, readErr := os.ReadFile(logPath)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			t.Fatal(readErr)
		}
		return root, strings.ReplaceAll(string(logged), root, "ROOT"), strings.ReplaceAll(string(raw), root, ""), err
	}
	includes := func(prefix string) string {
		return "root ALL=(ALL:ALL) ALL\n" + prefix + "includedir ROOT/etc/sudoers.d\n"
	}
	complete := func(apt string) string {
		return apt + "id -u sandbox\ninstall -d -o root -g root -m 0750 ROOT/etc/sudoers.d\nchown root:root ROOT/etc/sudoers.d/.yoloctl-privileged.TMP\nvisudo -cf ROOT/etc/sudoers.d/.yoloctl-privileged.TMP\nvisudo -c\n"
	}
	for name, tc := range map[string]struct {
		base
		log, refusal string
	}{
		"sudo present":             {base: base{sudo: true, apt: true, user: true, sudoers: includes("#")}, log: complete("")},
		"@includedir":              {base: base{sudo: true, user: true, sudoers: includes("@")}, log: complete("")},
		"sudo from apt":            {base: base{apt: true, user: true, sudoers: includes("#")}, log: complete("apt-get update\napt-get install -y --no-install-recommends sudo\nrm -rf ROOT/var/lib/apt/lists/*\n")},
		"no sudo and no apt":       {base: base{user: true, sudoers: includes("#")}, refusal: "a privileged box needs sudo and visudo in the image"},
		"no sandbox user":          {base: base{sudo: true, sudoers: includes("#")}, refusal: "a privileged box needs the sandbox user in the image"},
		"no sudoers.d include":     {base: base{sudo: true, user: true, sudoers: "root ALL=(ALL:ALL) ALL\n# includedir ROOT/etc/sudoers.d\n"}, refusal: "a privileged box needs /etc/sudoers to include /etc/sudoers.d"},
		"drop-in rejected by sudo": {base: base{sudo: true, user: true, visudoRejects: true, sudoers: includes("#")}},
	} {
		t.Run(name, func(t *testing.T) {
			root, log, out, err := run(t, tc.base)
			dropIn := filepath.Join(root, PrivilegedSudoers)
			info, statErr := os.Stat(dropIn)
			if tc.log == "" {
				if err == nil || !strings.Contains(out, tc.refusal) || !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("not refused: %v, drop-in %v, output %q", err, statErr, out)
				}
				return
			}
			tmps, _ := filepath.Glob(filepath.Join(root, "etc", "sudoers.d", ".yoloctl-privileged.*"))
			if len(tmps) != 0 {
				t.Fatalf("temporary drop-in left behind: %q", tmps)
			}
			log = tempName.ReplaceAllString(log, ".yoloctl-privileged.TMP")
			if err != nil || out != "" || log != tc.log {
				t.Fatalf("run = %v %q, log:\n%s\nwant:\n%s", err, out, log, tc.log)
			}
			content, err := os.ReadFile(dropIn)
			if err != nil || string(content) != "sandbox ALL=(ALL:ALL) NOPASSWD: ALL\n" || info.Mode().Perm() != 0o440 {
				t.Fatalf("drop-in %q mode %v: %v", content, info.Mode(), err)
			}
		})
	}
}
