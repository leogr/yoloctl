// Package ocifeature resolves public OCI Dev Container Features. Resolution
// never executes installers on the host or grants runtime authority.
package ocifeature

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/distribution/reference"
)

const maxArchive = 32 << 20
const maxMetadata = 128 << 10
const maxManifest = 1 << 20
const layerType = "application/vnd.devcontainers.layer.v1+tar"

// Registry deliberately uses anonymous HTTPS/Bearer pulls only. It does not
// consult host Docker credentials or forward credentials to redirect targets.
type Registry struct{ Client *http.Client }

func NewRegistry() *Registry {
	return &Registry{Client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != "https" {
			return fmt.Errorf("unsafe registry redirect")
		}
		if req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		return nil
	}}}
}

type Descriptor struct {
	MediaType string `json:"mediaType" yaml:"media_type"`
	Digest    string `json:"digest" yaml:"digest"`
	Size      int64  `json:"size" yaml:"size"`
}
type manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	Config        Descriptor   `json:"config"`
	Layers        []Descriptor `json:"layers"`
}

// Lock contains enough verified metadata for offline policy/image planning.
// Archive bytes are checked against Layer again before entering a build.
type Lock struct {
	Ref      string            `yaml:"ref" json:"ref"`
	Digest   string            `yaml:"digest" json:"digest"`
	Layer    Descriptor        `yaml:"layer" json:"layer"`
	Metadata string            `yaml:"metadata" json:"metadata"`
	Options  map[string]string `yaml:"options,omitempty" json:"options,omitempty"`
}

func IsReference(s string) bool { return strings.Contains(s, "/") }
func Parse(s string) (reference.Named, error) {
	if !IsReference(s) || strings.Contains(s, "://") || strings.HasPrefix(s, ".") {
		return nil, fmt.Errorf("expected registry/namespace/feature[:tag] or @sha256:digest: %q", s)
	}
	n, e := reference.ParseNormalizedNamed(s)
	if e != nil {
		return nil, e
	}
	// Require an explicit registry rather than silently choosing Docker Hub.
	host, _, _ := strings.Cut(s, "/")
	if !strings.ContainsAny(host, ".:") && host != "localhost" {
		return nil, fmt.Errorf("feature must include its registry: %s", s)
	}
	if d, ok := n.(reference.Digested); ok && !validDigest(d.Digest().String()) {
		return nil, fmt.Errorf("feature requires a sha256 digest")
	}
	return reference.TagNameOnly(n), nil
}

var sha256Digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validDigest(d string) bool { return sha256Digest.MatchString(d) }
func digest(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }
func bounded(r io.Reader, limit int64) ([]byte, error) {
	data, e := io.ReadAll(io.LimitReader(r, limit+1))
	if e == nil && int64(len(data)) > limit {
		e = fmt.Errorf("OCI response exceeds %d bytes", limit)
	}
	return data, e
}

var authPart = regexp.MustCompile(`([a-z]+)="([^"]*)"`)

func (r *Registry) get(ctx context.Context, endpoint string, limit int64) ([]byte, error) {
	token := ""
	for attempt := 0; attempt < 2; attempt++ {
		req, e := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if e != nil {
			return nil, e
		}
		req.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, e := r.Client.Do(req)
		if e != nil {
			return nil, e
		}
		if resp.StatusCode == 401 && attempt == 0 {
			challenge := resp.Header.Get("WWW-Authenticate")
			_ = resp.Body.Close()
			if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
				return nil, fmt.Errorf("registry requires authentication; only public Features are supported")
			}
			parts := map[string]string{}
			for _, m := range authPart.FindAllStringSubmatch(challenge, -1) {
				parts[m[1]] = m[2]
			}
			u, e := url.Parse(parts["realm"])
			if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
				return nil, fmt.Errorf("invalid registry token endpoint")
			}
			q := u.Query()
			q.Set("service", parts["service"])
			q.Set("scope", parts["scope"])
			u.RawQuery = q.Encode()
			tr, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
			if e != nil {
				return nil, e
			}
			reply, e := r.Client.Do(tr)
			if e != nil {
				return nil, e
			}
			data, e := bounded(reply.Body, maxMetadata)
			_ = reply.Body.Close()
			if e != nil {
				return nil, e
			}
			if reply.StatusCode != 200 {
				return nil, fmt.Errorf("registry token HTTP %d", reply.StatusCode)
			}
			var auth struct {
				Token       string `json:"token"`
				AccessToken string `json:"access_token"`
			}
			if e = json.Unmarshal(data, &auth); e != nil {
				return nil, e
			}
			token = auth.Token
			if token == "" {
				token = auth.AccessToken
			}
			if token == "" {
				return nil, fmt.Errorf("empty registry token")
			}
			continue
		}
		if resp.StatusCode != 200 {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("registry HTTP %d for %s", resp.StatusCode, req.URL.Path)
		}
		data, e := bounded(resp.Body, limit)
		_ = resp.Body.Close()
		return data, e
	}
	return nil, fmt.Errorf("registry rejected anonymous token")
}
func endpoint(n reference.Named, kind, selector string) string {
	return "https://" + reference.Domain(n) + "/v2/" + reference.Path(n) + "/" + kind + "/" + selector
}

func (l Lock) validateIdentity() error {
	n, err := Parse(l.Ref)
	if err != nil {
		return err
	}
	if !validDigest(l.Digest) || !validDigest(l.Layer.Digest) || l.Layer.MediaType != layerType || l.Layer.Size <= 0 || l.Layer.Size > maxArchive {
		return fmt.Errorf("invalid feature lock for %s", l.Ref)
	}
	if d, ok := n.(reference.Digested); ok && d.Digest().String() != l.Digest {
		return fmt.Errorf("feature lock differs from reference digest for %s", l.Ref)
	}
	return nil
}
func (r *Registry) Resolve(ctx context.Context, ref string) (Lock, error) {
	n, e := Parse(ref)
	if e != nil {
		return Lock{}, e
	}
	selector := "latest"
	if tag, ok := n.(reference.Tagged); ok {
		selector = tag.Tag()
	}
	if d, ok := n.(reference.Digested); ok {
		selector = d.Digest().String()
	}
	raw, e := r.get(ctx, endpoint(n, "manifests", selector), maxManifest)
	if e != nil {
		return Lock{}, e
	}
	if strings.HasPrefix(selector, "sha256:") && digest(raw) != selector {
		return Lock{}, fmt.Errorf("manifest digest mismatch")
	}
	var m manifest
	if e = json.Unmarshal(raw, &m); e != nil {
		return Lock{}, e
	}
	if m.SchemaVersion != 2 || m.Config.MediaType != "application/vnd.devcontainers" || len(m.Layers) != 1 || m.Layers[0].MediaType != layerType {
		return Lock{}, fmt.Errorf("%s is not a Dev Container Feature artifact", ref)
	}
	lock := Lock{Ref: ref, Digest: digest(raw), Layer: m.Layers[0]}
	files, e := r.Files(ctx, lock)
	if e != nil {
		return Lock{}, e
	}
	lock.Metadata = string(files["devcontainer-feature.json"])
	if _, e = lock.Definition(); e != nil {
		return Lock{}, fmt.Errorf("feature %s: %w", ref, e)
	}
	remember(lock)
	return lock, nil
}
func (r *Registry) Files(ctx context.Context, l Lock) (map[string][]byte, error) {
	if e := l.validateIdentity(); e != nil {
		return nil, e
	}
	n, _ := Parse(l.Ref)
	// Verify the layer is bound to the immutable manifest, including when a
	// declaration was manually edited or imported from an archive.
	manifestRaw, e := r.get(ctx, endpoint(n, "manifests", l.Digest), maxManifest)
	if e != nil {
		return nil, e
	}
	var pinned manifest
	if digest(manifestRaw) != l.Digest || json.Unmarshal(manifestRaw, &pinned) != nil || pinned.SchemaVersion != 2 || pinned.Config.MediaType != "application/vnd.devcontainers" || len(pinned.Layers) != 1 || pinned.Layers[0] != l.Layer {
		return nil, fmt.Errorf("feature lock differs from pinned manifest")
	}
	raw, e := r.get(ctx, endpoint(n, "blobs", l.Layer.Digest), maxArchive)
	if e != nil {
		return nil, e
	}
	if int64(len(raw)) != l.Layer.Size || digest(raw) != l.Layer.Digest {
		return nil, fmt.Errorf("feature layer checksum or size mismatch")
	}
	files, e := unpack(raw)
	if e != nil {
		return nil, e
	}
	if l.Metadata != "" && l.Metadata != string(files["devcontainer-feature.json"]) {
		return nil, fmt.Errorf("feature metadata differs from locked archive")
	}
	return files, nil
}

// Keep archive entries in memory; no registry-supplied paths ever reach the host
// filesystem. Reject links/devices/traversal and bound decompression and entries.
func unpack(raw []byte) (map[string][]byte, error) {
	var reader io.Reader = bytes.NewReader(raw)
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		gz, e := gzip.NewReader(reader)
		if e != nil {
			return nil, e
		}
		defer func() { _ = gz.Close() }()
		reader = gz
	}
	tr := tar.NewReader(io.LimitReader(reader, maxArchive+1))
	files := map[string][]byte{}
	seen := map[string]bool{}
	dirs := map[string]bool{}
	total := int64(0)
	for entries := 0; ; entries++ {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if entries >= 4096 {
			return nil, fmt.Errorf("too many feature files")
		}
		name := strings.TrimPrefix(h.Name, "./")
		name = strings.TrimSuffix(name, "/")
		if name == "." || name == "" {
			if h.Typeflag == tar.TypeDir {
				continue
			}
			return nil, fmt.Errorf("invalid archive root")
		}
		if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || path.Clean(name) != name || strings.HasPrefix(name, "../") || name == ".." || strings.ContainsAny(name, "\x00\r\n") || seen[name] {
			return nil, fmt.Errorf("unsafe or duplicate feature path %q", name)
		}
		seen[name] = true
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, ok := files[parent]; ok {
				return nil, fmt.Errorf("feature file is also a parent directory: %q", parent)
			}
			dirs[parent] = true
		}
		if h.Typeflag == tar.TypeDir {
			dirs[name] = true
			continue
		}
		if dirs[name] {
			return nil, fmt.Errorf("feature directory is also a file: %q", name)
		}
		if h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > maxArchive-total {
			return nil, fmt.Errorf("unsupported or oversized feature entry %q", name)
		}
		total += h.Size
		data, e := bounded(tr, h.Size)
		if e != nil {
			return nil, e
		}
		files[name] = data
	}
	if len(files["install.sh"]) == 0 || len(files["devcontainer-feature.json"]) == 0 || len(files["devcontainer-feature.json"]) > maxMetadata {
		return nil, fmt.Errorf("feature requires install.sh and bounded devcontainer-feature.json")
	}
	return files, nil
}
