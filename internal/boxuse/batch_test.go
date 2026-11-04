package boxuse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBatchValidatesEntireInputBeforeAttachment(t *testing.T) {
	for _, input := range []string{
		"", "\n", `{"id":"1","op":"screen"} garbage`,
		"{\"id\":\"1\",\"op\":\"screen\"}\n\n",
		"{\"id\":\"1\",\"op\":\"screen\"}\n \t\n",
		`{"id":"1","op":"screen","op":"key"}`,
		`{"id":"1","op":"screen","text":"wrong operation"}`,
		`{"id":"1","op":"text","text":null}`,
		`{"id":"1","op":"key","key":"invalid"}`,
		`{"id":"1","op":"key","key":"enter","modifiers":["invalid"]}`,
		`{"id":"1","op":"text","text":"\u0003"}`,
		`{"id":"1","op":"wait","timeout_ms":60001}`,
		`{"id":"1","op":"history","limit":0}`,
		"{\"id\":\"1\",\"op\":\"text\",\"text\":\"\xff\"}",
		"{\"id\":\"1\",\"op\":\"screen\"}\n{\"id\":\"1\",\"op\":\"screen\"}",
		"{\"id\":\"1\",\"op\":\"detach\"}\n{\"id\":\"2\",\"op\":\"text\",\"text\":\"lost\"}",
		`{"id":"1","op":"text","text":"` + strings.Repeat("x", (16<<10)+1) + `"}`,
		strings.Repeat(" ", 64<<10), strings.Repeat(" ", MaxBatchBytes+1),
	} {
		if _, err := ReadBatch(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted invalid batch %.100q", input)
		}
	}
	var lines strings.Builder
	for i := 0; i < MaxBatchActions; i++ {
		fmt.Fprintf(&lines, "{\"id\":\"%d\",\"op\":\"screen\"}\n", i)
	}
	if _, err := ReadBatch(strings.NewReader(lines.String())); err != nil {
		t.Fatal(err)
	}
	lines.WriteString(`{"id":"too many","op":"screen"}`)
	if _, err := ReadBatch(strings.NewReader(lines.String())); err == nil {
		t.Fatal("accepted too many actions")
	}
}

func testBatch(t *testing.T, lines string) Batch {
	t.Helper()
	b, err := ReadBatch(strings.NewReader(lines))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBatchPreservesIdentityAndPeerMetadataAndStopsOnRejection(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			batch := testBatch(t, "{\"id\":\"prompt Ω\",\"op\":\"text\",\"text\":\"hello\"}\n{\"id\":\"submit\",\"op\":\"key\",\"key\":\"enter\"}\n{\"id\":\"observe\",\"op\":\"screen\"}")
			var out bytes.Buffer
			var seen []string
			err := batch.Run(t.Context(), &out, func(_ context.Context, in io.Reader, out io.Writer) error {
				_, _ = io.WriteString(out, "{\"type\":\"ready\",\"version\":1,\"session\":\"incarnation\",\"client\":\"observer\"}\n")
				decoder, encoder := json.NewDecoder(in), json.NewEncoder(out)
				for {
					var r request
					if err := decoder.Decode(&r); err == io.EOF {
						break
					} else if err != nil {
						return err
					}
					seen = append(seen, r.ID)
					reply := map[string]any{"id": r.ID, "type": "sent"}
					if r.Op == "key" && reject {
						reply["type"], reply["error"] = "error", "fixture rejection"
					}
					if r.Op == "screen" {
						reply["type"], reply["screen"] = "screen", map[string]any{"input": map[string]any{"last_peer": "another participant", "peer_revision": 7}}
					}
					if err := encoder.Encode(reply); err != nil {
						return err
					}
				}
				_, err := io.WriteString(out, "{\"type\":\"detached\"}\n")
				return err
			})
			if reject {
				if err == nil || !strings.Contains(err.Error(), "remaining actions were not sent") {
					t.Fatalf("rejection: %v", err)
				}
				if !reflect.DeepEqual(seen, []string{"prompt Ω", "submit"}) {
					t.Fatalf("sent after rejection: %v", seen)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(seen, []string{"prompt Ω", "submit", "observe"}) {
					t.Fatal(seen)
				}
				if !strings.Contains(out.String(), "another participant") {
					t.Fatal("lost advisory peer metadata")
				}
			}
			if !strings.Contains(out.String(), "incarnation") || !strings.Contains(out.String(), "observer") || !strings.Contains(out.String(), "detached") {
				t.Fatal("lost identity or detach")
			}
		})
	}
}

func TestBatchUncertainDeliveryNeverSendsRemainingActions(t *testing.T) {
	for _, kind := range []string{"input_uncertain", "wrong-id", "eof", "timeout", "exit"} {
		t.Run(kind, func(t *testing.T) {
			batch := testBatch(t, "{\"id\":\"first\",\"op\":\"text\",\"text\":\"hello\"}\n{\"id\":\"second\",\"op\":\"key\",\"key\":\"enter\"}")
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			calls := 0
			var seen []string
			err := batch.Run(ctx, io.Discard, func(ctx context.Context, in io.Reader, out io.Writer) error {
				calls++
				_, _ = io.WriteString(out, "{\"type\":\"ready\",\"version\":1,\"session\":\"s\",\"client\":\"c\"}\n")
				d := json.NewDecoder(in)
				var r request
				if err := d.Decode(&r); err != nil {
					return err
				}
				seen = append(seen, r.ID)
				switch kind {
				case "eof":
					return nil
				case "timeout":
					<-ctx.Done()
					return ctx.Err()
				case "wrong-id":
					_, _ = io.WriteString(out, "{\"id\":\"wrong\",\"type\":\"sent\"}\n")
				default:
					_, _ = fmt.Fprintf(out, "{\"id\":\"first\",\"type\":%q,\"error\":\"uncertain fixture\"}\n", kind)
				}
				if d.Decode(&r) == nil {
					seen = append(seen, r.ID)
				}
				return nil
			})
			if err == nil || calls != 1 || !reflect.DeepEqual(seen, []string{"first"}) {
				t.Fatalf("err=%v calls=%d actions=%v", err, calls, seen)
			}
		})
	}
}

func TestBatchKeepsReceiptFailureAndBoundsBlockedOutput(t *testing.T) {
	batch := testBatch(t, `{"id":"d","op":"detach"}`)
	receipt := errors.New("fixture receipt missing")
	err := batch.Run(t.Context(), io.Discard, func(_ context.Context, in io.Reader, out io.Writer) error {
		_, _ = io.WriteString(out, "{\"type\":\"ready\",\"version\":1,\"session\":\"s\",\"client\":\"c\"}\n")
		var request request
		if err := json.NewDecoder(in).Decode(&request); err != nil {
			return err
		}
		_, _ = io.WriteString(out, "{\"id\":\"d\",\"type\":\"detached\"}\n")
		return receipt
	})
	if !errors.Is(err, receipt) {
		t.Fatalf("lost receipt error: %v", err)
	}
	if !strings.Contains(err.Error(), "per-action results remain valid") {
		t.Fatalf("receipt failure obscured action results: %v", err)
	}
	reader, blocked := io.Pipe()
	defer func() { _ = reader.Close(); _ = blocked.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	err = batch.Run(ctx, blocked, func(ctx context.Context, _ io.Reader, out io.Writer) error {
		_, _ = io.WriteString(out, "{\"type\":\"ready\",\"version\":1,\"session\":\"s\",\"client\":\"c\"}\n")
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked output did not honor deadline: %v", err)
	}
}

func TestBatchWaitUsesOnlyThisAttachmentsRevision(t *testing.T) {
	for _, previous := range []bool{false, true} {
		for _, localRevision := range []uint64{1, 37} {
			lines := ""
			if previous {
				lines = "{\"id\":\"view\",\"op\":\"screen\"}\n"
			}
			// Collision with the normal baseline ID must not reuse an action ID.
			lines += "{\"id\":\"yoloctl-baseline-0\",\"op\":\"wait\",\"timeout_ms\":0}\n{\"id\":\"expert\",\"op\":\"wait\",\"since\":123,\"timeout_ms\":0}"
			batch := testBatch(t, lines)
			var seen []request
			err := batch.Run(t.Context(), io.Discard, func(_ context.Context, in io.Reader, out io.Writer) error {
				_, _ = io.WriteString(out, "{\"type\":\"ready\",\"version\":1,\"session\":\"s\",\"client\":\"c\"}\n")
				d, e := json.NewDecoder(in), json.NewEncoder(out)
				for {
					var r request
					if err := d.Decode(&r); err == io.EOF {
						break
					} else if err != nil {
						return err
					}
					seen = append(seen, r)
					if err := e.Encode(map[string]any{"id": r.ID, "type": r.Op, "screen": map[string]any{"revision": localRevision}}); err != nil {
						return err
					}
				}
				_, err := io.WriteString(out, "{\"type\":\"detached\"}\n")
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(seen) != 3 || seen[0].Op != "screen" || seen[1].Since != localRevision || seen[2].Since != 123 {
				t.Fatalf("wrong local baseline: %+v", seen)
			}
			if !previous && seen[0].ID == seen[1].ID {
				t.Fatal("baseline reused an action ID")
			}
		}
	}
}
