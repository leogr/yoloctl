package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/manifestio"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// fromEnvUseName is the sentinel value cobra assigns to --from-env when the
// flag is passed without an argument: the secret's own name is then used as the
// env var to read.
const fromEnvUseName = "$NAME"

// readCredentialValue reads a secret value for `account secret set` from
// --from-env (an environment variable) or, by default, from stdin's first line
// so the value never appears in the process list. An empty value is rejected.
// The value is never echoed or logged.
func readCredentialValue(cmd *cobra.Command, fromEnv, name string) (string, error) {
	if secret, ok := cmd.Context().Value(controllerSecretKey{}).(controllerSecret); ok {
		return secret.Value, nil
	}
	if fromEnv != "" {
		envVar := fromEnv
		if envVar == fromEnvUseName {
			envVar = name
		}
		value := os.Getenv(envVar)
		if value == "" {
			return "", fmt.Errorf("environment variable %s is empty or unset", envVar)
		}
		return value, nil
	}
	r := bufio.NewReader(io.LimitReader(cmd.InOrStdin(), (1<<20)+1))
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value := strings.TrimRight(line, "\r\n")
	if len(value) > 1<<20 {
		return "", errors.New("secret exceeds size limit")
	}
	if value == "" {
		return "", fmt.Errorf("no value read from stdin")
	}
	return value, nil
}

// Accounts are named within a space. Secret values never appear in command
// output, JSON, or logs; show and secret list emit key names only.
func newAccountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Manage accounts (identity keychains: provider secrets + per-agent state)",
		Long: `An account belongs to one space and holds provider secrets plus per-agent
native state on that VM's persistent account disk. Boxes select an account by
plain name within their space. The same name in another space is independent.

Use --space or the current context to select the owning space. Space creation
initializes its required --account name; account create adds more accounts.
Missing accounts are never created implicitly. Secret values are never printed.`,
	}
	cmd.PersistentFlags().String("space", defaultSpaceName, "Space owning the account (defaults to current context)")
	_ = cmd.RegisterFlagCompletionFunc("space", completeSpaces)
	cmd.AddCommand(
		newAccountListCmd(),
		newAccountShowCmd(),
		newAccountCreateCmd(),
		newAccountEditCmd(),
		newAccountDestroyCmd(),
		newAccountSecretCmd(),
		newAccountAuthCmd(),
		newAccountObserveCmd(),
	)
	return cmd
}

// validAccountArg validates an ACCOUNT positional (a plain name; the "none"
// sentinel is not a manageable account).
func validAccountArg(name string) error {
	if err := account.ValidateName(name); err != nil {
		return asUserError(err)
	}
	return nil
}

// loadAccountOrHint loads an account, mapping a missing manifest to a clear
// "create it" user-error.
func loadAccountOrHint(space, name string) (*account.Config, error) {
	c, err := account.Load(space, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, asUserError(fmt.Errorf(
			"account %q does not exist in space %q — create it: yoloctl account create %s --space %s",
			name, space, name, space))
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

func newAccountCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create NAME",
		Short: "Create an empty account manifest",
		Long: `Creates a new empty account through the selected space's running controller.
Its declaration is stored on the native data disk; per-agent state directories
are prepared at runtime use. Space creation already initializes its first account.

  yoloctl account create personal
  yoloctl account create work`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			space, err := resolveAccountSpace(cmd)
			if err != nil {
				return err
			}
			name := args[0]
			if err := validAccountArg(name); err != nil {
				return err
			}
			release, err := account.Lock(space, name)
			if err != nil {
				return err
			}
			defer release()
			exists, err := account.Exists(space, name)
			if err != nil {
				return err
			}
			if exists {
				return asUserError(fmt.Errorf("account %q already exists", name))
			}
			c := account.New(space, name)
			if err := saveVerifiedAccount(cmd.Context(), c); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "created account %s at %s\n", name, c.Path())
			return nil
		},
	}
	return cmd
}

func newAccountListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List accounts",
		Long: `Lists accounts belonging to the selected space. Its controller must
already be running; use space start first when the VM is stopped.

  yoloctl account list
  yoloctl account list --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			space, err := resolveAccountSpace(cmd)
			if err != nil {
				return err
			}
			names, err := account.List(space)
			if err != nil {
				return err
			}
			if asJSON {
				return writeNameListJSON(cmd.OutOrStdout(), names)
			}
			for _, n := range names {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of the plain name list")
	return cmd
}

// writeNameListJSON emits a top-level array of {name} objects — the --json
// shape shared by `account ls` and `account secret ls`. Secret values are
// deliberately excluded — secret listing is name-only by design.
func writeNameListJSON(w io.Writer, names []string) error {
	type nameJSON struct {
		Name string `json:"name"`
	}
	out := make([]nameJSON, 0, len(names))
	for _, n := range names {
		out = append(out, nameJSON{Name: n})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func newAccountShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show NAME",
		Short: "Show an account's metadata, secret KEY NAMES, and per-agent state leaves",
		Long: `Shows the account's name, manifest path, identity home, the NAMES of
its stored secrets, and the per-agent native-state leaves present. Secret
VALUES are never printed.

  yoloctl account show default
  yoloctl account show personal --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			space, err := resolveAccountSpace(cmd)
			if err != nil {
				return err
			}
			name := args[0]
			if err := validAccountArg(name); err != nil {
				return err
			}
			c, err := loadAccountOrHint(space, name)
			if err != nil {
				return err
			}
			stateHome, err := account.StateHome(name)
			if err != nil {
				return err
			}
			leaves := accountStateLeaves(cmd.Context(), space, name)
			if asJSON {
				return writeAccountShowJSON(cmd.OutOrStdout(), c, stateHome, leaves)
			}
			return writeAccountShow(cmd.OutOrStdout(), c, stateHome, leaves)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of the human-readable view")
	return cmd
}

// accountStateLeaf is one per-agent native-state leaf present in an account's
// identity home, for `account show`.
type accountStateLeaf struct {
	Agent string
	Path  string
}

// accountStateLeaves lists the per-agent leaves present under the account's
// identity home, sorted by agent. Best-effort read: a missing home (no leaf
// materialized yet) is an empty list; unreadable entries are skipped.
func accountStateLeaves(ctx context.Context, space, name string) []accountStateLeaf {
	ctx, cancel := context.WithTimeout(ctx, vmhost.GuestProbeTimeout)
	defer cancel()
	result, err := accountStateOperation(ctx, space, accountstate.Request{Action: "list", Account: name})
	if err != nil {
		return nil
	}
	home, _ := account.StateHome(name)
	out := make([]accountStateLeaf, 0, len(result.Leaves))
	for _, agent := range result.Leaves {
		if layout.ValidateName("agent", agent) == nil {
			out = append(out, accountStateLeaf{Agent: agent, Path: filepath.Join(home, agent)})
		}
	}
	return out
}

func writeAccountShow(w io.Writer, c *account.Config, stateHome string, leaves []accountStateLeaf) error {
	_, _ = fmt.Fprintf(w, "Space:      %s\n", c.Space)
	_, _ = fmt.Fprintf(w, "Name:       %s\n", c.Name)
	_, _ = fmt.Fprintf(w, "Manifest:   %s\n", c.Path())
	_, _ = fmt.Fprintf(w, "State home: %s\n", stateHome)
	names := c.SecretNames()
	if len(names) == 0 {
		_, _ = fmt.Fprintln(w, "Secrets:    (none)")
	} else {
		_, _ = fmt.Fprintln(w, "Secrets:")
		for _, n := range names {
			_, _ = fmt.Fprintf(w, "  %s\n", n)
		}
	}
	if leaves == nil {
		_, _ = fmt.Fprintln(w, "State leaves: unavailable (space stopped or storage inaccessible)")
		return nil
	}
	if len(leaves) == 0 {
		_, _ = fmt.Fprintln(w, "State leaves: (none)")
		return nil
	}
	_, _ = fmt.Fprintln(w, "State leaves:")
	for _, l := range leaves {
		_, _ = fmt.Fprintf(w, "  %s\t%s\n", l.Agent, l.Path)
	}
	return nil
}

func writeAccountShowJSON(w io.Writer, c *account.Config, stateHome string, leaves []accountStateLeaf) error {
	type leafJSON struct {
		Agent     string `json:"agent"`
		StatePath string `json:"state_path"`
	}
	type accountJSON struct {
		Space          string     `json:"space"`
		StateAvailable bool       `json:"state_available"`
		Name           string     `json:"name"`
		ManifestPath   string     `json:"manifest_path"`
		StateHome      string     `json:"state_home"`
		Secrets        []string   `json:"secrets"` // KEY NAMES only — never values
		StateLeaves    []leafJSON `json:"state_leaves"`
	}
	names := c.SecretNames()
	if names == nil {
		names = []string{}
	}
	ls := make([]leafJSON, 0, len(leaves))
	for _, l := range leaves {
		ls = append(ls, leafJSON{Agent: l.Agent, StatePath: l.Path})
	}
	out := accountJSON{
		Space: c.Space, StateAvailable: leaves != nil,
		Name:         c.Name,
		ManifestPath: c.Path(),
		StateHome:    stateHome,
		Secrets:      names,
		StateLeaves:  ls,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func newAccountEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit NAME",
		Short: "Edit the account manifest in $EDITOR (validated before commit)",
		Long: `Opens a 0600 temporary COPY of the account's manifest in $EDITOR (default
vi). On save the copy is validated (schema, name-matches-account, invariants);
a valid edit is committed atomically to the canonical manifest, while an
invalid one is rejected with a clear error and the on-disk manifest is left
UNTOUCHED. Hand-editing secret values is allowed.

  yoloctl account edit default
  EDITOR=nano yoloctl account edit personal`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			space, err := resolveAccountSpace(cmd)
			if err != nil {
				return err
			}
			name := args[0]
			if err := validAccountArg(name); err != nil {
				return err
			}
			return runAccountEdit(cmd, space, name)
		},
	}
	return cmd
}

// runAccountEdit edits a 0600 temp copy of the manifest, validates the edited
// result, and commits through the atomic Save under the account lock. The
// canonical manifest is never touched by an invalid edit (the temp copy is
// discarded on invalid input; a concurrent-change conflict retains the copy).
func runAccountEdit(cmd *cobra.Command, space, name string) error {
	lock := func() (func(), error) {
		return lockManifestEdit(cmd.Context(), cmd.ErrOrStderr(), space, func() (func(), error) { return account.Lock(space, name) })
	}
	release, err := lock()
	if err != nil {
		return err
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	orig, err := loadAccountOrHint(space, name)
	if err != nil {
		return err
	}
	before, err := os.ReadFile(orig.Path())
	if err != nil {
		return err
	}
	manifestSnapshot, err := snapshotEditFile(orig.Path())
	if err != nil {
		return err
	}
	spaceSnapshot, err := snapshotEditSpace(space)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "yoloctl-account-"+name+"-*.yaml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil { // the copy holds secrets
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(before); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	release()
	release = nil
	if err := runEditor(cmd.Context(), tmpPath); err != nil {
		return err
	}
	after, err := os.ReadFile(tmpPath)
	if err != nil {
		return err
	}
	if string(before) == string(after) {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no changes")
		return nil
	}
	// Strict decode: a hand-edit that misspells a field — `secrets:`
	// above all — must be rejected here, not silently dropped and then
	// re-marshaled away by Save.
	edited, err := account.ParseManifest(after)
	if err != nil {
		return asUserError(fmt.Errorf("edited account manifest is invalid (canonical manifest untouched): %w", err))
	}
	if edited.SchemaVersion != account.SchemaVersion {
		return asUserError(fmt.Errorf("edited account manifest is invalid (canonical manifest untouched): schema_version must be %d", account.SchemaVersion))
	}
	if edited.Name != name {
		return asUserError(fmt.Errorf("edited account manifest is invalid (canonical manifest untouched): name %q must stay %q", edited.Name, name))
	}
	if err := edited.Validate(); err != nil {
		return asUserError(fmt.Errorf("edited account manifest is invalid (canonical manifest untouched): %w", err))
	}
	release, err = lock()
	if err != nil {
		removeTemp = false
		return fmt.Errorf("reacquire edit lock (edits kept at %s): %w", tmpPath, err)
	}
	if !spaceSnapshot.unchanged() || !manifestSnapshot.unchanged() {
		removeTemp = false
		return asUserError(fmt.Errorf("account or space changed while editing; retry against current state (edits kept at %s)", tmpPath))
	}
	edited.Space = space
	if err := edited.Save(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "saved account %s\n", name)
	printSecretPickupNote(cmd.ErrOrStderr(), name)
	return nil
}

func newAccountDestroyCmd() *cobra.Command {
	var (
		force bool
		yes   bool
	)
	cmd := &cobra.Command{
		Use:   "destroy NAME",
		Short: "Delete an account: its secrets AND its whole identity home",
		Long: `Removes every native-state leaf and then the account manifest and secrets.
Only boxes in the selected space reference this identity. Referencing boxes
block deletion unless --force is given; force stops active consumers first.
The space controller must already be running to delete native state safely.

State is removed first and the manifest last, so failures remain retryable.
Confirmation is required unless --force/--yes. A deleted account, including
the space's default, stays deleted until explicitly created again.

  yoloctl account destroy old-personal
  yoloctl account destroy default --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			space, err := resolveAccountSpace(cmd)
			if err != nil {
				return err
			}
			name := args[0]
			if err := validAccountArg(name); err != nil {
				return err
			}
			return runAccountDestroy(cmd, space, name, force, yes)
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Delete even when boxes still reference the account, and skip confirmation")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip the confirmation prompt")
	return cmd
}

func runAccountDestroy(cmd *cobra.Command, space, name string, force, yes bool) error {
	stderr := cmd.ErrOrStderr()
	release, err := account.Lock(space, name)
	if err != nil {
		return err
	}
	defer release()
	c, err := loadAccountOrHint(space, name)
	if err != nil {
		return err
	}

	// Scan the owning space before touching native state. Unreadable box
	// declarations are reported, never silently skipped.
	referencing, err := boxesReferencingSpace(space, stderr, force, func(r boxRefs) bool { return r.Account == name })
	if err != nil {
		return err
	}
	if len(referencing) > 0 {
		_, _ = fmt.Fprintf(stderr,
			"warning: %d box(es) still select account %s: %s\n",
			len(referencing), name, strings.Join(referencing, ", "))
		if !force {
			return asUserError(fmt.Errorf(
				"refusing to destroy account %s while boxes select it — repoint or destroy those boxes, or pass --force",
				name))
		}
	}

	if !force && !yes {
		prompt := fmt.Sprintf("destroy account %s in space %s (secrets + every per-agent state leaf)? [y/N]: ", name, space)

		ok, perr := promptYesNo(prompt, cmd.InOrStdin(), stderr)
		if perr != nil {
			return perr
		}
		if !ok {
			return asUserError(errors.New("destroy declined"))
		}
	}

	// State FIRST, manifest LAST: a failed state removal aborts with the
	// manifest intact, so the destroy is retryable and never leaves a
	// manifest-less orphan tree. Reported honestly — no tombstones.
	if err := accountEnsureSpace(cmd.Context(), space); err != nil {
		return err
	}
	if err := accountStopUsers(cmd.Context(), space, name, force, stderr); err != nil {
		return err
	}
	if _, err := accountStateOperation(cmd.Context(), space, accountstate.Request{Action: "remove", Account: name}); err != nil {
		return fmt.Errorf("remove account state (manifest kept): %w", err)
	}

	if err := c.Delete(); err != nil {
		return fmt.Errorf("delete account manifest (native state already removed — retry the destroy): %w", err)
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "destroyed account %s (secrets + native state)\n", name)

	return nil
}

// boxRefs is the part of a box manifest that names other entities: the account
// it selects and the catalog items it declares.
type boxRefs struct {
	Account      string   `yaml:"account"`
	Agent        string   `yaml:"agent"`
	Features     []string `yaml:"features"`
	Capabilities []string `yaml:"capabilities"`
}

// boxesReferencing returns "<space>/<box>" for every box manifest, in every
// space bundle, that match selects. Bundles without a loadable space.yaml
// still contribute. An unloadable box is read loosely for its references. If
// even that fails, the scan refuses to conclude that the name is unused unless
// --force was given; forced deletion reports the box and any decoded fields.
func boxesReferencing(stderr io.Writer, force bool, match func(boxRefs) bool) ([]string, error) {
	return boxesReferencingSpace("", stderr, force, match)
}
func boxesReferencingSpace(space string, stderr io.Writer, force bool, match func(boxRefs) bool) ([]string, error) {
	refs, err := agentbox.ListAllAgentboxes()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ref := range refs {
		if space != "" && ref.Space != space {
			continue
		}
		var r boxRefs
		cfg, err := agentbox.Load(ref.Space, ref.Name)
		if err == nil {
			r = boxRefs{Account: cfg.Account, Agent: cfg.Agent, Features: cfg.Features, Capabilities: cfg.Capabilities}
		} else {
			_, _ = fmt.Fprintf(stderr, "warning: box %s/%s manifest is unreadable (%v); reading its references loosely\n",
				ref.Space, ref.Name, err)
			var looseErr error
			r, looseErr = looseBoxRefs(ref.Space, ref.Name)
			if looseErr != nil {
				if !force {
					return nil, asUserError(fmt.Errorf(
						"cannot determine references in box %s/%s manifest: %w (repair it or pass --force)",
						ref.Space, ref.Name, looseErr))
				}
				_, _ = fmt.Fprintf(stderr, "warning: box %s/%s references could not be fully read (%v); continuing because --force was given\n",
					ref.Space, ref.Name, looseErr)
			}
		}
		if match(r) {
			out = append(out, ref.Space+"/"+ref.Name)
		}
	}
	return out, nil
}

// looseBoxRefs extracts just the reference fields from a box YAML with a
// tolerant parse, for manifests agentbox.Load rejects (schema drift, unknown
// fields). An error means at least one reference may be unknown; fields that
// decoded before a YAML type error are still returned for forced scans.
func looseBoxRefs(space, name string) (boxRefs, error) {
	var refs boxRefs
	path, err := layout.BoxPath(space, name)
	if err != nil {
		return refs, err
	}
	raw, err := manifestio.Read(path)
	if err != nil {
		return refs, err
	}
	if err := yaml.Unmarshal(raw, &refs); err != nil {
		return refs, fmt.Errorf("parse %s: %w", path, err)
	}
	return refs, nil
}

func newAccountSecretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage an account's secrets (values are never printed)",
		Long: `Sets, unsets, and lists the secrets stored inside an account. Secret
names are env-style keys (e.g. ANTHROPIC_API_KEY, GITHUB_TOKEN). Secret VALUES
are never printed by any subcommand.

Account changes never synchronize native agent state or restart boxes. To
replace a create-time environment credential in a running box, recreate that
box explicitly.`,
	}
	cmd.AddCommand(
		newAccountSecretSetCmd(),
		newAccountSecretUnsetCmd(),
		newAccountSecretListCmd(),
	)
	return cmd
}

// printSecretPickupNote is the courtesy note after an account content change:
// account content never produces a reconcile action, and environment
// injection is container-create-time.
func printSecretPickupNote(w io.Writer, name string) {
	_, _ = fmt.Fprintf(w,
		"note: running boxes selecting account %s are not updated automatically; recreate an affected box to replace its environment-injected credentials\n",
		name)
}

func newAccountSecretSetCmd() *cobra.Command {
	var fromEnv string
	cmd := &cobra.Command{
		Use:   "set NAME KEY",
		Short: "Set a secret on an account (reads the value from stdin by default)",
		Long: `Sets secret KEY on an existing account in the selected space.
This updates the controller-owned declaration and requires a running space.

By default the value is read from stdin so it never appears in the process
list. Pass --from-env (no value) to read from the environment variable named
KEY, or --from-env=VAR to read from a differently-named variable. The value is
never echoed or logged.

  printf '%s' "$TOKEN" | yoloctl account secret set default ANTHROPIC_API_KEY
  yoloctl account secret set personal OPENAI_API_KEY --from-env`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			space, err := resolveAccountSpace(cmd)
			if err != nil {
				return err
			}
			name, key := args[0], args[1]
			if err := validAccountArg(name); err != nil {
				return err
			}
			if strings.TrimSpace(key) == "" {
				return asUserError(errors.New("secret KEY must not be empty"))
			}
			// Validate the input before acquiring the account lock.
			value, err := readCredentialValue(cmd, fromEnv, key)
			if err != nil {
				return asUserError(err)
			}
			release, err := account.Lock(space, name)
			if err != nil {
				return err
			}
			defer release()
			c, err := account.Load(space, name)

			if errors.Is(err, fs.ErrNotExist) {
				return asUserError(fmt.Errorf(
					"account %q does not exist in space %q — create it: yoloctl account create %s --space %s",
					name, space, name, space))
			}
			if err != nil {
				return err
			}
			c.SetSecret(key, value)
			if err := saveVerifiedAccount(cmd.Context(), c); err != nil {
				return err
			}
			// Confirmation deliberately omits the value.
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "set secret %s on account %s\n", key, name)
			printSecretPickupNote(cmd.ErrOrStderr(), name)
			return nil
		},
	}
	cmd.Flags().StringVar(&fromEnv, "from-env", "",
		"Read value from this environment variable (omit value to use KEY)")
	cmd.Flags().Lookup("from-env").NoOptDefVal = fromEnvUseName
	return cmd
}

func newAccountSecretUnsetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unset NAME KEY",
		Short: "Remove a secret from an account",
		Long: `Removes secret KEY from an account. Reports whether the secret was
present. This never deletes or edits native agent state. A running box keeps
its create-time environment until explicitly recreated.

  yoloctl account secret unset default ANTHROPIC_API_KEY`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			space, err := resolveAccountSpace(cmd)
			if err != nil {
				return err
			}
			name, key := args[0], args[1]
			if err := validAccountArg(name); err != nil {
				return err
			}
			release, err := account.Lock(space, name)
			if err != nil {
				return err
			}
			defer release()
			c, err := loadAccountOrHint(space, name)
			if err != nil {
				return err
			}
			if !c.UnsetSecret(key) {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "secret %s was not set on account %s\n", key, name)
				return nil
			}
			if err := saveVerifiedAccount(cmd.Context(), c); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "unset secret %s on account %s\n", key, name)
			printSecretPickupNote(cmd.ErrOrStderr(), name)
			return nil
		},
	}
	return cmd
}

func newAccountSecretListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "list NAME",
		Aliases: []string{"ls"},
		Short:   "List an account's secret NAMES (never values)",
		Long: `Lists the names of the secrets stored in an account. Values are never
printed by this command.

  yoloctl account secret list default
  yoloctl account secret ls personal --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			space, err := resolveAccountSpace(cmd)
			if err != nil {
				return err
			}
			name := args[0]
			if err := validAccountArg(name); err != nil {
				return err
			}
			c, err := loadAccountOrHint(space, name)
			if err != nil {
				return err
			}
			names := c.SecretNames()
			if asJSON {
				return writeNameListJSON(cmd.OutOrStdout(), names)
			}
			for _, n := range names {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of the plain name list")
	return cmd
}

func resolveAccountSpace(cmd *cobra.Command) (string, error) {
	space, err := selectedSpace(cmd)
	if err != nil {
		return "", asUserError(err)
	}
	if _, err := spaceconfig.Load(space); err != nil {
		return "", fmt.Errorf("space %q must exist before managing accounts: %w", space, err)
	}
	return space, nil
}

var accountStateOperation = accountstate.Execute
var accountEnsureSpace = ensureSpaceRunning
var accountStopUsers = stopAccountUsers
