package audit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
)

// MaxRuleFile is the one user-selected rules-management capacity. There is no
// count, aggregate, selection or validation-duration limit.
const MaxRuleFile = 2 << 20

var ErrRuleGenerationChanged = errors.New("audit rules changed; reopen the editor or retry the explicit operation")

type RuleFile struct {
	Name     string    `json:"name"`
	SHA256   string    `json:"sha256"`
	Imported time.Time `json:"imported,omitempty"`
	Builtin  bool      `json:"builtin,omitempty"`
}

// RuleSet is an immutable native generation. Selection stays verbatim; files
// are metadata in load order, with the shipped rules first. Its fingerprint
// binds the tool/plugin pins, bytes, order, metadata and selection.
type RuleSet struct {
	Generation  string     `json:"generation"`
	Fingerprint string     `json:"fingerprint"`
	Pins        string     `json:"pins"`
	Files       []RuleFile `json:"files"`
	Selection   []byte     `json:"selection"`
	Warnings    uint64     `json:"warnings"`
}

// RuleStore never selects desired state or starts/stops capture. Its caller
// publishes a prepared generation through native settings under the existing
// audit lock/receipt. Failed/unselected preparation cannot change that setting.
type RuleStore struct{ Root string }

type RuleChange struct {
	Kind   string
	Name   string
	SHA256 string
	Body   io.Reader
}

type Validation struct {
	Diagnostics string `json:"diagnostics,omitempty"`
	Warnings    uint64 `json:"warnings"`
}

type RuleValidator func(context.Context, string, string) (Validation, error)

func RulesPinsDigest() string {
	raw, _ := json.Marshal(artifacts)
	return Digest(append([]byte(Version+"\n"+RulesDigest()+"\n"), raw...))
}

func defaultRuleSet() RuleSet {
	return RuleSet{Pins: RulesPinsDigest(), Files: []RuleFile{{Name: "rules.yaml", SHA256: Digest([]byte(Rules)), Builtin: true}}, Selection: []byte("rules: []\n")}
}

func (set RuleSet) digest() string {
	set.Generation, set.Fingerprint = "", ""
	raw, _ := json.Marshal(set)
	return Digest(raw)
}

func ruleFilePath(file RuleFile) string {
	if file.Builtin {
		return "rules.yaml"
	}
	return filepath.Join("files", file.SHA256+".yaml")
}

func validDigest(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == value
}

func validRuleName(name string) bool {
	// Display metadata only, never used as a storage path. Control characters
	// remain data and are escaped by the ordinary terminal display boundary.
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

func (set RuleSet) check() error {
	if !validDigest(set.Pins) || len(set.Files) == 0 || !set.Files[0].Builtin || set.Files[0].Name != "rules.yaml" || !set.Files[0].Imported.IsZero() {
		return errors.New("invalid audit rule manifest")
	}
	seen := map[string]bool{}
	for i, file := range set.Files {
		if !validDigest(file.SHA256) || !validRuleName(file.Name) || (i > 0 && (file.Builtin || file.Imported.IsZero())) || seen[file.SHA256] {
			return errors.New("invalid or duplicate audit rule file metadata")
		}
		seen[file.SHA256] = true
	}
	selection, err := SelectionDocument(set.Selection)
	if err != nil {
		return err
	}
	if !bytes.Equal(selection, set.Selection) {
		return errors.New("audit selection is not canonical")
	}
	if set.Fingerprint != set.digest() {
		return errors.New("audit rule manifest fingerprint mismatch")
	}
	return nil
}

// Read is read-only: it never installs tools, creates storage, or enables audit.
// A missing selected generation is an error, never a built-in-only fallback.
func (store RuleStore) Read(generation string) (RuleSet, error) {
	if generation == "" {
		set := defaultRuleSet()
		set.Fingerprint = set.digest()
		return set, nil
	}
	dir, err := layout.AuditRulesGeneration(store.Root, generation)
	if err != nil {
		return RuleSet{}, err
	}
	rel, err := filepath.Rel(store.Root, dir)
	if err != nil {
		return RuleSet{}, err
	}
	if err := safepath.VerifyPrivateDirectory(store.Root, rel, os.Geteuid(), os.Getegid()); err != nil {
		return RuleSet{}, err
	}
	// MaxInt64-1 is only io.LimitReader's representational ceiling (its helper
	// adds one); it is not a feature capacity for metadata or selection.
	raw, err := safepath.ReadFile(dir, "manifest.json", math.MaxInt64-1)
	if err != nil {
		return RuleSet{}, err
	}
	set, err := DecodeRuleSet(raw)
	if err != nil {
		return set, err
	}
	if set.Generation != generation {
		return set, errors.New("audit rule generation mismatch")
	}
	for _, file := range set.Files {
		raw, err := safepath.ReadFile(dir, ruleFilePath(file), MaxRuleFile)
		if err != nil {
			return set, err
		}
		if Digest(raw) != file.SHA256 {
			return set, fmt.Errorf("audit rule file %q checksum mismatch", Clean(file.Name))
		}
	}
	selection, err := safepath.ReadFile(dir, "selection.yaml", math.MaxInt64-1)
	if err != nil {
		return set, err
	}
	if !bytes.Equal(selection, set.Selection) {
		return set, errors.New("audit rule selection checksum mismatch")
	}
	return set, nil
}

func (store RuleStore) file(set RuleSet, file RuleFile) ([]byte, error) {
	if set.Generation == "" && file.Builtin {
		return []byte(Rules), nil
	}
	dir, err := layout.AuditRulesGeneration(store.Root, set.Generation)
	if err != nil {
		return nil, err
	}
	return safepath.ReadFile(dir, ruleFilePath(file), MaxRuleFile)
}

// Compatible is separate from Read so an incompatible retained generation can
// still be inspected and explicitly repaired without discarding custom files.
func (set RuleSet) Compatible() bool { return set.Pins == RulesPinsDigest() }

// Configuration verifies the selected immutable envelope before consumption.
// Changed pins require explicit refresh, not an implicit built-in fallback.
func (store RuleStore) Configuration(generation, tools, metadata string) ([]byte, error) {
	set, err := store.Read(generation)
	if err != nil {
		return nil, err
	}
	if !set.Compatible() {
		return nil, errors.New("retained audit rules need explicit validation with this build")
	}
	dir, err := layout.AuditRulesGeneration(store.Root, generation)
	if err != nil {
		return nil, err
	}
	expected, err := ruleConfiguration(dir, tools, metadata, set)
	if err != nil {
		return nil, err
	}
	raw, err := safepath.ReadFile(dir, "falco.json", math.MaxInt64-1)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(raw, expected) {
		return nil, errors.New("audit rule configuration differs from its immutable envelope")
	}
	return raw, nil
}

func ruleIndex(files []RuleFile, name, prefix string) (int, error) {
	if prefix != "" {
		if len(prefix) < 8 || len(prefix) > 64 {
			return -1, errors.New("--sha256 needs at least eight hex digits")
		}
		if _, err := hex.DecodeString(prefix + strings.Repeat("0", len(prefix)%2)); err != nil {
			return -1, errors.New("invalid SHA256 prefix")
		}
		prefix = strings.ToLower(prefix)
	}
	index := -1
	for i, file := range files {
		if file.Name == name && strings.HasPrefix(file.SHA256, prefix) {
			if index != -1 {
				return -1, errors.New("ambiguous rule file name; use --sha256 with a unique prefix")
			}
			index = i
		}
	}
	if index < 0 {
		return -1, fmt.Errorf("no rule file named %q matches", Clean(name))
	}
	if files[index].Builtin {
		return -1, errors.New("the built-in rule file cannot be removed")
	}
	return index, nil
}

// Prepare writes a new private generation and validates exactly once. The
// returned set is unselected until the caller commits native desired state.
// The caller supplies already verified installed tools and metadata path.
func (store RuleStore) Prepare(ctx context.Context, expected string, change RuleChange, tools, metadata string, validate RuleValidator) (set RuleSet, validation Validation, changed bool, result error) {
	old, err := store.Read(expected)
	if err != nil {
		return set, validation, false, err
	}
	set = old
	set.Files = slices.Clone(old.Files)
	set.Selection = bytes.Clone(old.Selection)
	var imported []byte
	switch change.Kind {
	case "import":
		if !validRuleName(change.Name) || change.Body == nil {
			return set, validation, false, errors.New("import needs a basename and rule bytes")
		}
		imported, err = io.ReadAll(io.LimitReader(change.Body, MaxRuleFile+1))
		if err != nil {
			return set, validation, false, err
		}
		if len(imported) > MaxRuleFile {
			return set, validation, false, errors.New("one rule file must not exceed 2 MiB")
		}
		hash := Digest(imported)
		for _, file := range old.Files {
			if file.SHA256 == hash {
				return old, validation, false, nil
			}
		}
		set.Files = append(set.Files, RuleFile{Name: change.Name, SHA256: hash, Imported: time.Now().UTC()})
	case "remove":
		i, err := ruleIndex(set.Files, change.Name, change.SHA256)
		if err != nil {
			return set, validation, false, err
		}
		set.Files = slices.Delete(set.Files, i, i+1)
	case "edit":
		if change.Body == nil {
			return set, validation, false, errors.New("selection bytes are required")
		}
		raw, err := io.ReadAll(change.Body)
		if err != nil {
			return set, validation, false, err
		}
		set.Selection, err = SelectionDocument(raw)
		if err != nil {
			return set, validation, false, err
		}
		if bytes.Equal(set.Selection, old.Selection) && old.Compatible() {
			return old, validation, false, nil
		}
	case "refresh": // Explicit re-enable/upgrade keeps imports and selection.
	default:
		return set, validation, false, errors.New("unknown audit rule change")
	}
	if err := ctx.Err(); err != nil {
		return set, validation, false, err
	}
	set.Pins = RulesPinsDigest()
	set.Files[0] = defaultRuleSet().Files[0]
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return set, validation, false, err
	}
	set.Generation = hex.EncodeToString(nonce[:])
	dir, err := layout.AuditRulesGeneration(store.Root, set.Generation)
	if err != nil {
		return set, validation, false, err
	}
	rel, err := filepath.Rel(store.Root, dir)
	if err != nil {
		return set, validation, false, err
	}
	if err := safepath.PrivateDirectory(store.Root, rel, os.Geteuid(), os.Getegid()); err != nil {
		return set, validation, false, err
	}
	// Only this newly allocated, unselected private attempt is owned here.
	defer func() {
		if result != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	for i, file := range set.Files {
		var raw []byte
		switch {
		case i == 0:
			raw = []byte(Rules)
		case change.Kind == "import" && i == len(set.Files)-1:
			raw = imported
		default:
			raw, err = store.file(old, file)
		}
		if err != nil {
			return set, validation, false, err
		}
		if Digest(raw) != file.SHA256 {
			return set, validation, false, errors.New("audit rule file changed during preparation")
		}
		if err := safepath.WriteFile(dir, ruleFilePath(file), raw, 0600); err != nil {
			return set, validation, false, err
		}
	}
	if err := safepath.WriteFile(dir, "selection.yaml", set.Selection, 0600); err != nil {
		return set, validation, false, err
	}
	config, err := ruleConfiguration(dir, tools, metadata, set)
	if err != nil {
		return set, validation, false, err
	}
	if err := safepath.WriteFile(dir, "falco.json", config, 0600); err != nil {
		return set, validation, false, err
	}
	if validate == nil {
		return set, validation, false, errors.New("audit rule validator is required")
	}
	validation, err = validate(ctx, filepath.Join(tools, "bin", "falco"), filepath.Join(dir, "falco.json"))
	if err != nil {
		return set, validation, false, err
	}
	if err := ctx.Err(); err != nil {
		return set, validation, false, err
	}
	set.Warnings = validation.Warnings
	set.Fingerprint = set.digest()
	if err := set.check(); err != nil {
		return set, validation, false, err
	}
	raw, err := json.Marshal(set)
	if err != nil {
		return set, validation, false, err
	}
	if err := safepath.WriteFile(dir, "manifest.json", raw, 0600); err != nil {
		return set, validation, false, err
	}
	return set, validation, true, nil
}

func ruleConfiguration(dir, tools, metadata string, set RuleSet) ([]byte, error) {
	raw, err := Configuration(dir, tools, metadata)
	if err != nil {
		return nil, err
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(set.Files))
	for _, file := range set.Files {
		paths = append(paths, filepath.Join(dir, ruleFilePath(file)))
	}
	config["rules_files"] = paths
	config["rules"] = []any{}
	config["config_files"] = []any{map[string]string{"path": filepath.Join(dir, "selection.yaml"), "strategy": "override"}}
	return json.MarshalIndent(config, "", "  ")
}

// DecodeRuleSet checks only storage framing/integrity, never rule semantics.
// Archive import uses it before any native publication and remains disabled.
func DecodeRuleSet(raw []byte) (RuleSet, error) {
	var set RuleSet
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&set); err != nil {
		return set, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return set, errors.New("trailing audit rule manifest data")
	}
	if _, err := layout.AuditRulesGeneration("/", set.Generation); err != nil {
		return set, err
	}
	return set, set.check()
}

// FileBytes is a verified read from a previously read immutable set. Display
// names never participate in paths, and callers cannot supply a digest path.
func (store RuleStore) FileBytes(set RuleSet, file RuleFile) ([]byte, error) {
	if !validDigest(file.SHA256) {
		return nil, errors.New("invalid audit rule file digest")
	}
	raw, err := store.file(set, file)
	if err != nil {
		return nil, err
	}
	if Digest(raw) != file.SHA256 {
		return nil, errors.New("audit rule file checksum mismatch")
	}
	return raw, nil
}
