package container

import (
	"fmt"
	"testing"
	"time"
)

func TestInspectDoesNotInferUnreportedPrivilege(t *testing.T) {
	for _, hostConfig := range []string{`{}`, `{"Privileged":null}`, `{"Privileged":false}`} {
		t.Run(hostConfig, func(t *testing.T) {
			details, err := parseInspect([]byte(fmt.Sprintf(`[{"Name":"box","HostConfig":%s}]`, hostConfig)), "box")
			if err != nil {
				t.Fatal(err)
			}
			if hostConfig == `{"Privileged":false}` {
				if details.Privileged == nil || *details.Privileged {
					t.Fatal("explicit false privilege record was not preserved")
				}
			} else if details.Privileged != nil {
				t.Fatal("unreported privilege must remain unknown")
			}
		})
	}
}

func TestParseRuntimeStates(t *testing.T) {
	raw := []byte(`[
 {"Id":"abc","Name":"/api","State":{"Running":true,"Pid":4242,"StartedAt":"2026-10-16T10:00:00.5Z"},"NetworkSettings":{"IPAddress":"10.4.0.12"}},
 {"Id":"def","Name":"stopped","State":{"Running":false,"Pid":99,"StartedAt":"0001-01-01T00:00:00Z"},"NetworkSettings":{"IPAddress":"10.4.0.13"}},
 {"Id":"ghi","Name":"nostart","State":{"Running":true,"Pid":7},"NetworkSettings":{}}
]`)
	states, err := parseRuntimeStates(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 3 {
		t.Fatalf("got %d states", len(states))
	}
	api := states[0]
	if api.Name != "api" || api.ID != "abc" || api.PID != 4242 || api.IP != "10.4.0.12" || !api.StartedAt.Equal(time.Date(2026, 10, 16, 10, 0, 0, 5e8, time.UTC)) {
		t.Fatalf("api = %+v", api)
	}
	// A stopped container's stale PID and address must never be sampled.
	if s := states[1]; s.Running || s.PID != 0 || s.IP != "" || !s.StartedAt.IsZero() {
		t.Fatalf("stopped = %+v", s)
	}
	if s := states[2]; !s.StartedAt.IsZero() || s.PID != 7 {
		t.Fatalf("missing start time = %+v", s)
	}
	if _, err := parseRuntimeStates([]byte("{")); err == nil {
		t.Fatal("malformed inspect accepted")
	}
	if states, err := parseRuntimeStates(nil); err != nil || states != nil {
		t.Fatalf("empty = %v %v", states, err)
	}
}

// The batched inspect carries the launch-mode evidence Inspect would: the
// privileged record, PID 1 argv, mounts and labels. A malformed resource
// field drops only that entry's details, never its identity.
func TestParseRuntimeStatesCarriesLaunchEvidence(t *testing.T) {
	raw := []byte(`[
 {"Id":"p1","Name":"/yolo-dev","Path":"/usr/local/bin/yoloctl-entrypoint",
  "Args":["/opt/yoloctl/bin/yoloctl","box-endpoint-internal","--protocol","1","--workdir","/workdir","--ssh-socket-path","/run/openshell/ssh.sock"],
  "State":{"Status":"running","Running":true,"Pid":900,"StartedAt":"2026-10-27T09:00:00Z"},
  "Config":{"Labels":{"yoloctl.agentbox":"yolo-dev","yoloctl.runtime_mode":"privileged","yoloctl.launcher":"yoloctl-box-endpoint/1"},"Env":["SECRET=x"]},
  "HostConfig":{"Privileged":true,"Memory":0,"CpuQuota":-1},
  "Mounts":[{"Source":"/opt/yoloctl/bin","Destination":"/opt/yoloctl/bin","Mode":"ro","RW":false},{"Source":"/src","Destination":"/workdir","Mode":"","RW":true}],
  "NetworkSettings":{"IPAddress":"10.4.0.20"}},
 {"Id":"c1","Name":"/api","Path":"/usr/local/bin/yoloctl-entrypoint",
  "Args":["/opt/openshell/bin/openshell-sandbox","--workdir","/workdir","--","sleep","infinity"],
  "State":{"Status":"running","Running":true,"Pid":901},
  "Config":{"Labels":{"yoloctl.agentbox":"api"}},
  "HostConfig":{"Privileged":false,"Memory":0},
  "Mounts":[{"Source":"/src","Destination":"/workdir","RW":true}],
  "NetworkSettings":{"IPAddress":"10.4.0.21"}},
 {"Id":"m1","Name":"/odd","State":{"Running":true,"Pid":902},"HostConfig":{"Memory":-1,"Privileged":true},"NetworkSettings":{"IPAddress":"10.4.0.22"}}
]`)
	states, err := parseRuntimeStates(raw)
	if err != nil || len(states) != 3 {
		t.Fatalf("states = %+v %v", states, err)
	}
	priv, conf, odd := states[0], states[1], states[2]
	if priv.Name != "yolo-dev" || priv.ID != "p1" || priv.PID != 900 || priv.IP != "10.4.0.20" || priv.StartedAt.IsZero() {
		t.Fatalf("privileged identity = %+v", priv)
	}
	d := priv.Details
	if d == nil || d.Privileged == nil || !*d.Privileged || d.Labels["yoloctl.runtime_mode"] != "privileged" ||
		len(d.Command) != 9 || d.Command[0] != "/usr/local/bin/yoloctl-entrypoint" || d.Command[2] != "box-endpoint-internal" ||
		len(d.Mounts) != 2 || d.Mounts[0] != (DetailMount{Source: "/opt/yoloctl/bin", Destination: "/opt/yoloctl/bin", ReadOnly: true}) {
		t.Fatalf("privileged details = %+v", d)
	}
	if d := conf.Details; d == nil || d.Privileged == nil || *d.Privileged || d.Labels["yoloctl.runtime_mode"] != "" || d.Mounts[0].ReadOnly {
		t.Fatalf("confined details = %+v", d)
	}
	if odd.Details != nil || odd.PID != 902 || odd.IP != "10.4.0.22" {
		t.Fatalf("malformed entry = %+v", odd)
	}
}
