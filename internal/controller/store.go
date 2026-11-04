package controller

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

// StoreBundle is accepted only by the root infrastructure initializer, before
// the controller opens admission. It is never an operator RPC payload.
type StoreBundle struct {
	Schema   int                  `json:"schema"`
	Settings spaceconfig.Settings `json:"settings"`
	Boxes    map[string][]byte    `json:"boxes"`
	Accounts map[string][]byte    `json:"accounts"`
	Chat     []byte               `json:"chat,omitempty"`
}

func (b StoreBundle) Digest(boot *spaceconfig.Bootstrap) (string, error) {
	docs, err := b.Documents(boot)
	if err != nil {
		return "", err
	}
	return documentDigest(docs), nil
}

type StoreState struct {
	Digest    string `json:"digest,omitempty"`
	Activated bool   `json:"activated"`
}

func InspectStore(disk string) (StoreState, error) {
	var state StoreState
	root := filepath.Join(disk, ".controller")
	ready, err := safepath.ReadFile(root, "ready", 4096)
	if errors.Is(err, fs.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	state.Digest = string(ready)
	if decoded, err := hex.DecodeString(state.Digest); err != nil || len(decoded) != sha256.Size {
		return state, errors.New("invalid store initialization digest")
	}
	active, err := safepath.ReadFile(root, "activated", 4096)
	if errors.Is(err, fs.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if string(active) != state.Digest {
		return state, errors.New("store activation checkpoint mismatch")
	}
	state.Activated = true
	return state, nil
}

func (b StoreBundle) Documents(boot *spaceconfig.Bootstrap) (map[string][]byte, error) {
	if b.Schema != 1 {
		return nil, errors.New("unsupported configuration store schema")
	}
	if _, err := b.Settings.Compose(boot); err != nil {
		return nil, err
	}
	if _, ok := b.Accounts[b.Settings.DefaultAccount]; !ok {
		return nil, errors.New("default account missing from configuration inventory")
	}
	docs := make(map[string][]byte, len(b.Boxes)+len(b.Accounts)+1)
	for name, raw := range b.Accounts {
		if err := account.ValidateName(name); err != nil {
			return nil, err
		}
		c, err := account.ParseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("account %s: invalid declaration", name)
		}
		c.Space = boot.Name
		if c.Name != name || c.SchemaVersion != account.SchemaVersion {
			return nil, fmt.Errorf("account %s: identity or schema mismatch", name)
		}
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("account %s: %w", name, err)
		}
		docs["accounts/"+name+".yaml"] = raw
	}
	for name, raw := range b.Boxes {
		if err := layout.ValidateName("agentbox", name); err != nil {
			return nil, err
		}
		c, err := agentbox.ParseManifest(raw)
		if err != nil {
			return nil, fmt.Errorf("box %s: invalid declaration", name)
		}
		if c.Name != name || c.Space != boot.Name {
			return nil, fmt.Errorf("box %s: identity mismatch", name)
		}
		if err := c.Validate(); err != nil {
			return nil, fmt.Errorf("box %s: %w", name, err)
		}
		if c.Account != "none" {
			if _, ok := b.Accounts[c.Account]; !ok {
				return nil, fmt.Errorf("box %s references missing account %s", name, c.Account)
			}
		}
		docs["boxes/"+name+".yaml"] = raw
	}
	raw, err := yaml.Marshal(b.Settings)
	if err != nil {
		return nil, err
	}
	docs["space.yaml"] = raw
	if len(b.Chat) != 0 {
		room, err := managedchat.Parse(b.Chat)
		if err != nil {
			return nil, err
		}
		if room.DiskID != boot.DiskID {
			return nil, errors.New("managed chat declaration belongs to another disk")
		}
		if raw, ok := b.Boxes[room.Server.Name]; ok {
			box, err := agentbox.ParseManifest(raw)
			if err != nil || box.ID != room.Server.ID {
				return nil, errors.New("managed chat server ownership conflict")
			}
		}
		docs["chat.json"] = b.Chat
	}
	for _, raw := range docs {
		if len(raw) > MaxDocument {
			return nil, errors.New("configuration declaration exceeds size limit")
		}
	}
	return docs, nil
}

func documentDigest(docs map[string][]byte) string {
	raw, _ := json.Marshal(docs) // string keys and byte slices cannot fail
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// PublishStore stages and flushes the complete initial store before renaming
// it into place. Repeated identical initialization is safe until activation;
// an existing differing store is never replaced. Native account files are not
// visited. disk must already have passed UUID/ownership/mount verification.
func PublishStore(disk string, boot *spaceconfig.Bootstrap, bundle StoreBundle) (string, error) {
	docs, err := bundle.Documents(boot)
	if err != nil {
		return "", err
	}
	digest := documentDigest(docs)
	meta, err := PrivateDir(disk, ".controller")
	if err != nil {
		return "", err
	}
	if _, err := safepath.ReadFile(meta, "activated", 4096); err == nil {
		return "", errors.New("configuration store has been activated; refusing initialization replay")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	config := filepath.Join(disk, "config")
	if _, err := os.Lstat(config); err == nil {
		// Handles a crash between publication and the ready checkpoint. Exact
		// inventory equality prevents a retry overwriting later modifications.
		if err := verifyDocuments(config, docs); err != nil {
			return "", err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	} else {
		stage, err := PrivateDir(disk, ".config-"+rand.Text())
		if err != nil {
			return "", err
		}
		defer func() { _ = os.RemoveAll(stage) }()
		for rel, raw := range docs {
			if err := safepath.WriteFile(stage, rel, raw, 0o600); err != nil {
				return "", err
			}
		}
		for _, name := range []string{"boxes", "accounts"} {
			if _, err := PrivateDir(stage, name); err != nil {
				return "", err
			}
		}
		if err := os.Rename(stage, config); err != nil {
			return "", err
		}
		if err := atomicfile.SyncDir(disk); err != nil {
			return "", &atomicfile.CommitError{Err: err}
		}
	}
	if err := safepath.WriteFile(meta, "ready", []byte(digest), 0o600); err != nil {
		return "", err
	}
	return digest, nil
}

func verifyDocuments(root string, docs map[string][]byte) error {
	actual := make(map[string][]byte, len(docs))
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if rel != "." && rel != "boxes" && rel != "accounts" {
				return fmt.Errorf("unexpected directory in unpublished store: %s", rel)
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if _, ok := docs[rel]; !ok {
			return fmt.Errorf("unexpected entry in unpublished store: %s", rel)
		}
		raw, err := safepath.ReadFile(root, rel, MaxDocument)
		if err != nil {
			return err
		}
		actual[rel] = raw
		return nil
	})
	if err != nil {
		return err
	}
	if documentDigest(actual) != documentDigest(docs) {
		return errors.New("existing configuration store differs from initialization; recovery required")
	}
	return nil
}

func ActivateStore(disk string) error {
	meta, err := PrivateDir(disk, ".controller")
	if err != nil {
		return err
	}
	ready, err := safepath.ReadFile(meta, "ready", 4096)
	decoded, decodeErr := hex.DecodeString(strings.TrimSpace(string(ready)))
	if err != nil || decodeErr != nil || len(decoded) != sha256.Size {
		return errors.New("configuration store has no valid initialization checkpoint")
	}
	return safepath.WriteFile(meta, "activated", ready, 0o600)
}

// VerifyStoreDirectories checks existing native storage; it never creates a
// replacement configuration directory when a mount or publication is missing.
func VerifyStoreDirectories(disk string) error {
	for _, rel := range []string{"config", "config/accounts", "config/boxes", ".controller"} {
		path := filepath.Join(disk, rel)
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("open native store %s: %w", rel, err)
		}
		var st unix.Stat_t
		err = unix.Fstat(fd, &st)
		_ = unix.Close(fd)
		if err != nil {
			return err
		}
		if st.Uid != uint32(os.Geteuid()) || st.Mode&0o077 != 0 {
			return fmt.Errorf("native store %s is not private to its owner", rel)
		}
	}
	return nil
}
