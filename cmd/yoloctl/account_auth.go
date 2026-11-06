package main

import (
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
	"github.com/leogr/yoloctl/internal/agentprofile"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
)

// newAccountAuthCmd is the parent for `yoloctl account auth …` — the
// account-scoped, agent-driven auth surface. Since an
// account serves multiple agents, every subcommand takes an explicit AGENT:
// it acts on AGENT's declared auth: block against the account's per-agent
// state leaf, account-state/NAME/AGENT/.
//
// The model: the agent's native auth file (auth.credential_file, e.g. codex
// auth.json / claude .credentials.json) lives in that leaf, which is
// bind-mounted into every box selecting the account with that agent. Interactive
// first-run login happens INSIDE the box (the agent's own flow); `import` copies
// an existing host login in, and `status` reports whether one is present. An
// import is a one-time copy: after delivery, the file belongs to the agent and
// yoloctl never synchronizes or rewrites it.
func newAccountAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage an account's agent login state (per agent)",
		Long: `Manage the agent login held in an account's native state.

These commands are account-scoped but agent-DRIVEN: NAME picks the account,
AGENT picks whose declared auth: block applies, reading/writing the account's
per-agent state leaf (account-state/NAME/AGENT/), which is bind-mounted into
any box selecting the account with that agent.

  yoloctl account auth import default codex    # copy an existing host login into the account
  yoloctl account auth status default codex    # is a login present?

Interactive first-run login happens inside the box (run the agent and follow
its own login flow); native state remains agent-owned. Managed Claude sessions
relocate Anthropic profiles to /home/sandbox/.claude/anthropic inside the existing
leaf. Before redirecting/recreating an older disposable login, preserve it
privately first; stop account consumers, import it, then launch/recreate explicitly.
Bare direct invocations need the same config-path prefix, for example:
  yoloctl box exec BOX -- env CLAUDE_CONFIG_DIR=/home/sandbox/.claude ANTHROPIC_CONFIG_DIR=/home/sandbox/.claude/anthropic claude auth status
For Codex, keep cli_auth_credentials_store="file" for file-backed persistence;
yoloctl does not override the user's selected store or import a host keyring.
Status reports local material, not provider acceptance or external auth.

For Claude API-key auth, store ANTHROPIC_API_KEY with
` + "`yoloctl account secret set NAME ANTHROPIC_API_KEY`" + `; Claude reads it
from its environment. Codex API-key auth is native state: run
` + "`codex login --with-api-key`" + ` inside the box and pipe the key on stdin,
or import an existing Codex auth.json here.

For Pi, use --agent pi, then /login to select a provider and /model to select
a model. The built-in policy covers Anthropic, OpenAI/ChatGPT, Gemini API and
OpenRouter. Native import copies ~/.pi/agent/auth.json once, including all
provider credentials in that file; host settings and sessions are not imported.
Alternatively set the matching account secret: ANTHROPIC_API_KEY, OPENAI_API_KEY,
GEMINI_API_KEY or OPENROUTER_API_KEY. Changed secrets require box recreation.
Pi prefers stored credentials over environment keys; use /logout to remove
a superseded stored credential before switching to an account secret.
Remote OAuth can require pasting the final browser redirect URL into Pi.

For OpenCode, use --agent opencode. Store OPENCODE_API_KEY (Zen),
ANTHROPIC_API_KEY, OPENAI_API_KEY, GOOGLE_GENERATIVE_AI_API_KEY, or
OPENROUTER_API_KEY as account secrets. Native import copies the entire
~/.local/share/opencode/auth.json once, including all stored providers;
use --file for a custom host XDG_DATA_HOME. Settings are not imported.
For ChatGPT login in a remote terminal, select the headless device method:

  yoloctl box create opencode-login --agent opencode --account NAME --no-attach
  yoloctl box exec opencode-login -- opencode auth login --provider openai --method 'ChatGPT Pro/Plus (headless)'

Other providers require additional explicit network capabilities and, when
using environment credentials, a custom profile's secret_injection rule.

For Antigravity CLI, use --agent agy and complete its Google URL/code login.
Native import copies ~/.gemini/antigravity-cli/antigravity-oauth-token once;
host keyring credentials are not exported. API-key mode needs GEMINI_API_KEY
in the account AND modelProvider: "gemini" in
~/.gemini/antigravity-cli/settings.json inside the box.

For Gemini API-key auth, store GEMINI_API_KEY with
` + "`yoloctl account secret set NAME GEMINI_API_KEY`" + ` and select Gemini API
Key on first interactive launch. Personal accounts directed to Antigravity
should use agy. Eligible Google login uses Gemini's URL/code flow;
import copies ~/.gemini/oauth_creds.json, not host settings or keychain state.
Select Google login once after import, or launch headless OAuth with an explicit
command prefix inside the sandbox (replace NAME with the imported account):

  yoloctl box create gemini-oauth --agent gemini --account NAME --no-attach
  yoloctl box exec gemini-oauth -- env GOOGLE_GENAI_USE_GCA=true GEMINI_SANDBOX=false NO_BROWSER=true gemini --approval-mode yolo --prompt 'Summarize this project'

The box's --env flag alone does not survive OpenShell's SSH environment reset.
A saved auth selection takes precedence. Status checks native file presence,
not API-key delivery or provider validity.`,
	}
	cmd.AddCommand(
		newAccountAuthImportCmd(),
		newAccountAuthStatusCmd(),
	)
	return cmd
}

// accountAuthTarget identifies a space-owned account and its agent-native leaf.
type accountAuthTarget struct {
	space    string
	name     string
	profile  *agentprofile.Profile
	stateDir string
}

// display renders the fully scoped native identity.
func (t accountAuthTarget) display() string { return t.space + "/" + t.name + "/" + t.profile.Name }

// authFilePath returns the absolute VM path of the agent's native auth file
// inside the account's state leaf (profile.Auth.CredentialFile, validated
// relative + no-escape by the profile loader).
func (t accountAuthTarget) authFilePath() string {
	return filepath.Join(t.stateDir, t.profile.Auth.CredentialFile)
}

// resolveAccountAuthTarget validates the NAME AGENT positionals.
func resolveAccountAuthTarget(space string, args []string) (accountAuthTarget, error) {
	name, agent := args[0], args[1]
	if err := account.ValidateName(name); err != nil {
		return accountAuthTarget{}, asUserError(err)
	}
	profile, err := lookupAgentProfile(agent)
	if err != nil {
		return accountAuthTarget{}, err
	}
	if profile.Auth == nil {
		return accountAuthTarget{}, asUserError(fmt.Errorf(
			"agent %q has no account auth import/status declaration; use the agent's native login flow or configure an officially supported secret_injection",
			agent))
	}
	stateDir, err := account.StatePath(name, agent)
	if err != nil {
		return accountAuthTarget{}, err
	}
	return accountAuthTarget{space: space, name: name, profile: profile, stateDir: stateDir}, nil
}

// requireAuthAccount enforces account existence for the read-side auth verb
// (status): a missing account is a create hint, never an auto-create.
func requireAuthAccount(t accountAuthTarget) error {
	_, err := loadAccountOrHint(t.space, t.name)
	return err
}

// defaultHostAuthPaths returns the candidate host login files `account auth
// import` tries when --file is omitted, in preference order under
// ~/auth.import_home_dir (default: ~/<base of state_container_path>/):
// each auth.import_files entry first (preferred host sources, in listed
// order), then credential_file as
// the last-resort fallback (deduped). codex → [~/.codex/auth.json]; claude →
// [~/.claude/.credentials.json] for the built-in profiles.
func defaultHostAuthPaths(profile *agentprofile.Profile) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	base := filepath.Base(profile.StateContainerPath)
	if profile.Auth.ImportHomeDir != "" {
		base = profile.Auth.ImportHomeDir
	}
	files := append(append([]string{}, profile.Auth.ImportFiles...), profile.Auth.CredentialFile)
	paths := make([]string, 0, len(files))
	seen := map[string]bool{}
	for _, f := range files {
		p := filepath.Join(home, base, f)
		if seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	return paths, nil
}

// firstExistingFile returns the first path that exists and is a regular file,
// or "" if none do.
func firstExistingFile(paths []string) string {
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

func newAccountAuthImportCmd() *cobra.Command {
	var file, anthropicDir string
	cmd := &cobra.Command{
		Use:   "import NAME AGENT [--file PATH | --anthropic-dir DIR]",
		Short: "Copy an existing host login file into the account's state leaf",
		Long: `Copies the agent's existing host login file into the account's per-agent
state leaf at the agent's auth.credential_file. With no --file, tries the
agent's conventional host path(s) in preference order, first existing wins
(codex: ~/.codex/auth.json; claude: ~/.claude/.credentials.json;
gemini: ~/.gemini/oauth_creds.json; pi: ~/.pi/agent/auth.json;
opencode: ~/.local/share/opencode/auth.json);
override with --file.

For Claude Anthropic profiles, --anthropic-dir DIR imports the selected profile's
config, credential and active selection into the Claude leaf's anthropic subtree.
It does not import inactive profiles or external identity files. If both legacy
and Anthropic sources exist, select --file or --anthropic-dir explicitly. A new
profile credential/config file alone cannot be imported as a legacy login.

The files are COPIED (mode 0600), never bind-mounted from your host login dir:
the box only ever sees the copy in the account's leaf. Its contents are never
printed. The account and a running space controller are required. Native import
refuses to overwrite state used by a running box.

  yoloctl account auth import default codex
  yoloctl account auth import personal codex --file ~/.codex/auth.json
  yoloctl account auth import personal claude --anthropic-dir ~/.config/anthropic`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			space, err := resolveAccountSpace(cmd)
			if err != nil {
				return err
			}
			target, err := resolveAccountAuthTarget(space, args)
			if err != nil {
				return err
			}
			doc, err := prepareAuthImport(args, file, anthropicDir, space)
			if err != nil {
				return err
			}
			if doc.SecretValue != "" {
				return importSecretIntoAccount(target, *target.profile.Auth.ImportSecret, cmd.OutOrStdout(), cmd.ErrOrStderr())
			}
			writes, err := nativeAuthWrites(target, doc)
			if err != nil {
				return err
			}
			release, err := lockBoxAccount(target.space, target.name)
			if err != nil {
				return err
			}
			defer release()
			if err := ensureAccountSelectableLocked(target.space, target.name); err != nil {
				return err
			}
			if err := accountEnsureSpace(cmd.Context(), target.space); err != nil {
				return err
			}
			if err := accountStopUsers(cmd.Context(), target.space, target.name, false, cmd.ErrOrStderr()); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "importing %s\n", doc.Source)
			for _, write := range writes {
				if _, err := accountStateOperation(cmd.Context(), target.space, accountstate.Request{Action: "write", Account: target.name, Agent: target.profile.Name, Path: write.Path, Data: write.Data}); err != nil {
					return err
				}
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "imported %s login into account %s (%s)\n", target.profile.Name, target.name, target.stateDir)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "Host legacy/native login file to import")
	cmd.Flags().StringVar(&anthropicDir, "anthropic-dir", "", "Claude Anthropic directory containing active_config, configs and credentials")
	return cmd
}

// importSecretIntoAccount is the API-key fallback for `account auth import`:
// when no native credential_file login exists, extract the key from a host
// config file (per the agent's auth.import_secret rule — e.g. claude's
// ~/.claude.json primaryApiKey) and store it as the account secret, which
// reaches the agent via env injection. The key VALUE is never logged — only
// the file path and field/secret names. The account must exist. Its lock is
// held from validation through the secret Save so a concurrent destroy or
// mutation can't interleave with the load-modify-write.
func importSecretIntoAccount(t accountAuthTarget, rule agentprofile.ImportSecret, out, errw io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, rule.File)
	blob, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return asUserError(fmt.Errorf(
				"no login to import for %s: no native login file, and %s does not exist.\n"+
					"  set the key directly: yoloctl account secret set %s %s",
				t.display(), path, t.name, rule.Secret))
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(blob, &doc); err != nil {
		return asUserError(fmt.Errorf("%s is not valid JSON: %w", path, err))
	}
	raw, ok := doc[rule.Field]
	if !ok {
		return asUserError(fmt.Errorf(
			"%s has no %q field — nothing to import.\n"+
				"  set the key directly: yoloctl account secret set %s %s",
			path, rule.Field, t.name, rule.Secret))
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return asUserError(fmt.Errorf("%s field %q is empty or not a string value", path, rule.Field))
	}
	// Source path is logged (the user's own host path); the VALUE never is.
	_, _ = fmt.Fprintf(errw, "extracting %s from %s\n", rule.Field, path)
	release, err := lockBoxAccount(t.space, t.name)
	if err != nil {
		return err
	}
	defer release()
	if err := ensureAccountSelectableLocked(t.space, t.name); err != nil {
		return err
	}
	acct, err := account.Load(t.space, t.name)
	if err != nil {
		return err
	}
	acct.SetSecret(rule.Secret, value)
	if err := acct.Save(); err != nil {
		return fmt.Errorf("save account %s: %w", t.name, err)
	}
	_, _ = fmt.Fprintf(out, "imported %s API key from %s into account %s (secret %s)\n",
		t.profile.Name, path, t.name, rule.Secret)
	_, _ = fmt.Fprintf(errw, "the box delivers it to %s via env injection; create or recreate the box to apply\n", t.profile.Name)
	return nil
}

func newAccountAuthStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status NAME AGENT",
		Short: "Show local native credential material without checking provider validity",
		Long: `Reports local native credential material, not provider authentication.
Claude and Codex formats are inspected with bounded VM-native reads; only
classifications and source names leave the native helper, never values.
Other profiles retain file-presence status. Environment and external credential
stores are not inspected. Inaccessible native storage is reported unavailable.

  yoloctl account auth status default codex`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			space, err := resolveAccountSpace(cmd)
			if err != nil {
				return err
			}
			t, err := resolveAccountAuthTarget(space, args)
			if err != nil {
				return err
			}
			if err := requireAuthAccount(t); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			findings, err := accountAuthStatus(cmd.Context(), t)
			if err != nil {
				_, _ = fmt.Fprintf(out, "%s: unavailable (space stopped or native state inaccessible)\n", t.display())
				return err
			}
			labels := map[string]string{"credentials-present": "credentials present", "file-present": "file present (format not inspected)", "missing": "missing", "invalid": "empty or malformed", "unverified": "unrecognized or incomplete credential material", "external": "externally configured (not verified)"}
			parts := make([]string, 0, len(findings))
			for _, finding := range findings {
				label, ok := labels[finding.Outcome]
				if !ok {
					return errors.New("unknown native credential classification")
				}
				parts = append(parts, finding.Source+": "+label)
			}

			_, _ = fmt.Fprintf(out, "%s: %s; provider validity not checked; environment/external auth not inspected\n", t.display(), strings.Join(parts, ", "))
			return nil
		},
	}
	return cmd
}

// accountAuthStatus returns only native classifications; credential bytes never
// leave the verified VM helper. A missing disk/read failure remains unavailable.
func accountAuthStatus(ctx context.Context, t accountAuthTarget) ([]accountstate.AuthFinding, error) {
	ctx, cancel := context.WithTimeout(ctx, vmhost.GuestProbeTimeout)
	defer cancel()
	if t.profile.Auth.Format == "" {
		result, err := accountStateOperation(ctx, t.space, accountstate.Request{Action: "exists", Account: t.name, Agent: t.profile.Name, Path: t.profile.Auth.CredentialFile})
		if err != nil {
			return nil, err
		}
		outcome := "missing"
		if result.Present {
			outcome = "file-present"
		}
		return []accountstate.AuthFinding{{Source: t.profile.Auth.CredentialFile, Outcome: outcome}}, nil
	}
	spec := accountstate.AuthStatusSpec{Format: t.profile.Auth.Format, ProfilesDir: t.profile.Auth.ProfilesDir}
	data, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	result, err := accountStateOperation(ctx, t.space, accountstate.Request{Action: "auth-status", Account: t.name, Agent: t.profile.Name, Path: t.profile.Auth.CredentialFile, Data: data})
	if err != nil {
		return nil, err
	}
	if len(result.Auth) == 0 {
		return nil, errors.New("native credential classification unavailable")
	}
	return result.Auth, nil
}
