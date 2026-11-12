package main

import "github.com/leogr/yoloctl/internal/audit"

type auditRequest struct {
	Query         audit.Query        `json:"query,omitempty"`
	ArchiveSHA256 string             `json:"archive_sha256,omitempty"`
	Rules         *auditRulesRequest `json:"rules,omitempty"`
}

// Rule content/selection travel over stdin, not the bounded request envelope.
type auditRulesRequest struct {
	Expected string `json:"expected"`
	Name     string `json:"name,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
}
type auditRulesResult struct {
	Architecture string           `json:"architecture"`
	Revision     string           `json:"revision"`
	Rules        audit.RuleSet    `json:"rules"`
	Validation   audit.Validation `json:"validation"`
	Changed      bool             `json:"changed"`
	Status       *audit.Status    `json:"status,omitempty"`
}
