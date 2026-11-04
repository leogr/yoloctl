//go:build linux && amd64

package firecracker

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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"golang.org/x/sys/unix"
)

// Release builders may link a fixed HTTPS archive URL and its SHA256. Without
// these pins, first use prepares the image locally and subsequent spaces reuse it.
var DefaultImageURL, DefaultImageSHA256 string

func (Driver) ResolveImage(ctx context.Context, source string) (string, error) {
	recipe := ""
	if source == "" {
		var err error
		recipe, err = defaultRecipe()
		if err != nil {
			return "", err
		}
	}
	return resolveImage(ctx, source, recipe, prepareLocalImage)
}

func resolveImage(ctx context.Context, source, recipe string, prepare func(context.Context, string) (string, error)) (string, error) {
	root, err := layout.VMImagesDir("firecracker")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	unlock, err := imageLock(ctx, root)
	if err != nil {
		return "", err
	}
	defer unlock()
	ref := ""
	if source == "" {
		sum, err := hex.DecodeString(recipe)
		if err != nil || len(sum) != sha256.Size {
			return "", errors.New("invalid VM recipe identity")
		}
		ref = "default-" + recipe + ".json"
	}
	if source == "" {
		raw, err := safepath.ReadFile(root, ref, 4096)
		if err == nil {
			var id string
			if err := strictJSON(raw, &id); err != nil {
				return "", err
			}
			sum, err := hex.DecodeString(id)
			if err != nil || len(sum) != sha256.Size {
				return "", errors.New("invalid default VM image identity")
			}
			dir := filepath.Join(root, id)
			if err := verifyCachedIdentity(dir, id); err != nil {
				return "", err
			}
			return dir, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		stage, err := os.MkdirTemp(root, ".download-")
		if err != nil {
			return "", err
		}
		defer func() { _ = os.RemoveAll(stage) }()
		if DefaultImageURL == "" && DefaultImageSHA256 == "" {
			source, err = prepare(ctx, stage)
			if err != nil {
				return "", err
			}
		} else {
			if err := downloadImage(ctx, stage, DefaultImageURL, DefaultImageSHA256); err != nil {
				return "", err
			}
			source = stage
		}
	}
	source, err = filepath.Abs(source)
	if err == nil {
		source, err = filepath.EvalSymlinks(source)
	}
	if err != nil {
		return "", err
	}
	manifest, err := ReadImage(source)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("%x", sha256.Sum256(raw))
	dest := filepath.Join(root, id)
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	if _, err := os.Lstat(dest); errors.Is(err, os.ErrNotExist) {
		stage, err := os.MkdirTemp(root, ".install-")
		if err != nil {
			return "", err
		}
		defer func() { _ = os.RemoveAll(stage) }()
		for _, name := range []string{"image.json", "kernel", "rootfs.ext4", "firecracker"} {
			if err := copyImageFile(ctx, source, stage, name); err != nil {
				return "", err
			}
		}
		verified, _, err := VerifyImage(stage)
		if err != nil {
			return "", err
		}
		if verified != manifest {
			return "", errors.New("VM image manifest changed while installing")
		}
		if err := atomicfile.SyncDir(stage); err != nil {
			return "", err
		}
		// A concurrent installer may have published this identical content already.
		if err := os.Rename(stage, dest); err != nil && !errors.Is(err, unix.ENOTEMPTY) && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		if err := atomicfile.SyncDir(root); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if err := verifyCachedIdentity(dest, id); err != nil {
		return "", err
	}
	// Explicit overrides do not become the default for a different recipe.
	if ref != "" {
		selected, _ := json.Marshal(id)
		if err := safepath.WriteFile(root, ref, selected, 0600); err != nil {
			return "", err
		}
	}
	return dest, nil
}

func verifyCachedIdentity(dir, id string) error {
	manifest, _, err := cachedImage(dir)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != id {
		return errors.New("cached VM image identity differs from its directory")
	}
	return nil
}

type imageReader struct {
	ctx context.Context
	r   io.Reader
}

func (r imageReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func copyImageFile(ctx context.Context, source, dest, name string) error {
	fd, err := unix.Open(filepath.Join(source, name), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	in := os.NewFile(uintptr(fd), name)
	defer func() { _ = in.Close() }()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("VM image artifacts must be regular files")
	}
	mode := os.FileMode(0444)
	if name == "firecracker" {
		mode = 0500
	}
	out, err := os.OpenFile(filepath.Join(dest, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if err := copySparse(out, imageReader{ctx, in}); err != nil {
		return err
	}
	return out.Sync()
}

// Keep zero-filled ext4 regions sparse even when input arrived in a gzip tar.
// The final length preserves trailing holes; later checksum verification reads
// the same logical bytes irrespective of the host filesystem's allocation.
func copySparse(out *os.File, in io.Reader) error {
	buf, zero := make([]byte, 1<<20), make([]byte, 1<<20)
	var length int64
	for {
		n, err := io.ReadFull(in, buf)
		if n > 0 {
			length += int64(n)
			if bytes.Equal(buf[:n], zero[:n]) {
				if _, e := out.Seek(int64(n), io.SeekCurrent); e != nil {
					return e
				}
			} else if _, e := out.Write(buf[:n]); e != nil {
				return e
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return out.Truncate(length)
		}
		if err != nil {
			return err
		}
	}
}

func downloadImage(ctx context.Context, stage, location, checksum string) error {
	u, err := url.Parse(location)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("VM image release requires an HTTPS URL")
	}
	expected, err := hex.DecodeString(checksum)
	if err != nil || len(expected) != sha256.Size {
		return errors.New("VM image release requires a pinned SHA256")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) >= 10 {
			return errors.New("invalid VM image redirect")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("VM image download: HTTP %d", resp.StatusCode)
	}
	archive, err := os.CreateTemp(stage, ".archive-")
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(archive, digest), io.LimitReader(resp.Body, (4<<30)+1))
	if err != nil {
		return err
	}
	if n > 4<<30 || hex.EncodeToString(digest.Sum(nil)) != strings.ToLower(checksum) {
		return errors.New("VM image archive size or checksum mismatch")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return unpackImage(archive, stage)
}

func unpackImage(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	limits := map[string]int64{"image.json": 64 << 10, "kernel": 128 << 20, "firecracker": 128 << 20, "rootfs.ext4": 8 << 30}
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		limit, ok := limits[h.Name]
		if !ok || seen[h.Name] || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > limit {
			return errors.New("invalid VM image archive entry")
		}
		seen[h.Name] = true
		mode := os.FileMode(0444)
		if h.Name == "firecracker" {
			mode = 0500
		}
		f, err := os.OpenFile(filepath.Join(dest, h.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(f, tr)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return err
		}
	}
	if len(seen) != len(limits) {
		return errors.New("incomplete VM image archive")
	}
	_, err = io.Copy(io.Discard, gz) // validate the gzip trailer too
	return err
}
