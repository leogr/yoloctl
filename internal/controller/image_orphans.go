package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
)

type ImageOrphan struct {
	DiskID  string     `json:"disk_id"`
	Phase   ImagePhase `json:"image"`
	Outcome string     `json:"execution_outcome"`
}

var writeImageOrphan = safepath.WriteFile

func (s Receipts) SaveImageOrphan(r Receipt) error {
	if r.Image == nil || !r.abandonedImage() || !r.Image.Residual {
		return errors.New("orphan requires abandoned private image evidence")
	}
	dir, err := PrivateDir(s.Dir, "image-orphans")
	if err != nil {
		return err
	}
	fact := ImageOrphan{DiskID: r.DiskID, Phase: *r.Image, Outcome: "unknown"}
	fact.Phase.OrphanRecorded = true
	raw, err := json.Marshal(fact)
	if err != nil {
		return err
	}
	if err = writeImageOrphan(dir, r.ID+".json", raw, 0600); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stderr, "image preparation %s abandoned: slot released; leftover build work may still use CPU, memory or disk; no acknowledgment required\n", r.ID)
	return nil
}
func (s Receipts) ImageOrphans() ([]ImageOrphan, error) {
	dir := s.Dir + "/image-orphans"
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var facts []ImageOrphan
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			return nil, errors.New("unexpected image orphan entry")
		}
		raw, err := safepath.ReadFile(dir, entry.Name(), MaxDocument)
		if err != nil {
			return nil, err
		}
		var fact ImageOrphan
		if err := decodeStrict(bytes.TrimSpace(raw), &fact); err != nil {
			return nil, err
		}
		p := fact.Phase
		if layout.ValidateIdentity(fact.DiskID) != nil || p.PrivateVersion != 1 || p.JobID+".json" != entry.Name() || p.AbandonedAt.IsZero() || !p.Residual || fact.Outcome != "unknown" {
			return nil, errors.New("invalid image orphan evidence")
		}
		r := Receipt{ID: p.JobID, Method: "image.job", ProofVersion: 2, Scope: &Scope{Writes: []Resource{inputResource(p.StableInput), {Kind: ResourceImages, Name: p.PreparedRef}}}}
		if err := p.validate(r); err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, nil
}
