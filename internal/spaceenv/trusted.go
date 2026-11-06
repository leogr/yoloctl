package spaceenv

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/leogr/yoloctl/internal/safepath"
	"golang.org/x/sys/unix"
)

// LoadTrusted reads only the fixed, root-controlled infrastructure context.
// An environment variable or a copied executable cannot select another file.
func LoadTrusted() (Context, error) {
	var c Context
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return c, errors.New("native controller execution requires VM root infrastructure")
	}
	for _, path := range []string{"/etc", "/etc/yoloctl", ContextPath} {
		var st unix.Stat_t
		if err := unix.Lstat(path, &st); err != nil {
			return c, err
		}
		if st.Uid != 0 || st.Mode&0o022 != 0 || st.Mode&unix.S_IFMT == unix.S_IFLNK {
			return c, fmt.Errorf("untrusted controller context path %s", path)
		}
	}
	raw, err := safepath.ReadFile("/", strings.TrimPrefix(ContextPath, "/"), 1<<20)
	if err != nil {
		return c, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return c, errors.New("trailing controller context data")
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

func (c Context) Validate() error {
	if c.ServiceManager != "" && c.ServiceManager != "systemd" && c.ServiceManager != "microvm" {
		return errors.New("invalid guest service manager")
	}
	if c.Schema != 1 || c.Space == "" || strings.ContainsAny(c.Space, "/\\\x00") || c.UID <= 0 || c.GID <= 0 {
		return errors.New("invalid VM execution identity")
	}
	id, err := hex.DecodeString(c.DiskID)
	if err != nil || len(id) != 16 || c.DiskUUID == "" || c.Bundle == "" || c.Generation == "" {
		return errors.New("controller disk and deployment identities are required")
	}
	if !filepath.IsAbs(c.DiskMount) || filepath.Clean(c.DiskMount) != c.DiskMount ||
		c.ConfigDir != filepath.Join(c.DiskMount, "config") || c.CatalogDir != CatalogRoot ||
		!filepath.IsAbs(c.HostConfigRoot) {
		return errors.New("invalid VM execution paths")
	}
	for _, m := range c.Mounts {
		if !filepath.IsAbs(m.Source) || filepath.Clean(m.Source) != m.Source {
			return errors.New("invalid VM mount grant")
		}
	}
	if c.ImageLayerCompression != "" && c.ImageLayerCompression != ImageLayerGzip {
		return errors.New("invalid image layer compression")
	}
	return nil
}

// ImageLayerGzip selects gzip for newly written private image layers; the
// default (empty) writes them uncompressed.
const ImageLayerGzip = "gzip"
