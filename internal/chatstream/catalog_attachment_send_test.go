package chatstream

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

// A newer native catalog omits the probed attachment capability. An image
// send after that refresh still passes the real adapter, shared wire exchange
// and controller filter, and its echo confirms the original caller ID.
func TestChannelsImageSendAfterCatalogRefreshUsesSharedExchange(t *testing.T) {
	for _, replyTo := range []string{"", "parent-msgid"} {
		t.Run("reply="+replyTo, func(t *testing.T) {
			d, events, _ := channelDriverFixture(t)
			d.wire.header.Attachments = true
			d.catalog.Attachments = true
			// RunChannels copies the probed header into each adapter at startup.
			for _, channel := range d.wire.header.Channels {
				d.addChannel(channel.Name, "joined")
				d.channels[channel.Name].bootstrap = "ready"
			}
			// Drop the fixture's exchange stub: pre-write validation and Write
			// are production code, as RunChannels configures them.
			d.wire.exchangeFn = nil
			d.wire.allowedChannel = func(channel string) bool { return d.channels[channel] != nil }
			d.wire.in = io.NopCloser(strings.NewReader(`{"id":"q1","ok":true,"result":{"receipt":"r","state":"pending"}}` + "\n"))
			d.wire.read = bufio.NewReaderSize(d.wire.in, ResponseLimit+1)
			output := &writeCloser{}
			d.wire.out = output

			next := normalizedHeader(d.catalog)
			next.Generation++
			next.Attachments = false // catalog reads omit the connection probe
			if err := d.refreshCatalog(Catalog{Header: next}); err != nil {
				t.Fatal(err)
			}
			last := (*events)[len(*events)-1]
			if last.Catalog == nil || last.Catalog.Header.Generation != next.Generation || !last.Catalog.Header.Attachments || !d.wire.header.Attachments || !d.channels["#space"].adapter.header.Attachments {
				t.Error("newer catalog was refused or dropped the connection's attachment capability")
			}

			// "é" is two bytes: the marker starts at byte 7, not rune 6.
			text := "é see [Image #1]"
			sent := Send{ID: "local-image", Channel: "#space", Text: text, ReplyTo: replyTo, Attach: []Attach{{ID: attachA, At: 7}}}
			if err := d.send(sent); err != nil {
				t.Fatalf("image send failed after the refresh (%d bytes written): %v", output.Len(), err)
			}
			line := output.String()
			var request Request
			if strings.Count(line, "\n") != 1 || json.Unmarshal([]byte(line), &request) != nil {
				t.Fatalf("want exactly one complete request, got %q", line)
			}
			if request.Op != "send" || request.Target != "#space" || request.Text != text || request.ReplyTo != replyTo || len(request.Attach) != 1 || request.Attach[0] != sent.Attach[0] {
				t.Fatalf("wire request differs from the send: %+v", request)
			}
			var authorized []string
			var filtered strings.Builder
			err := FilterChannels(strings.NewReader(line), &filtered, func(channel string) error {
				authorized = append(authorized, channel)
				if channel != "#space" {
					return errors.New("channel is not authorized")
				}
				return nil
			}, true)
			if err != nil || filtered.String() != line || len(authorized) != 1 || authorized[0] != "#space" {
				t.Fatalf("controller filter refused or changed the canonical request: %v %q", err, filtered.String())
			}

			echo := selfRecord(d, "PRIVMSG", "#space", text)
			echo.Tags = map[string]string{"msgid": "stable", "account": d.wire.header.SelfAccount, "+airc-send": "r", "airc-artifacts": attachA + "/3/" + strings.Repeat("b", 64) + "/7/paste-a.png"}
			if replyTo != "" {
				echo.Tags["+reply"] = replyTo
			}
			if err := d.record(echo); err != nil {
				t.Fatal(err)
			}
			var deliveries []Event
			echoed := false
			for _, event := range *events {
				if event.Kind == "delivery" {
					deliveries = append(deliveries, event)
				}
				if event.Kind == "message" && event.Message.ClientID == sent.ID && event.Message.ReplyTo == replyTo {
					echoed = true
				}
			}
			if len(deliveries) != 1 || deliveries[0].ID != sent.ID || deliveries[0].State != "confirmed" || !echoed || d.pendingSend() || output.String() != line {
				t.Fatalf("echo did not confirm exactly one send: deliveries=%+v echoed=%v pending=%v", deliveries, echoed, d.pendingSend())
			}
		})
	}
}
