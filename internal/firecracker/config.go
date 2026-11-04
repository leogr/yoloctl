//go:build linux && amd64

// Package firecracker implements the Linux microVM adapter. Its host helper is
// per-VM device plumbing, not a controller or an installed host service.
package firecracker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/resources"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/vmapi"
	"github.com/leogr/yoloctl/internal/vmwire"
	"golang.org/x/sys/unix"
)

const ImageVersion = 1

type Image struct {
	Schema            int    `json:"schema"`
	Architecture      string `json:"architecture"`
	KernelSHA256      string `json:"kernel_sha256"`
	RootfsSHA256      string `json:"rootfs_sha256"`
	FirecrackerSHA256 string `json:"firecracker_sha256"`
	// The guest runtime protocol is independent of controller build identity.
	GuestProtocol int `json:"guest_protocol"`
	// DataDiskGrow is the data disk grow its init performs before mounting
	// (DataDiskGrowVersion), verified e2fsck and resize2fs included; zero for
	// images built before it, which must be updated before a grow.
	DataDiskGrow int `json:"data_disk_grow,omitempty"`
}

// DataDiskGrowVersion is the pre-mount data disk grow of this build's init.
const DataDiskGrowVersion = 1

type Config struct {
	Schema int        `json:"schema"`
	Name   string     `json:"name"`
	Spec   vmapi.Spec `json:"spec"`
	UID    int        `json:"uid"`
	GID    int        `json:"gid"`
	// Paths exist only on the host. The guest receives a reduced Boot record.
	Executable string                   `json:"executable"`
	Image      Image                    `json:"image"`
	ImageFiles map[string]artifactStamp `json:"image_files"`
}

type artifactStamp struct {
	Device   uint64        `json:"device"`
	Inode    uint64        `json:"inode"`
	Size     int64         `json:"size"`
	Mode     uint32        `json:"mode"`
	Modified unix.Timespec `json:"modified"`
	Changed  unix.Timespec `json:"changed"`
}

func stamp(st unix.Stat_t) artifactStamp {
	return artifactStamp{st.Dev, st.Ino, st.Size, st.Mode, st.Mtim, st.Ctim}
}

// sameArtifactFile compares a verified artifact's recorded stamp with its
// current one, ignoring only the host device number: a reboot or remount (for
// example after a power loss) may renumber the same filesystem. The inode,
// size, mode and nanosecond modify/change times must still match exactly, so
// any rewrite of the file is still refused.
func sameArtifactFile(current, recorded artifactStamp) bool {
	current.Device = recorded.Device
	return current == recorded
}
func strictJSON(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(dst); e != nil {
		return e
	}
	if e := d.Decode(new(any)); !errors.Is(e, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func ReadConfig(dir string) (Config, error) {
	var c Config
	b, e := safepath.ReadFile(dir, "vm.json", 1<<20)
	if e != nil {
		return c, e
	}
	e = strictJSON(b, &c)
	if e == nil && (c.Schema != 1 || c.Name == "" || c.Spec.DiskID == "" || c.UID < 1 || c.GID < 1) {
		e = errors.New("invalid Firecracker VM configuration")
	}
	return c, e
}
func WriteConfig(dir string, c Config) error {
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	return safepath.WriteFile(dir, "vm.json", b, 0600)
}
func ReadImage(dir string) (Image, error) {
	var m Image
	b, e := safepath.ReadFile(dir, "image.json", 1<<20)
	if e != nil {
		return m, fmt.Errorf("prepared Firecracker image: %w", e)
	}
	if e = strictJSON(b, &m); e != nil {
		return m, e
	}
	if m.Schema != ImageVersion || m.GuestProtocol != vmwire.Protocol || m.Architecture != "amd64" {
		return m, errors.New("unsupported Firecracker image manifest")
	}
	for _, p := range []string{"kernel", "rootfs.ext4", "firecracker"} {
		st, e := os.Lstat(filepath.Join(dir, p))
		if e != nil {
			return m, e
		}
		if !st.Mode().IsRegular() {
			return m, fmt.Errorf("image artifact %s must be a regular file", p)
		}
	}
	return m, nil
}
func Bytes(s string) (uint64, error) {
	return resources.VMBytes(s)
}

// VerifyImage runs during VM creation, outside the boot hot path. ReadImage on
// later starts checks the version and exact manifest pinned in VM metadata.
func VerifyImage(dir string) (Image, map[string]artifactStamp, error) {
	m, e := ReadImage(dir)
	if e != nil {
		return m, nil, e
	}
	files := make(map[string]artifactStamp)
	for name, want := range map[string]string{"kernel": m.KernelSHA256, "rootfs.ext4": m.RootfsSHA256, "firecracker": m.FirecrackerSHA256} {
		sum, e := hex.DecodeString(want)
		if e != nil || len(sum) != sha256.Size {
			return m, nil, fmt.Errorf("invalid %s digest", name)
		}
		fd, e := unix.Open(filepath.Join(dir, name), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return m, nil, e
		}
		f := os.NewFile(uintptr(fd), name)
		var before, after unix.Stat_t
		if e = unix.Fstat(fd, &before); e != nil || before.Mode&unix.S_IFMT != unix.S_IFREG {
			_ = f.Close()
			return m, nil, fmt.Errorf("invalid image file %s: %v", name, e)
		}
		h := sha256.New()
		_, e = io.Copy(h, f)
		e = errors.Join(e, unix.Fstat(fd, &after))
		_ = f.Close()
		if e != nil {
			return m, nil, e
		}
		if stamp(before) != stamp(after) || hex.EncodeToString(h.Sum(nil)) != want {
			return m, nil, fmt.Errorf("%s checksum mismatch or file changed while verifying", name)
		}
		files[name] = stamp(after)
	}
	return m, files, nil
}

// Verify exactly the files whose bytes were checked at creation. ctime also
// changes when a writer restores mtime, so later starts cannot silently accept
// changed artifacts. The image directory belongs to the trusted host user.
func unchangedImage(c Config) error {
	image, e := ReadImage(c.Spec.Image)
	if e != nil {
		return e
	}
	if image != c.Image || len(c.ImageFiles) != 3 {
		return errors.New("prepared image identity changed; recreate with a verified image")
	}
	for _, name := range []string{"kernel", "rootfs.ext4", "firecracker"} {
		var st unix.Stat_t
		if e = unix.Lstat(filepath.Join(c.Spec.Image, name), &st); e != nil {
			return e
		}
		if !sameArtifactFile(stamp(st), c.ImageFiles[name]) {
			return fmt.Errorf("prepared image artifact %s changed; recreate with a verified image", name)
		}
	}
	return nil
}

// Hash a prepared image once per isolated host configuration. Later VM creates
// reuse that verification only while the exact manifest and inode stamps match.
func cachedImage(dir string) (Image, map[string]artifactStamp, error) {
	home, e := layout.FirecrackerHome()
	if e != nil {
		return Image{}, nil, e
	}
	cache := filepath.Join(home, "verified-images")
	if e = os.MkdirAll(cache, 0700); e != nil {
		return Image{}, nil, e
	}
	key := fmt.Sprintf("%x.json", sha256.Sum256([]byte(dir)))
	var record struct {
		Image Image
		Files map[string]artifactStamp
		Path  string
	}
	if raw, err := safepath.ReadFile(cache, key, 16384); err == nil && strictJSON(raw, &record) == nil && record.Path == dir {
		if unchangedImage(Config{Spec: vmapi.Spec{Image: dir}, Image: record.Image, ImageFiles: record.Files}) == nil {
			return record.Image, record.Files, nil
		}
	}
	image, files, e := VerifyImage(dir)
	if e != nil {
		return image, nil, e
	}
	record.Image, record.Files, record.Path = image, files, dir
	raw, e := json.Marshal(record)
	if e == nil {
		e = safepath.WriteFile(cache, key, raw, 0600)
	}
	return image, files, e
}
