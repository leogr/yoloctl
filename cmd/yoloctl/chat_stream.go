package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/chatstream"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/managedchat"
	"golang.org/x/sys/unix"
)

func runControllerChatStream(ctx context.Context, req controller.Request, in *os.File, out, notice io.Writer) error {
	if len(req.Args) != 0 || len(req.Payload) != 0 || req.Terminal || req.View {
		return errors.New("chat stream accepts no command, payload or terminal mode")
	}
	takeOver, err := chatStreamTakeOver(req.Flags)
	if err != nil {
		return err
	}
	store, err := nativeChatStoreFn()
	if err != nil {
		return err
	}
	connection, err := openManagedChatConnectionFn(ctx, req.Space, store)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	r := connection.Room
	var probe bytes.Buffer
	probeCtx, stop := context.WithTimeout(ctx, chatObservationTimeout)
	defer stop()
	if err := connection.Run(probeCtx, []string{"/usr/local/bin/airc", "features"}, nil, &boundedChatWriter{Out: &probe, Remaining: managedchat.ControlLimit}, notice); err != nil {
		return err
	}
	if err := validateManagedChatClientFeatures(probe.Bytes()); err != nil {
		return err
	}
	if err := connection.Verify(probeCtx); err != nil {
		return err
	}
	header, err := chatstream.RoomHeader(r)
	if err != nil {
		return err
	}
	// One human session holds the saved nickname. While another session of
	// this account is connected, refuse before any effect: replacing it is the
	// user's explicit choice, never this stream's. A different account holding
	// the nickname is AIRC's ordinary refusal (433) at registration.
	if !takeOver && connection.Connected(header.HumanAccount) {
		if err := json.NewEncoder(out).Encode(struct {
			Event string `json:"event"`
		}{chatstream.NickHeldEvent}); err != nil {
			return err
		}
		return chatstream.ErrNickHeld
	}
	header.Attachments = managedChatAttachments(probe.Bytes())
	if err := controller.BeginScopedEffects(ctx, chatIntent("")); err != nil {
		return err
	}
	if err := json.NewEncoder(out).Encode(header); err != nil {
		return err
	}
	// The view owns only its transient confined client. Native mutations and
	// lifecycle drain may proceed after this checkpoint, just as for attach.
	if err := releaseSpaceOperation(ctx); err != nil {
		return err
	}
	return relayManagedChatWithExec(ctx, connection.Config, in, out, notice, store, r, header.Nick, takeOver, header.Attachments,
		func(ctx context.Context, _ *agentbox.Config, argv []string, input *os.File, output, errors io.Writer) error {
			return connection.Run(ctx, argv, input, output, errors)
		})
}

func validateManagedChatClientFeatures(probe []byte) error {
	var features managedchat.Features
	if json.Unmarshal(probe, &features) != nil {
		return errors.New("invalid AIRC client feature probe")
	}
	return features.Require("client-unix", "jsonl-v2", "client-presence", "client-topic", chatstream.RegainFeature)
}

// chatStreamTakeOver accepts no flags, or exactly the take-over request.
func chatStreamTakeOver(flags map[string][]string) (bool, error) {
	switch {
	case len(flags) == 0:
		return false, nil
	case len(flags) == 1 && slices.Equal(flags[chatstream.TakeOverFlag], []string{"true"}):
		return true, nil
	}
	return false, errors.New("chat stream accepts only the take-over flag")
}

// managedChatAttachments reports the optional AIRC feature that lets one
// message publish staged images; without it the view sends ordered parts.
func managedChatAttachments(probe []byte) bool {
	var features managedchat.Features
	return json.Unmarshal(probe, &features) == nil && features.Has(chatstream.AttachmentsFeature)
}

// managedChatPython runs the stream's fixed client launcher in the server box.
const managedChatPython = "/usr/bin/python3"

func relayManagedChatWithExec(ctx context.Context, cfg *agentbox.Config, in *os.File, out, notice io.Writer, store managedchat.Store, room *managedchat.Room, nick string, takeOver, attachments bool, exec chatParticipantExec) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	input, err := chatFilterInput(in)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	filtered, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = filtered.Close(); _ = writer.Close() }()
	filteredDone := make(chan error, 1)
	go func() {
		origin := newChatStreamOrigin(room)
		err := chatstream.FilterChannels(input, writer, func(channel string) error {
			return authorizeChatStreamChannel(ctx, store, origin, channel)
		}, attachments)
		// Publish before EOF: a client that exits successfully on EOF must
		// not let cleanup discard the refusal that ended it.
		filteredDone <- err
		_ = writer.Close()
		if err != nil {
			cancel()
		}
	}()
	// Fixed argv and private config only: no host tokens, process argv secrets,
	// outer-container exec, shell-side file operations or TCP fallback. The
	// launcher reads the published config in the confined box and passes AIRC
	// an anonymous copy that differs only in this connection's nickname.
	argv := chatstream.ClientArgv(managedChatPython, "/usr/local/bin/airc", managedchat.HumanMount+"/client.toml", nick, takeOver)
	err = exec(ctx, cfg, argv, filtered, out, notice)
	// Preserve a filter failure that already ended the client. Once the client
	// has exited, closing its still-open input is cleanup; the resulting read
	// error must not turn a successful client exit into a protocol failure.
	var filterErr error
	filterFinished := false
	select {
	case filterErr = <-filteredDone:
		filterFinished = true
	default:
	}
	_ = input.Close() // wake the filter even while the host keeps stdin open
	_ = in.Close()    // the worker owns this stdin
	_ = writer.Close()
	if !filterFinished {
		<-filteredDone
	}
	if filterErr != nil && err == nil {
		return filterErr
	}
	return err
}

// The worker inherits a blocking stdin pipe. os.File.Close cannot interrupt a
// read on that original Go file, so give the filter a descriptor registered
// with Go's poller. The worker owns stdin and its shared nonblocking status;
// nothing else reads it. Do not call Fd on the returned file before reading.
func chatFilterInput(in *os.File) (*os.File, error) {
	fd, err := unix.FcntlInt(in.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.SetNonblock(fd, true); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "managed-chat-input"), nil
}

// chatStreamOrigin is what one stream was opened for: the immutable room, disk
// and human, the room generation and the original channel IDs by name.
type chatStreamOrigin struct {
	RoomID, DiskID, HumanID string
	Generation              uint64
	Channels                map[string]string
}

func newChatStreamOrigin(r *managedchat.Room) chatStreamOrigin {
	origin := chatStreamOrigin{RoomID: r.ID, DiskID: r.DiskID, HumanID: r.Human.ID, Generation: r.Generation, Channels: map[string]string{}}
	for _, channel := range r.ChannelCatalog() {
		origin.Channels[channel.Name] = channel.ID
	}
	return origin
}

// Stream lifetime does not freeze the catalog. Recheck immutable room/disk/
// human identity and desired permission from one bounded atomic native manifest
// snapshot. Do not wait behind lifecycle mutation locks on the live stream.
// AIRC independently enforces its currently applied ACL; this is not a grant.
// A name deleted after the stream opened, or now naming another channel ID
// than the original catalog did, needs a new stream even if it was recreated.
func authorizeChatStreamChannel(ctx context.Context, store managedchat.Store, origin chatStreamOrigin, channel string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r, err := store.ReadManifestSnapshot()
	if err != nil {
		return err
	}
	if r.ID != origin.RoomID || r.DiskID != origin.DiskID || r.Human.ID != origin.HumanID || !r.RuntimeEnabled || !r.Prepared {
		return errors.New("managed stream room/disk/identity changed; reconnect explicitly")
	}
	deleted := errors.New("channel was deleted/recreated after this stream opened; open a new chat connection to use it")
	for _, record := range r.DeletedChannels {
		if record.Name == channel && record.Generation > origin.Generation {
			return deleted
		}
	}
	for _, entry := range r.ChannelCatalog() {
		if entry.Name != channel {
			continue
		}
		if id, known := origin.Channels[channel]; known && id != entry.ID {
			return deleted
		}
		for _, memberID := range entry.Members {
			if memberID == origin.HumanID {
				return nil
			}
		}
	}
	return errors.New("channel is outside this human's current managed-room access")
}
