package main

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/chatui"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

// streamChatTelemetry feeds the participants panel from the same
// space.telemetry read as space view, so box activity comes from one shared
// source; availability stays the channel's own IRC state. It reconnects
// with backoff until ctx ends and, like space view, never boots a VM.
func streamChatTelemetry(ctx context.Context, space string, send func(tea.Msg)) {
	backoff := time.Second
	for {
		got := false
		err := streamSpaceView(ctx, space, chatui.TelemetryInterval, func(f spacetelemetry.Frame) {
			got = true
			send(chatui.TelemetryMsg(f))
		})
		if ctx.Err() != nil {
			return
		}
		if got {
			backoff = time.Second
		}
		send(chatui.TelemetryLostMsg{Err: err})
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 15*time.Second)
	}
}
