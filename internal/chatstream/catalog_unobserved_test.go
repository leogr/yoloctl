package chatstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestDesiredCatalogDriverKeepsRuntimeUnobserved(t *testing.T) {
	d, events, requests := channelDriverFixture(t)
	for _, generation := range []uint64{d.catalog.Generation, d.catalog.Generation + 1} {
		c := Catalog{Header: normalizedHeader(d.catalog), RuntimeApplied: true, RuntimeUnobserved: true, Pending: []string{"stale observation"}, Read: CatalogRead{Link: 3, Sequence: generation}}
		c.Header.Generation = generation
		d.receiveCatalog(c)
		e := (*events)[len(*events)-1]
		if e.Catalog == nil || !e.Catalog.RuntimeUnobserved || e.Catalog.RuntimeApplied || len(e.Catalog.Pending) != 0 || e.Catalog.Read != c.Read || len(*requests) != 0 {
			t.Fatal("driver fabricated runtime proof or sent an action", e)
		}
	}
}

func TestStreamHeaderCatalogStartsUnobserved(t *testing.T) {
	d, _, _ := channelDriverFixture(t)
	header := d.wire.header
	var input bytes.Buffer
	encoder := json.NewEncoder(&input)
	if err := encoder.Encode(header); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(map[string]any{"event": "ready", "version": 2, "epoch": "unobserved-startup", "account": header.SelfAccount, "nick": header.Nick, "multiline": map[string]int{"max_bytes": 65536, "max_lines": 1000}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var startup *Catalog
	_ = RunChannels(ctx, io.NopCloser(strings.NewReader(input.String())), nopWriteCloser{io.Discard}, nil, nil, nil, nil, func(event Event) {
		if event.Kind == "catalog" && startup == nil {
			startup = event.Catalog
			cancel()
		}
	})
	if startup == nil || !startup.RuntimeUnobserved || startup.RuntimeApplied || len(startup.Pending) != 0 {
		t.Fatal("stream identity header fabricated runtime status", startup)
	}
}
