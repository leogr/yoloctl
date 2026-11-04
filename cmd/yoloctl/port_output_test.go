package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/port"
)

func TestPortOutputRetainsActualUnitsForMixedTransports(t *testing.T) {
	spec := port.Spec{Agentbox: "web", HostPort: 8080, ContainerPort: 80}
	native, err := port.OpenShellUnitName(spec)
	if err != nil {
		t.Fatal(err)
	}
	services := []port.ForwardService{{Spec: spec, Unit: native, OpenShell: true}, {Spec: spec, Unit: port.UnitName(spec)}}
	var table, output bytes.Buffer
	if err := writePortsTable(&table, services); err != nil {
		t.Fatal(err)
	}
	if strings.Count(table.String(), native) != 1 || strings.Count(table.String(), port.UnitName(spec)) != 1 {
		t.Fatal("table reconstructed the wrong transport", table.String())
	}
	if err := writePortsJSON(&output, services); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(output.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["unit"] != native || rows[1]["unit"] != port.UnitName(spec) {
		t.Fatal("JSON collapsed routes", output.String())
	}
	for _, row := range rows {
		if len(row) != 6 || row["box"] != "web" || row["host_ip"] != "127.0.0.1" || row["published"] != float64(8080) || row["target"] != float64(80) || row["protocol"] != "tcp" {
			t.Fatal("existing JSON contract changed", row)
		}
	}
}
