package main

import (
	"context"
	"errors"
	"testing"

	"github.com/leogr/yoloctl/internal/chatui"
)

func TestChatBrowserPolicyAndValidation(t *testing.T) {
	for _, policy := range []string{"on", "ask", "off", "unknown"} {
		t.Run(policy, func(t *testing.T) {
			var opened []string
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			opts := chatui.Options{}
			configureChatBrowser(ctx, &opts, policy, func(_ context.Context, url string) error {
				opened = append(opened, url)
				return nil
			})
			if opts.ConfirmLinks != (policy != "on") || (opts.OpenLink == nil) != (policy == "off") {
				t.Fatalf("browser policy lost: %+v", opts)
			}
			if policy == "off" {
				return
			}
			for _, url := range []string{"file:///etc/passwd", "https://user:pass@example.org", "https://example.org/\x1b[2J", "https://example.org/\u202ehidden"} {
				msg := opts.OpenLink(url)().(chatui.ActionResultMsg)
				if msg.Err == nil || len(opened) != 0 {
					t.Fatalf("invalid URL reached opener: %q", url)
				}
			}
			const url = "https://example.org/fixed?q=one&second=two"
			if msg := opts.OpenLink(url)().(chatui.ActionResultMsg); msg.Err != nil || len(opened) != 1 || opened[0] != url {
				t.Fatalf("validated fixed URL did not reach opener: %+v, %v", msg, opened)
			}
			cancel()
			if msg := opts.OpenLink(url)().(chatui.ActionResultMsg); !errors.Is(msg.Err, context.Canceled) || len(opened) != 1 {
				t.Fatalf("canceled view opened browser: %+v, %v", msg, opened)
			}
		})
	}
}
