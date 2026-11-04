package session

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestHolderInitialTitleAndAgentUpdates(t *testing.T) {
	for _, tc := range []struct {
		name, template, strip, initial, agentTitle, updated string
	}{
		{"default", "", "", "box: ", "native title", "box: native title"},
		{"agent icon", "⋏ leogr's agyagy: {title}", "⋏", "⋏ leogr's agyagy: ", "⋏ native title", "⋏ leogr's agyagy: native title"},
		{"suffix", "{title} · box", "", " · box", "native title", "native title · box"},
		{"literal", "my box", "", "my box", "native title", "my box"},
		{"composition disabled", "{title}", "", "", "native title", "native title"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _ := testPaths(t)
			opts.TitleTemplate, opts.TitleStrip = tc.template, tc.strip
			fs := startFakeSandbox(t, opts.SSHSocket)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			res := runHolderAsync(ctx, opts)
			<-fs.started
			raw := attachWhenReady(t, opts.AttachSocket)
			attachManaged := func() *screenClient {
				conn, err := net.Dial("unix", opts.AttachSocket)
				if err != nil {
					t.Fatal(err)
				}
				return attachScreenConn(t, conn)
			}
			assertTitle := func(c *screenClient, want string) {
				c.mu.Lock()
				defer c.mu.Unlock()
				if got := c.vt.Title(); got != want {
					t.Fatalf("snapshot title = %q, want %q", got, want)
				}
			}
			// The agent has emitted NOTHING: both the first snapshot and a later
			// attachment must already identify the box, without query replies.
			first, second := attachManaged(), attachManaged()
			assertTitle(first, tc.initial)
			assertTitle(second, tc.initial)
			fs.mu.Lock()
			input := fs.stdin.String()
			fs.mu.Unlock()
			if input != "" {
				t.Fatalf("title initialization sent input to agent: %q", input)
			}
			initial := ""
			if tc.template != "{title}" {
				initial = "\x1b]2;" + tc.initial + "\x1b\\"
			}
			waitFor(t, "initial passthrough title", func() bool { return raw.output() == initial })

			// A title split across SSH reads replaces the seed using the normal
			// composition/strip rules. Reattachment must retain the new title.
			fs.emit("hello\x1b]2;" + tc.agentTitle)
			fs.emit("\x1b\\world")
			updated := "hello\x1b]2;" + tc.updated + "\x1b\\world"
			waitFor(t, "updated passthrough title", func() bool { return raw.output() == initial+updated })
			for _, c := range []*screenClient{first, second} {
				waitFor(t, "live managed title", func() bool {
					c.mu.Lock()
					defer c.mu.Unlock()
					return c.vt.Title() == tc.updated && strings.Contains(string(c.effects), "world")
				})
			}
			assertTitle(attachManaged(), tc.updated)
			rejoined := attach(t, ctx, opts.AttachSocket)
			waitFor(t, "passthrough title on reattach", func() bool { return rejoined.output() == initial+updated })
			fs.exit(0)
			if result := <-res; result.err != nil || result.status != 0 {
				t.Fatalf("holder result: %+v", result)
			}
			for _, c := range []*attachClient{raw, rejoined} {
				if err := c.wait(t); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
