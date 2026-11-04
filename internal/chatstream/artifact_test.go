package chatstream

import (
	"maps"
	"strings"
	"testing"
)

func artifactTags() map[string]string {
	return map[string]string{
		"airc-artifact":        "616fd98a-70e3-470a-b182-bd6e44f99e83",
		"airc-artifact-name":   "notes.txt",
		"airc-artifact-bytes":  "123",
		"airc-artifact-sha256": strings.Repeat("a", 64),
	}
}

func TestArtifactMetadataRequiresCompleteReservedTags(t *testing.T) {
	tags := artifactTags()
	if got := artifactFromTags(tags); got == nil || got.Name != "notes.txt" || got.Bytes != 123 {
		t.Fatal(got)
	}
	for key := range tags {
		bad := maps.Clone(tags)
		delete(bad, key)
		bad["+"+key] = tags[key]
		if artifactFromTags(bad) != nil {
			t.Fatal("client-only or missing tag accepted", key)
		}
	}
	for _, change := range []struct{ key, value string }{
		{"airc-artifact", "http://example.com/image.png"},
		{"airc-artifact", strings.ToUpper(tags["airc-artifact"])},
		{"airc-artifact-name", "../secret.png"},
		{"airc-artifact-name", "a\nb.png"},
		{"airc-artifact-name", strings.Repeat("x", 101)},
		{"airc-artifact-sha256", strings.Repeat("A", 64)},
		{"airc-artifact-bytes", "-1"},
		{"airc-artifact-bytes", "0123"},
		{"airc-artifact-bytes", "9223372036854775808"},
	} {
		bad := maps.Clone(tags)
		bad[change.key] = change.value
		if artifactFromTags(bad) != nil {
			t.Fatal("invalid reserved metadata accepted", change)
		}
	}
}

func TestArtifactMetadataSurvivesLiveAndHistoryWithoutTrustingText(t *testing.T) {
	h := testHeader(t)
	var messages []Message
	d := &driver{header: h, batches: map[string]*historyBatch{"history": {}}, emit: func(e Event) {
		if e.Kind == "message" {
			messages = append(messages, e.Message)
		}
	}}
	tags := artifactTags()
	tags["msgid"], tags["account"] = "first", h.SelfAccount
	r := record{Command: "PRIVMSG", Prefix: h.Nick + "!human@airc", Params: []string{h.Channel, "any display text"}, Tags: tags}
	if err := d.record(r); err != nil {
		t.Fatal(err)
	}
	tags["msgid"], tags["batch"] = "replayed", "history"
	if err := d.record(r); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Artifact == nil || messages[1].Artifact == nil || !messages[1].Replay || *messages[0].Artifact != *messages[1].Artifact {
		t.Fatal(messages)
	}
	r.Tags = map[string]string{"msgid": "fake", "account": h.SelfAccount, "+airc-artifact": tags["airc-artifact"]}
	r.Params[1] = "Artifact notes.txt (123 bytes): http://airc.local/v1/artifacts/" + tags["airc-artifact"]
	if err := d.record(r); err != nil {
		t.Fatal(err)
	}
	if messages[2].Artifact != nil {
		t.Fatal("message text granted artifact authority")
	}
	r.Tags = maps.Clone(tags)
	delete(r.Tags, "account")
	if err := d.record(r); err != nil {
		t.Fatal(err)
	}
	if messages[3].Artifact != nil {
		t.Fatal("unauthenticated account granted artifact authority")
	}
}
