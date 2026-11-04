package controller

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestChatStreamReleasesAdmissionWithDurableSetupReceipt(t *testing.T) {
	s := &Server{Hello: hello(), Receipts: Receipts{t.TempDir()}, Operations: map[string]Operation{"chat.stream": {Mutates: true}}}
	s.Execute = func(ctx context.Context, _ Principal, _ Request, streams Streams) (int, error) {
		_ = streams.Release()
		bounded, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		// A view must not keep lifecycle drain waiting after setup.
		return 0, s.Drain(bounded)
	}
	req := request("chat.stream")
	if err := invoke(t, s, req, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	r, err := s.Receipts.Read(req.ID)
	if err != nil || !r.SetupComplete || r.State != "complete" {
		t.Fatal("missing durable stream setup proof", r, err)
	}
	if recovery, err := s.Receipts.Recover(); err != nil || recovery {
		t.Fatal("finished view blocked native mutations", recovery, err)
	}
	req.View = true
	if req.Validate() == nil {
		t.Fatal("chat JSONL accepted a private VT view channel")
	}
}
