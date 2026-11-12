package spacearchive

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
)

// Only selected immutable source is backed up. Tools, logs, readiness, service
// epochs, attempts and absolute generated configurations are never serialized.
func writeAuditRuleState(ctx context.Context, tw *tar.Writer, disk string, settings *spaceconfig.AuditSettings) error {
	if settings == nil || settings.RulesGeneration == "" {
		return nil
	}
	store := audit.RuleStore{Root: layout.AuditDiskDir(disk)}
	set, err := store.Read(settings.RulesGeneration)
	if err != nil {
		return err
	}
	if set.Fingerprint != settings.EffectiveSHA256 {
		return errors.New("selected audit rule fingerprint differs from native settings")
	}
	raw, err := json.Marshal(set)
	if err != nil {
		return err
	}
	write := func(name string, raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: path.Join("audit-rules", name), Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(raw))}); err != nil {
			return err
		}
		_, err := tw.Write(raw)
		return err
	}
	if err := write("manifest.json", raw); err != nil {
		return err
	}
	for _, file := range set.Files {
		raw, err := store.FileBytes(set, file)
		if err != nil {
			return err
		}
		if err := write(auditArchiveFile(file), raw); err != nil {
			return err
		}
	}
	return nil
}

func auditArchiveFile(file audit.RuleFile) string {
	if file.Builtin {
		return "rules.yaml"
	}
	return file.SHA256 + ".yaml"
}

type auditArchiveReader struct {
	selected *spaceconfig.AuditSettings
	set      *audit.RuleSet
	seen     map[string]bool
	expected map[string]audit.RuleFile
}

func (reader *auditArchiveReader) consume(h *tar.Header, in io.Reader) ([]byte, error) {
	if reader.selected == nil || reader.selected.RulesGeneration == "" || reader.selected.Enabled || h.Name != path.Clean(h.Name) || h.Size < 0 || h.Size > MaxState || h.Typeflag != tar.TypeReg || h.Mode != 0600 || h.Linkname != "" {
		return nil, errors.New("audit rule archive entry is undeclared, unsafe or not disabled")
	}
	name := path.Base(h.Name)
	if h.Name != path.Join("audit-rules", name) {
		return nil, errors.New("audit rule archive path escapes its inventory")
	}
	if name == "manifest.json" {
		if reader.set != nil {
			return nil, errors.New("duplicate audit rule manifest")
		}
		raw, err := io.ReadAll(in)
		if err != nil {
			return nil, err
		}
		set, err := audit.DecodeRuleSet(raw)
		if err != nil {
			return nil, err
		}
		if set.Generation != reader.selected.RulesGeneration || set.Fingerprint != reader.selected.EffectiveSHA256 {
			return nil, errors.New("audit rule manifest differs from declaration")
		}
		reader.set = &set
		reader.seen = map[string]bool{}
		reader.expected = make(map[string]audit.RuleFile, len(set.Files))
		for _, file := range set.Files {
			reader.expected[auditArchiveFile(file)] = file
		}
		return raw, nil
	}
	if reader.set == nil {
		return nil, errors.New("audit rule manifest must precede files")
	}
	if h.Size > audit.MaxRuleFile {
		return nil, errors.New("one audit rule file must not exceed 2 MiB")
	}
	file, ok := reader.expected[name]
	if !ok {
		return nil, errors.New("audit rule archive contains an unlisted file")
	}
	raw, err := io.ReadAll(in)
	if err != nil {
		return nil, err
	}
	if audit.Digest(raw) != file.SHA256 {
		return nil, errors.New("audit rule archive checksum mismatch")
	}
	reader.seen[name] = true
	return raw, nil
}

func (reader *auditArchiveReader) complete() error {
	if reader.selected == nil || reader.selected.RulesGeneration == "" {
		return nil
	}
	if reader.set == nil {
		return errors.New("selected audit rules are missing from archive")
	}
	for _, file := range reader.set.Files {
		if !reader.seen[auditArchiveFile(file)] {
			return errors.New("audit rule archive lacks a selected file")
		}
	}
	return nil
}

func restoreAuditRuleState(ctx context.Context, stage, disk string, settings *spaceconfig.AuditSettings) error {
	if settings == nil || settings.RulesGeneration == "" {
		return nil
	}
	raw, err := safepath.ReadFile(stage, filepath.Join("audit-rules", "manifest.json"), MaxState)
	if err != nil {
		return err
	}
	set, err := audit.DecodeRuleSet(raw)
	if err != nil {
		return err
	}
	return (audit.RuleStore{Root: layout.AuditDiskDir(disk)}).Restore(ctx, set, func(file audit.RuleFile) ([]byte, error) {
		return safepath.ReadFile(stage, filepath.Join("audit-rules", auditArchiveFile(file)), audit.MaxRuleFile)
	})
}
