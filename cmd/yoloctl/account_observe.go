package main

import (
	"encoding/json"
	"fmt"
	"path"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/spf13/cobra"
)

// newAccountObserveCmd sets or restores the status line reporter of an agent
// whose only reporter is the status line in the account's own settings
// (agy: no system settings layer). It is the one-time opt-in for an existing
// account; a new account is seeded by the agent's launcher. It writes the
// agent-owned settings file, so it is never implicit and never runs at
// launch.
func newAccountObserveCmd() *cobra.Command {
	var enable, disable bool
	cmd := &cobra.Command{
		Use:   "observe NAME AGENT --enable|--disable",
		Short: "Set or restore an agent's status line reporter in the account's settings",
		Long: `Sets the agent's status line reporter in the account's own settings, for an
agent with no system settings layer (agy). Model, effort, context and state
are then reported by boxes started afterwards; they are agent-reported.

--enable sets statusLine to the reporter, keeping every other setting. A
status line command already there is saved beside the settings and runs
after each report, with the same input and layout; without one, agy's own
line stays (stack_with_default). --disable restores the saved statusLine,
or removes the reporter's. A statusLine changed since enabling is left alone.

An account without settings is refused: the agent's launcher creates them,
with the reporter, at the account's first start. The settings' contents are
never printed. The account and a running space controller are required, and
no running box may use the account.

  yoloctl account observe default agy --enable
  yoloctl account observe default agy --disable`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if enable == disable {
				return asUserError(fmt.Errorf("pass exactly one of --enable or --disable"))
			}
			space, err := resolveAccountSpace(cmd)
			if err != nil {
				return err
			}
			name, agent := args[0], args[1]
			if err := account.ValidateName(name); err != nil {
				return asUserError(err)
			}
			profile, err := lookupAgentProfile(agent)
			if err != nil {
				return err
			}
			var line *agentprofile.AccountStatusLine
			if s := profile.Session; s != nil && s.Observe != nil {
				line = s.Observe.AccountStatusLine
			}
			if line == nil {
				return asUserError(fmt.Errorf("agent %s reports without the account's settings; nothing to enable", agent))
			}
			data, err := json.Marshal(accountstate.StatusLine{Enable: enable, Command: line.Command})
			if err != nil {
				return err
			}
			// As native import: one account lock across the selectability
			// guard, the consumer check and the write.
			release, err := lockBoxAccount(space, name)
			if err != nil {
				return err
			}
			defer release()
			if err := ensureAccountSelectableLocked(space, name); err != nil {
				return err
			}
			if err := accountEnsureSpace(cmd.Context(), space); err != nil {
				return err
			}
			if err := accountStopUsers(cmd.Context(), space, name, false, cmd.ErrOrStderr()); err != nil {
				return err
			}
			result, err := accountStateOperation(cmd.Context(), space, accountstate.Request{Action: "statusline", Account: name, Agent: profile.Name, Path: line.Settings, Data: data})
			if err != nil {
				return err
			}
			where := path.Join(profile.StateContainerPath, line.Settings)
			out := cmd.OutOrStdout()
			switch {
			case result.Outcome == "enabled":
				_, _ = fmt.Fprintf(out, "enabled the %s status line reporter in account %s (%s); boxes started from now on report agent state\n", profile.Name, name, where)
			case result.Outcome == "restored":
				_, _ = fmt.Fprintf(out, "disabled the %s status line reporter in account %s (%s); the previous status line is restored\n", profile.Name, name, where)
			case result.Outcome == "removed":
				_, _ = fmt.Fprintf(out, "disabled the %s status line reporter in account %s (%s); no previous status line was saved, so it is removed\n", profile.Name, name, where)
			case enable && result.Outcome == "unchanged":
				_, _ = fmt.Fprintf(out, "the %s status line reporter is already enabled in account %s\n", profile.Name, name)
			case enable:
				// No settings yet: the launcher seeds them, with the reporter
				// and its own defaults, at the account's first start.
				return asUserError(fmt.Errorf("account %s has no %s settings yet (%s); a new account is seeded with the reporter by the agent's launcher at its first start; nothing changed", name, profile.Name, where))
			default:
				_, _ = fmt.Fprintf(out, "the %s status line reporter is not enabled in account %s; nothing changed\n", profile.Name, name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&enable, "enable", false, "Set the reporter as the account's status line")
	cmd.Flags().BoolVar(&disable, "disable", false, "Restore the status line the reporter replaced")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	return cmd
}
