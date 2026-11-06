package audit

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/auditplugin"
	"github.com/leogr/yoloctl/internal/safepath"
)

type Artifact struct {
	Arch, Name, SHA256, FalcoSHA256, ContainerSHA256 string
	Bytes                                            int64
}

// Official release tarballs linked from Falco 0.45.2; selected-file hashes
// additionally verify immutable installations before reuse or execution.
var artifacts = map[string]Artifact{
	"amd64": {Arch: "amd64", Name: "x86_64", SHA256: "b2e9961e51601095765c6c644329673e6af2f06c02357d0e77da0f680e4910da", Bytes: 52786116, FalcoSHA256: "702900ebeee85e03823f817af99a19fcc0389bfc8f9cacab251bcaac0981d02e", ContainerSHA256: "44dc72826376ca988ad349d990d93cd2d8e37ac98c99d071efe1e4054cb7b5ec"},
	"arm64": {Arch: "arm64", Name: "aarch64", SHA256: "5bd79b879f07b605f02f57841c1b6dcda1604eb3da9aec80611ac52e9e3aa84f", Bytes: 48031098, FalcoSHA256: "878a08c3680e69980ff44aff9e3f7a79a700417f956ffdcbb4e57d42446f541f", ContainerSHA256: "807d80c470fd54335804df47d7e2d34aa40ea3c485bf3b33cd40635e035e2f9d"},
}

func Pinned(arch string) (Artifact, error) {
	a, ok := artifacts[arch]
	if !ok {
		return a, errors.New("falco audit supports only Linux amd64 and arm64 guests")
	}
	return a, nil
}
func (a Artifact) URL() string {
	return "https://download.falco.org/packages/bin/" + a.Name + "/falco-" + Version + "-" + a.Name + ".tar.gz"
}
func Digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func (a Artifact) Verify(raw []byte) error {
	if int64(len(raw)) != a.Bytes || Digest(raw) != a.SHA256 {
		return errors.New("falco archive size or SHA256 does not match this build's pin")
	}
	return nil
}
func Fetch(ctx context.Context, cache, arch string) ([]byte, error) {
	a, err := Pinned(arch)
	if err != nil {
		return nil, err
	}
	name := a.SHA256 + ".tar.gz"
	if raw, err := safepath.ReadFile(cache, name, MaxArchive); err == nil {
		return raw, a.Verify(raw)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL(), nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download pinned Falco archive: HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxArchive+1))
	if err != nil {
		return nil, err
	}
	if err := a.Verify(raw); err != nil {
		return nil, err
	}
	if err := safepath.WriteFile(cache, name, raw, 0600); err != nil {
		return nil, err
	}
	return raw, nil
}

// Install publishes a fresh immutable, private tool directory. Only the exact
// pinned executable and container plugin are selected; falcoctl, upstream rules,
// service files, links and package scripts are never installed or executed.
func Install(root, arch string, raw []byte) (string, error) {
	a, err := Pinned(arch)
	if err != nil {
		return "", err
	}
	if err := a.Verify(raw); err != nil {
		return "", err
	}
	plugin, _, err := auditplugin.Plugin(arch)
	if err != nil {
		return "", err
	}
	dest, err := a.InstalledDir(root)
	if err != nil {
		return "", err
	}

	if _, err := os.Lstat(dest); err == nil {
		return dest, a.VerifyInstalled(dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	if err := safepath.WriteFile(root, "tools/.format", []byte("yoloctl-audit-tools/v1\n"), 0600); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(filepath.Join(root, "tools"), ".install-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(io.LimitReader(gz, 256<<20))
	prefix := "falco-" + Version + "-" + a.Name + "/"
	selected := map[string]string{prefix + "usr/bin/falco": "bin/falco", prefix + "usr/share/falco/plugins/libcontainer.so": "plugins/libcontainer.so"}
	found := map[string]bool{}
	for n := 0; ; n++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if n >= 1024 || path.IsAbs(h.Name) || path.Clean(h.Name) != strings.TrimSuffix(h.Name, "/") || strings.HasPrefix(h.Name, "../") {
			return "", errors.New("invalid Falco archive entry")
		}
		rel, ok := selected[h.Name]
		if !ok {
			continue
		}
		if found[rel] || h.Typeflag != tar.TypeReg || h.Size < 1 || h.Size > 64<<20 {
			return "", errors.New("invalid selected Falco archive entry")
		}
		body, err := io.ReadAll(io.LimitReader(tr, (64<<20)+1))
		if err != nil || int64(len(body)) != h.Size {
			return "", errors.New("truncated Falco archive entry")
		}
		if err := safepath.WriteFile(stage, rel, body, 0500); err != nil {
			return "", err
		}
		found[rel] = true
	}
	if len(found) != len(selected) {
		return "", errors.New("falco archive lacks executable or container plugin")
	}
	if err := safepath.WriteFile(stage, "plugins/libyolo.so", plugin, 0500); err != nil {
		return "", err
	}
	if err := safepath.WriteFile(stage, "archive.sha256", []byte(a.SHA256), 0400); err != nil {
		return "", err
	}
	if err := os.Rename(stage, dest); err != nil {
		return "", err
	}
	return dest, atomicfile.SyncDir(filepath.Dir(dest))
}

func (a Artifact) VerifyInstalled(dest string) error {
	st, err := os.Lstat(dest)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
		return errors.New("invalid installed audit tools directory")
	}
	manifest, err := safepath.ReadFile(dest, "archive.sha256", 128)
	if err != nil || string(manifest) != a.SHA256 {
		return errors.New("installed audit tools do not match the archive pin")
	}
	_, pluginDigest, err := auditplugin.Plugin(a.Arch)
	if err != nil {
		return err
	}
	for rel, digest := range map[string]string{"plugins/libyolo.so": pluginDigest, "bin/falco": a.FalcoSHA256, "plugins/libcontainer.so": a.ContainerSHA256} {
		raw, info, err := safepath.ReadFileInfo(dest, rel, 64<<20)
		if err != nil || Digest(raw) != digest || info.Mode().Perm() != 0500 {
			return errors.New("installed audit executable or container plugin does not match its pin")
		}
	}
	return nil
}

// InstalledDir binds immutable tools to both the Falco and extraction pins.
func (a Artifact) InstalledDir(root string) (string, error) {
	_, digest, err := auditplugin.Plugin(a.Arch)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "tools", Version+"-"+a.SHA256[:16]+"-"+digest[:16]), nil
}
