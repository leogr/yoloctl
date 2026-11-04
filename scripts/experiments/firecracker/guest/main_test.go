//go:build linux

package main

import (
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/spaceconfig"
)

func validRequest() request {
	return request{Action: "activate", Nonce: "0123456789abcdef0123456789abcdef",
		DiskID: "fedcba9876543210fedcba9876543210", Generation: "00000000000000000000000000000001",
		DiskUUID: "12345678-1234-1234-1234-123456789abc", UnixNano: time.Now().UnixNano()}
}

func TestActivationRejectsMalformedIdentityBeforeGuestIO(t *testing.T) {
	for _, mutate := range []func(*request){
		func(r *request) { r.DiskID = "../../host" },
		func(r *request) { r.Generation = "" },
		func(r *request) { r.Nonce = "replay" },
		func(r *request) { r.DiskUUID = "" },
		func(r *request) { r.UnixNano = 0 },
	} {
		r := validRequest()
		mutate(&r)
		s := service{ready: true, bundle: "test"}
		response := s.handle(r)
		if response.OK || response.Error == "" || s.activated {
			t.Fatalf("invalid identity reached activation: %+v", response)
		}
	}
}

func TestActivationNeverReplayed(t *testing.T) {
	s := service{ready: true, activated: true, bundle: "test"}
	r := s.handle(validRequest())
	if r.OK || r.Error == "" {
		t.Fatalf("activation replay allowed: %+v", r)
	}
	r = s.handle(request{Action: "status"})
	if r.OK {
		t.Fatal("activated guest advertised as reusable clean template")
	}
}

func TestSeedUsesCurrentProductionStoreContract(t *testing.T) {
	b := initialStore()
	boot := &spaceconfig.Bootstrap{Name: "fc-poc", DiskID: validRequest().DiskID, DiskUUID: validRequest().DiskUUID,
		Mounts: []spaceconfig.Mount{{Source: "/poc-workdir", Writable: true}}}
	docs, err := b.Documents(boot)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || len(docs["accounts/trial.yaml"]) == 0 || len(docs["space.yaml"]) == 0 {
		t.Fatalf("invalid initialization inventory: %v", docs)
	}
}
