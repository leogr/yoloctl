package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/storagegc"
	"gopkg.in/yaml.v3"
)

func TestStorageReserveNativeTransaction(t *testing.T) {
	if os.Getenv("YOLOCTL_TEST_RESERVE_SET") == "" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestStorageReserveNativeTransaction$")
		cmd.Env = append(os.Environ(), "YOLOCTL_TEST_RESERVE_SET=1")
		if raw, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		return
	}
	disk := t.TempDir()
	c := spaceenv.Context{Schema: 1, Space: "test", DiskID: strings.Repeat("a", 32), DiskUUID: "fixture", DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(disk, "host"), UID: 1000, GID: 1000, Bundle: "fixture", Generation: "fixture", Mounts: []spaceenv.Mount{{Source: "/projects", Writable: true}}}
	if err := spaceenv.Configure(c); err != nil {
		t.Fatal(err)
	}
	settings := spaceconfig.Settings{SchemaVersion: 1, Name: "test", DefaultAccount: "personal", DefaultWorkdir: "/projects"}
	raw, err := yaml.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := safepath.WriteFile(c.ConfigDir, "space.yaml", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if observed := readBuildReserve("test"); observed.Err != nil || observed.Setting != nil || observed.Reserve.Cap == nil || *observed.Reserve.Cap != 10*testGiB {
		t.Fatal("existing omitted setting did not use default", observed)
	}
	held := false
	storageSetAcquire = func(_ context.Context, space string, exclusive bool) (func(), error) {
		if space != "test" || !exclusive {
			t.Fatal("not the native space mutation guard", space, exclusive)
		}
		held = true
		return func() { held = false }, nil
	}
	storageSetDisk = func() (storagegc.Disk, error) {
		return storagegc.Disk{Total: 145 * testGiB, Available: 15 * testGiB}, nil
	}
	req := controller.Request{ID: "reserve-setting-fixture-012345", Method: spaceStorageSetMethod, Space: "test", Dash: -1, Payload: []byte(`{"max":"0GiB"}`)}
	denied := controller.WithEffectGate(t.Context(), func(context.Context, controller.EffectIntent) error {
		if !held {
			t.Fatal("effect before lock")
		}
		return errors.New("checkpoint denied")
	})
	if handled, err := runControllerStorageSet(denied, req, &bytes.Buffer{}); !handled || err == nil {
		t.Fatal(handled, err)
	}
	unchanged, err := os.ReadFile(filepath.Join(c.ConfigDir, "space.yaml"))
	if err != nil || !bytes.Equal(raw, unchanged) {
		t.Fatal("changed before checkpoint", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runControllerStorageSet(cancelled, req, &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled setter did not preserve cancellation", err)
	}
	operator := controller.Principal{Box: "worker", Incarnation: "run"}
	if err := controller.Authorize(operator, req, "test", controllerOperations); err == nil {
		t.Fatal("operator authorized for native setting")
	}
	if handled, err := runControllerStorageSet(withControllerPrincipal(t.Context(), operator), req, &bytes.Buffer{}); !handled || err == nil {
		t.Fatal("direct operator bypass", handled, err)
	}
	for _, value := range []string{"0GiB", "none", "12GiB"} {
		req.Payload = []byte(`{"max":"` + value + `"}`)
		ctx := controller.WithEffectGate(t.Context(), func(context.Context, controller.EffectIntent) error {
			if !held {
				t.Fatal("effect before lock")
			}
			return nil
		})
		var out bytes.Buffer
		if handled, err := runControllerStorageSet(ctx, req, &out); !handled || err != nil || held {
			t.Fatal(handled, err, held)
		}
		cfg, err := spaceconfig.Load("test")
		if err != nil || cfg.Storage == nil || cfg.Storage.BuildReserveMax == nil || *cfg.Storage.BuildReserveMax != value {
			t.Fatal(cfg, err)
		}
		if cfg.DefaultAccount != "personal" || cfg.DefaultWorkdir != "/projects" || cfg.AccountDiskID != c.DiskID {
			t.Fatal("setting changed unrelated native state", cfg)
		}
		var result storageReserveSetResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.BuildReserve == nil {
			t.Fatal(out.String(), err)
		}
		reserve, err := cfg.Storage.BuildReserve()
		if err != nil || result.BuildReserve.Required != reserve.AdmissionFree(storagegc.Disk{Total: 145 * testGiB}) {
			t.Fatal(result, err)
		}
		observed := readBuildReserve("test")
		if observed.Err != nil || observed.Setting == nil || *observed.Setting != value || observed.Reserve.AdmissionFree(storagegc.Disk{Total: 145 * testGiB}) != result.BuildReserve.Required {
			t.Fatal("next preflight did not observe saved setting", observed)
		}
	}
}

func TestStorageReserveSetRejectsInvalidValuesBeforeHostContact(t *testing.T) {
	for _, v := range []string{"500MiB", "1.5GiB", "garbage", "17179869184GiB"} {
		cmd := newSpaceStorageSetCmd()
		cmd.SetArgs([]string{"--build-reserve-max", v})
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "build reserve") {
			t.Fatalf("value=%s err=%v", v, err)
		}
	}
}
