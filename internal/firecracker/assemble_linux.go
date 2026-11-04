//go:build linux && amd64

package firecracker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	backendfile "github.com/diskfs/go-diskfs/backend/file"
	"github.com/diskfs/go-diskfs/filesystem/ext4"
	"github.com/diskfs/go-diskfs/filesystem/squashfs"
	"github.com/klauspost/compress/zstd"
	"github.com/leogr/yoloctl/assets"
	"github.com/leogr/yoloctl/internal/guestbinary"
	"github.com/leogr/yoloctl/internal/vmwire"
)

type imagePin struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Entries remain virtual until written into ext4. In particular, package links
// are never followed by a host extraction tool or materialized on the host.
type imageEntry struct {
	header tar.Header
	open   func() (io.ReadCloser, error)
}
type imageTree map[string]imageEntry

func imageName(name string) (string, error) {
	name = strings.TrimPrefix(name, "./")
	if strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\x00\\") {
		return "", fmt.Errorf("invalid image path %q", name)
	}
	for _, p := range strings.Split(name, "/") {
		if p == ".." {
			return "", fmt.Errorf("image path traversal %q", name)
		}
	}
	return path.Clean(name), nil
}

func mergedName(name string) string {
	for _, dir := range []string{"bin", "sbin", "lib", "lib64"} {
		if name == dir || strings.HasPrefix(name, dir+"/") {
			return "usr/" + name
		}
	}
	return name
}

func (t imageTree) put(name string, mode int64, data []byte) {
	t[name] = imageEntry{tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg}, func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }}
}
func (t imageTree) link(name, target string) {
	t[name] = imageEntry{header: tar.Header{Name: name, Mode: 0777, Typeflag: tar.TypeSymlink, Linkname: target}}
}
func (t imageTree) directory(name string) {
	if _, ok := t[name]; !ok {
		t[name] = imageEntry{header: tar.Header{Name: name, Mode: 0755, Typeflag: tar.TypeDir}}
	}
}

// Only the newly assembled virtual image is changed. Existing VM files,
// installed images, container argv and user/data disks are never modified.
func (t imageTree) dropLegacyOpenShell() {
	for name := range t {
		if name == "opt/openshell" || strings.HasPrefix(name, "opt/openshell/") || name == "etc/yoloctl/sandbox-policy.rego" {
			delete(t, name)
		}
	}
}

func (t imageTree) resolve(name string) (imageEntry, error) {
	parts := strings.Split(strings.TrimPrefix(name, "/"), "/")
	current := ""
	links := 0
	for len(parts) > 0 {
		p := parts[0]
		parts = parts[1:]
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			if current == "" {
				return imageEntry{}, errors.New("guest link escapes root")
			}
			current = path.Dir(current)
			if current == "." {
				current = ""
			}
			continue
		}
		current = path.Join(current, p)
		e, ok := t[current]
		if ok && (e.header.Typeflag == tar.TypeSymlink || e.header.Typeflag == tar.TypeLink) {
			links++
			if links > 40 {
				return imageEntry{}, errors.New("guest link loop")
			}
			if strings.HasPrefix(e.header.Linkname, "/") || e.header.Typeflag == tar.TypeLink {
				current = ""
			} else {
				current = path.Dir(current)
				if current == "." {
					current = ""
				}
			}
			parts = append(strings.Split(strings.TrimPrefix(e.header.Linkname, "/"), "/"), parts...)
		}
	}
	e, ok := t[current]
	if !ok {
		return e, fmt.Errorf("missing guest dependency %s", name)
	}
	return e, nil
}

func (t imageTree) verifyRuntime() error {
	needed := []string{"/usr/sbin/sshd", "/usr/bin/ssh", "/usr/bin/sshfs", "/usr/bin/sudo", "/usr/bin/socat", "/usr/sbin/ip", "/usr/sbin/iptables", "/usr/sbin/ip6tables", "/usr/sbin/iptables-save", "/usr/sbin/nft", "/usr/bin/python3", "/usr/bin/findmnt", "/usr/bin/nsenter", "/usr/bin/setsid", "/usr/bin/flock", "/usr/bin/curl", "/usr/sbin/blkid", "/usr/sbin/fstrim", "/usr/sbin/e2fsck", "/usr/sbin/resize2fs", "/bin/bash"}
	search := []string{"/lib/x86_64-linux-gnu", "/usr/lib/x86_64-linux-gnu", "/lib64", "/lib", "/usr/libexec/sudo"}
	seen := map[string]bool{}
	for len(needed) > 0 {
		name := needed[len(needed)-1]
		needed = needed[:len(needed)-1]
		e, err := t.resolve(name)
		if err != nil {
			return err
		}
		if seen[e.header.Name] {
			continue
		}
		seen[e.header.Name] = true
		if e.open == nil || e.header.Size > 128<<20 {
			return fmt.Errorf("invalid runtime dependency %s", name)
		}
		r, err := e.open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(r, (128<<20)+1))
		_ = r.Close()
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(data, []byte("\x7fELF")) {
			continue
		}
		f, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			return err
		}
		for _, p := range f.Progs {
			if p.Type == elf.PT_INTERP {
				raw, err := io.ReadAll(p.Open())
				if err != nil {
					return err
				}
				needed = append(needed, strings.TrimRight(string(raw), "\x00"))
			}
		}
		libs, err := f.ImportedLibraries()
		if err != nil {
			return err
		}
		for _, lib := range libs {
			found := ""
			for _, dir := range search {
				if _, err := t.resolve(dir + "/" + lib); err == nil {
					found = dir + "/" + lib
					break
				}
			}
			if found == "" {
				return fmt.Errorf("missing %s required by %s", lib, name)
			}
			needed = append(needed, found)
		}
	}
	return nil
}

func (t imageTree) squash(ctx context.Context, filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	sq, err := squashfs.Read(backendfile.New(f, true), st.Size(), 0, 0)
	if err != nil {
		return err
	}
	// Squashfs entries lazily read from this file while converting the tree.
	// Keep a separate file descriptor per read; no descriptor survives Build.
	var walk func(string) error
	walk = func(dir string) error {
		entries, err := sq.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := path.Join(dir, entry.Name())
			name = strings.TrimPrefix(name, "/")
			if name == "dev" || name == "root/.ssh" || name == "etc/ssh" || name == "var/lib/cloud" || name == "etc/machine-id" {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode()&(fs.ModeSocket|fs.ModeDevice|fs.ModeNamedPipe) != 0 {
				continue
			}
			link := ""
			if stat, ok := info.Sys().(*squashfs.StatT); ok {
				link = stat.LinkTarget
			}
			h, err := tar.FileInfoHeader(info, link)
			if err != nil {
				return err
			}
			h.Name = name
			h.Uid, h.Gid = 0, 0
			var open func() (io.ReadCloser, error)
			if info.Mode().IsRegular() {
				open = func() (io.ReadCloser, error) {
					in, err := os.Open(filename)
					if err != nil {
						return nil, err
					}
					reader, err := squashfs.Read(backendfile.New(in, true), st.Size(), 0, 0)
					if err != nil {
						_ = in.Close()
						return nil, err
					}
					file, err := reader.OpenFile(name, os.O_RDONLY)
					if err != nil {
						_ = in.Close()
						return nil, err
					}
					return &imageReadCloser{Reader: file, close: func() error { return errors.Join(file.Close(), in.Close()) }}, nil
				}
			}
			t[name] = imageEntry{*h, open}
			if info.IsDir() {
				if err := walk(name); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(".")
}

type imageReadCloser struct {
	io.Reader
	close func() error
}

func (r *imageReadCloser) Close() error { return r.close() }

func (t imageTree) tar(ctx context.Context, r io.Reader, stage string, remap func(string) string) error {
	tr := tar.NewReader(r)
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if count > 100000 || h.Size < 0 || h.Size > 1<<30 {
			return errors.New("oversized package entry")
		}
		name, err := imageName(h.Name)
		if err != nil {
			return err
		}
		if name == "." {
			continue
		}
		name = remap(name)
		if name == "" {
			continue
		}
		h.Name = name
		h.Uid, h.Gid = 0, 0
		h.Uname, h.Gname = "", ""
		e := imageEntry{header: *h}
		switch h.Typeflag {
		case tar.TypeReg:
			f, err := os.CreateTemp(stage, "file-")
			if err != nil {
				return err
			}
			_, err = io.Copy(f, imageReader{ctx, tr})
			err = errors.Join(err, f.Close())
			if err != nil {
				return err
			}
			e.header.Typeflag = tar.TypeReg
			e.open = func() (io.ReadCloser, error) { return os.Open(f.Name()) }
		case tar.TypeLink:
			link, err := imageName(h.Linkname)
			if err != nil {
				return err
			}
			e.header.Linkname = remap(link)
		case tar.TypeDir, tar.TypeSymlink:
		default:
			return fmt.Errorf("unsupported package entry %s", name)
		}
		t[name] = e
	}
}

func (t imageTree) deb(ctx context.Context, filename, stage string) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	header := make([]byte, 8)
	if _, err = io.ReadFull(f, header); err != nil || string(header) != "!<arch>\n" {
		return errors.New("invalid Debian archive")
	}
	for {
		h := make([]byte, 60)
		if _, err = io.ReadFull(f, h); err != nil {
			return errors.New("missing Debian data archive")
		}
		size, err := strconv.ParseInt(strings.TrimSpace(string(h[48:58])), 10, 64)
		if err != nil || size < 0 || size > 1<<30 || string(h[58:]) != "`\n" {
			return errors.New("invalid Debian member")
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(h[:16])), "/")
		if name == "data.tar.zst" {
			zr, err := zstd.NewReader(io.LimitReader(f, size), zstd.WithDecoderMaxMemory(256<<20), zstd.WithDecoderConcurrency(1))
			if err != nil {
				return err
			}
			defer zr.Close()
			return t.tar(ctx, zr, stage, mergedName)
		}
		if _, err = io.CopyN(io.Discard, f, size+size%2); err != nil {
			return err
		}
	}
}

func (t imageTree) ext4(ctx context.Context, target string, size int64) error {
	for name := range t {
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			t.directory(dir)
		}
	}
	names := make([]string, 0, len(t))
	for name := range t {
		names = append(names, name)
	}
	slices.Sort(names)
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err = f.Truncate(size); err != nil {
		return err
	}
	filesystem, err := ext4.Create(backendfile.New(&contextDisk{File: f, ctx: ctx}, false), size, 0, 512, &ext4.Params{
		SectorsPerBlock: 8, InodeRatio: 16384,
		Features: []ext4.FeatureOpt{ext4.WithFeatureReservedGDTBlocksForExpansion(false), ext4.WithFeatureHasJournal(false)},
	})
	if err != nil {
		return err
	}
	buffer := make([]byte, 1<<20)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		e := t[name]
		if e.header.Typeflag == tar.TypeLink {
			source, err := t.resolve(name)
			if err != nil {
				return err
			}
			source.header.Name = name
			e = source
		}
		switch e.header.Typeflag {
		case tar.TypeDir:
			err = filesystem.Mkdir(name)
		case tar.TypeSymlink:
			err = filesystem.Symlink(e.header.Linkname, name)
		case tar.TypeReg:
			var out io.WriteCloser
			out, err = filesystem.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR)
			if err == nil {
				in, openErr := e.open()
				if openErr != nil {
					_ = out.Close()
					return openErr
				}
				var n int64
				n, err = io.CopyBuffer(out, imageReader{ctx, in}, buffer)
				err = errors.Join(err, in.Close(), out.Close())
				if err == nil && n != e.header.Size {
					err = errors.New("image source size changed")
				}
			}
		default:
			return fmt.Errorf("unsupported image entry %s", name)
		}
		if err != nil {
			return fmt.Errorf("write image path %s: %w", name, err)
		}
		if e.header.Typeflag != tar.TypeSymlink {
			if err = filesystem.Chmod(name, e.header.FileInfo().Mode()); err != nil {
				return err
			}
			if err = filesystem.Chown(name, 0, 0); err != nil {
				return err
			}
		}
	}
	return errors.Join(filesystem.Close(), f.Sync())
}

func fetchImageInput(ctx context.Context, cache string, pin imagePin) (string, error) {
	if len(pin.SHA256) != 64 || !strings.HasPrefix(pin.URL, "https://") {
		return "", errors.New("invalid input pin")
	}
	if _, err := hex.DecodeString(pin.SHA256); err != nil {
		return "", err
	}
	if err := os.MkdirAll(cache, 0700); err != nil {
		return "", err
	}
	target := filepath.Join(cache, pin.SHA256)
	if st, err := os.Lstat(target); err == nil {
		if !st.Mode().IsRegular() {
			return "", errors.New("cached input is not a regular file")
		}
		sum, err := guestbinary.SHA256File(target)
		if err != nil {
			return "", err
		}
		if sum != pin.SHA256 {
			return "", errors.New("cached input checksum mismatch")
		}
		return target, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pin.URL, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 15 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) >= 10 {
			return errors.New("invalid input redirect")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("input download: HTTP %d", resp.StatusCode)
	}
	f, err := os.CreateTemp(cache, ".download-")
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, (2<<30)+1))
	if err != nil {
		return "", err
	}
	if n > 2<<30 || hex.EncodeToString(h.Sum(nil)) != pin.SHA256 {
		return "", errors.New("input checksum or size mismatch")
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(f.Name(), target); err != nil {
		return "", err
	}
	return target, syncDir(cache)
}

func unpackPinnedKernel(ctx context.Context, out string) error {
	data, err := assets.VMKernel.ReadFile("vm/firecracker/kernel.json")
	if err != nil {
		return err
	}
	var manifest struct {
		KernelSHA256 string              `json:"kernel_sha256"`
		ConfigSHA256 string              `json:"config_sha256"`
		Inputs       map[string]imagePin `json:"inputs"`
		Enabled      []string            `json:"enabled"`
		Disabled     []string            `json:"disabled"`
	}
	if err = json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	config, err := assets.VMKernel.ReadFile("vm/firecracker/kernel.config")
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(config)) != manifest.ConfigSHA256 {
		return errors.New("kernel configuration checksum mismatch")
	}
	pinData, err := assets.VMImageRecipe.ReadFile("vm/firecracker/pins.json")
	if err != nil {
		return err
	}
	var pins map[string]imagePin
	if err = json.Unmarshal(pinData, &pins); err != nil {
		return err
	}
	if len(manifest.Inputs) != 2 {
		return errors.New("invalid bundled kernel provenance")
	}
	for _, name := range []string{"linux-7.2.11.tar.xz", "kernel-6.18.config"} {
		if manifest.Inputs[name] != pins[name] {
			return errors.New("bundled kernel does not match source pins")
		}
	}
	for _, name := range manifest.Enabled {
		if !strings.Contains("\n"+string(config), "\nCONFIG_"+name+"=y\n") {
			return fmt.Errorf("bundled kernel lacks %s", name)
		}
	}
	for _, name := range manifest.Disabled {
		if !strings.Contains("\n"+string(config), "\n# CONFIG_"+name+" is not set\n") {
			return fmt.Errorf("unsupported kernel option %s", name)
		}
	}
	lsm := false
	for _, line := range strings.Split(string(config), "\n") {
		if strings.HasPrefix(line, "CONFIG_LSM=\"") {
			lsm = slices.Contains(strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, "CONFIG_LSM=\""), "\""), ","), "landlock")
		}
	}
	if !lsm {
		return errors.New("kernel LSM list lacks Landlock")
	}
	for _, option := range []string{"SECURITY_LANDLOCK", "OVERLAY_FS", "FUSE_FS", "TUN", "VIRTIO_VSOCKETS", "SECCOMP_FILTER", "NET_NS", "VETH", "NF_TABLES", "CGROUPS"} {
		if !strings.Contains("\n"+string(config), "\nCONFIG_"+option+"=y\n") {
			return fmt.Errorf("kernel lacks %s", option)
		}
	}
	compressed, err := assets.VMKernel.Open("vm/firecracker/kernel-linux-amd64.gz")
	if err != nil {
		return err
	}
	defer func() { _ = compressed.Close() }()
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	f, err := os.OpenFile(filepath.Join(out, "kernel"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0444)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), imageReader{ctx, io.LimitReader(gz, 128<<20)})
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != manifest.KernelSHA256 {
		return errors.New("bundled kernel checksum mismatch")
	}
	return nil
}

func assembleLocalImage(ctx context.Context, out, cache string, init guestbinary.Artifact, log io.Writer) error {
	if err := os.Mkdir(out, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(out), "assembly-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	pins := map[string]imagePin{}
	for _, name := range []string{"pins.json", "runtime-pins.json"} {
		raw, err := assets.VMImageRecipe.ReadFile("vm/firecracker/" + name)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &pins); err != nil {
			return err
		}
	}
	delete(pins, "linux-7.2.11.tar.xz")
	delete(pins, "kernel-6.18.config")
	names := make([]string, 0, len(pins))
	for name := range pins {
		names = append(names, name)
	}
	slices.Sort(names)
	inputs := map[string]string{}
	for _, name := range names {
		_, _ = fmt.Fprintf(log, "Preparing pinned %s\n", name)
		p, err := fetchImageInput(ctx, filepath.Join(cache, "downloads"), pins[name])
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		inputs[name] = p
	}
	_, _ = fmt.Fprintln(log, "Assembling Ubuntu filesystem locally (no host tools).")
	tree := imageTree{}
	if err = tree.squash(ctx, inputs["ubuntu.squashfs"]); err != nil {
		return fmt.Errorf("read Ubuntu squashfs: %w", err)
	}
	tree.dropLegacyOpenShell()
	if err = tree.dropManagedCertificates(); err != nil {
		return fmt.Errorf("prepare CA store: %w", err)
	}
	for _, name := range names {
		if strings.HasSuffix(name, ".deb") {
			if err = tree.deb(ctx, inputs[name], stage); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	if err = tree.rebuildTrustStore(); err != nil {
		return fmt.Errorf("rebuild CA store: %w", err)
	}
	for _, dir := range []string{"bin", "sbin", "lib", "lib64"} {
		tree.link(dir, "usr/"+dir)
	}
	for _, family := range []string{"iptables", "ip6tables", "arptables", "ebtables"} {
		for _, suffix := range []string{"", "-save", "-restore"} {
			tree.link("usr/sbin/"+family+suffix, family+"-nft"+suffix)
		}
	}
	for _, archive := range []string{"nerdctl.tgz", "firecracker.tgz"} {
		f, err := os.Open(inputs[archive])
		if err != nil {
			return err
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			_ = f.Close()
			return err
		}
		remap := func(name string) string {
			switch archive {
			case "nerdctl.tgz":
				if strings.HasPrefix(name, "bin/") && path.Dir(name) == "bin" {
					return "usr/local/bin/" + path.Base(name)
				}
				if strings.HasPrefix(name, "libexec/cni/") && path.Dir(name) == "libexec/cni" {
					return "opt/cni/bin/" + path.Base(name)
				}
			case "firecracker.tgz":
				if name == "release-v1.17.0-x86_64/firecracker-v1.17.0-x86_64" {
					return "firecracker"
				}
			}
			return ""
		}
		err = tree.tar(ctx, gz, stage, remap)
		err = errors.Join(err, gz.Close(), f.Close())
		if err != nil {
			return err
		}
	}
	fc, ok := tree["firecracker"]
	if !ok {
		return errors.New("missing pinned VMM")
	}
	in, err := fc.open()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(out, "firecracker"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0500)
	if err != nil {
		_ = in.Close()
		return err
	}
	_, err = io.Copy(f, imageReader{ctx, in})
	err = errors.Join(err, in.Close(), f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if _, err = guestbinary.LinuxExecutable(filepath.Join(out, "firecracker"), elf.EM_X86_64, false); err != nil {
		return fmt.Errorf("pinned Firecracker must be a static host executable: %w", err)
	}
	delete(tree, "firecracker")
	for _, name := range []string{"dev", "proc", "sys", "run", "tmp", "mnt", "var", "opt/yoloctl", "etc/yoloctl", "etc/openshell-tls", "etc/ssh", "etc/sudoers.d", "etc/containerd", "etc/cni/net.d", "opt/cni/bin", "usr/local/bin", "home/yolo"} {
		tree.directory(name)
	}
	tree.put("etc/containerd/config.toml", 0644, []byte("version = 2\nroot = \"/var/lib/containerd\"\nstate = \"/run/containerd\"\ndisabled_plugins = [\"io.containerd.grpc.v1.cri\", \"io.containerd.cri.v1.runtime\", \"io.containerd.cri.v1.images\"]\n"))
	tree.put("etc/ssh/sshd_microvm_config", 0644, []byte("Port 2222\nListenAddress 127.0.0.1\nHostKey /run/ssh/host_ed25519\nAuthorizedKeysFile /run/ssh/authorized_keys\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin no\nAllowUsers yolo\nUsePAM no\nStrictModes yes\nPidFile /run/ssh/sshd.pid\nSubsystem sftp internal-sftp\n"))
	sudo, ok := tree["usr/bin/sudo"]
	if !ok {
		return errors.New("missing sudo")
	}
	sudo.header.Mode = 04755
	tree["usr/bin/sudo"] = sudo
	data, err := os.ReadFile(init.Path)
	if err != nil {
		return err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != init.Sum {
		return errors.New("guest init changed during preparation")
	}
	tree.put("usr/sbin/yoloctl-init", 0755, data)
	if err = tree.verifyRuntime(); err != nil {
		return fmt.Errorf("verify runtime: %w", err)
	}
	if err = tree.ext4(ctx, filepath.Join(out, "rootfs.ext4"), 2<<30); err != nil {
		return fmt.Errorf("write rootfs: %w", err)
	}
	if err = unpackPinnedKernel(ctx, out); err != nil {
		return err
	}
	manifest := Image{Schema: ImageVersion, Architecture: "amd64", GuestProtocol: vmwire.Protocol, DataDiskGrow: DataDiskGrowVersion}
	for name, dst := range map[string]*string{"kernel": &manifest.KernelSHA256, "rootfs.ext4": &manifest.RootfsSHA256, "firecracker": &manifest.FirecrackerSHA256} {
		*dst, err = guestbinary.SHA256File(filepath.Join(out, name))
		if err != nil {
			return err
		}
	}
	data, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(out, "image.json"), append(data, '\n'), 0444); err != nil {
		return err
	}
	if err = os.Chmod(filepath.Join(out, "rootfs.ext4"), 0444); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(log, "Local Firecracker image prepared.")
	return nil
}
