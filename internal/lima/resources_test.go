package lima

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/leogr/yoloctl/internal/vmapi"
)

// resourceClient is a Lima client whose limactl prints the given list and
// records every invocation.
func resourceClient(t *testing.T, status, vmType string) (*Client, string, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	list := filepath.Join(bin, "list.json")
	if err := os.WriteFile(list, []byte(`{"name":"yolo-dev","status":"`+status+`","vmType":"`+vmType+`"}`+"\n"+`{"name":"foreign","status":"Stopped"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$@\" >> '" + calls + "'\n[ \"$*\" = 'list --format json' ] || exit 9\ncat '" + list + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := InstanceDir("dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "lima.yaml")
	if err := os.WriteFile(path, TemplateWithBootSettings([]Mount{{Location: "/work", Writable: true}}, "disk-id", false, 4, "4GiB", "30GiB"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Client{binary: filepath.Join(bin, "limactl"), stdout: io.Discard}, path, calls
}

func TestSetInstanceResourcesRewritesOnlyStoppedCPUsAndMemory(t *testing.T) {
	c, path, calls := resourceClient(t, "Stopped", "qemu")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetInstanceResources(t.Context(), "dev", vmapi.Resources{CPUs: 6, Memory: "12GiB"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(bytes.Replace(before, []byte("\ncpus: 4\n"), []byte("\ncpus: 6\n"), 1), []byte("\nmemory: \"4GiB\"\n"), []byte("\nmemory: \"12GiB\"\n"), 1)
	if bytes.Equal(want, before) {
		t.Fatal("fixture lacks top-level cpus/memory lines")
	}
	if !bytes.Contains(after, []byte("\ncpus: 6\n")) || !bytes.Contains(after, []byte("\nmemory: \"12GiB\"\n")) || !bytes.Contains(after, []byte("\ndisk: \"30GiB\"\n")) {
		t.Fatalf("resources not applied:\n%s", after)
	}
	if got := normalized(t, after); got != normalized(t, want) {
		t.Fatalf("lima.yaml changed beyond cpus/memory:\n%s", after)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info, err)
	}
	st, _ := os.Stat(path)
	inode := st.Sys().(*syscall.Stat_t).Ino
	if err := c.SetInstanceResources(t.Context(), "dev", vmapi.Resources{CPUs: 6, Memory: "12GiB"}); err != nil {
		t.Fatal(err)
	}
	if st, _ = os.Stat(path); st.Sys().(*syscall.Stat_t).Ino != inode {
		t.Fatal("equal resources rewrote lima.yaml")
	}
	invoked, _ := os.ReadFile(calls)
	for _, r := range []vmapi.Resources{{CPUs: 0, Memory: "4GiB"}, {CPUs: 2, Memory: "0GiB"}, {CPUs: 2, Memory: "4G"}} {
		if err := c.SetInstanceResources(t.Context(), "dev", r); err == nil || errors.Is(err, vmapi.ErrNotStopped) {
			t.Fatalf("%+v: %v", r, err)
		}
	}
	if again, _ := os.ReadFile(calls); !bytes.Equal(again, invoked) {
		t.Fatal("invalid resources reached limactl")
	}
}

// normalized compares YAML documents by their semantic content.
func normalized(t *testing.T, raw []byte) string {
	t.Helper()
	out, err := setResourcesInYAML(raw, vmapi.Resources{CPUs: 1, Memory: "1GiB"})
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestSetInstanceResourcesRefusesUnprovenStoppedState(t *testing.T) {
	for _, status := range []string{"Running", "Starting", "Broken", ""} {
		t.Run(strconv.Quote(status), func(t *testing.T) {
			c, path, _ := resourceClient(t, status, "qemu")
			before, _ := os.ReadFile(path)
			if err := c.SetInstanceResources(t.Context(), "dev", vmapi.Resources{CPUs: 2, Memory: "2GiB"}); !errors.Is(err, vmapi.ErrNotStopped) {
				t.Fatalf("status %q: %v", status, err)
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
				t.Fatal("refused change rewrote lima.yaml")
			}
		})
	}
	for _, vmType := range []string{"", "krunkit"} {
		t.Run("vm type "+strconv.Quote(vmType), func(t *testing.T) {
			c, path, _ := resourceClient(t, "Stopped", vmType)
			before, _ := os.ReadFile(path)
			if err := c.SetInstanceResources(t.Context(), "dev", vmapi.Resources{CPUs: 2, Memory: "2GiB"}); !errors.Is(err, vmapi.ErrNotStopped) {
				t.Fatalf("unverifiable VM type: %v", err)
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
				t.Fatal("refused change rewrote lima.yaml")
			}
		})
	}
	t.Run("surviving process", func(t *testing.T) {
		c, path, _ := resourceClient(t, "Stopped", "vz")
		before, _ := os.ReadFile(path)
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), "ha.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := c.SetInstanceResources(t.Context(), "dev", vmapi.Resources{CPUs: 2, Memory: "2GiB"}); !errors.Is(err, vmapi.ErrNotStopped) {
			t.Fatalf("live host agent: %v", err)
		}
		if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
			t.Fatal("refused change rewrote lima.yaml")
		}
	})
	t.Run("symlinked config", func(t *testing.T) {
		c, path, _ := resourceClient(t, "Stopped", "qemu")
		target := filepath.Join(t.TempDir(), "elsewhere.yaml")
		if err := os.Rename(path, target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(target)
		if err := c.SetInstanceResources(t.Context(), "dev", vmapi.Resources{CPUs: 2, Memory: "2GiB"}); err == nil {
			t.Fatal("followed a symlinked lima.yaml")
		}
		if after, _ := os.ReadFile(target); !bytes.Equal(before, after) {
			t.Fatal("wrote through a symlinked lima.yaml")
		}
	})
	t.Run("missing instance", func(t *testing.T) {
		c, _, _ := resourceClient(t, "Stopped", "qemu")
		if err := c.SetInstanceResources(t.Context(), "absent", vmapi.Resources{CPUs: 2, Memory: "2GiB"}); err == nil || errors.Is(err, vmapi.ErrNotStopped) || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("missing instance: %v", err)
		}
	})
}

func TestSetResourcesInYAMLAppendsMissingKeysAndKeepsComments(t *testing.T) {
	raw := []byte("# keep me\nvmType: vz\nmounts: []\n")
	out, err := setResourcesInYAML(raw, vmapi.Resources{CPUs: 3, Memory: "3GiB"})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(out); !strings.Contains(s, "# keep me") || !strings.Contains(s, "vmType: vz") || !strings.Contains(s, "cpus: 3\n") || !strings.Contains(s, "memory: \"3GiB\"\n") {
		t.Fatalf("rewrite:\n%s", out)
	}
	out, err = setResourcesInYAML([]byte("cpus: \"4\"\nmemory: 4GiB\n"), vmapi.Resources{CPUs: 4, Memory: "4GiB"})
	if err != nil || !strings.Contains(string(out), "cpus: 4\n") || strings.Contains(string(out), `"4"`) {
		t.Fatalf("quoted cpus kept as a string: %v\n%s", err, out)
	}
	for _, malformed := range []string{"cpus: [1]\n", "cpus: 4\nmemory: \"4GiB\"\ncpus: 8\n", "cpus: 4\n---\ncpus: 8\n", "", "memory: \"4GiB\"\ncpus: 3\nmemory: \"3GiB\"\n"} {
		if _, err := setResourcesInYAML([]byte(malformed), vmapi.Resources{CPUs: 3, Memory: "3GiB"}); err == nil {
			t.Fatalf("accepted malformed resources:\n%s", malformed)
		}
	}
}
