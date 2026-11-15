package spaceconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/leogr/yoloctl/internal/strictyaml"
)

func TestStorageReserveSettingsRoundTripAndStayOffBootstrap(t *testing.T) {
	for _, value := range []*string{nil, reserveString("none"), reserveString("0GiB"), reserveString("12GiB")} {
		cfg, err := New("work", "/projects", "personal")
		if err != nil {
			t.Fatal(err)
		}
		if value != nil {
			cfg.Storage = &StorageSettings{BuildReserveMax: value}
		}
		cfg.AccountDiskUUID = "fixture"
		cfg.path = filepath.Join(t.TempDir(), "space.yaml")
		if err := cfg.Save(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(cfg.path)
		if err != nil {
			t.Fatal(err)
		}
		var loaded Config
		if err := strictyaml.Decode(raw, &loaded); err != nil {
			t.Fatal(err)
		}
		if err := loaded.Validate(); err != nil {
			t.Fatal(err)
		}
		boot, settings, err := Split(&loaded, 1000, 1000, "generation")
		if err != nil {
			t.Fatal(err)
		}
		bootRaw, err := strictyaml.Marshal(boot)
		if err != nil || bytes.Contains(bootRaw, []byte("storage")) {
			t.Fatal(string(bootRaw), err)
		}
		settingsRaw, err := strictyaml.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		var parsed Settings
		if err := strictyaml.Decode(settingsRaw, &parsed); err != nil {
			t.Fatal(err)
		}
		composed, err := parsed.Compose(boot)
		if err != nil {
			t.Fatal(err)
		}
		if value == nil {
			if composed.Storage != nil {
				t.Fatal("omission collapsed", composed.Storage)
			}
		} else if composed.Storage == nil || composed.Storage.BuildReserveMax == nil || *composed.Storage.BuildReserveMax != *value {
			t.Fatalf("setting lost: want=%v got=%+v", value, composed.Storage)
		}
	}
}

func reserveString(s string) *string { return &s }
