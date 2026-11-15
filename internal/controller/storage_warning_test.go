package controller

import (
	"bytes"
	"encoding/json"
	"net"
	"testing"
)

func TestStorageNoticeKeepsPingAuthorizationAndOrdinaryOutput(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested bool
		operator  bool
		draining  bool
	}{
		{name: "ordinary"}, {name: "requested", requested: true},
		{name: "operator", requested: true, operator: true},
		{name: "maintenance", requested: true, draining: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := storeServer(t)
			s.draining = tc.draining
			reads := 0
			s.StorageWarning = func() string { reads++; return "VM system disk is below the image reserve" }
			req := request("control.ping")
			if tc.requested {
				req.Flags = map[string][]string{StorageWarningFlag: {"true"}}
			}
			principal := Principal{}
			if tc.operator {
				principal = Principal{Box: "worker", Incarnation: "run"}
			}
			server, client := net.Pipe()
			done := make(chan struct{})
			go func() { defer close(done); s.ServeConn(t.Context(), server, principal) }()
			var out bytes.Buffer
			err := Call(t.Context(), client, s.Hello, req, ClientIO{Out: &out})
			_ = client.Close()
			take(t, done)
			allowed := !tc.operator && !tc.draining
			if (err == nil) != allowed || reads != boolInt(tc.requested && allowed) {
				t.Fatalf("reads=%d err=%v", reads, err)
			}
			if tc.requested && allowed {
				var note string
				if err := json.Unmarshal(out.Bytes(), &note); err != nil || note != "VM system disk is below the image reserve" {
					t.Fatal(out.String(), err)
				}
			} else if out.Len() != 0 {
				t.Fatal("ordinary/refused ping acquired output", out.String())
			}
		})
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
