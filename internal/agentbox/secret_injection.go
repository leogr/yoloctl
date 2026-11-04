package agentbox

import (
	"encoding/json"
	"fmt"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentprofile"
)

// openshellUserEnvironment is the container env var the openshell-sandbox
// supervisor reads at startup (OPENSHELL_USER_ENVIRONMENT): a JSON-serialized
// map of env vars it injects into the SSH child process (the agent), which
// otherwise runs under env_clear() for isolation. This is the file-mode
// credential-delivery primitive — the value reaches the agent's ENVIRONMENT,
// never its command line, so it never lands in /proc/<agent>/cmdline. The bare
// `openshell-sandbox` binary has no `--credential` flag (that is a gateway /
// gRPC-mode feature); this env channel is how secrets reach a file-mode agent.
// Source: NVIDIA/OpenShell crates/openshell-core/src/sandbox_env.rs.
//
// Delivery is container-CREATE-time by construction: the value is part of the
// nerdctl run env. Replacing an account secret does not update a running box;
// the user explicitly recreates it when they want the new environment.
const openshellUserEnvironment = "OPENSHELL_USER_ENVIRONMENT"

// userEnvironmentJSON builds the OPENSHELL_USER_ENVIRONMENT JSON map for c from
// the agent's secret_injection rules + the box's account secrets: each rule
// whose account secret is present contributes {EnvName: value}. Returns "" when
// the box has no agent/account, the agent declares no injections, or no secret
// matches (nothing to inject). The projection is the narrowing router: only the env
// keys the agent DECLARES are read out of the shared account.
//
// Strict like EffectiveFeaturesStrict: this runs on the build/apply
// path (container create/recreate), so an agent profile that fails to
// resolve or an account manifest that fails to load or decode is an ERROR —
// never a silent no-injection launch. The CLI's lifecycle preflight already
// enforces account existence up front with the actionable create hint; a
// failure surfacing here is the narrow check-to-create race (or a corrupt
// manifest) and fails the create cleanly instead of starting the agent
// unauthenticated. The account reload is unconditional for every real-agent
// box, including agents with no injection rules (such as Codex), because
// their native state still belongs to the selected account. A missing secret
// VALUE stays non-fatal — the agent falls back to its own auth.
func userEnvironmentJSON(c *Config) (string, error) {
	if !c.HasRealAccount() {
		return "", nil
	}
	profile, err := agentprofile.Lookup(c.Agent)
	if err != nil {
		return "", fmt.Errorf("resolve agent %q profile for secret injection: %w", c.Agent, err)
	}
	acct, err := account.Load(c.Space, c.Account)
	if err != nil {
		return "", fmt.Errorf("load account %q for container creation: %w", c.Account, err)
	}
	if len(profile.SecretInjection) == 0 {
		return "", nil
	}
	env := map[string]string{}
	for _, inj := range profile.SecretInjection {
		if v, ok := acct.Secrets[inj.Secret]; ok && v != "" {
			env[inj.EnvName()] = v
		}
	}
	if len(env) == 0 {
		return "", nil
	}
	b, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
