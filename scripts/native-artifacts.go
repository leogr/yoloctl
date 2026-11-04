//go:build ignore

// Native-box artifacts the yoloctl build embeds (internal/nativeartifacts).
//
//	go run ./scripts/native-artifacts.go reproduce
//	go run ./scripts/native-artifacts.go qualify INPUTS ARCHIVE_DIR EVIDENCE_DIR
//
// reproduce builds every architecture twice into separate private outputs,
// the second with a fresh build cache, compares bytes and descriptors, prints
// the recipe/toolchain/source/binary results as JSON and removes its outputs.
//
// qualify prepares a build that can run native boxes: it copies the built
// OpenShell sandbox/supervisor archives named in INPUTS from ARCHIVE_DIR into
// the embed directory after checking their archive and binary hashes, builds
// and packs both helper archives, assembles the qualification manifest from
// INPUTS and the source's fixed constants, validates it with the real loader
// and writes the selection to internal/nativeartifacts/embedded/selection/.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	helperinputs "github.com/leogr/yoloctl"
	// Linking the helper's own package gives this program the helper's module
	// closure, which helperinputs.Current reads from its build information.
	"github.com/leogr/yoloctl/internal/openshell"
)

// helperBuild records one helper build: its bytes and what produced them.
type helperBuild struct {
	Schema       int    `json:"schema"`
	Name         string `json:"name"`
	Arch         string `json:"arch"`
	BinarySHA256 string `json:"binary_sha256"`
	SourceSHA256 string `json:"source_sha256"`
	RecipeSHA256 string `json:"recipe_sha256"`
	Toolchain    string `json:"toolchain"`
}

type recipe struct {
	Schema        int      `json:"schema"`
	Command       string   `json:"command"`
	GOOS          string   `json:"goos"`
	CGOEnabled    string   `json:"cgo_enabled"`
	Architectures []string `json:"architectures"`
	Flags         []string `json:"flags"`
	Modules       []string `json:"modules"`
}

func main() {
	var err error
	switch {
	case len(os.Args) == 2 && os.Args[1] == "reproduce":
		err = reproduce()
	case len(os.Args) == 5 && os.Args[1] == "qualify":
		err = qualify(os.Args[2], os.Args[3], os.Args[4])
	default:
		err = fmt.Errorf("usage: native-artifacts reproduce | qualify INPUTS ARCHIVE_DIR EVIDENCE_DIR")
	}
	if err != nil {
		fail(err)
	}
}

func reproduce() error {
	temporary, err := os.MkdirTemp("", "yoloctl-native-reproduce-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	first, second, cache := filepath.Join(temporary, "first"), filepath.Join(temporary, "second"), filepath.Join(temporary, "cache")
	for _, dir := range []string{first, second, cache} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return err
		}
	}
	a, err := buildHelper(first, nil)
	if err != nil {
		return err
	}
	// The second build cannot reuse the first one's compiled packages.
	b, err := buildHelper(second, []string{"GOCACHE=" + cache})
	if err != nil {
		return err
	}
	result := map[string]any{"name": "openshell-helper"}
	archs := map[string]any{}
	for i, d := range a {
		x, err := os.ReadFile(filepath.Join(first, d.Name+"-linux-"+d.Arch))
		if err != nil {
			return err
		}
		y, err := os.ReadFile(filepath.Join(second, d.Name+"-linux-"+d.Arch))
		if err != nil {
			return err
		}
		if !slices.Equal(x, y) || d != b[i] {
			return fmt.Errorf("helper linux/%s is not reproducible: %s vs %s", d.Arch, d.BinarySHA256, b[i].BinarySHA256)
		}
		archs[d.Arch] = map[string]string{"binary_sha256": d.BinarySHA256}
		result["source_sha256"], result["recipe_sha256"], result["toolchain"] = d.SourceSHA256, d.RecipeSHA256, d.Toolchain
	}
	result["architectures"], result["reproducible"] = archs, true
	out, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "native-artifacts:", err)
	os.Exit(1)
}

func sum(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:])
}

func buildHelper(dir string, env []string) ([]helperBuild, error) {
	rawRecipe, err := os.ReadFile("scripts/openshell-helper-recipe.json")
	if err != nil {
		return nil, err
	}
	var r recipe
	var built []helperBuild
	if err := json.Unmarshal(rawRecipe, &r); err != nil {
		return nil, err
	}
	if r.Schema != 1 || r.GOOS != "linux" || r.CGOEnabled != "0" || r.Command == "" {
		return nil, fmt.Errorf("unsupported helper recipe")
	}
	// The identity of this checkout, as the consuming CLI will compile it.
	current, err := helperinputs.Current()
	if err != nil {
		return nil, err
	}
	for _, arch := range r.Architectures {
		base := filepath.Join(dir, "openshell-helper"+"-linux-"+arch)
		staged := base + ".tmp"
		args := append([]string{"build"}, r.Flags...)
		args = append(args, "-o", staged, "./"+r.Command)
		cmd := exec.Command("go", args...)
		cmd.Env = append(append(os.Environ(), "GOOS="+r.GOOS, "GOARCH="+arch, "CGO_ENABLED="+r.CGOEnabled, "GOFLAGS="), env...)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("build helper linux/%s: %w", arch, err)
		}
		binary, err := os.ReadFile(staged)
		if err != nil {
			return nil, err
		}
		info, err := buildinfo.ReadFile(staged)
		if err != nil {
			return nil, fmt.Errorf("helper linux/%s build info: %w", arch, err)
		}
		// The accepted producer check: static ELF, architecture, command path,
		// recipe settings and module versions of the actual bytes.
		file, err := os.Open(staged)
		if err != nil {
			return nil, err
		}
		goVersion := openshell.HelperGoVersion(info.GoVersion)
		err = openshell.CheckHelperProducer(file, openshell.HelperPins{Inputs: current, GoVersion: goVersion}, arch)
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("helper linux/%s: %w", arch, err)
		}
		d := helperBuild{
			Schema: 1, Name: "openshell-helper", Arch: arch,
			BinarySHA256: sum(binary), SourceSHA256: current.SourceSHA256, RecipeSHA256: sum(rawRecipe), Toolchain: goVersion,
		}
		built = append(built, d)
		descriptor, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(base+".json.tmp", append(descriptor, '\n'), 0o644); err != nil {
			return nil, err
		}
		// Publish the binary before its descriptor: a reader that sees the
		// descriptor sees the bytes it describes.
		if err := os.Rename(staged, base); err != nil {
			return nil, err
		}
		if err := os.Rename(base+".json.tmp", base+".json"); err != nil {
			return nil, err
		}
	}
	return built, nil
}

// embedDir is where every yoloctl binary embeds its qualified archives.
const embedDir = "internal/nativeartifacts/embedded"

type archiveInput struct {
	Archive       string `json:"archive"`
	ArchiveSHA256 string `json:"archive_sha256"`
	BinarySHA256  string `json:"binary_sha256"`
}

// platformInput is one architecture's pair; its loader contracts are read
// from the actual binaries, never typed in.
type platformInput struct {
	Boundary   archiveInput `json:"boundary"`
	Supervisor archiveInput `json:"supervisor"`
}

// qualifyInputs are the facts of the built OpenShell pair a build qualifies.
// Proofs name real evidence files whose sha256 the manifest records, and the
// Rego is the verified pair's policy, checked in under third_party/openshell.
type qualifyInputs struct {
	Revision   string `json:"revision"`
	ForkCommit string `json:"fork_commit"`
	ForkTree   string `json:"fork_tree"`
	// The fork's second patch is identified by its recorded sha256; the
	// named record (e.g. the signed source pin) must contain that value.
	Patch2SHA256 string `json:"patch2_sha256"`
	Patch2Record string `json:"patch2_record"`
	// A local build with patches after the second names the second patch's
	// tree and each later patch; their hashes must be in the same record.
	Patch2Tree   string                   `json:"patch2_tree,omitempty"`
	ExtraPatches []extraPatch             `json:"extra_patches,omitempty"`
	SourceProof  string                   `json:"source_proof"`
	BuildProofs  map[string]string        `json:"build_proofs"`
	Rego         string                   `json:"rego"`
	Platforms    map[string]platformInput `json:"platforms"`
}

type extraPatch struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Tree   string `json:"tree"`
}

// evidenceDir resolves the evidence and record paths named in the inputs.
var evidenceDir = "."

// evidence is the sha256 of a named, existing evidence file.
func evidence(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("an evidence file must be named")
	}
	raw, err := os.ReadFile(filepath.Join(evidenceDir, path))
	if err != nil {
		return "", fmt.Errorf("evidence %s: %w", path, err)
	}
	return sum(raw), nil
}

// sourceConstant reads a fixed qualification constant from the Go source, so
// the manifest carries the values the loader itself validates against.
func sourceConstant(name string) (string, error) {
	raw, err := os.ReadFile("internal/openshell/release.go")
	if err != nil {
		return "", err
	}
	m := regexp.MustCompile(`\b` + name + `\s*=\s*"([^"]+)"`).FindSubmatch(raw)
	if m == nil {
		return "", fmt.Errorf("missing source constant %s", name)
	}
	return string(m[1]), nil
}

func qualify(inputsPath, archiveDir, evidenceRoot string) error {
	evidenceDir = evidenceRoot
	raw, err := os.ReadFile(inputsPath)
	if err != nil {
		return err
	}
	var in qualifyInputs
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		return fmt.Errorf("%s: %w", inputsPath, err)
	}
	fixed := map[string]string{}
	for _, name := range []string{"qualifiedVersion", "qualifiedUpstreamCommit", "qualifiedUpstreamTree", "qualifiedPatch1Tree", "qualifiedPatch1SHA256", "protocolFixtureVersion"} {
		if fixed[name], err = sourceConstant(name); err != nil {
			return err
		}
	}
	if clean := filepath.Clean(in.Rego); filepath.IsAbs(clean) || !strings.HasPrefix(clean, "third_party/openshell/") {
		return fmt.Errorf("rego %q must be a checked-in file under third_party/openshell/", in.Rego)
	}
	rego, err := os.ReadFile(in.Rego)
	if err != nil {
		return err
	}
	record, err := os.ReadFile(filepath.Join(evidenceDir, in.Patch2Record))
	if err != nil {
		return fmt.Errorf("patch2 record: %w", err)
	}
	if len(in.Patch2SHA256) != 64 || !bytes.Contains(record, []byte(in.Patch2SHA256)) {
		return fmt.Errorf("patch2_sha256 is not recorded in %s", in.Patch2Record)
	}
	sourceProof, err := evidence(in.SourceProof)
	if err != nil {
		return err
	}
	buildProofs := map[string]string{}
	for arch, path := range in.BuildProofs {
		if buildProofs[arch], err = evidence(path); err != nil {
			return err
		}
	}
	fixture, err := os.ReadFile("third_party/openshell/bootstrap-fixture.json")
	if err != nil {
		return err
	}
	patches, err := patchChain(in, fixed, record)
	if err != nil {
		return err
	}
	pins := openshell.ReleasePins{
		Version: fixed["qualifiedVersion"], UpstreamCommit: fixed["qualifiedUpstreamCommit"], UpstreamTree: fixed["qualifiedUpstreamTree"],
		ForkCommit: in.ForkCommit, ForkTree: in.ForkTree,
		Patches:   patches,
		Platforms: map[string]openshell.PlatformArtifacts{}, RegoSHA256: sum(rego), BootstrapSchema: openshell.BootstrapSchema,
		ProtocolFixtureVersion: fixed["protocolFixtureVersion"], ProtocolFixtureSHA256: sum(fixture),
	}
	// OpenShell pair: copy each built archive in only after its archive and
	// member binary hashes match the inputs.
	for arch, p := range in.Platforms {
		var pair openshell.PlatformArtifacts
		for _, role := range []struct {
			in     archiveInput
			member string
			out    *openshell.Artifact
			loader *string
		}{{p.Boundary, "openshell-sandbox", &pair.Boundary, &pair.BoundaryLoader}, {p.Supervisor, "openshell-supervisor", &pair.Supervisor, &pair.SupervisorLoader}} {
			artifact := openshell.Artifact{URL: openshell.EmbeddedArchivePrefix + role.in.Archive, ArchiveSHA256: role.in.ArchiveSHA256, BinarySHA256: role.in.BinarySHA256, Member: role.member}
			archive, err := os.ReadFile(filepath.Join(archiveDir, filepath.Base(role.in.Archive)))
			if err != nil {
				return err
			}
			var binary bytes.Buffer
			if err := openshell.VerifyArchive(bytes.NewReader(archive), artifact, &binary); err != nil {
				return fmt.Errorf("%s: %w", role.in.Archive, err)
			}
			// The same ELF contract the installer checks on the installed file.
			if *role.loader, err = openshell.ELFContract(bytes.NewReader(binary.Bytes()), arch); err != nil {
				return fmt.Errorf("%s: %w", role.in.Archive, err)
			}
			if err := writeEmbedded(role.in.Archive, archive); err != nil {
				return err
			}
			*role.out = artifact
		}
		pins.Platforms[arch] = pair
	}
	// Helper: this checkout's static helper for both architectures, packed as
	// the single-member archive the installer verifies.
	temporary, err := os.MkdirTemp("", "yoloctl-native-qualify-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	built, err := buildHelper(temporary, nil)
	if err != nil {
		return err
	}
	current, err := helperinputs.Current()
	if err != nil {
		return err
	}
	reproduction, err := json.Marshal(built)
	if err != nil {
		return err
	}
	pins.Helper = openshell.HelperPins{Protocol: openshell.HelperProtocol, Inputs: current, GoVersion: built[0].Toolchain,
		SourceProofSHA256: sum(reproduction), BuildProofSHA256: map[string]string{}, Platforms: map[string]openshell.Artifact{}}
	for _, d := range built {
		binary, err := os.ReadFile(filepath.Join(temporary, d.Name+"-linux-"+d.Arch))
		if err != nil {
			return err
		}
		archive, err := packArchive(openshell.HelperMember, binary)
		if err != nil {
			return err
		}
		name := "yoloctl-openshell-helper-linux-" + d.Arch + ".tar.gz"
		if err := writeEmbedded(name, archive); err != nil {
			return err
		}
		descriptor, err := json.Marshal(d)
		if err != nil {
			return err
		}
		pins.Helper.BuildProofSHA256[d.Arch] = sum(descriptor)
		pins.Helper.Platforms[d.Arch] = openshell.Artifact{URL: openshell.EmbeddedArchivePrefix + name, ArchiveSHA256: sum(archive), BinarySHA256: d.BinarySHA256, Member: openshell.HelperMember}
	}
	subject, err := json.Marshal(pins)
	if err != nil {
		return err
	}
	manifest, err := json.Marshal(struct {
		Pins          openshell.ReleasePins         `json:"pins"`
		Qualification openshell.QualificationRecord `json:"qualification"`
	}{pins, openshell.QualificationRecord{Revision: in.Revision, SubjectSHA256: sum(subject), SourceProofSHA256: sourceProof, BuildProofSHA256: buildProofs}})
	if err != nil {
		return err
	}
	digest := sum(manifest)
	if _, err := openshell.LoadQualifiedRelease(manifest, digest); err != nil {
		return fmt.Errorf("assembled manifest is not a valid qualification: %w", err)
	}
	if bytes.ContainsRune(manifest, '`') {
		return fmt.Errorf("manifest cannot be a raw string literal")
	}
	return writeSelection(manifest, digest, rego)
}

func writeEmbedded(name string, raw []byte) error {
	path := filepath.Join(embedDir, name)
	if err := os.WriteFile(path+".tmp", raw, 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// packArchive is a deterministic gzip tar holding exactly one regular file.
func packArchive(member string, binary []byte) ([]byte, error) {
	var out bytes.Buffer
	gz, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: member, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(binary)), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(binary); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// writeSelection writes the qualified selection next to the embedded archives
// (git-ignored internal/nativeartifacts/embedded/selection/), so make build
// never rewrites a committed file. Each file is replaced atomically.
func writeSelection(manifest []byte, digest string, rego []byte) error {
	dir := filepath.Join("internal", "nativeartifacts", "embedded", "selection")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		raw  []byte
	}{{"manifest.json", manifest}, {"manifest.sha256", []byte(digest)}, {"sandbox-policy.rego", rego}} {
		temporary := filepath.Join(dir, "."+f.name+".tmp")
		if err := os.WriteFile(temporary, f.raw, 0o644); err != nil {
			return err
		}
		if err := os.Rename(temporary, filepath.Join(dir, f.name)); err != nil {
			return err
		}
	}
	return nil
}

// patchChain is the ordered qualified patch stack: the fixed first patch, the
// second, then any later patches, each parented on the previous tree and the
// last ending at the fork tree. Every later patch hash is in the record.
func patchChain(in qualifyInputs, fixed map[string]string, record []byte) ([]openshell.SourcePatch, error) {
	patches := []openshell.SourcePatch{
		{Name: "unmediated-network.patch", SHA256: fixed["qualifiedPatch1SHA256"], ParentTree: fixed["qualifiedUpstreamTree"], Tree: fixed["qualifiedPatch1Tree"]},
	}
	if len(in.ExtraPatches) == 0 {
		return append(patches, openshell.SourcePatch{Name: "workspace-read-only.patch", SHA256: in.Patch2SHA256, ParentTree: fixed["qualifiedPatch1Tree"], Tree: in.ForkTree}), nil
	}
	if in.Patch2Tree == "" {
		return nil, fmt.Errorf("extra_patches require patch2_tree")
	}
	patches = append(patches, openshell.SourcePatch{Name: "workspace-read-only.patch", SHA256: in.Patch2SHA256, ParentTree: fixed["qualifiedPatch1Tree"], Tree: in.Patch2Tree})
	for _, p := range in.ExtraPatches {
		if len(p.SHA256) != 64 || !bytes.Contains(record, []byte(p.SHA256)) {
			return nil, fmt.Errorf("patch %s sha256 is not recorded in %s", p.Name, in.Patch2Record)
		}
		patches = append(patches, openshell.SourcePatch{Name: p.Name, SHA256: p.SHA256, ParentTree: patches[len(patches)-1].Tree, Tree: p.Tree})
	}
	if patches[len(patches)-1].Tree != in.ForkTree {
		return nil, fmt.Errorf("last patch tree %s differs from fork_tree %s", patches[len(patches)-1].Tree, in.ForkTree)
	}
	return patches, nil
}
