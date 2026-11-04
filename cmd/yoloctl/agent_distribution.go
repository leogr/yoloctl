package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/nativerelease"
)

func fetchAgentLatest(ctx context.Context, p *agentprofile.Profile) (*agentLatestInfo, error) {
	if p.NativeRelease == "" {
		return fetchNpmLatest(ctx, p.NpmPackage)
	}
	r, err := nativerelease.Lookup(p.NativeRelease)
	if err != nil {
		return nil, err
	}
	url, err := r.ManifestURL(runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("native manifest: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 1024*1024 {
		return nil, fmt.Errorf("native manifest too large")
	}
	// Some upstream responses carry gzip bytes without Content-Encoding.
	if bytes.HasPrefix(raw, []byte{0x1f, 0x8b}) {
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer func() { _ = gz.Close() }()
		raw, err = io.ReadAll(io.LimitReader(gz, 1024*1024+1))
		if err != nil || len(raw) > 1024*1024 {
			return nil, fmt.Errorf("invalid compressed native manifest")
		}
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, err
	}
	if strings.TrimSpace(manifest.Version) == "" || strings.ContainsAny(manifest.Version, "\r\n\x1b") {
		return nil, fmt.Errorf("native manifest has invalid version")
	}
	return &agentLatestInfo{Latest: manifest.Version}, nil
}

func fetchAgentLatestCached(ctx context.Context, p *agentprofile.Profile, ttl time.Duration) (*agentLatestInfo, error) {
	key := p.Distribution()
	if info, ok := readNpmLatestCache(key, ttl, time.Now()); ok {
		return info, nil
	}
	info, err := fetchAgentLatest(ctx, p)
	if err != nil {
		return nil, err
	}
	_ = writeNpmLatestCache(key, info, time.Now())
	return info, nil
}

func fetchInstalledAgentVersion(ctx context.Context, c *container.Client, box string, p *agentprofile.Profile) (string, error) {
	if p.NativeRelease == "" {
		return fetchInstalledNpmVersion(ctx, c, box, p.NpmPackage)
	}
	r, err := nativerelease.Lookup(p.NativeRelease)
	if err != nil {
		return "", err
	}
	return fetchInstalledVersionFile(ctx, c, box, r.MetadataPath())
}

func installAgentLatest(ctx context.Context, c *container.Client, box string, p *agentprofile.Profile) error {
	if p.NativeRelease == "" {
		return npmInstallGlobalAtLatest(ctx, c, box, p.NpmPackage)
	}
	r, err := nativerelease.Lookup(p.NativeRelease)
	if err != nil {
		return err
	}
	return c.Shell(ctx, nativeInstallArgs(box, r)...)
}

func nativeInstallArgs(box string, r nativerelease.Release) []string {
	return append([]string{"sudo", "nerdctl", "exec", "--user", "sandbox", box}, r.InstallArgs()...)
}
