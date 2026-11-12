package mermaid

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

func hashBytes(p []byte) string {
	h := sha256.Sum256(p)
	return hex.EncodeToString(h[:])
}

func TestArtifactManifestMatchesPinnedInputs(t *testing.T) {
	var manifest struct {
		Schema   int               `json:"schema"`
		Recipe   string            `json:"recipe_sha256"`
		Artifact string            `json:"artifact_sha256"`
		Rust     string            `json:"rust"`
		Target   string            `json:"target"`
		Inputs   map[string]string `json:"inputs"`
		Patches  []struct {
			File string `json:"file"`
			Hash string `json:"sha256"`
		} `json:"patches"`
		Source string `json:"source_sha256"`
	}
	decoder := json.NewDecoder(bytes.NewReader(artifactManifest))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != 1 || manifest.Artifact != hashBytes(artifact) || !bytes.HasPrefix(artifact, []byte("\x00asm\x01\x00\x00\x00")) {
		t.Fatal("embedded Mermaid does not match its manifest")
	}
	root := filepath.Join("..", "..")
	pinsDir := filepath.Join(root, "third_party", "mermaid-text")
	p, err := os.ReadFile(filepath.Join(pinsDir, "pins.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string]any
	if err := json.Unmarshal(p, &pins); err != nil {
		t.Fatal(err)
	}
	if pins["rust"] != manifest.Rust || pins["target"] != manifest.Target || pins["artifact_sha256"] != manifest.Artifact {
		t.Fatal("artifact metadata differs from pins")
	}
	delete(pins, "artifact_sha256")
	canonical, err := json.Marshal(pins)
	if err != nil || hashBytes(canonical) != manifest.Recipe {
		t.Fatal("artifact recipe differs from pins", err)
	}
	check := func(path, expected string) {
		t.Helper()
		p, err := os.ReadFile(path)
		if err != nil || hashBytes(p) != expected {
			t.Fatalf("%s changed without regenerating the pinned artifact: %v", path, err)
		}
	}
	for path, hash := range manifest.Inputs {
		check(filepath.Join(pinsDir, filepath.FromSlash(path)), hash)
	}
	for _, patch := range manifest.Patches {
		check(filepath.Join(pinsDir, filepath.FromSlash(patch.File)), patch.Hash)
	}
	check(filepath.Join(root, "scripts", "build-mermaid.py"), pins["builder_sha256"].(string))
	if pins["upstream"].(map[string]any)["sha256"] != manifest.Source {
		t.Fatal("artifact source differs from pins")
	}
}

func TestPinnedWASICommand(t *testing.T) {
	ctx := t.Context()
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(1024).WithCloseOnContextDone(true))
	defer func() { _ = runtime.Close(ctx) }()
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		t.Fatal(err)
	}
	compiled, err := runtime.CompileModule(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, source string
		args         []string
		code         uint32
		contains     []string
	}{
		{"flow", "flowchart LR\n A[Build] --> B[Test]\n", []string{"80"}, 0, []string{"Build", "Test"}},
		{"sequence", "sequenceDiagram\n participant A as Alice\n participant B as Bob\n A->>B: Hello\n", []string{"80"}, 0, []string{"Alice", "Bob", "Hello"}},
		{"narrow", "flowchart LR\n A[Build] --> B[Test]\n", []string{"1"}, 4, nil},
		{"invalid", "not a diagram", []string{"80"}, 3, nil},
		// No source cap in the command: a source over 8 KiB reaches the renderer.
		{"large", "flowchart LR\n A[Build] --> B[Test]\n" + strings.Repeat("%% comment line\n", 600), []string{"80"}, 0, []string{"Build", "Test"}},
		{"invalid UTF8", "\xff", []string{"80"}, 2, nil},
		{"bad width", "flowchart LR\n A-->B", []string{"0"}, 2, nil},
		{"extra argument", "flowchart LR\n A-->B", []string{"80", "extra"}, 2, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			var out, diagnostic bytes.Buffer
			args := append([]string{"mermaid"}, tc.args...)
			module, err := runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig().
				WithName("").WithArgs(args...).WithStdin(strings.NewReader(tc.source)).
				WithStdout(&out).WithStderr(&diagnostic))
			if module != nil {
				defer func() { _ = module.Close(ctx) }()
			}
			var exit *sys.ExitError
			var code uint32
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tc.code {
				t.Fatalf("exit %d, want %d; diagnostic=%q", code, tc.code, diagnostic.String())
			}
			if code != 0 && out.Len() != 0 {
				t.Fatal("failed renderer emitted drawing bytes")
			}
			for _, text := range tc.contains {
				if !strings.Contains(out.String(), text) {
					t.Errorf("drawing lost %q: %s", text, out.String())
				}
			}
			if tc.name == "narrow" && strings.TrimSpace(diagnostic.String()) == "" {
				t.Fatal("too-wide result omitted required column count")
			}
		})
	}
}
