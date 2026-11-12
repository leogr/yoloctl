package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/audit"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/spf13/cobra"
)

func newAuditRulesCmd() *cobra.Command {
	rules := &cobra.Command{Use: "rules", Short: "Manage ordered native Falco rules and selection (host only)", Args: cobra.NoArgs}
	list := &cobra.Command{Use: "list", Short: "List stored rule files, hashes, dates and selection", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		target, err := auditTarget(cmd)
		if err != nil {
			return err
		}
		snapshot, err := fetchAuditRules(cmd, target)
		if err != nil {
			return err
		}
		jsonOutput, _ := cmd.Flags().GetBool("json")
		if jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(snapshot)
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), "FILE\tSHA256\tIMPORTED"); err != nil {
			return err
		}
		for _, file := range snapshot.Rules.Files {
			date := file.Imported.Format("2006-01-02T15:04:05Z07:00")
			if file.Builtin {
				date = "built-in (not removable)"
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", audit.Clean(file.Name), file.SHA256, date); err != nil {
				return err
			}
		}
		entries, err := audit.SelectionEntries(snapshot.Rules.Selection)
		if err != nil {
			return err
		}
		selection := "default"
		if entries > 0 {
			selection = fmt.Sprintf("customized (%d entries)", entries)
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Selection: %s\nFalco warnings at validation: %d (acceptance does not prove runtime health)\n", selection, snapshot.Rules.Warnings)
		return err
	}}
	list.Flags().Bool("json", false, "Print stored rule-set metadata and selection as JSON")
	imp := &cobra.Command{Use: "import FILE", Short: "Append a rule file after built-ins, validate and apply", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		target, err := auditTarget(cmd)
		if err != nil {
			return err
		}
		raw, err := safepath.ReadFile(filepath.Dir(args[0]), filepath.Base(args[0]), audit.MaxRuleFile)
		if err != nil {
			return err
		}
		snapshot, err := fetchAuditRules(cmd, target)
		if err != nil {
			return err
		}
		return changeAuditRules(cmd, target, "import", snapshot, auditRulesRequest{Name: filepath.Base(args[0])}, bytes.NewReader(raw))
	}}
	remove := &cobra.Command{Use: "remove NAME", Short: "Remove a custom file; disambiguate duplicate names by SHA256", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		target, err := auditTarget(cmd)
		if err != nil {
			return err
		}
		snapshot, err := fetchAuditRules(cmd, target)
		if err != nil {
			return err
		}
		prefix, _ := cmd.Flags().GetString("sha256")
		return changeAuditRules(cmd, target, "remove", snapshot, auditRulesRequest{Name: args[0], SHA256: prefix}, nil)
	}}
	remove.Flags().String("sha256", "", "Unique SHA256 prefix (at least eight hex digits)")
	edit := &cobra.Command{Use: "edit", Short: "Edit just Falco's rules selection with $EDITOR", Args: cobra.NoArgs, RunE: editAuditRules}
	rules.AddCommand(list, imp, remove, edit)
	return rules
}

// Content streams through the ordinary controller stdout/stdin channels. The
// request envelope contains only finite metadata; no catalog/selection cap is
// inherited from the existing bounded audit-status/history envelope.
func fetchAuditRules(cmd *cobra.Command, target controllerTarget) (auditRulesResult, error) {
	var result auditRulesResult
	var out bytes.Buffer
	if err := callAudit(cmd.Context(), target, "audit.rules.list", nil, nil, &out); err != nil {
		return result, err
	}
	decoder := json.NewDecoder(&out)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return result, errors.New("trailing audit rule response")
	}
	return result, nil
}

var fetchAuditRuleTools = audit.Fetch

func changeAuditRules(cmd *cobra.Command, target controllerTarget, kind string, snapshot auditRulesResult, change auditRulesRequest, body io.Reader) error {
	cache, err := layout.AuditCacheDir()
	if err != nil {
		return err
	}
	archive, err := fetchAuditRuleTools(cmd.Context(), cache, snapshot.Architecture)
	if err != nil {
		return err
	}
	pin, err := audit.Pinned(snapshot.Architecture)
	if err != nil {
		return err
	}
	change.Expected = snapshot.Revision
	input := &auditRequest{ArchiveSHA256: pin.SHA256, Rules: &change}
	if body == nil {
		body = bytes.NewReader(nil)
	}
	var out bytes.Buffer
	callErr := callAudit(cmd.Context(), target, "audit.rules."+kind, input, io.MultiReader(bytes.NewReader(archive), body), &out)
	var response auditRulesResult
	if out.Len() > 0 {
		decoder := json.NewDecoder(&out)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&response); err != nil {
			return errors.Join(callErr, err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return errors.Join(callErr, errors.New("trailing audit rule result"))
		}
		if response.Validation.Diagnostics != "" {
			if _, err := fmt.Fprintln(cmd.ErrOrStderr(), audit.DiagnosticText(response.Validation.Diagnostics)); err != nil {
				return errors.Join(callErr, err)
			}
		}
	}
	if callErr != nil {
		return callErr
	}
	if !response.Changed {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "no changes")
		return err
	}
	state := "stored; audit remains disabled"
	if response.Status != nil && response.Status.Desired.Enabled {
		state = "applied; collector restarted"
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Rules %s: %s (%d Falco warnings)\n", kind, state, response.Rules.Warnings)
	return err
}

const auditSelectionHelp = `# Edit only rules: (native Falco syntax, ordered operations).
# Example: - disable: {rule: 'Yolo *'}
# Example: - enable: {tag: yolo_escape_candidate}
# Wildcard selection can enable shipped-disabled, unqualified rules and can
# disable built-ins. More events can increase existing drops/retention pressure.
# Blank/comment-only input resets selection to the empty default.
`

var auditRulesEditor = runEditor

func editAuditRules(cmd *cobra.Command, _ []string) error {
	target, err := auditTarget(cmd)
	if err != nil {
		return err
	}
	snapshot, err := fetchAuditRules(cmd, target)
	if err != nil {
		return err
	}
	initial := append([]byte(auditSelectionHelp), snapshot.Rules.Selection...)
	tmp, err := os.CreateTemp("", "yoloctl-audit-rules-*.yaml")
	if err != nil {
		return err
	}
	path := tmp.Name()
	retain := false
	defer func() {
		if !retain {
			_ = os.Remove(path)
		}
	}()
	if _, err := tmp.Write(initial); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// The human editor runs without any native mutation lock.
	if err := auditRulesEditor(cmd.Context(), path); err != nil {
		return err
	}
	edited, err := safepath.ReadFile(filepath.Dir(path), filepath.Base(path), math.MaxInt64-1)
	if err != nil {
		return err
	}
	if bytes.Equal(edited, initial) {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "no changes")
		return err
	}
	if _, err := audit.SelectionDocument(edited); err != nil {
		retain = true
		return fmt.Errorf("edits kept at %s: %w", path, err)
	}
	if err := changeAuditRules(cmd, target, "edit", snapshot, auditRulesRequest{}, bytes.NewReader(edited)); err != nil {
		retain = true
		return fmt.Errorf("edits kept at %s: %w", path, err)
	}
	return nil
}
