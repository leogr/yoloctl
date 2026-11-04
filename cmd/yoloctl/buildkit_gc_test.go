package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/resources"
)

func TestConfigureBuildkitRestartsOnlyForChangeAndRollsBack(t *testing.T) {
	for _, failure := range []string{"", "reload", "slice", "pool", "restart", "probe"} {
		t.Run("failure="+failure, func(t *testing.T) {
			dir := t.TempDir()
			units := t.TempDir()
			path := filepath.Join(dir, "buildkitd.toml")
			raw := []byte("# admin comment\n[worker.containerd]\nenabled=true\nnamespace='default'\n")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			restarts := 0
			reloads := 0
			run := func(ctx context.Context, args ...string) ([]byte, error) {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded service command")
				}
				switch args[0] {
				case "buildkitd":
					return []byte("buildkitd v0.31.2"), nil
				case "cat":
					if failure == "pool" {
						return []byte("max\nmax\n0\n"), nil
					}
					b, _ := resources.Workloads(4 * resources.GiB)
					return []byte(fmt.Sprintf("%d\nmax\n0\nMemTotal: 4194304 kB\nMemAvailable: 100 kB\n", b.Max)), nil
				case "systemctl":
					if args[1] == "daemon-reload" {
						reloads++
						if failure == "reload" && reloads == 1 {
							return nil, errors.New("reload failed")
						}
						return nil, nil
					}
					if args[1] == "start" {
						if args[2] != resources.WorkloadSlice {
							t.Fatal(args)
						}
						if failure == "slice" {
							return nil, errors.New("slice failed")
						}
						return nil, nil
					}
					restarts++
					current, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if reloads == 1 && string(current) == string(raw) {
						t.Fatal("restart before policy publication")
					}
					if failure != "" && reloads == 2 && string(current) != string(raw) {
						t.Fatal("rollback restart before original configuration was restored")
					}
					if reloads == 2 && failure != "" {
						for _, name := range []string{resources.WorkloadSlice, "buildkit.service.d/50-yoloctl-workloads.conf"} {
							if _, err := os.Stat(filepath.Join(units, name)); !errors.Is(err, os.ErrNotExist) {
								t.Fatal("rollback restart before unit restoration", name, err)
							}
						}
					}
					if failure == "restart" && restarts == 1 {
						return nil, errors.New("restart failed")
					}
				case "buildctl":
					if _, err := os.Stat(filepath.Join(dir, ".yoloctl-gc-applied")); restarts == 1 && !errors.Is(err, os.ErrNotExist) {
						t.Fatal("activation marked complete before worker probe")
					}
					if failure == "probe" && restarts == 1 {
						return nil, errors.New("worker unavailable")
					}
					return []byte("ID\nworker1"), nil
				default:
					t.Fatal(args)
				}
				return nil, nil
			}
			err := configureBuildkitAt(t.Context(), dir, units, 4*resources.GiB, run)
			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if failure != "" {
				wantRestarts := 2
				if failure == "reload" || failure == "slice" || failure == "pool" {
					wantRestarts = 1
				}
				if err == nil || string(got) != string(raw) || restarts != wantRestarts {
					t.Fatalf("rollback failed: %v %d %s", err, restarts, got)
				}
				if _, err := os.Stat(filepath.Join(dir, ".yoloctl-gc-applied")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed activation left an applied marker")
				}
			} else {
				if err != nil || restarts != 1 || !strings.Contains(string(got), "maxUsedSpace") {
					t.Fatalf("install: %v %d %s", err, restarts, got)
				}
				marker, err := os.ReadFile(filepath.Join(dir, ".yoloctl-gc-applied"))
				if err != nil || len(marker) != 64 {
					t.Fatalf("activation marker does not identify probed configuration: %q %v", marker, err)
				}
				if err := configureBuildkitAt(t.Context(), dir, units, 4*resources.GiB, run); err != nil || restarts != 1 {
					t.Fatalf("restarted unchanged config: %v %d", err, restarts)
				}
				// Simulate interruption after config publication but before the
				// daemon restart/health acknowledgment was durably recorded.
				if err := os.Remove(filepath.Join(dir, ".yoloctl-gc-applied")); err != nil {
					t.Fatal(err)
				}
				if err := configureBuildkitAt(t.Context(), dir, units, 4*resources.GiB, run); err != nil || restarts != 2 {
					t.Fatalf("did not resume policy activation: %v %d", err, restarts)
				}
			}
		})
	}
}
