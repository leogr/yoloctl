package agentbox

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentprofile"
)

// openshellUserEnvironment is the JSON child environment envelope used by
// the privileged endpoint and older confined launchers. The native confined
// launcher sends the map in the private bootstrap instead.
const openshellUserEnvironment = "OPENSHELL_USER_ENVIRONMENT"

// ValidateSecretInjection resolves the profile for a declaration before
// saving or applying it. This check never reads the account's values.
func (c *Config) ValidateSecretInjection() error {
	if len(c.SecretInjection) == 0 {
		return nil
	}
	if !c.HasRealAccount() {
		return errors.New("secret_injection requires a real agent and account")
	}
	profile, err := agentprofile.Lookup(c.Agent)
	if err != nil {
		return fmt.Errorf("resolve agent %q profile for secret injection: %w", c.Agent, err)
	}
	return c.validateSecretInjection(profile)
}

func (c *Config) validateSecretInjection(profile *agentprofile.Profile) error {
	if len(c.SecretInjection) == 0 {
		return nil
	}
	if !c.HasRealAccount() {
		return errors.New("secret_injection requires a real agent and account")
	}
	sources := make(map[string]string, len(c.SecretInjection))
	for _, rule := range c.SecretInjection {
		target := rule.EnvName()
		if strings.TrimSpace(rule.Secret) == "" {
			return errors.New("secret_injection requires a nonblank account key")
		}
		if !agentprofile.ValidEnvName(target) {
			return fmt.Errorf("secret_injection target %q is not a valid environment variable name", target)
		}
		switch {
		case strings.HasPrefix(target, "OPENSHELL_"),
			target == "YOLOCTL_CHAT_CONFIG", target == "YOLOCTL_CHAT_ROOM", target == "YOLOCTL_CHAT_ACCOUNT":
			return fmt.Errorf("secret_injection target %q is reserved", target)
		}
		if _, ok := c.Env[target]; ok {
			return fmt.Errorf("secret_injection target %q also has a box env literal", target)
		}
		if previous, ok := sources[target]; ok && previous != rule.Secret {
			return secretTargetConflict(target, previous, rule.Secret)
		}
		sources[target] = rule.Secret
	}
	if profile != nil {
		for _, rule := range profile.SecretInjection {
			if source, ok := sources[rule.EnvName()]; ok && source != rule.Secret {
				return secretTargetConflict(rule.EnvName(), rule.Secret, source)
			}
		}
		for _, rule := range c.SecretInjection {
			if _, ok := profile.LaunchEnv[rule.EnvName()]; ok {
				return fmt.Errorf("secret_injection target %q also has an agent launch_env entry", rule.EnvName())
			}
		}
	}
	return nil
}

func secretTargetConflict(target, first, second string) error {
	return fmt.Errorf("secret_injection target %q maps both account keys %q and %q", target, first, second)
}

// AddSecretInjection appends only pairs that are new to the declaration.
func (c *Config) AddSecretInjection(rules []agentprofile.SecretInjection) bool {
	before := len(c.SecretInjection)
	for _, rule := range rules {
		if !slices.ContainsFunc(c.SecretInjection, func(other agentprofile.SecretInjection) bool {
			return rule.Secret == other.Secret && rule.EnvName() == other.EnvName()
		}) {
			c.SecretInjection = append(c.SecretInjection, rule)
		}
	}
	return len(c.SecretInjection) != before
}

func (c *Config) secretInjectionSnapshot() string {
	if len(c.SecretInjection) == 0 {
		return ""
	}
	pairs := make([][2]string, 0, len(c.SecretInjection))
	for _, rule := range c.SecretInjection {
		pairs = append(pairs, [2]string{rule.Secret, rule.EnvName()})
	}
	slices.SortFunc(pairs, func(a, b [2]string) int {
		if order := strings.Compare(a[0], b[0]); order != 0 {
			return order
		}
		return strings.Compare(a[1], b[1])
	})
	pairs = slices.Compact(pairs)
	encoded, _ := json.Marshal(pairs)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

// SecretInjectionChange is reported when an existing container still has its
// original settings and needs a deliberate replacement.
type SecretInjectionChange struct {
	Box, Space string
}

func (e *SecretInjectionChange) Error() string {
	return fmt.Sprintf("account secret mappings changed; apply them with yoloctl box recreate %s --space %s", e.Box, e.Space)
}

func (c *Config) secretInjectionChanged(labels map[string]string) bool {
	return c.secretInjectionSnapshot() != labels[LabelSecretInjection]
}

// CheckSecretInjectionRecreate protects direct recreation paths outside Diff,
// including reconcile --rebuild. The box recreate command supplies the same
// explicit consent used for a change of launch mode.
func CheckSecretInjectionRecreate(ctx context.Context, c *Config, labels map[string]string) error {
	if !RuntimeModeChangeAllowed(ctx) && c.secretInjectionChanged(labels) {
		return &SecretInjectionChange{Box: c.Name, Space: c.Space}
	}
	return nil
}

// userEnvironmentJSON resolves the profile defaults and box-specific account
// references into the private environment for a new container.
// Strict like EffectiveFeaturesStrict: this runs on the build/apply
// path (container create/recreate), so an agent profile that fails to
// resolve or an account manifest that fails to load or decode is an ERROR —
// never a silent no-injection launch. The CLI's lifecycle preflight already
// enforces account existence up front with the actionable create hint; a
// failure surfacing here is the narrow check-to-create race (or a corrupt
// manifest) and fails the create cleanly instead of starting the agent
// unauthenticated. The account reload is unconditional for every real-agent
// box, including custom agents with no injection rules, because
// their native state still belongs to the selected account. A missing secret
// VALUE stays non-fatal — the agent falls back to its own auth.
func userEnvironmentJSON(c *Config) (string, error) {
	if !c.HasRealAccount() {
		return "", c.validateSecretInjection(nil)
	}
	profile, err := agentprofile.Lookup(c.Agent)
	if err != nil {
		return "", fmt.Errorf("resolve agent %q profile for secret injection: %w", c.Agent, err)
	}
	if err := c.validateSecretInjection(profile); err != nil {
		return "", err
	}
	acct, err := account.Load(c.Space, c.Account)
	if err != nil {
		return "", fmt.Errorf("load account %q for container creation: %w", c.Account, err)
	}
	if len(profile.SecretInjection) == 0 && len(c.SecretInjection) == 0 {
		return "", nil
	}
	env := map[string]string{}
	for _, inj := range slices.Concat(profile.SecretInjection, c.SecretInjection) {
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
