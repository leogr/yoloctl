// Package spacearchive implements portable, versioned backups of declarations
// and native account files. VM disks, runtime receipts, host projects and global
// catalogs are outside the archive. Import always targets a new space identity.
package spacearchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spaceconfig"
)

const MaxState = int64(16 << 30)
const maxDeclarations = 64 << 20

type Manifest struct {
	Version     int                   `json:"version"`
	Created     time.Time             `json:"created"`
	Bootstrap   spaceconfig.Bootstrap `json:"bootstrap"`
	StateSize   int64                 `json:"state_size"`
	StateSHA256 string                `json:"state_sha256"`
}

func (m Manifest) Validate() error {
	if m.Version != 1 || m.StateSize <= 0 || m.StateSize > MaxState {
		return errors.New("unsupported space archive version or size")
	}
	sum, err := hex.DecodeString(m.StateSHA256)
	if err != nil || len(sum) != sha256.Size {
		return errors.New("invalid space archive checksum")
	}
	if m.Bootstrap.Import != nil || m.Bootstrap.Initializing || m.Bootstrap.DiskUUID == "" {
		return errors.New("cannot archive an uninitialized space")
	}
	return m.Bootstrap.Validate()
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func decode(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing archive metadata")
	}
	return nil
}

func Write(ctx context.Context, out io.Writer, boot spaceconfig.Bootstrap, state *os.File) error {
	if _, err := state.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, contextReader{ctx, io.LimitReader(state, MaxState+1)})
	if err != nil {
		return err
	}
	m := Manifest{Version: 1, Created: time.Now().UTC(), Bootstrap: boot, StateSize: size, StateSHA256: hex.EncodeToString(hash.Sum(nil))}
	if err := m.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if _, err := state.Seek(0, io.SeekStart); err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := tw.Write(raw); err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "state.tar", Mode: 0600, Size: size, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := io.Copy(tw, contextReader{ctx, state}); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// Read validates the entire gzip/tar and checksum before the caller allocates a
// VM. state is a caller-owned private temporary file, never an extraction path.
func Read(ctx context.Context, in io.Reader, state *os.File) (Manifest, *controller.StoreBundle, error) {
	var m Manifest
	gz, err := gzip.NewReader(contextReader{ctx, in})
	if err != nil {
		return m, nil, err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	h, err := tr.Next()
	if err != nil {
		return m, nil, err
	}
	if h.Name != "manifest.json" || h.Typeflag != tar.TypeReg || h.Size > 1<<20 {
		return m, nil, errors.New("space archive must start with manifest.json")
	}
	raw, err := io.ReadAll(tr)
	if err != nil {
		return m, nil, err
	}
	if err := decode(raw, &m); err != nil {
		return m, nil, err
	}
	if err := m.Validate(); err != nil {
		return m, nil, err
	}
	h, err = tr.Next()
	if err != nil {
		return m, nil, err
	}
	if h.Name != "state.tar" || h.Typeflag != tar.TypeReg || h.Size != m.StateSize {
		return m, nil, errors.New("invalid space archive payload")
	}
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(state, hash), tr); err != nil {
		return m, nil, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != m.StateSHA256 {
		return m, nil, errors.New("space archive checksum mismatch")
	}
	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		return m, nil, errors.New("unexpected space archive member")
	}
	if err := zeroTail(gz); err != nil {
		return m, nil, err
	}
	if err := state.Sync(); err != nil {
		return m, nil, err
	}
	if _, err := state.Seek(0, io.SeekStart); err != nil {
		return m, nil, err
	}
	bundle, err := inspectState(ctx, state, &m.Bootstrap, nil)
	if err != nil {
		return m, nil, err
	}
	return m, bundle, nil
}

func zeroTail(r io.Reader) error {
	// Accept only bounded tar padding after the logical end, not concatenated
	// archives or unbounded compressed zeros. Reading EOF validates gzip CRC.
	var buf [4096]byte
	total := 0
	for {
		n, err := r.Read(buf[:])
		total += n
		if total > 1<<20 {
			return errors.New("excessive archive padding")
		}
		for _, b := range buf[:n] {
			if b != 0 {
				return errors.New("trailing archive data")
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func StateDigest(ctx context.Context, f *os.File) (string, int64, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(h, contextReader{ctx, io.LimitReader(f, MaxState+1)})
	if err == nil && n > MaxState {
		err = fmt.Errorf("space state exceeds %d bytes", MaxState)
	}
	return hex.EncodeToString(h.Sum(nil)), n, err
}
