package chatstream

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCatalogReadIdentityIsLocalAndSurvivesDriverOutcome(t *testing.T) {
	d, events, requests := channelDriverFixture(t)
	read := CatalogRead{Link: 31, Sequence: 17}
	c := Catalog{Header: normalizedHeader(d.catalog), Read: read}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	plain := c
	plain.Read = CatalogRead{}
	want, _ := json.Marshal(plain)
	if !bytes.Equal(raw, want) {
		t.Fatal("local correlation leaked into serialized catalog")
	}
	d.receiveCatalog(c)
	last := (*events)[len(*events)-1]
	if last.Catalog == nil || last.Catalog.Read != read || len(*requests) != 0 {
		t.Fatal("acceptance lost identity or caused an action", last)
	}
	c.Header.Generation--
	c.Read.Sequence++
	d.receiveCatalog(c)
	last = (*events)[len(*events)-1]
	if last.State != "refused" || last.Catalog != nil || last.CatalogRead != c.Read || last.Err == nil || len(*requests) != 0 {
		t.Fatal("refusal lost identity or changed authority", last)
	}
	raw, _ = json.Marshal(last)
	last.CatalogRead = CatalogRead{}
	want, _ = json.Marshal(last)
	if !bytes.Equal(raw, want) {
		t.Fatal("local refusal correlation leaked into serialized event")
	}
}
