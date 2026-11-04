package agentbox

import (
	"encoding/json"
	"errors"

	"github.com/leogr/yoloctl/internal/container"
)

// retainChatEnvironment adds only the controller's public chat projection to
// an existing private account override. That override intentionally replaces
// image/literal envelopes, so reading the later inspected envelope cannot
// supply chat discovery when account injection is present. Keep the account
// values private; neither inspected ENV nor launch argv receives this map.
func (p *nativeContainerPlan) retainChatEnvironment(c *Config) error {
	if c.chat == nil || p.privateEnv == "" {
		return nil // preserve the existing inspected-envelope fallback
	}
	private, err := nativeChatEnvironmentMap(p.privateEnv)
	if err != nil {
		return err
	}
	// Use the same projection and collision rules as addChatRunOptions, without
	// native directory preparation, image ENV or another account lookup.
	var public container.RunOptions
	if err := addChatEnvironment(c, &public, c.chat); err != nil {
		return err
	}
	projected, err := nativeChatEnvironmentMap(public.Env[0][len(openshellUserEnvironment)+1:])
	if err != nil {
		return err
	}
	for key, value := range projected {
		if _, exists := private[key]; exists {
			return errors.New("account injection overlaps managed chat discovery or temporary directory")
		}
		private[key] = value
	}
	raw, err := json.Marshal(private)
	if err != nil || len(raw) > 1<<20 {
		return errors.New("private chat environment exceeds its bootstrap bound")
	}
	p.privateEnv = string(raw)
	return nil
}

func nativeChatEnvironmentMap(raw string) (map[string]string, error) {
	var values map[string]*string
	if len(raw) > 1<<20 || json.Unmarshal([]byte(raw), &values) != nil || values == nil {
		return nil, errors.New("invalid private chat environment")
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		if value == nil {
			return nil, errors.New("invalid private chat environment")
		}
		result[key] = *value
	}
	return result, nil
}
