// Package installmigration binds each optional migration to its consent snapshot.
package installmigration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/leogr/yoloctl/internal/strictyaml"
)

const SkipStatus = 76

var ErrChanged = errors.New("state changed, skipped")

type Snapshot struct {
	Space      string `json:"space" yaml:"space"`
	Bundle     string `json:"bundle" yaml:"bundle"`
	State      string `json:"state" yaml:"state"`
	DiskID     string `json:"disk_id" yaml:"disk_id"`
	DiskUUID   string `json:"disk_uuid" yaml:"disk_uuid"`
	Generation string `json:"generation" yaml:"generation"`
	Bootstrap  string `json:"bootstrap" yaml:"bootstrap"`
}

func (s Snapshot) Validate() error {
	if s.Space == "" || s.Bundle == "" || s.DiskID == "" || s.DiskUUID == "" || s.Generation == "" || len(s.Bootstrap) != 64 || (s.State != "Running" && s.State != "Stopped") {
		return errors.New("incomplete consent snapshot")
	}
	return nil
}
func (s Snapshot) Encode() string {
	raw, _ := json.Marshal(s)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func Decode(value string) (Snapshot, error) {
	var s Snapshot
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) > 8192 {
		return s, errors.New("invalid consent precondition")
	}
	if !json.Valid(raw) {
		return s, errors.New("invalid consent JSON")
	}
	if err := strictyaml.Decode(raw, &s); err != nil {
		return s, err
	}
	return s, s.Validate()
}

type Receipt struct {
	Before Snapshot `json:"before" yaml:"before"`
	After  Snapshot `json:"after" yaml:"after"`
}

type Command struct {
	Args     []string
	Warning  string
	Recreate bool
}
type Proposal struct {
	Snapshot Snapshot
	Commands []Command
	Note     string
}

type Execution struct {
	TargetBundle string
	Interactive  bool
	Yes          bool // accepts installation upgrades only, never migrations
	Confirm      func(string) (bool, error)
	Observe      func(context.Context, string) (Snapshot, error)
	Run          func(context.Context, Snapshot, Command) (Receipt, error)
	Out          io.Writer
}

// Execute rechecks each consent, retains ordering and stops on the first failed
// command. A skip abandons that space's remaining commands and visits the next.
func Execute(ctx context.Context, proposals []Proposal, e Execution) error {
	if !e.Interactive || e.Yes {
		return nil
	}
	for _, proposal := range proposals {
		expected := proposal.Snapshot
		for _, command := range proposal.Commands {
			if err := ctx.Err(); err != nil {
				return err // interrupted: no further prompt or command
			}
			ok, err := e.Confirm(fmt.Sprintf("%s: %s Execute this command? [y/N]: ", expected.Space, command.Warning))
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr // interrupted at the prompt: never a decline or success
			}
			if err != nil {
				return err
			}
			if !ok {
				break
			}
			current, err := e.Observe(ctx, expected.Space)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr // interrupted while observing: no "skipped" line
			}
			if err != nil {
				_, _ = fmt.Fprintf(e.Out, "%s: state unavailable (%v), skipped\n", expected.Space, err)
				break
			}
			if current != expected {
				_, _ = fmt.Fprintf(e.Out, "%s: state changed, skipped\n", expected.Space)
				break
			}
			receipt, err := e.Run(ctx, expected, command)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("migration interrupted for %s; remaining commands were not run (nothing retried): %w", expected.Space, errors.Join(ctxErr, err))
			}
			if errors.Is(err, ErrChanged) {
				_, _ = fmt.Fprintf(e.Out, "%s: state changed, skipped\n", expected.Space)
				break
			}
			if err != nil {
				return fmt.Errorf("migration failed for %s; remaining commands were not run (nothing retried): %w", expected.Space, err)
			}
			if receipt.Before != expected || receipt.After.Validate() != nil || receipt.After.Space != expected.Space || receipt.After.DiskID != expected.DiskID || receipt.After.DiskUUID != expected.DiskUUID || receipt.After.State != "Running" || receipt.After.Bundle != e.TargetBundle {
				return errors.New("migration result unavailable or inconsistent; remaining commands were not run")
			}
			expected = receipt.After
		}
	}
	return nil
}
