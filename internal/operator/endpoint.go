// Package operator defines the narrow infrastructure shared by provisioning
// and the in-box CLI. Possessing its paths or metadata grants no authority.
package operator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

const (
	Capability       = "yolo.space.operator"
	Root             = "/run/yoloctl/operators"
	Mount            = "/run/yoloctl/operator"
	Socket           = Mount + "/control.sock"
	HelperMount      = "/opt/yoloctl/helper"
	IncarnationLabel = "yoloctl.operator-incarnation"
	CodexSkill       = "/etc/codex/skills/yolo-box-operator"
	ClaudeSkill      = "/etc/claude-code/.claude/skills/yolo-box-operator"
	GeminiSkill      = "/home/sandbox/.agents/skills/yolo-box-operator"
)

type Client struct {
	Space      string `json:"space"`
	Generation string `json:"generation"`
}

type Endpoint struct {
	Box         string `json:"box"`
	Incarnation string `json:"incarnation"`
}

func (e Endpoint) Validate() error {
	if err := layout.ValidateName("agentbox", e.Box); err != nil {
		return err
	}
	b, err := hex.DecodeString(e.Incarnation)
	if err != nil || len(b) != 16 {
		return errors.New("invalid operator incarnation")
	}
	return nil
}

func (e Endpoint) Directory() string { return filepath.Join(Root, e.Incarnation) }

// RemoveBox retires registrations after container removal, or old incarnations
// after a successful replacement. Called only inside native lifecycle code.
func RemoveBox(box, keep string) error {
	if !spaceenv.Native() {
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("operator cleanup requires VM infrastructure")
	}
	return removeBoxAt(Root, box, keep, os.RemoveAll)
}

// The generic path preflights the entire identity inventory before any effect.
// Existing lifecycle admission/publication serialization must remain held.
func removeBoxAt(root, box, keep string, remove func(string) error) error {
	if remove == nil || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("operator cleanup lacks a canonical root or effect owner")
	}
	if err := layout.ValidateName("agentbox", box); err != nil {
		return err
	}
	if keep != "" {
		if err := (Endpoint{Box: box, Incarnation: keep}).Validate(); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 1024 {
		return errors.New("operator cleanup identity inventory exceeds bound")
	}
	var selected []RegistrationIdentity
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("operator cleanup contains an unsafe registration")
		}
		if err := (Endpoint{Box: "inventory", Incarnation: entry.Name()}).Validate(); err != nil {
			return errors.New("invalid operator cleanup registration name")
		}
		raw, err := safepath.ReadFile(root, filepath.Join(entry.Name(), "identity.json"), 4096)
		if err != nil {
			return err
		}
		identity, err := DecodeRegistrationIdentity(raw)
		if err != nil || identity.Incarnation != entry.Name() {
			return errors.New("invalid operator cleanup identity")
		}
		if entry.Name() == keep && identity.Box != box {
			return errors.New("kept operator incarnation belongs to another box")
		}
		if identity.Box == box {
			// Native registrations go too, as on the old path (leogr's cleanup
			// decision): a box whose create failed before its container has
			// only this cleanup, through the missing-container destroy.
			if identity.Incarnation != keep {
				selected = append(selected, identity)
			}
		}
	}
	for _, identity := range selected {
		if err := remove(filepath.Join(root, identity.Incarnation)); err != nil {
			return err
		}
	}
	return nil
}

// Prepare is called only by root VM infrastructure, never by an RPC client.
// The directory is readable but not writable by workloads and is bound read-only.
func (e Endpoint) Prepare() error {
	if !spaceenv.Native() || os.Geteuid() != 0 {
		return errors.New("operator provisioning requires VM infrastructure")
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if err := guardLegacyIdentityPreparation(e.Directory(), e); err != nil {
		return err
	}
	for _, path := range []string{"/run/yoloctl", Root, e.Directory()} {
		if err := os.Mkdir(path, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !st.IsDir() || st.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("operator infrastructure directory is unsafe: %s", path)
		}
		var info unix.Stat_t
		if err := unix.Lstat(path, &info); err != nil {
			return err
		}
		if info.Uid != 0 {
			return errors.New("operator infrastructure must belong to root")
		}
		if err := os.Chmod(path, 0o755); err != nil {
			return err
		}
	}
	if err := prepareLegacyIdentityAt(e.Directory(), e); err != nil {
		return err
	}
	c := spaceenv.Current()
	raw, _ := json.Marshal(Client{Space: c.Space, Generation: c.Generation})
	return safepath.WriteFile(e.Directory(), "client.json", raw, 0o644)
}

func Provision(ctx context.Context, box string) (Endpoint, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Endpoint{}, err
	}
	e := Endpoint{Box: box, Incarnation: hex.EncodeToString(nonce[:])}
	if err := e.Prepare(); err != nil {
		return Endpoint{}, err
	}
	return e, waitForEndpoint(ctx, e)
}

// waitForEndpoint waits for the controller to serve e's socket.
func waitForEndpoint(ctx context.Context, e Endpoint) error {
	deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if st, err := os.Lstat(filepath.Join(e.Directory(), "control.sock")); err == nil && st.Mode()&os.ModeSocket != 0 {
			return nil
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("wait for operator endpoint: %w", deadline.Err())
		case <-ticker.C:
		}
	}
}

func LoadClient() (*Client, error) {
	raw, err := safepath.ReadFile(Mount, "client.json", 4096)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var client Client
	if err := json.Unmarshal(raw, &client); err != nil {
		return nil, err
	}
	if err := layout.ValidateName("space", client.Space); err != nil {
		return nil, err
	}
	if client.Generation == "" {
		return nil, errors.New("invalid box operator endpoint metadata")
	}
	return &client, nil
}
