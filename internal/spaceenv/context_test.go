package spaceenv

import (
	"os"
	"strings"
	"testing"
)

func validContext() Context {
	return Context{
		Schema: 1, Space: "work", DiskID: strings.Repeat("a", 32), DiskUUID: "uuid",
		DiskMount: "/mnt/native", ConfigDir: "/mnt/native/config", CatalogDir: CatalogRoot,
		HostConfigRoot: "/home/developer/.config/yoloctl", UID: 1000, GID: 20,
		Mounts: []Mount{{Source: "/projects", Writable: true}}, Bundle: "bundle", Generation: "generation",
	}
}

func TestContextRejectsInvalidAuthority(t *testing.T) {
	if err := validContext().Validate(); err != nil {
		t.Fatal(err)
	}
	gzip := validContext()
	gzip.ImageLayerCompression = ImageLayerGzip
	if err := gzip.Validate(); err != nil {
		t.Fatal("gzip layer compression rejected:", err)
	}
	for name, change := range map[string]func(*Context){
		"schema":             func(c *Context) { c.Schema++ },
		"space traversal":    func(c *Context) { c.Space = "../other" },
		"root user":          func(c *Context) { c.UID = 0 },
		"root group":         func(c *Context) { c.GID = 0 },
		"missing disk":       func(c *Context) { c.DiskID = "" },
		"missing UUID":       func(c *Context) { c.DiskUUID = "" },
		"missing bundle":     func(c *Context) { c.Bundle = "" },
		"missing generation": func(c *Context) { c.Generation = "" },
		"config escape":      func(c *Context) { c.ConfigDir = "/host/config" },
		"catalog override":   func(c *Context) { c.CatalogDir = "/tmp/untrusted" },
		"unclean mount":      func(c *Context) { c.Mounts[0].Source = "/projects/../etc" },
		"layer compression":  func(c *Context) { c.ImageLayerCompression = "zstd" },
	} {
		t.Run(name, func(t *testing.T) {
			c := validContext()
			change(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("invalid native authority accepted")
			}
		})
	}
}

func TestContextConfiguredOnceAndConfinedToSpace(t *testing.T) {
	t.Cleanup(func() { current.Store(nil); catalogSnapshot.Store(nil) })
	c := validContext()
	if err := Configure(c); err != nil {
		t.Fatal(err)
	}
	c.Mounts[0].Source = "/changed"
	if Current().Mounts[0].Source != "/projects" {
		t.Fatal("caller mutated the installed mount grants")
	}
	if err := Configure(validContext()); err == nil {
		t.Fatal("controller identity was replaced")
	}
	if RequireSpace("work") != nil || RequireSpace("other") == nil {
		t.Fatal("native scope was not enforced")
	}
	if uid, gid := Identity(); uid != 1000 || gid != 20 {
		t.Fatalf("wrong workload identity: %d:%d", uid, gid)
	}
}

func TestEnvironmentDoesNotAuthorizeNativeExecution(t *testing.T) {
	t.Setenv(ContextEnv, "/tmp/caller-selected-context")
	if Native() {
		t.Fatal("environment selected native execution")
	}
	if os.Geteuid() != 0 {
		if _, err := LoadTrusted(); err == nil {
			t.Fatal("unprivileged caller loaded controller authority")
		}
	}
}
