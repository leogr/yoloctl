// Package installmigration describes a space as the read-only install
// inventory observes it. Install never runs a space command.
package installmigration

import "errors"

// Snapshot is a space's controller build, VM state, disk identity,
// generation and bootstrap digest at one observation.
type Snapshot struct {
	Space      string `json:"space" yaml:"space"`
	Bundle     string `json:"bundle" yaml:"bundle"`
	State      string `json:"state" yaml:"state"`
	DiskID     string `json:"disk_id" yaml:"disk_id"`
	DiskUUID   string `json:"disk_uuid" yaml:"disk_uuid"`
	Generation string `json:"generation" yaml:"generation"`
	Bootstrap  string `json:"bootstrap" yaml:"bootstrap"`
}

// Validate refuses an incomplete observation.
func (s Snapshot) Validate() error {
	if s.Space == "" || s.Bundle == "" || s.DiskID == "" || s.DiskUUID == "" || s.Generation == "" || len(s.Bootstrap) != 64 || (s.State != "Running" && s.State != "Stopped") {
		return errors.New("incomplete consent snapshot")
	}
	return nil
}
