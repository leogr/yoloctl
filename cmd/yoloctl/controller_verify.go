package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/spacelock"
)

// runControllerVerifier is the read-only recovery verifier. Under the native
// space lock and each entity lock it reads the documents a receipt's proofs
// name and returns their exact bytes and metadata to the controller, which
// alone holds the fingerprint key. It never reconciles, prepares, reloads,
// installs, prunes, imports or replays anything, and writes only lock files.
func runControllerVerifier(ctx context.Context) error {
	c := spaceenv.Current()
	if err := controller.VerifyStoreDirectories(c.DiskMount); err != nil {
		return err
	}
	input := os.NewFile(3, "verifier-request")
	output := os.NewFile(4, "verifier-result")
	if input == nil || output == nil {
		return errors.New("verifier descriptors missing")
	}
	defer func() { _ = input.Close(); _ = output.Close() }()
	var in controller.VerifierInput
	if err := decodeBoundedJSON(input, &in, controller.MaxDocument); err != nil {
		return err
	}
	if err := in.Validate(c.Space); err != nil {
		return err
	}
	unlock, err := spacelock.Acquire(ctx, c.Space, true)
	if err != nil {
		return err
	}
	defer unlock()
	// Lock waits consume the verifier budget. Recheck the live disk/bind
	// after that wait, rather than relying only on process-entry evidence.
	if err := controller.VerifyRuntimeStorage(ctx); err != nil {
		return err
	}
	observed := make([]controller.DocumentIntent, 0, len(in.Documents))
	for _, d := range in.Documents {
		var doc controller.DocumentIntent
		switch d.Kind {
		case controller.DocumentAccount:
			release, err := account.Lock(c.Space, d.Name)
			if err != nil {
				return err
			}
			doc, err = observeAccountDocument(c, d.Name)
			release()
			if err != nil {
				return err
			}
		case controller.DocumentChat:
			doc, err = observeChatDocument()
			if err != nil {
				return err
			}
		default:
			return errors.New("unsupported verifier document")
		}
		observed = append(observed, doc)
	}
	return json.NewEncoder(output).Encode(observed)
}

// observeAccountDocument reads one account declaration without following
// links, with the ownership, mode and link-count facts its proof binds.
func observeAccountDocument(c *spaceenv.Context, name string) (controller.DocumentIntent, error) {
	doc := controller.DocumentIntent{Kind: controller.DocumentAccount, Name: name}
	path, err := layout.AccountPath(c.Space, name)
	if err != nil {
		return doc, err
	}
	rel, err := filepath.Rel(c.ConfigDir, path)
	if err != nil || rel != filepath.Join("accounts", name+".yaml") {
		return doc, errors.New("account declaration escapes the native store")
	}
	raw, info, err := safepath.ReadFileInfo(c.ConfigDir, rel, controller.MaxDocument)
	if errors.Is(err, fs.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return doc, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink != 1 {
		return doc, errors.New("account declaration identity is unavailable")
	}
	doc.Present, doc.Data = true, raw
	doc.UID, doc.GID, doc.Mode = int(st.Uid), int(st.Gid), uint32(info.Mode().Perm())
	return doc, nil
}

// observeChatDocument reads the exact room manifest with Load's private-file,
// disk-identity and prepared-storage checks; it reads no credentials.
func observeChatDocument() (controller.DocumentIntent, error) {
	doc := controller.DocumentIntent{Kind: controller.DocumentChat}
	store, err := controller.NativeChatStore()
	if err != nil {
		return doc, err
	}
	raw, err := store.VerifiedManifest()
	if errors.Is(err, fs.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return doc, err
	}
	doc.Present, doc.Data = true, raw
	doc.UID, doc.GID, doc.Mode = os.Geteuid(), os.Getegid(), 0o600
	return doc, nil
}
