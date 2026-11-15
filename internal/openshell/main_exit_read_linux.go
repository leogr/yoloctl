package openshell

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/safepath"
)

// ReadMainExit reads PID 1's runtime status for the one generation the caller
// selected: recordPath is that generation's record.json and recordSHA256 the
// hash of the record the caller verified (the selected current run; every
// start prepares a fresh generation, so this binds the exact run). The status
// is the record's own generation's status/runtime.json, decoded strictly
// against that record. It returns the main process's exit code when PID 1
// recorded one (reason main-exited, phase stopping or stopped); false
// otherwise. Reading a code is no claim that the container has stopped.
func ReadMainExit(recordPath, recordSHA256 string) (int, bool, error) {
	return readMainExitAt("/", recordPath, recordSHA256, 0, 0)
}

// readMainExitAt walks from base (production: "/"), which must contain
// recordPath, with no-follow I/O of every component below it.
func readMainExitAt(base, recordPath, recordSHA256 string, uid, gid int) (int, bool, error) {
	if !filepath.IsAbs(recordPath) || filepath.Clean(recordPath) != recordPath || filepath.Base(recordPath) != "record.json" || !lowerHex(recordSHA256, 32) {
		return 0, false, errors.New("invalid selected runtime record")
	}
	raw, err := readPrivateRuntimeFile(base, recordPath, uid, gid)
	if err != nil {
		return 0, false, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != recordSHA256 {
		return 0, false, errors.New("runtime record is not the selected generation")
	}
	var record RuntimeRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return 0, false, err
	}
	if filepath.Base(filepath.Dir(recordPath)) != record.Generation {
		return 0, false, errors.New("runtime record is outside its generation")
	}
	statusRaw, err := readPrivateRuntimeFile(base, filepath.Join(filepath.Dir(recordPath), "status", "runtime.json"), uid, gid)
	if err != nil {
		return 0, false, err
	}
	status, err := DecodeRuntimeStatus(statusRaw, record)
	if err != nil {
		return 0, false, err
	}
	if status.Reason != "main-exited" || status.MainExit == nil {
		return 0, false, nil
	}
	return *status.MainExit, true, nil
}

// readPrivateRuntimeFile reads a private file of uid:gid inside base through
// no-follow I/O of every component below base.
func readPrivateRuntimeFile(base, name string, uid, gid int) ([]byte, error) {
	rel, err := filepath.Rel(base, name)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil, errors.New("runtime file is outside its trusted root")
	}
	raw, info, err := safepath.ReadFileInfo(base, rel, 1<<20)
	if err != nil {
		return nil, err
	}
	if err := privateFileOwner(info, uid, gid); err != nil {
		return nil, err
	}
	return raw, nil
}
