package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
)

// tagImageRows stamps each image with its owning space, preserving order.
func TestTagImageRows(t *testing.T) {
	imgs := []container.Image{
		{Repository: "yoloctl/agentbox", Tag: "ubuntu-24.04"},
		{Repository: "ubuntu", Tag: "24.04"},
	}
	rows := tagImageRows("prod", imgs)
	if len(rows) != 2 {
		t.Fatalf("tagImageRows len = %d, want 2", len(rows))
	}
	for i, r := range rows {
		if r.Space != "prod" {
			t.Errorf("row %d space = %q, want prod", i, r.Space)
		}
		if r.Image.Repository != imgs[i].Repository {
			t.Errorf("row %d repo = %q, want %q", i, r.Image.Repository, imgs[i].Repository)
		}
	}
}

// The table view leads with a SPACE column and stamps every row with its
// owning space — the always-present column that becomes meaningful under
// --all-spaces.
func TestWriteImagesTable_SpaceColumn(t *testing.T) {
	rows := []imageRow{
		{Space: "prod", Image: container.Image{Repository: "yoloctl/agentbox", Tag: "ubuntu-24.04", ID: "a1b2c3", Size: "512MiB", CreatedAt: "2h ago"}},
		{Space: "dev", Image: container.Image{Repository: "ubuntu", Tag: "24.04", ID: "d4e5f6", Size: "78MiB", CreatedAt: "3d ago"}},
	}
	var buf bytes.Buffer
	if err := writeImagesTable(&buf, rows); err != nil {
		t.Fatalf("writeImagesTable: %v", err)
	}
	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header + 2 rows, got %d lines:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[0]), "SPACE") {
		t.Errorf("header should lead with SPACE, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "prod") || !strings.Contains(lines[1], "yoloctl/agentbox") {
		t.Errorf("row 1 missing space/repo: %q", lines[1])
	}
	if !strings.Contains(lines[2], "dev") || !strings.Contains(lines[2], "ubuntu") {
		t.Errorf("row 2 missing space/repo: %q", lines[2])
	}
}

// The JSON view carries a per-object `space` field.
func TestWriteImagesJSON_SpaceField(t *testing.T) {
	rows := []imageRow{
		{Space: "prod", Image: container.Image{Repository: "yoloctl/agentbox", Tag: "ubuntu-24.04", ID: "a1b2c3", Size: "512MiB"}},
	}
	var buf bytes.Buffer
	if err := writeImagesJSON(&buf, rows); err != nil {
		t.Fatalf("writeImagesJSON: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 object, got %d", len(got))
	}
	if got[0]["space"] != "prod" {
		t.Errorf("space field = %v, want prod", got[0]["space"])
	}
	if got[0]["repository"] != "yoloctl/agentbox" {
		t.Errorf("repository field = %v, want yoloctl/agentbox", got[0]["repository"])
	}
}
