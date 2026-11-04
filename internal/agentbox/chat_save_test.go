package agentbox

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

func TestChatServerSaveRejectsForeignImageWithoutPublishing(t *testing.T) {
	// Native context is intentionally installed once per process. Keep this
	// private-filesystem fixture out of the rest of the package's host tests.
	if os.Getenv("YOLOCTL_TEST_CHAT_NATIVE_SAVE") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestChatServerSaveRejectsForeignImageWithoutPublishing$")
		cmd.Env = append(os.Environ(), "YOLOCTL_TEST_CHAT_NATIVE_SAVE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("native save fixture: %v\n%s", err, out)
		}
		return
	}
	disk := t.TempDir()
	if err := os.Mkdir(filepath.Join(disk, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if uid == 0 {
		uid = 1000
	}
	if gid == 0 {
		gid = 1000
	}
	s := managedchat.Store{Disk: disk, DiskID: NewIdentity(), UID: uid, GID: gid}
	i := managedchat.Inventory{Complete: true, Declarations: map[string]string{}, Runtime: map[string]bool{}}
	r, err := s.Ensure("", "person", i)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.EnableRuntime(r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	if err := spaceenv.Configure(spaceenv.Context{Schema: 1, Space: "work", DiskID: s.DiskID, DiskUUID: "fixture", DiskMount: disk, ConfigDir: filepath.Join(disk, "config"), CatalogDir: spaceenv.CatalogRoot, HostConfigRoot: filepath.Join(disk, "host"), UID: uid, GID: gid, Bundle: "fixture", Generation: "fixture"}); err != nil {
		t.Fatal(err)
	}
	spaceenv.UseCatalogSnapshot(filepath.Join(disk, "catalogs"))
	c := &Config{Name: r.Server.Name, ID: r.Server.ID, Space: "work", Agent: "none", Account: "none", Image: agentboximage.RefAgentbox, Workdir: NewEphemeralWorkdir(r.Server.Name), Features: []string{"yolo.chat.skill"}, SessionCommand: ChatServerCommand()}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(c.Path())
	if err != nil {
		t.Fatal(err)
	}
	c.ID = "" // editor omission cannot evade the reserved identity check
	c.Image = "unrelated/server:1"
	if err := c.Save(); err == nil {
		t.Fatal("foreign server image was saved")
	}
	after, err := os.ReadFile(c.Path())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected save changed native declaration", err)
	}
	if c.ID != r.Server.ID {
		t.Fatal("identity removal escaped immutability")
	}
	ordinary := &Config{Name: "ordinary", Space: "work", Agent: "none", Account: "none", Image: agentboximage.RefAgentbox, Workdir: NewEphemeralWorkdir("ordinary")}
	if err := ordinary.Save(); err != nil || ordinary.ID != "" {
		t.Fatal("ordinary native save minted an identity or was blocked", err)
	}
}
