package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/safepath"
)

type authImportFile struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}

func anthropicSelection(raw []byte) (string, error) {
	if len(raw) > 256 {
		return "", errors.New("anthropic active profile selector is too large")
	}
	name := strings.TrimSpace(string(raw))
	if name == "" || name == "." || name == ".." || len(name) > 250 || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\\x00\r\n\t") {
		return "", errors.New("anthropic active profile selector is invalid")
	}
	return name, nil
}

// Validate the relationship before any writes. Errors never include parsed values.
func validateAnthropicFiles(target accountAuthTarget, files []authImportFile) ([]authImportFile, error) {
	spec := target.profile.Auth
	if spec.Format != agentprofile.AuthFormatClaude || spec.ProfilesDir == "" {
		return nil, errors.New("this profile does not declare Anthropic profile storage")
	}
	if len(files) != 3 {
		return nil, errors.New("anthropic import requires credentials, config and active selection")
	}
	entries := map[string][]byte{}
	total := 0
	for _, f := range files {
		total += len(f.Data)
		if _, duplicate := entries[f.Path]; duplicate || total > controller.MaxDocument {
			return nil, errors.New("anthropic import is duplicated or exceeds size limit")
		}
		entries[f.Path] = f.Data
	}
	activePath := path.Join(spec.ProfilesDir, "active_config")
	active, ok := entries[activePath]
	if !ok {
		return nil, errors.New("anthropic import is missing active selection")
	}
	name, err := anthropicSelection(active)
	if err != nil {
		return nil, err
	}
	configPath := path.Join(spec.ProfilesDir, "configs", name+".json")
	credentialPath := path.Join(spec.ProfilesDir, "credentials", name+".json")
	config, configOK := entries[configPath]
	credentials, credentialsOK := entries[credentialPath]
	if !configOK || !credentialsOK {
		return nil, errors.New("anthropic import must contain the selected config and credential files")
	}
	var cfg struct {
		Authentication struct {
			Type string `json:"type"`
		} `json:"authentication"`
	}
	var cred struct {
		Type        string `json:"type"`
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(config, &cfg) != nil || json.Unmarshal(credentials, &cred) != nil || cfg.Authentication.Type != "user_oauth" || cred.Type != "oauth_token" || strings.TrimSpace(cred.AccessToken) == "" {
		return nil, errors.New("anthropic import requires a supported user_oauth config and nonempty oauth_token credential; external auth is not imported")
	}
	// Publish the selector last. Individual files retain the native helper's
	// atomic write semantics; this is not a cross-file transaction.
	return []authImportFile{{configPath, config}, {credentialPath, credentials}, {activePath, active}}, nil
}

func prepareAnthropicImport(target accountAuthTarget, dir string) (authImportDocument, error) {
	doc := authImportDocument{Source: dir}
	if target.profile.Auth.Format != agentprofile.AuthFormatClaude || target.profile.Auth.ProfilesDir == "" {
		return doc, errors.New("--anthropic-dir requires a Claude profile with declared profile storage")
	}
	dir, err := expandUserPath(dir)
	if err != nil {
		return doc, err
	}
	doc.Source = dir
	active, err := safepath.ReadFile(dir, "active_config", 256)
	if errors.Is(err, os.ErrNotExist) {
		active = []byte("default\n")
	} else if err != nil {
		return doc, errors.New("cannot read Anthropic active selection")
	}
	name, err := anthropicSelection(active)
	if err != nil {
		return doc, err
	}
	base := target.profile.Auth.ProfilesDir
	for _, rel := range []string{path.Join("configs", name+".json"), path.Join("credentials", name+".json")} {
		body, err := safepath.ReadFile(dir, rel, controller.MaxDocument)
		if err != nil {
			return doc, errors.New("cannot read selected Anthropic config/credential; pass the complete profile directory")
		}
		doc.NativeFiles = append(doc.NativeFiles, authImportFile{Path: path.Join(base, rel), Data: body})
	}
	doc.NativeFiles = append(doc.NativeFiles, authImportFile{Path: path.Join(base, "active_config"), Data: active})
	doc.NativeFiles, err = validateAnthropicFiles(target, doc.NativeFiles)
	return doc, err
}

func rejectMisplacedAnthropicFile(target accountAuthTarget, raw []byte) error {
	if target.profile.Auth.Format != agentprofile.AuthFormatClaude {
		return nil
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return nil
	} // legacy import remains opaque
	if _, ok := value["access_token"]; ok {
		return errors.New("anthropic profile credentials need their config/selection; use --anthropic-dir DIR instead of --file")
	}
	if _, ok := value["authentication"]; ok {
		return errors.New("anthropic profile config is not a legacy login; use --anthropic-dir DIR")
	}
	return nil
}

func nativeAuthWrites(target accountAuthTarget, doc authImportDocument) ([]authImportFile, error) {
	branches := 0
	if len(doc.Native) > 0 {
		branches++
	}
	if len(doc.NativeFiles) > 0 {
		branches++
	}
	if doc.SecretValue != "" {
		branches++
	}
	if branches != 1 {
		return nil, errors.New("exactly one native login, Anthropic profile or imported secret is required")
	}
	if len(doc.SecretValue) > controller.MaxDocument {
		return nil, errors.New("import exceeds size limit")
	}
	if doc.SecretValue != "" {
		return nil, nil
	}
	if len(doc.NativeFiles) > 0 {
		return validateAnthropicFiles(target, doc.NativeFiles)
	}
	if len(doc.Native) > controller.MaxDocument || len(bytes.TrimSpace(doc.Native)) == 0 {
		return nil, errors.New("login file is empty or exceeds size limit")
	}

	if err := rejectMisplacedAnthropicFile(target, doc.Native); err != nil {
		return nil, err
	}
	return []authImportFile{{Path: target.profile.Auth.CredentialFile, Data: doc.Native}}, nil
}

func defaultAnthropicImportDir(target accountAuthTarget) (string, error) {
	if target.profile.Auth.Format != agentprofile.AuthFormatClaude || target.profile.Auth.ProfilesDir == "" {
		return "", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "anthropic")
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect Anthropic import directory: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("anthropic import source is not a directory")
	}
	active, err := safepath.ReadFile(dir, "active_config", 256)
	name := "default"
	if err == nil {
		name, err = anthropicSelection(active)
	} else if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return "", errors.New("cannot inspect Anthropic active selection; choose an explicit import source")
	}
	present, err := safepath.RegularFileExists(dir, path.Join("credentials", name+".json"))
	if err != nil {
		return "", errors.New("cannot inspect selected Anthropic credential; choose an explicit import source")
	}
	if !present {
		return "", nil
	}
	return dir, nil
}
