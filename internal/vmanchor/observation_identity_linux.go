//go:build linux

package vmanchor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// ObservationIdentity reports correlation from this ORIGINAL retained boot
// source. Matching strings grant no admission, process ownership or cleanup
// authority. It never constructs another source or transfers Close ownership.
func (s *FirecrackerSource) ObservationIdentity() (bootID, launcherFingerprint string, err error) {
	if s == nil {
		return "", "", errors.New("original boot observation source unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.check(); err != nil {
		return "", "", err
	}
	fingerprint, err := observationFingerprintV1(s.boot, s.launcher.identity)
	if err != nil {
		return "", "", err
	}
	if err = s.check(); err != nil {
		return "", "", err
	}
	return strings.Clone(s.boot), fingerprint, nil
}

// Version1 bytes are fixed: UTF-8 domain followed by canonical JSON of these
// ordered fields, including every original launcher identity field. First16
// SHA256 bytes are lowercase hex. This is a stable correlation fingerprint,
// never an unforgeable witness, capability, source reconstruction or VM test.
func observationFingerprintV1(boot string, launcher processIdentity) (string, error) {
	raw, err := json.Marshal(struct {
		Version  int             `json:"version"`
		Boot     string          `json:"boot"`
		Launcher processIdentity `json:"launcher"`
	}{1, boot, launcher})
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("yoloctl/firecracker-launch-observation/1\x00"))
	_, _ = hash.Write(raw)
	return hex.EncodeToString(hash.Sum(nil)[:16]), nil
}
