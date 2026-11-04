package chatstream

import (
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/managedchat"
)

func TestChannelsNoMOTDDoesNotRefuseUnrelatedOperations(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "startup", true: "pending-join"}[pending], func(t *testing.T) {
			d, events, requests := channelDriverFixture(t)
			if pending {
				d.channels["#build"].membership = "left"
				if err := d.action(Action{ID: "join", Kind: "join", Channel: "#build"}); err != nil {
					t.Fatal(err)
				}
			}
			*events = nil
			if err := d.record(numericRecord(d, "422", "No MOTD")); err != nil {
				t.Fatal(err)
			}
			if len(*events) != 0 || (d.control != nil) != pending {
				t.Fatalf("No MOTD changed an unrelated operation: %+v", *events)
			}
			if pending {
				if err := d.record(selfRecord(d, "JOIN", "#build")); err != nil {
					t.Fatal(err)
				}
				if len(*requests) != 1 || d.control != nil || (*events)[0].State != "joined" {
					t.Fatal("legitimate JOIN did not confirm after No MOTD")
				}
			}
		})
	}
	for _, field := range []string{"server", "recipient"} {
		t.Run(field, func(t *testing.T) {
			d, _, _ := channelDriverFixture(t)
			r := numericRecord(d, "422", "No MOTD")
			if field == "server" {
				r.Prefix = "foreign"
			} else {
				r.Params[0] = "other"
			}
			if err := d.record(r); err == nil {
				t.Fatal("No MOTD bypassed origin/recipient validation")
			}
		})
	}
}

func TestChannelsJoinAppliesItsTrustedCatalogBeforeAdmission(t *testing.T) {
	for _, backgroundFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "background-after", true: "background-before"}[backgroundFirst], func(t *testing.T) {
			d, events, requests := channelDriverFixture(t)
			old := Catalog{Header: normalizedHeader(d.catalog)}
			next := Catalog{Header: normalizedHeader(d.catalog), RuntimeApplied: true}
			next.Header.Generation++
			next.Header.Channels = append(next.Header.Channels, managedchat.Channel{ID: managedchat.NewID(), Name: "#new", Members: append([]string(nil), next.Header.Channels[0].Members...)})
			if backgroundFirst {
				if err := d.refreshCatalog(old); err != nil {
					t.Fatal(err)
				}
			}
			*events = nil
			if err := d.action(Action{ID: "new-join", Kind: "join", Channel: "#new", Catalog: &next}); err != nil {
				t.Fatal(err)
			}
			if !backgroundFirst {
				if err := d.refreshCatalog(old); err == nil {
					t.Fatal("late background snapshot replaced the joined catalog")
				}
			}
			if len(*requests) != 1 || (*requests)[0].Op != "join" || (*requests)[0].Channel != "#new" {
				t.Fatalf("new catalog did not admit exactly one JOIN: %+v", *requests)
			}
			if len(*events) != 2 || (*events)[0].Kind != "catalog" || (*events)[1].State != "joining" {
				t.Fatalf("membership preceded trusted catalog: %+v", *events)
			}
			if d.control == nil || d.control.Catalog != nil {
				t.Fatal("pending action lost its correlation or retained the catalog")
			}
			if err := d.record(selfRecord(d, "JOIN", "#new")); err != nil {
				t.Fatal(err)
			}
			confirmed := false
			for _, event := range *events {
				confirmed = confirmed || event.Kind == "control" && event.ID == "new-join" && event.State == "confirmed"
			}
			if d.control != nil || !confirmed {
				t.Fatal("authentic membership did not confirm the original action")
			}
		})
	}
}

func TestChannelsJoinCatalogFailuresNeverReachWire(t *testing.T) {
	for _, failure := range []string{"unknown-channel", "stale", "conflict", "foreign", "not-join"} {
		t.Run(failure, func(t *testing.T) {
			d, events, requests := channelDriverFixture(t)
			catalog := Catalog{Header: normalizedHeader(d.catalog)}
			action := Action{ID: "join-new", Kind: "join", Channel: "#new", Catalog: &catalog}
			code := ""
			switch failure {
			case "unknown-channel":
				code = failure
			case "stale":
				catalog.Header.Generation--
			case "conflict":
				catalog.Header.Identities[0].Name = "other-name"
			case "foreign":
				catalog.Header.DiskID = managedchat.NewID()
			case "not-join":
				action.Kind, action.Channel, code = "part", "#space", ""
			}
			if err := d.action(action); err != nil {
				t.Fatal(err)
			}
			if len(*requests) != 0 || d.control != nil || d.channels["#new"] != nil {
				t.Fatal("untrusted/unknown channel was sent, created or left pending")
			}
			last := (*events)[len(*events)-1]
			if last.State != "refused" || last.ID != action.ID || last.Code != code || last.Err == nil {
				t.Fatalf("missing correlated refusal: %+v", last)
			}
			if failure == "unknown-channel" && !strings.Contains(last.Err.Error(), "yoloctl chat channel create new") {
				t.Fatal("unknown managed channel lacks create-first guidance")
			}
		})
	}
}

func TestChannelsRealRefusalRemainsCorrelated(t *testing.T) {
	for _, code := range []string{"403", "474", "482"} {
		t.Run(code, func(t *testing.T) {
			d, events, _ := channelDriverFixture(t)
			d.channels["#build"].membership = "left"
			if err := d.action(Action{ID: "join", Kind: "join", Channel: "#build"}); err != nil {
				t.Fatal(err)
			}
			if err := d.record(numericRecord(d, code, "#build", "refused")); err != nil {
				t.Fatal(err)
			}
			last := (*events)[len(*events)-1]
			if d.control != nil || d.channels["#build"].membership != "left" || last.State != "refused" || !strings.Contains(last.Err.Error(), code) {
				t.Fatalf("real refusal lost its outcome: %+v", last)
			}
			if (last.Code == "acl-denied") != (code == "403") {
				t.Fatalf("wrong ACL classification for %s: %+v", code, last)
			}
		})
	}
}
