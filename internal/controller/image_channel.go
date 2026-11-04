package controller

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"

	"github.com/leogr/yoloctl/internal/imagejob"
)

type ImageRequest struct {
	Box       string      `json:"box,omitempty"`
	Remote    bool        `json:"remote,omitempty"`
	Action    string      `json:"action"`
	Recipe    ImageRecipe `json:"recipe,omitempty"`
	Reference string      `json:"reference,omitempty"`
	JobID     string      `json:"job_id,omitempty"`
}
type ImageReply struct {
	Pin          PinnedImage           `json:"pin,omitempty"`
	LocalTarget  *imagejob.LocalTarget `json:"local_target,omitempty"`
	LocalMissing bool                  `json:"local_missing,omitempty"`
	Digest       string                `json:"digest,omitempty"`
	Platform     string                `json:"platform,omitempty"`
	Uncertain    bool                  `json:"uncertain,omitempty"`
	Error        string                `json:"error,omitempty"`
}
type ImageService interface {
	Resolve(context.Context, string) (string, string, error)
	ResolveRemote(context.Context, string) (string, string, error)
	Publish(context.Context, PinnedImage, string) error
}
type ImageCall func(context.Context, ImageRequest) (ImageReply, error)

// Image uses the same private, serialized checkpoint descriptor, before or
// after apply permission as appropriate. Terminal output has no authority.
func (c *WorkerCheckpoints) Image(ctx context.Context, r ImageRequest) (ImageReply, error) {
	raw, transportErr := c.SharedChannel().Image(ctx, r)
	return receivedImageOutcome(ctx, raw, transportErr)
}

// Only COMPLETE physical frames are passed by Channel.Image. Known typed
// parent outcomes survive a concurrent cancellation/error. Partial data never
// reaches semantic interpretation. No callback or alternate source is used.
func receivedImageOutcome(ctx context.Context, raw []byte, transportErr error) (ImageReply, error) {
	var reply ImageReply
	if len(raw) == 0 {
		if transportErr == nil {
			transportErr = errors.New("complete private reply unavailable")
		}
		return reply, transportErr
	}
	if err := decodeStrict(raw, &reply); err != nil {
		return ImageReply{}, errors.Join(transportErr, errors.New("invalid worker checkpoint"))
	}
	if reply.Error != "" {
		cause := errors.Join(errors.New(reply.Error), transportErr)
		if reply.Uncertain {
			return reply, &EffectsUncertain{Cause: cause}
		}
		return reply, cause
	}
	return reply, errors.Join(transportErr, ctx.Err())
}
func serveImageCheckpoint(ctx context.Context, file io.Writer, call ImageCall, r ImageRequest) error {
	reply := ImageReply{}
	if call == nil {
		reply.Error = "controller image service unavailable"
	} else {
		result, err := call(ctx, r)
		reply = result
		if err != nil {
			reply.Error = err.Error()
			var uncertain *EffectsUncertain
			reply.Uncertain = errors.As(err, &uncertain)
		}
	}
	raw, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(raw)))
	return writeAll(file, append(header, raw...))
}
