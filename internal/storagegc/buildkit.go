package storagegc

import (
	"bytes"
	"fmt"
	"reflect"
	"regexp"
	"strconv"

	"github.com/pelletier/go-toml/v2"
)

// BuildkitConfig preserves unrelated Lima/administrator settings. GC settings
// for the system containerd worker are yoloctl-owned. The original bytes are
// retained on a no-op so restarts do not continually rewrite configuration.
func BuildkitConfig(raw []byte, version string, disk Disk) ([]byte, error) {
	match := regexp.MustCompile(`\bv(\d+)\.(\d+)\.`).FindStringSubmatch(version)
	if len(match) == 0 {
		return nil, fmt.Errorf("cannot identify BuildKit version %q", version)
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	if major == 0 && minor < 17 {
		return nil, fmt.Errorf("automatic storage GC requires BuildKit 0.17 or newer (found %s); update the VM runtime", version)
	}
	var config map[string]any
	if err := toml.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("parse BuildKit configuration: %w", err)
	}
	workers, _ := config["worker"].(map[string]any)
	worker, _ := workers["containerd"].(map[string]any)
	if worker == nil || worker["enabled"] != true || worker["namespace"] != "default" {
		return nil, fmt.Errorf("expected an enabled containerd BuildKit worker in namespace default")
	}
	before, err := toml.Marshal(config)
	if err != nil {
		return nil, err
	}
	worker["gc"] = true
	delete(worker, "gckeepstorage")
	// Explicit policy avoids inherited age floors retaining large recent cache.
	// All permits eviction of cache shared with images; it does not remove
	// containerd image/container references or active build records.
	worker["gcpolicy"] = []map[string]any{{
		"all": true, "reservedSpace": int64(min(256*MiB, disk.CacheBudget())),
		"maxUsedSpace": int64(disk.CacheBudget()), "minFreeSpace": int64(disk.TargetFree()),
	}}
	for _, key := range []string{"reservedSpace", "maxUsedSpace", "minFreeSpace"} {
		delete(worker, key) // explicit gcpolicy owns these values
	}
	after, err := toml.Marshal(config)
	if err != nil {
		return nil, err
	}
	// A second decode normalizes []map versus []any and TOML integer types.
	var old, next map[string]any
	if err := toml.Unmarshal(before, &old); err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(after, &next); err != nil {
		return nil, err
	}
	if reflect.DeepEqual(old, next) {
		return bytes.Clone(raw), nil
	}
	return after, nil
}
