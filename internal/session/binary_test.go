package session

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"

	"github.com/leogr/yoloctl/internal/guestbinary"
)

func writeGuestELF(t *testing.T, path string, machine elf.Machine, dynamic bool) {
	t.Helper()
	h := elf.Header64{Type: uint16(elf.ET_EXEC), Machine: uint16(machine), Version: 1, Ehsize: 64}
	copy(h.Ident[:], []byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), 1})
	if dynamic {
		h.Phoff, h.Phentsize, h.Phnum = 64, 56, 1
	}
	var b bytes.Buffer
	if err := binary.Write(&b, binary.LittleEndian, h); err != nil {
		t.Fatal(err)
	}
	if dynamic {
		if err := binary.Write(&b, binary.LittleEndian, elf.Prog64{Type: uint32(elf.PT_INTERP)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, b.Bytes(), 0o755); err != nil {
		t.Fatal(err)
	}
}

func guestBundle(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	self := filepath.Join(dir, "yoloctl")
	// A host executable that is deliberately not ELF (as on macOS).
	if err := os.WriteFile(self, []byte("host executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	// guestBinary resolves symlinks, including macOS's /var -> /private/var.
	self, err := filepath.EvalSymlinks(self)
	if err != nil {
		t.Fatal(err)
	}
	dir = filepath.Dir(self)
	writeGuestELF(t, filepath.Join(dir, "yoloctl-linux-amd64"), elf.EM_X86_64, false)
	writeGuestELF(t, filepath.Join(dir, "yoloctl-linux-arm64"), elf.EM_AARCH64, false)
	oldAMD64, oldARM64 := guestbinary.AMD64SHA256, guestbinary.ARM64SHA256
	t.Cleanup(func() { guestbinary.AMD64SHA256, guestbinary.ARM64SHA256 = oldAMD64, oldARM64 })
	guestbinary.AMD64SHA256, _ = sha256File(filepath.Join(dir, "yoloctl-linux-amd64"))
	guestbinary.ARM64SHA256, _ = sha256File(filepath.Join(dir, "yoloctl-linux-arm64"))
	return self
}

func TestGuestBinaryUsesVMArchitectureAndBundleDirectory(t *testing.T) {
	self := guestBundle(t)
	link := filepath.Join(t.TempDir(), "yoloctl")
	if err := os.Symlink(self, link); err != nil {
		t.Fatal(err)
	}
	for uname, arch := range map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"} {
		got, err := guestBinary(link, uname)
		want, _ := filepath.EvalSymlinks(filepath.Join(filepath.Dir(self), "yoloctl-linux-"+arch))
		if err != nil || got.path != want || got.dynamic {
			t.Fatalf("%s: got %+v, %v; want %q", uname, got, err, want)
		}
	}
}

func TestGuestBinaryRejectsUnusableArtifacts(t *testing.T) {
	for _, mode := range []string{"missing", "wrong-arch", "host-binary", "dynamic", "unsupported", "stale", "unstamped"} {
		t.Run(mode, func(t *testing.T) {
			self := guestBundle(t)
			path := filepath.Join(filepath.Dir(self), "yoloctl-linux-arm64")
			uname := "aarch64"
			switch mode {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "wrong-arch":
				writeGuestELF(t, path, elf.EM_X86_64, false)
			case "host-binary":
				if err := os.WriteFile(path, []byte("not ELF"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "dynamic":
				writeGuestELF(t, path, elf.EM_AARCH64, true)
			case "unsupported":
				uname = "riscv64"
			case "stale":
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = f.WriteString("different build, still a valid ELF")
				_ = f.Close()
			case "unstamped":
				guestbinary.ARM64SHA256 = ""
			}
			if _, err := guestBinary(self, uname); err == nil {
				t.Fatal("accepted unusable guest binary")
			}
		})
	}
}

func TestGuestBinaryStandaloneLinux(t *testing.T) {
	for _, dynamic := range []bool{false, true} {
		t.Run(fmt.Sprintf("dynamic=%v", dynamic), func(t *testing.T) {
			self := guestBundle(t)
			writeGuestELF(t, self, elf.EM_X86_64, dynamic)
			// A go build/go install executable has no bundle hashes. It must
			// select itself even with an old companion still beside it.
			guestbinary.AMD64SHA256 = ""
			got, err := guestBinary(self, "x86_64")
			if err != nil || got.path != self || got.dynamic != dynamic {
				t.Fatalf("standalone: %+v, %v", got, err)
			}
			// A make-built Linux host copied alone also works.
			guestbinary.AMD64SHA256 = "bundle hash"
			if err := os.Remove(filepath.Join(filepath.Dir(self), "yoloctl-linux-amd64")); err != nil {
				t.Fatal(err)
			}
			got, err = guestBinary(self, "x86_64")
			if err != nil || got.path != self || got.dynamic != dynamic {
				t.Fatalf("copied host: %+v, %v", got, err)
			}
			guestbinary.ARM64SHA256 = ""
			if _, err := guestBinary(self, "aarch64"); err == nil {
				t.Fatal("copied a Linux executable of the wrong architecture")
			}
		})
	}
}

func TestStampedLinuxHostRejectsMixedBundle(t *testing.T) {
	self := guestBundle(t)
	writeGuestELF(t, self, elf.EM_X86_64, false)
	guestbinary.AMD64SHA256 = strings.Repeat("0", 64)
	if _, err := guestBinary(self, "x86_64"); err == nil || !strings.Contains(err.Error(), "does not match this host build") {
		t.Fatalf("mixed bundle silently fell back to self: %v", err)
	}
}

func TestEnsureVMBinaryTransfer(t *testing.T) {
	for _, mode := range []string{"copy", "cached", "copy-failed", "install-failed", "mkdir-failed", "dynamic", "check-failed", "response-failed"} {
		t.Run(mode, func(t *testing.T) {
			self := guestBundle(t)
			dir := filepath.Dir(self)
			artifact := filepath.Join(dir, "yoloctl-linux-arm64")
			if mode == "dynamic" || mode == "check-failed" || mode == "response-failed" {
				writeGuestELF(t, self, elf.EM_AARCH64, true)
				guestbinary.ARM64SHA256 = ""
				artifact = self
			}
			sum, err := sha256File(artifact)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("TEST_SUM", sum)
			t.Setenv("TEST_MODE", mode)
			t.Setenv("TEST_LOG", filepath.Join(dir, "calls"))
			t.Setenv("TEST_STAGE", filepath.Join(dir, "stage"))
			t.Setenv("TEST_ARTIFACT", artifact)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$TEST_LOG"
if [ "$1" = copy ]; then
    [ "$2" = "$TEST_ARTIFACT" ]
    [ "$3" = "yolo-binary-test:$(cat "$TEST_STAGE")/yoloctl" ]
    [ "$TEST_MODE" != copy-failed ]
    exit
fi
[ "$1" = shell ] && [ "$2" = yolo-binary-test ] && [ "$3" = -- ]
shift 3
case "$1" in
sh)
    case "$4" in
    yoloctl-probe)
        printf 'aarch64\n'
        if [ "$TEST_MODE" = cached ]; then printf '%s  binary\n' "$TEST_SUM"; fi;;
    yoloctl-check)
        [ "$TEST_MODE" != check-failed ]
        if [ "$TEST_MODE" = response-failed ]; then echo wrong; else echo yoloctl-session-binary-ok; fi;;
    *) exit 13;;
    esac;;
mkdir)
    [ "$TEST_MODE" != mkdir-failed ]
    [ "$2" = -m ] && [ "$3" = 0700 ]
    case "$4" in /tmp/yoloctl-session.*) ;; *) exit 14;; esac
    printf '%s' "$4" > "$TEST_STAGE"
    echo 'unexpected stdout is not interpreted as a path';;
rm|rmdir) exit 0;;
sudo)
    case "$2" in
    sh) [ "$TEST_MODE" != install-failed ];;
    *) exit 11;;
    esac;;
*) exit 12;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "limactl"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			err = ensureVMBinaryFrom(t.Context(), container.New("binary-test"), self)
			wantErr := strings.HasSuffix(mode, "failed")
			if (err != nil) != wantErr {
				t.Fatalf("got %v, want error=%v", err, wantErr)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "cached":
				if bytes.Contains(calls, []byte("copy ")) || bytes.Contains(calls, []byte("mkdir")) {
					t.Fatal("transferred cached artifact")
				}
			case "mkdir-failed":
				if bytes.Contains(calls, []byte("rmdir")) || bytes.Contains(calls, []byte("copy ")) {
					t.Fatal("touched a staging directory we did not create")
				}
			default:
				stage, _ := os.ReadFile(filepath.Join(dir, "stage"))
				if !bytes.Contains(calls, []byte("rm -f "+string(stage)+"/yoloctl")) || !bytes.Contains(calls, []byte("rmdir "+string(stage))) {
					t.Fatalf("transfer did not clean staging: %s", calls)
				}
			}
			if mode == "check-failed" || mode == "response-failed" {
				if bytes.Contains(calls, []byte("yoloctl-install")) {
					t.Fatal("published an executable that failed the guest loader check")
				}
			}
			if strings.Count(string(calls), "yoloctl-probe") != 1 {
				t.Fatal("expected one combined architecture/hash probe")
			}
		})
	}
}

// Exercise the actual install script on local temporary files. Failed checksum
// verification must leave the old executable intact; success changes its inode.
func TestInstallVMBinaryAtomicReplacement(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum required")
	}
	dir := t.TempDir()
	source, dest := filepath.Join(dir, "source"), filepath.Join(dir, "yoloctl")
	for path, content := range map[string]string{source: "new binary", dest: "old binary"} {
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old, err := os.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	sum, err := sha256File(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, digest := range []string{strings.Repeat("0", 64), sum} {
		cmd := exec.Command("sh", "-c", installVMBinaryScript, "test-install", source, dest, digest)
		out, err := cmd.CombinedOutput()
		if (err == nil) != (digest == sum) {
			t.Fatalf("install: %v: %s", err, out)
		}
		data, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		want := "old binary"
		if digest == sum {
			want = "new binary"
		}
		if string(data) != want {
			t.Fatalf("destination = %q, want %q", data, want)
		}
	}
	data := make([]byte, 10)
	if _, err := old.Read(data); err != nil || string(data) != "old binary" {
		t.Fatalf("old inode changed: %q, %v", data, err)
	}
	leftovers, _ := filepath.Glob(dest + ".*")
	if len(leftovers) != 0 {
		t.Fatalf("staging files left: %v", leftovers)
	}
}

func TestCheckVMBinaryVerifiesBeforeExecution(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum required")
	}
	dir := t.TempDir()
	path, marker := filepath.Join(dir, "candidate"), filepath.Join(dir, "executed")
	// Use a script as the candidate so this tests the actual transfer probe
	// without assuming the development host's OS or C library matches a VM.
	script := "#!/bin/sh\n[ \"$1\" = --session-binary-check ] || exit 2\nprintf yoloctl-session-binary-ok\ntouch \"$TEST_MARKER\"\n"
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_MARKER", marker)
	sum, err := sha256File(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, digest := range []string{strings.Repeat("0", 64), sum} {
		cmd := exec.Command("sh", "-c", checkVMBinaryScript, "check", path, digest, BinaryCheckArgument)
		out, err := cmd.CombinedOutput()
		if digest != sum {
			if err == nil {
				t.Fatal("accepted a changed transfer")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("executed the candidate before checking its checksum")
			}
		} else if err != nil || string(out) != BinaryCheckResponse {
			t.Fatalf("startup probe: %v: %s", err, out)
		}
	}
}
