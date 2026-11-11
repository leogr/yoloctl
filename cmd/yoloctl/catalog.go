package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/spf13/cobra"
)

// Catalog commands: agent / feature / capability each expose
// the SAME five verbs over a built-in (yoloctl-owned, embedded, read-only) +
// custom (user-owned, on-disk) catalog:
//
//	<kind> ls               list built-in + custom, marking which
//	<kind> show NAME        show a built-in OR custom item
//	<kind> copy SRC NEW      copy a built-in OR custom item to a NEW custom name
//	<kind> edit NAME        $EDITOR on a CUSTOM item (built-in → error)
//	<kind> destroy NAME     delete a CUSTOM item (built-in → error)
//
// Same-name overrides of built-ins are not allowed: copy/edit/destroy refuse a
// built-in NAME, and the registry loaders reject a custom file shadowing a
// built-in. The per-kind specifics (how to list/show/copy/locate/validate an
// item) are injected via catalogKind so the three command trees share one
// skeleton.

// catalogKind abstracts the per-kind operations the shared catalog commands
// need. Every function operates on plain names; the disjoint-namespace
// invariant (custom may not shadow built-in) is enforced by the loaders and the
// copy/edit/destroy guards.
type catalogKind struct {
	// name is the kind label used in command Use/help and ValidateName.
	name string
	// short is the one-line command-tree description.
	short string
	// builtinNames returns the sorted built-in names.
	builtinNames func() ([]string, error)
	// customNames returns the sorted user-defined names (custom only).
	customNames func() ([]string, error)
	// isBuiltin reports whether name is a built-in.
	isBuiltin func(name string) bool
	// exists reports whether name resolves to any item (built-in or custom).
	exists func(name string) (bool, error)
	// validateName validates a NEW custom name (charset/length).
	validateName func(name string) error
	// writeShow renders the named item (built-in or custom) for humans.
	writeShow func(w io.Writer, name string) error
	// writeShowJSON renders the named item as JSON.
	writeShowJSON func(w io.Writer, name string) error
	// copy reads src (built-in or custom) and writes a new custom item newName.
	// The guards (newName not a built-in / not already taken) run before this.
	copy func(src, newName string) error
	// customPath returns the on-disk path of a CUSTOM item (for `edit`). It is
	// only called after the built-in guard, so name is always custom.
	customPath func(name string) (string, error)
	// validateAfterEdit reloads + validates a custom item after $EDITOR exits.
	validateAfterEdit func(name string) error
	// deleteCustom removes a custom item. The built-in guard runs before this.
	deleteCustom func(name string) error
	// referrers lists what still names the custom item, as "box SPACE/NAME" or
	// "<kind> NAME". `destroy` refuses while it is non-empty, unless forced.
	referrers func(name string, stderr io.Writer, force bool) ([]string, error)
}

// newCatalogCmd builds the parent command for a catalog kind, wiring the five
// shared verbs.
func newCatalogCmd(k catalogKind) *cobra.Command {
	cmd := &cobra.Command{
		Use:   k.name,
		Short: k.short,
	}
	cmd.AddCommand(
		newCatalogLsCmd(k),
		newCatalogShowCmd(k),
		newCatalogCopyCmd(k),
		newCatalogEditCmd(k),
		newCatalogDestroyCmd(k),
	)
	return cmd
}

// catalogItem is one row of `<kind> ls`.
type catalogItem struct {
	Name   string `json:"name"`
	Source string `json:"source"` // "built-in" | "custom"
}

// collectCatalogItems merges built-in + custom names into a sorted, source-
// tagged list. Built-in and custom names are disjoint by invariant, so a name
// appears once.
func collectCatalogItems(k catalogKind) ([]catalogItem, error) {
	builtins, err := k.builtinNames()
	if err != nil {
		return nil, err
	}
	customs, err := k.customNames()
	if err != nil {
		return nil, err
	}
	items := make([]catalogItem, 0, len(builtins)+len(customs))
	for _, n := range builtins {
		items = append(items, catalogItem{Name: n, Source: "built-in"})
	}
	for _, n := range customs {
		items = append(items, catalogItem{Name: n, Source: "custom"})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func newCatalogLsCmd(k catalogKind) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   fmt.Sprintf("List %ss (built-in + custom)", k.name),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			items, err := collectCatalogItems(k)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(items)
			}
			for _, it := range items {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\t(%s)\n", it.Name, it.Source)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of the plain list")
	return cmd
}

func newCatalogShowCmd(k catalogKind) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show NAME",
		Short: fmt.Sprintf("Show a %s (built-in or custom)", k.name),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ok, err := k.exists(name)
			if err != nil {
				return err
			}
			if !ok {
				return asUserError(fmt.Errorf("unknown %s %q", k.name, name))
			}
			if asJSON {
				return k.writeShowJSON(cmd.OutOrStdout(), name)
			}
			return k.writeShow(cmd.OutOrStdout(), name)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of the human-readable view")
	return cmd
}

func newCatalogCopyCmd(k catalogKind) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "copy SRC NEWNAME",
		Short: fmt.Sprintf("Copy a built-in or custom %s to a new custom %s", k.name, k.name),
		Long: fmt.Sprintf(`Copies an existing %[1]s (built-in OR custom) to a NEW custom %[1]s.
NEWNAME must be a valid name, must NOT be a built-in name, and must not already
exist. Customize a built-in by copying it to a custom name, then editing the
copy (built-ins themselves are read-only).`, k.name),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, newName := args[0], args[1]
			if err := k.validateName(newName); err != nil {
				return asUserError(err)
			}
			// SRC must exist (built-in or custom).
			srcExists, err := k.exists(src)
			if err != nil {
				return err
			}
			if !srcExists {
				return asUserError(fmt.Errorf("unknown %s %q to copy", k.name, src))
			}
			// NEWNAME must not collide with a built-in (no same-name override)…
			if k.isBuiltin(newName) {
				return asUserError(fmt.Errorf("%q is a built-in %s; pick a different custom name", newName, k.name))
			}
			// …and must not already exist as a custom item.
			newExists, err := k.exists(newName)
			if err != nil {
				return err
			}
			if newExists {
				return asUserError(fmt.Errorf("%s %q already exists", k.name, newName))
			}
			if err := k.copy(src, newName); err != nil {
				return err
			}
			cliInfo(logging.Host, "copied %s %q to custom %s %q", k.name, src, k.name, newName)
			return nil
		},
	}
	return cmd
}

func newCatalogEditCmd(k catalogKind) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit NAME",
		Short: fmt.Sprintf("Edit a custom %s in $EDITOR (built-ins are read-only)", k.name),
		Long: fmt.Sprintf(`Opens a CUSTOM %[1]s's YAML in $EDITOR (default vi). On save the result is
reloaded and validated; an invalid edit is rejected and the file is left as you
saved it. Built-ins are read-only — copy a built-in to a custom name first
(yoloctl %[1]s copy SRC NEWNAME).`, k.name),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCatalogEdit(cmd.Context(), cmd.OutOrStdout(), k, args[0])
		},
	}
	return cmd
}

// runCatalogEdit edits a custom item in place, then reloads+validates it. A
// built-in NAME is rejected with a copy-first hint; a missing custom item is
// rejected with a clear error.
func runCatalogEdit(ctx context.Context, out io.Writer, k catalogKind, name string) error {
	if k.isBuiltin(name) {
		return asUserError(fmt.Errorf(
			"%s %q is a built-in and is read-only; copy it to a custom name first: yoloctl %s copy %s my-%s",
			k.name, name, k.name, name, name))
	}
	ok, err := k.exists(name)
	if err != nil {
		return err
	}
	if !ok {
		return asUserError(fmt.Errorf("unknown custom %s %q (nothing to edit)", k.name, name))
	}
	path, err := k.customPath(name)
	if err != nil {
		return err
	}
	before, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := runEditor(ctx, path); err != nil {
		return err
	}
	after, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(before) == string(after) {
		cliInfo(logging.Host, "no changes")
		return nil
	}
	if err := k.validateAfterEdit(name); err != nil {
		return asUserError(fmt.Errorf("edited %s %q is invalid (left as saved at %s): %w", k.name, name, path, err))
	}
	cliInfo(logging.Host, "saved %s %q", k.name, name)
	return nil
}

func newCatalogDestroyCmd(k catalogKind) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "destroy NAME",
		Aliases: []string{"rm"},
		Short:   fmt.Sprintf("Delete a custom %s (built-ins cannot be deleted)", k.name),
		Long: fmt.Sprintf(`Deletes a CUSTOM %[1]s. Built-ins are yoloctl-owned and cannot be deleted.

Before anything is removed, every box manifest in every space and the custom
catalogs are scanned for the name. A %[1]s that is still named, or a box whose
references cannot be read, is kept unless --force is given: a dangling name
makes every box that reaches it fail to compose its policy. Confirmation is
required unless --force is passed.`, k.name),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCatalogDestroy(cmd, k, args[0], force)
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Delete even when something still names the item, and skip confirmation")
	return cmd
}

func runCatalogDestroy(cmd *cobra.Command, k catalogKind, name string, force bool) error {
	if k.isBuiltin(name) {
		return asUserError(fmt.Errorf(
			"%s %q is a built-in and cannot be deleted; only custom %s items can be removed", k.name, name, k.name))
	}
	ok, err := k.exists(name)
	if err != nil {
		return err
	}
	if !ok {
		return asUserError(fmt.Errorf("unknown custom %s %q (nothing to destroy)", k.name, name))
	}

	// Scan for referrers before touching anything, as `account destroy` does.
	referrers, err := k.referrers(name, cmd.ErrOrStderr(), force)
	if err != nil {
		return err
	}
	if len(referrers) > 0 {
		cliWarn(logging.Box, "%s %q is still named by: %s", k.name, name, strings.Join(referrers, ", "))
		if !force {
			return asUserError(fmt.Errorf(
				"refusing to destroy %s %q while it is still named — remove those references, or pass --force",
				k.name, name))
		}
	}

	if !force {
		ok, perr := promptYesNo(
			fmt.Sprintf("destroy custom %s %q? [y/N]: ", k.name, name),
			cmd.InOrStdin(), cmd.ErrOrStderr())
		if perr != nil {
			return perr
		}
		if !ok {
			return asUserError(errors.New("destroy declined"))
		}
	}
	if err := k.deleteCustom(name); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return asUserError(fmt.Errorf("unknown custom %s %q (nothing to destroy)", k.name, name))
		}
		return err
	}
	cliInfo(logging.Host, "destroyed custom %s %q", k.name, name)
	return nil
}

// The reference graph the destroy guard walks: a box names an agent, features,
// and capabilities; a custom agent requires features; a custom feature requires
// capabilities. Built-ins only name built-ins, which cannot be destroyed.

// boxReferrers lists the boxes match selects, as "box SPACE/NAME".
func boxReferrers(stderr io.Writer, force bool, match func(boxRefs) bool) ([]string, error) {
	boxes, err := boxesReferencing(stderr, force, match)
	if err != nil {
		return nil, err
	}
	for i, b := range boxes {
		boxes[i] = "box " + b
	}
	return boxes, nil
}

func capabilityReferrers(name string, stderr io.Writer, force bool) ([]string, error) {
	out, err := boxReferrers(stderr, force, func(r boxRefs) bool { return slices.Contains(r.Capabilities, name) })
	if err != nil {
		return nil, err
	}
	custom, err := features.LoadUserFeatures()
	if err != nil {
		return nil, err
	}
	var requiring []string
	for n, f := range custom {
		if slices.Contains(f.RequiresCapabilities, name) {
			requiring = append(requiring, "feature "+n)
		}
	}
	sort.Strings(requiring)
	return append(out, requiring...), nil
}

func featureReferrers(name string, stderr io.Writer, force bool) ([]string, error) {
	out, err := boxReferrers(stderr, force, func(r boxRefs) bool { return slices.Contains(r.Features, name) })
	if err != nil {
		return nil, err
	}
	agents, err := agentprofile.All()
	if err != nil {
		return nil, err
	}
	var requiring []string
	for n, p := range agents {
		if !agentprofile.IsBuiltin(n) && slices.Contains(p.RequiresFeatures, name) {
			requiring = append(requiring, "agent "+n)
		}
	}
	sort.Strings(requiring)
	return append(out, requiring...), nil
}

func agentReferrers(name string, stderr io.Writer, force bool) ([]string, error) {
	return boxReferrers(stderr, force, func(r boxRefs) bool { return r.Agent == name })
}
