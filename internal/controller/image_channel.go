package controller

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/imagejob"
)

type ImageRequest struct {
	Box       string      `json:"box,omitempty"`
	Remote    bool        `json:"remote,omitempty"`
	Action    string      `json:"action"`
	Recipe    ImageRecipe `json:"recipe,omitempty"`
	Reference string      `json:"reference,omitempty"`
	JobID     string      `json:"job_id,omitempty"`
	// Features names some of a composed image's features in a "preparing"
	// notice (display only, at most ProgressFeatures); MoreFeatures counts the
	// rest.
	Features     []string `json:"features,omitempty"`
	MoreFeatures int      `json:"more_features,omitempty"`
}

// ProgressFeatures is how many feature names a preparing notice shows.
const ProgressFeatures = 16

// ProgressFeatureNames is the display part of a feature list: the first
// ProgressFeatures names and the count of the others.
func ProgressFeatureNames(names []string) ([]string, int) {
	if len(names) <= ProgressFeatures {
		return names, 0
	}
	return names[:ProgressFeatures], len(names) - ProgressFeatures
}

// progressFeatures formats the display of a preparing notice's features. It
// refuses only input over its bounds (more than ProgressFeatures names, an
// implausible count); a name that is not plain printable text is counted
// instead of shown, never a reason to stop a preparation.
func progressFeatures(names []string, more int) (string, error) {
	if len(names) > ProgressFeatures || more < 0 || more > 1<<20 {
		return "", errors.New("invalid feature progress")
	}
	shown := make([]string, 0, len(names))
	for _, n := range names {
		if plainName(n) {
			shown = append(shown, n)
		} else {
			more++
		}
	}
	if more > 0 {
		shown = append(shown, "+"+strconv.Itoa(more)+" more")
	}
	if len(shown) == 0 {
		return "", nil
	}
	return " (features: " + strings.Join(shown, ", ") + ")", nil
}

func plainName(n string) bool {
	if n == "" || len(n) > 128 || !utf8.ValidString(n) {
		return false
	}
	for _, r := range n {
		if r <= ' ' || r == 0x7f || unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
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
