package main

import "github.com/leogr/yoloctl/internal/audit"

type auditRequest struct {
	Query         audit.Query `json:"query,omitempty"`
	ArchiveSHA256 string      `json:"archive_sha256,omitempty"`
}
