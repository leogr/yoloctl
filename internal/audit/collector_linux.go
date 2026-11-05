package audit

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/safepath"
)

func ProbeSupport() error {
	info, err := os.Stat("/sys/kernel/btf/vmlinux")
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("the running VM lacks kernel BTF required for modern eBPF; explicitly recreate with a compatible VM image")
	}
	return nil
}

// Collect runs only as fixed VM-root infrastructure, outside box namespaces.
// The short real capture must exit successfully before a loaded health result.
func Collect(parent context.Context, root, run, tools string, service ServiceConfig) (result error) {
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	health := Health{Epoch: service.Epoch, State: "starting", Version: service.Version, RulesSHA256: service.RulesSHA256}
	save := func() error {
		raw, err := json.Marshal(health)
		if err != nil {
			return err
		}
		return safepath.WriteFile(run, "health.json", raw, 0600)
	}
	defer func() {
		if result != nil && ctx.Err() == nil {
			health.State, health.Error = "failed", "Falco validation, modern eBPF load or collection failed; a compatible VM kernel is required"
		} else {
			health.State = "inactive"
		}
		if err := save(); result == nil {
			result = err
		}
	}()
	if err := save(); err != nil {
		return err
	}
	if service.Version != Version || service.RulesSHA256 != RulesDigest() {
		return errors.New("audit service pins differ from this controller build; explicitly enable to update")
	}
	if err := ProbeSupport(); err != nil {
		return err
	}
	binary := filepath.Join(tools, "bin", "falco")
	args := []string{"-c", filepath.Join(root, "falco.json")}
	validate, cancelValidate := context.WithTimeout(ctx, 15*time.Second)
	err := falcoCommand(validate, binary, append(append([]string{}, args...), "-V", filepath.Join(root, "rules.yaml"))...).Run()
	cancelValidate()
	if err != nil {
		return err
	}
	probe, cancelProbe := context.WithTimeout(ctx, 15*time.Second)
	err = falcoCommand(probe, binary, append(append([]string{}, args...), "-M", "2")...).Run()
	cancelProbe()
	if err != nil {
		return err
	}
	writer, err := OpenWriter(Store{Root: filepath.Join(root, "logs")})
	if err != nil {
		return err
	}
	defer func() {
		if err := writer.Close(); result == nil {
			result = err
		}
	}()
	cmd := falcoCommand(ctx, binary, args...)
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	health.State, health.Loaded = "running", true
	if err := save(); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}

	readerCtx, stopReader := context.WithCancel(ctx)
	defer stopReader()
	input := make(chan collectorInput, 32)
	readerDone := make(chan struct{})
	go func() { defer close(readerDone); readCollector(readerCtx, output, input) }()
	waited := false
	defer func() {
		stopReader()
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		_ = output.Close()
		<-readerDone
	}()
	expiry := time.NewTicker(time.Minute)
	defer expiry.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			waited = true
			return nil
		case <-expiry.C:
			if err := writer.Prune(ctx); err != nil {
				return err
			}
		case item := <-input:
			if item.err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				waited = true
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("falco stopped while auditing remained enabled")
			}
			if item.bad {
				if err := writer.Dropped(); err != nil {
					return err
				}
				continue
			}
			if item.metrics {
				if item.drops != nil {
					health.DriverDrops = item.drops
					if err := save(); err != nil {
						return err
					}
				}
				continue
			}
			if err := writer.Append(item.event); err != nil {
				return err
			}
		}
	}
}

type collectorInput struct {
	event        Event
	err          error
	bad, metrics bool
	drops        *uint64
}

func readCollector(ctx context.Context, output io.Reader, out chan<- collectorInput) {
	reader := bufio.NewReaderSize(output, MaxEvent+1)
	send := func(v collectorInput) bool {
		select {
		case out <- v:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for {
		line, err := reader.ReadSlice('\n')
		oversized := errors.Is(err, bufio.ErrBufferFull)
		if oversized {
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = reader.ReadSlice('\n')
			}
		}
		if len(line) != 0 {
			event, parseErr := ParseEvent(line)
			item := collectorInput{event: event, bad: oversized || parseErr != nil}
			if !item.bad && event.Rule == "Falco internal: metrics snapshot" {
				item.metrics = true
				var metric struct {
					Fields map[string]json.RawMessage `json:"output_fields"`
				}
				if json.Unmarshal(line, &metric) == nil {
					for _, key := range []string{"scap.n_drops", "n_drops"} {
						var drops uint64
						if json.Unmarshal(metric.Fields[key], &drops) == nil {
							item.drops = &drops
							break
						}
					}
				}
			}
			if !send(item) {
				return
			}
		}
		if err != nil {
			send(collectorInput{err: err})
			return
		}
	}
}

func falcoCommand(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 2 * time.Second
	return cmd
}
