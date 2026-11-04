package controller

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
	digest "github.com/opencontainers/go-digest"
)

// BlockerSummary is a closed, bounded advisory vocabulary. It contains no argv,
// flags, account names, payloads, environment, command text or authority.
type BlockerSummary struct {
	Method string `json:"method,omitempty"`
	Box    string `json:"box,omitempty"`
	Digest string `json:"digest,omitempty"`
}

func (s BlockerSummary) String() string {
	out := strings.ReplaceAll(s.Method, ".", " ")
	if s.Box != "" {
		out += " " + s.Box
	}
	if s.Digest != "" {
		out += " (image " + s.Digest + ")"
	}
	return out
}
func summaryForRequest(r Request) BlockerSummary {
	summary := BlockerSummary{Method: r.Method}
	if len(r.Method) > 128 || strings.ContainsAny(r.Method, "\r\n\x1b\t") {
		summary.Method = "operation"
	}
	if strings.HasPrefix(r.Method, "box.") && len(r.Args) > 0 {
		name := r.Args[0]
		if r.Method == "box.copy" && len(r.Args) > 1 {
			name = r.Args[1]
		}
		if layout.ValidateName("box", name) == nil {
			summary.Box = name
		}
	}
	return summary
}

type AdvisoryNotice struct {
	Action    string `json:"action"`
	Token     string `json:"token,omitempty"`
	Space     string `json:"space,omitempty"`
	Exclusive bool   `json:"exclusive,omitempty"`
}
type AdvisoryReply struct {
	Holder *BlockerSummary `json:"holder,omitempty"`
}
type AdvisoryCall func(context.Context, AdvisoryNotice) (AdvisoryReply, error)
type lockAdvisory struct {
	owner, token string
	summary      BlockerSummary
	ctx          context.Context
}

func (s *Server) advisory(ctx context.Context, request Request, notice AdvisoryNotice) (AdvisoryReply, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch notice.Action {
	case "acquired":
		if notice.Space == s.Hello.Space && notice.Exclusive && notice.Token != "" && len(notice.Token) <= 80 {
			s.lockHolder = &lockAdvisory{owner: request.ID, token: notice.Token, summary: summaryForRequest(request), ctx: ctx}
		}
	case "released":
		if s.lockHolder != nil && s.lockHolder.owner == request.ID && s.lockHolder.token == notice.Token {
			s.lockHolder = nil
		}
	case "blocker":
		if h := s.lockHolder; h != nil && h.ctx.Err() == nil {
			copy := h.summary
			return AdvisoryReply{Holder: &copy}, nil
		}
	default:
		return AdvisoryReply{}, errors.New("unknown advisory action")
	}
	return AdvisoryReply{}, nil
}
func (s *Server) clearAdvisory(owner string) {
	s.mu.Lock()
	if s.lockHolder != nil && s.lockHolder.owner == owner {
		s.lockHolder = nil
	}
	s.mu.Unlock()
}
func (c *WorkerCheckpoints) Advisory(ctx context.Context, n AdvisoryNotice) (AdvisoryReply, error) {
	var reply AdvisoryReply
	raw, transportErr := c.SharedChannel().Advisory(ctx, n)
	if len(raw) == 0 {
		if transportErr == nil {
			transportErr = errors.New("complete private reply unavailable")
		}
		return reply, transportErr
	}
	if err := decodeStrict(raw, &reply); err != nil {
		return AdvisoryReply{}, errors.Join(transportErr, errors.New("invalid worker checkpoint"))
	}
	return reply, transportErr
}
func serveAdvisory(ctx context.Context, file io.Writer, call AdvisoryCall, n AdvisoryNotice) error {
	var reply AdvisoryReply
	if call != nil {
		result, _ := call(ctx, n)
		reply = result
	}
	raw, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(raw)))
	return writeAll(file, append(header, raw...))
}

type ImageWait struct {
	Kind     string
	JobID    string
	Blockers []BlockerSummary
}

func (w ImageWait) String() string {
	out := "Waiting for " + w.Kind
	if w.JobID != "" {
		out += " " + w.JobID
	}
	if len(w.Blockers) != 0 {
		out += " held by "
		for i, b := range w.Blockers {
			if i > 0 {
				out += ", "
			}
			out += b.String()
		}
	}
	return out + "; Ctrl-C cancels.\n"
}
func boundedImageSummary(s BlockerSummary, p PinnedImage) BlockerSummary {
	if digest.Digest(p.Digest).Validate() == nil {
		s.Digest = p.Digest
	}
	return s
}
