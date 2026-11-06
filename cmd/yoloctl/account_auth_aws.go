package main

import (
	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentprofile"
)

// Stored presence is not credential completeness, provider selection or evidence
// that an existing container received a subsequently changed account secret.
func awsSecretsPresent(profile *agentprofile.Profile, acct *account.Config) bool {
	for _, rule := range profile.SecretInjection {
		switch rule.EnvName() {
		case "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_BEARER_TOKEN_BEDROCK":
			if acct.Secrets[rule.Secret] != "" {
				return true
			}
		}
	}
	return false
}
