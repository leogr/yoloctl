package features

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAWSInstallVerifiesBeforeUnpackingAndExecuting(t *testing.T) {
	for _, tc := range []struct{ arch, failure, wantArch string }{
		{"amd64", "", "x86_64"}, {"arm64", "", "aarch64"},
		{"riscv64", "", ""}, {"arm64", "signature", "aarch64"},
		{"amd64", "tamper", "x86_64"}, {"arm64", "key", "aarch64"},
	} {
		t.Run(tc.arch+"/"+tc.failure, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			work := filepath.Join(root, "work")
			for _, path := range []string{bin, work} {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			mock := `#!/bin/sh
set -eu
case "${0##*/}" in
dpkg) printf '%s' "$TEST_ARCH";;
gpg)
  case "$*" in
  *show-only*)
    if [ "$TEST_FAILURE" = key ]; then printf 'fpr:::::::::WRONG:\n'; else printf 'fpr:::::::::FB5DB77FD5C118B80511ADA8A6310ACC4672475C:\n'; fi;;
  *--export*) printf 'reviewed key';;
  esac;;
curl)
  while [ "$#" -gt 0 ]; do
    case "$1" in
    https:*) printf '%s\n' "$1" >> "$TEST_ROOT/calls";;
    -o) shift; target=$1;;
    esac
    shift
  done
  printf 'signed fixture' > "$target"
  if [ "$TEST_FAILURE" = tamper ]; then printf 'altered fixture' > "$target"; fi;;
gpgv)
  echo verify >> "$TEST_ROOT/calls"
  [ "$TEST_FAILURE" != signature ]
  for item in "$@"; do
    case "$item" in *.zip) [ "$(cat "$item")" = 'signed fixture' ];; esac
  done;;
unzip)
  echo unpack >> "$TEST_ROOT/calls"
  while [ "$#" -gt 0 ]; do
    if [ "$1" = -d ]; then shift; dest=$1; fi
    shift
  done
  mkdir "$dest/aws"
  cp "$TEST_ROOT/fake-installer" "$dest/aws/install";;
esac
`
			for _, name := range []string{"dpkg", "gpg", "gpgv", "curl", "unzip"} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(mock), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			installer := `#!/bin/sh
set -eu
echo install >> "$TEST_ROOT/calls"
update=0
while [ "$#" -gt 0 ]; do
  case "$1" in --bin-dir) shift; dest=$1;; --update) update=1;; esac
  shift
done
[ "$update" = 1 ]
mkdir -p "$dest"
printf '#!/bin/sh\nprintf "aws-cli/fixture\\n"\n' > "$dest/aws"
chmod 0755 "$dest/aws"
`
			if err := os.WriteFile(filepath.Join(root, "fake-installer"), []byte(installer), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", awsInstallScript, "aws-install", filepath.Join(root, "key.asc"), filepath.Join(root, "install"), filepath.Join(root, "commands"))
			cmd.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), "TMPDIR=" + work, "TEST_ROOT=" + root, "TEST_ARCH=" + tc.arch, "TEST_FAILURE=" + tc.failure}
			output, err := cmd.CombinedOutput()
			wantSuccess := tc.wantArch != "" && tc.failure == ""
			if (err == nil) != wantSuccess {
				t.Fatalf("verification/install boundary: %v: %s", err, output)
			}
			calls, _ := os.ReadFile(filepath.Join(root, "calls"))
			text := string(calls)
			if !wantSuccess && (strings.Contains(text, "unpack") || strings.Contains(text, "install")) {
				t.Fatal("unverified bytes reached extraction or installation")
			}
			if wantSuccess && (!strings.Contains(text, "linux-"+tc.wantArch+".zip.sig") || !strings.Contains(text, "verify\nunpack\ninstall\n")) {
				t.Fatal("architecture, signature download or execution ordering changed", text)
			}
		})
	}
}

func TestAWSInstallerAndSigningKeyAreHashedRecipeInputs(t *testing.T) {
	originalScript, originalKey := awsInstallScript, awsCLIKey
	t.Cleanup(func() { awsInstallScript, awsCLIKey = originalScript, originalKey })
	before := awsInstallDockerfile()
	for _, input := range []string{originalScript, originalKey} {
		if !strings.Contains(before, base64.StdEncoding.EncodeToString([]byte(input))) {
			t.Fatal("AWS installer/trust bytes absent from generated Dockerfile")
		}
	}
	awsInstallScript += "\n# changed installer fixture\n"
	if before == awsInstallDockerfile() {
		t.Fatal("installer change did not affect image recipe")
	}
	awsInstallScript = originalScript
	awsCLIKey += "\n"
	if before == awsInstallDockerfile() {
		t.Fatal("trust-key change did not affect image recipe")
	}
	feature, err := Lookup("aws")
	if err != nil || len(feature.RequiresCapabilities) != 0 {
		t.Fatal("AWS installation acquired automatic policy", err)
	}
}
