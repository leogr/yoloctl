package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
)

func TestChatViewCallsCaptureAndLogDiagnostics(t *testing.T) {
	// These are the finite calls used by initial setup, reconnect, nickname
	// changes and identity-guarded attach resolution while the renderer runs.
	for _, method := range []string{"chat.status", "chat.ensure", "chat.nickname", "chat.attach-target"} {
		for _, status := range []int{0, 23} {
			t.Run(method+"/exit="+strconv.Itoa(status), func(t *testing.T) {
				t.Setenv("YOLOCTL_HOME", t.TempDir())
				target := controllerTarget{Space: "work", Call: func(_ctx context.Context, req controller.Request, streams controller.ClientIO) error {
					if req.Method != method {
						t.Fatalf("wrong method %s", req.Method)
					}
					_, _ = io.WriteString(streams.Err, "launcher warning\x1b[31m\n")
					if status != 0 {
						return &controller.ExitError{Status: status}
					}
					return json.NewEncoder(streams.Out).Encode(map[string]string{"result": "ok"})
				}}
				var result map[string]string
				var err error
				withoutCatalogTerminalWrites(t, func() {
					err = callChatViewTargetInto(t.Context(), target, method, struct{}{}, &result)
				})
				if status == 0 {
					if err != nil || result["result"] != "ok" {
						t.Fatalf("result lost: %v %v", result, err)
					}
				} else {
					var exit *controller.ExitError
					if !errors.As(err, &exit) || exit.Status != status || !strings.Contains(err.Error(), "launcher warning") || strings.ContainsAny(err.Error(), "\x1b\r\n") {
						t.Fatalf("unsafe/lost failure: %v", err)
					}
				}
				path, _ := layout.HostDiagnosticsLogPath()
				log, err := os.ReadFile(path)
				if err != nil || !strings.Contains(string(log), method) || !strings.Contains(string(log), "launcher warning") || strings.ContainsRune(string(log), '\x1b') {
					t.Fatalf("missing/unsafe logged diagnostic: %q %v", log, err)
				}
			})
		}
	}
}
