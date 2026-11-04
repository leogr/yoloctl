package agentbox

import "github.com/leogr/yoloctl/internal/operator"

// OperatorEnabled resolves the same positive grant used for policy composition.
func OperatorEnabled(c *Config) (bool, error) {
	effective, err := EffectiveCapabilities(c)
	if err != nil {
		return false, err
	}
	return effective.Has(operator.Capability), nil
}
