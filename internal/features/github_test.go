package features

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The github feature installs gh itself: the base image carries only git.
func TestGitHubFeatureRendersTheGhInstall(t *testing.T) {
	df, installing, hasInstall, err := InstallPlan("base", []string{"github"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasInstall || strings.Join(installing, ",") != "github" {
		t.Fatalf("github installs nothing: %v %v", installing, hasInstall)
	}
	for _, want := range []string{
		"# Feature: github",
		"install -y --no-install-recommends ca-certificates gnupg",
		aptCacheMounts,
		base64.StdEncoding.EncodeToString([]byte(githubInstallScript)),
		base64.StdEncoding.EncodeToString([]byte(githubCLIKey)),
		`sh "$gh_tmp/install.sh" "$gh_tmp/key.asc"`,
	} {
		if !strings.Contains(df, want) {
			t.Errorf("rendered recipe lacks %q:\n%s", want, df)
		}
	}
	feature, err := Lookup("github")
	if err != nil || strings.Join(feature.RequiresCapabilities, ",") != "network.github" {
		t.Fatal("github capability changed", feature.RequiresCapabilities, err)
	}
	original := githubCLIKey
	t.Cleanup(func() { githubCLIKey = original })
	githubCLIKey += "\n"
	if githubInstallDockerfile() == feature.Install.Dockerfile {
		t.Fatal("trust-key change did not affect the image recipe")
	}
}

// The script accepts only the published key set, gives apt only those keys and
// installs gh from GitHub's repository at the path the capability names.
func TestGitHubInstallTrustsOnlyThePublishedKeys(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not installed")
	}
	other := filepath.Join(t.TempDir(), "other.asc")
	if err := os.WriteFile(other, []byte(strings.Replace(githubCLIKey, "mQINBGMXLooB", "mQINBGMXLooC", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, key string
		ok        bool
	}{{"published", "", true}, {"altered", other, false}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			key := tc.key
			if key == "" {
				key = filepath.Join(root, "key.asc")
				if err := os.WriteFile(key, []byte(githubCLIKey), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			mock := "#!/bin/sh\ncase \"${0##*/}\" in\ndpkg) printf arm64;;\n*) printf '%s %s\\n' \"${0##*/}\" \"$*\" >> \"$TEST_ROOT/calls\";;\nesac\n"
			for _, name := range []string{"dpkg", "apt-get", "gh"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(mock), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			keyring := filepath.Join(root, "keyrings", "github.gpg")
			sources := filepath.Join(root, "github-cli.sources")
			cmd := exec.Command("sh", "-c", githubInstallScript, "github-install", key, keyring, sources, filepath.Join(bin, "gh"))
			cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "TMPDIR=" + root, "TEST_ROOT=" + root}
			output, err := cmd.CombinedOutput()
			calls, _ := os.ReadFile(filepath.Join(root, "calls"))
			if !tc.ok {
				if err == nil || len(calls) != 0 {
					t.Fatalf("altered key reached apt: %v %s %s", err, output, calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v: %s", err, output)
			}
			if !strings.Contains(string(calls), "update --error-on=any\n") || !strings.HasSuffix(string(calls), "install -y --no-install-recommends gh\ngh --version\n") {
				t.Fatalf("apt install order changed:\n%s", calls)
			}
			got, _ := os.ReadFile(sources)
			want := "Types: deb\nURIs: https://cli.github.com/packages\nSuites: stable\nComponents: main\nArchitectures: arm64\nSigned-By: " + keyring + "\n"
			if string(got) != want {
				t.Fatalf("sources = %q, want %q", got, want)
			}
			home := filepath.Join(root, "check")
			if err := os.Mkdir(home, 0o700); err != nil {
				t.Fatal(err)
			}
			listed, err := exec.Command("gpg", "--no-options", "--batch", "--homedir", home, "--with-colons", "--import-options", "show-only", "--import", keyring).Output()
			if err != nil {
				t.Fatal(err)
			}
			var primaries []string
			lines := strings.Split(string(listed), "\n")
			for i, line := range lines {
				if strings.HasPrefix(line, "pub:") && i+1 < len(lines) {
					primaries = append(primaries, strings.Split(lines[i+1], ":")[9])
				}
			}
			if strings.Join(primaries, " ") != "2C6106201985B60E6C7AC87323F3D4EA75716059 7F38BBB59D064DBCB3D84D725612B36462313325" {
				t.Fatalf("apt keyring primaries = %v", primaries)
			}
		})
	}
}
