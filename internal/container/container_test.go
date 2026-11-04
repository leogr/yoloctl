package container

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseContainers(t *testing.T) {
	input := `{"ID":"abc","Names":"my-ab","Image":"yoloctl/agentbox:ubuntu-24.04","Status":"Up 5 minutes","Labels":"yoloctl.agentbox=my-ab,yoloctl.space=default"}
{"ID":"def","Names":"other","Image":"alpine","Status":"Exited (0) 1 minute ago","Labels":""}
`
	got, err := parseContainers(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseContainers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d containers, want 2", len(got))
	}
	if got[0].Name != "my-ab" || !strings.Contains(got[0].Status, "Up") {
		t.Errorf("first container mismatch: %+v", got[0])
	}
	if got[1].Name != "other" || !strings.Contains(got[1].Status, "Exited") {
		t.Errorf("second container mismatch: %+v", got[1])
	}
}

func TestParseContainersEmpty(t *testing.T) {
	got, err := parseContainers(strings.NewReader(""))
	if err != nil {
		t.Fatalf("parseContainers: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d containers, want 0", len(got))
	}
}

func TestParseInspect(t *testing.T) {
	// Trimmed-down nerdctl inspect output. The real thing carries
	// HostConfig, NetworkSettings, etc., but parseInspect should
	// only pluck the fields Details declares.
	input := []byte(`[
  {
    "Id": "abc123",
    "Image": "sha256:1234567890abcdef",
    "Name": "/coder",
    "Path": "/usr/local/bin/yoloctl-entrypoint",
    "Args": ["/opt/openshell/bin/openshell-sandbox", "--workdir", "/workdir/tmp/myrepo", "--", "sleep", "infinity"],
    "Created": "2026-05-19T10:00:00Z",
    "State": {"Status": "running", "Running": true},
    "Config": {
      "Image": "yoloctl/agentbox:ubuntu-24.04",
      "Labels": {"yoloctl.agentbox": "coder", "yoloctl.space": "default", "yoloctl.project": "myrepo"},
      "Env": ["PATH=/usr/bin", "YOLOCTL_HOST_UID=1000"],
      "WorkingDir": "/unrelated-image-default"
    },
    "HostConfig": {"CapAdd": ["SYS_ADMIN", "NET_ADMIN"]},
    "Mounts": [
      {"Source": "/tmp/myrepo", "Destination": "/workdir", "Mode": "", "RW": true},
      {"Source": "/etc/yoloctl/policy.yaml", "Destination": "/etc/openshell/policy.yaml", "Mode": "ro", "RW": false}
    ]
  }
]`)
	got, err := parseInspect(input, "coder")
	if err != nil {
		t.Fatalf("parseInspect: %v", err)
	}
	if got.Name != "coder" {
		t.Errorf("Name: got %q, want %q (leading slash should be stripped)", got.Name, "coder")
	}
	if got.Image != "yoloctl/agentbox:ubuntu-24.04" {
		t.Errorf("Image: got %q", got.Image)
	}
	if got.ImageID != "sha256:1234567890abcdef" {
		t.Fatalf("lost actual image identity: %q", got.ImageID)
	}
	if !got.Running || got.Status != "running" {
		t.Errorf("State: %+v", got)
	}
	if got.Labels["yoloctl.project"] != "myrepo" {
		t.Errorf("Label yoloctl.project: got %q", got.Labels["yoloctl.project"])
	}
	if len(got.Mounts) != 2 {
		t.Fatalf("Mounts: got %d, want 2", len(got.Mounts))
	}
	if got.Mounts[0].ReadOnly {
		t.Errorf("Mount 0 (workdir): expected RW")
	}
	if !got.Mounts[1].ReadOnly {
		t.Errorf("Mount 1 (policy.yaml): expected RO")
	}
	if len(got.Env) != 2 || got.Env[1] != "YOLOCTL_HOST_UID=1000" {
		t.Errorf("Env: %v", got.Env)
	}
	wantCommand := []string{"/usr/local/bin/yoloctl-entrypoint", "/opt/openshell/bin/openshell-sandbox", "--workdir", "/workdir/tmp/myrepo", "--", "sleep", "infinity"}
	if !reflect.DeepEqual(got.Command, wantCommand) {
		t.Fatalf("lost sandbox launch command: %v", got.Command)
	}
}

func TestParseInspect_EmptyArray(t *testing.T) {
	if _, err := parseInspect([]byte(`[]`), "ghost"); err == nil {
		t.Fatal("expected error on empty inspect array")
	}
}

func TestParseImages(t *testing.T) {
	// nerdctl images --format json emits one JSON object per line.
	input := `{"Repository":"yoloctl/agentbox","Tag":"ubuntu-24.04","ID":"abc123","Size":"512MiB","CreatedAt":"2026-05-18T10:00:00Z"}
{"Repository":"yoloctl/project-myrepo","Tag":"base","ID":"def456","Size":"640MiB","CreatedAt":"2026-05-19T11:00:00Z"}
`
	got, err := parseImages(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseImages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d images, want 2", len(got))
	}
	if got[0].Repository != "yoloctl/agentbox" || got[0].Tag != "ubuntu-24.04" {
		t.Errorf("first image mismatch: %+v", got[0])
	}
	if got[1].ID != "def456" || got[1].Size != "640MiB" {
		t.Errorf("second image mismatch: %+v", got[1])
	}
}

func TestParseImagesEmpty(t *testing.T) {
	got, err := parseImages(strings.NewReader(""))
	if err != nil {
		t.Fatalf("parseImages: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d images, want 0", len(got))
	}
}
