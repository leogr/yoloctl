package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/spacelock"
	"gopkg.in/yaml.v3"
)

type editDocument struct {
	Revision string `json:"revision"`
	Data     []byte `json:"data"`
}

type authImportDocument struct {
	Source        string           `json:"source"`
	ProfileDigest string           `json:"profile_digest"`
	Native        []byte           `json:"native,omitempty"`
	NativeFiles   []authImportFile `json:"native_files,omitempty"`
	SecretValue   string           `json:"secret_value,omitempty"`
}

type copySourceKey struct{}
type controllerSecretKey struct{}
type controllerSecret struct {
	Value string `json:"value"`
}

// controllerDocumentFn is a test seam; production reads the native store.
var controllerDocumentFn = controllerDocument

func controllerDocument(kind, name, catalogDigest string) (editDocument, error) {
	c := spaceenv.Current()
	return controllerDocumentAt(c.ConfigDir, c.Generation, kind, name, catalogDigest)
}

func controllerDocumentAt(root, generation, kind, name, catalogDigest string) (editDocument, error) {
	var doc editDocument
	if kind != "box" && kind != "account" {
		return doc, errors.New("invalid declaration kind")
	}
	validationKind := kind
	if kind == "box" {
		validationKind = "agentbox"
	}
	if err := layout.ValidateName(validationKind, name); err != nil {
		return doc, err
	}
	dir := "boxes"
	if kind == "account" {
		dir = "accounts"
	}
	raw, info, err := safepath.ReadFileInfo(root, dir+"/"+name+".yaml", controller.MaxDocument)
	if err != nil {
		return doc, err
	}
	settings, sInfo, err := safepath.ReadFileInfo(root, "space.yaml", controller.MaxDocument)
	if err != nil {
		return doc, err
	}
	hash := sha256.New()
	for _, data := range [][]byte{raw, settings, []byte(catalogDigest), []byte(generation)} {
		_, _ = fmt.Fprintf(hash, "%d:", len(data))
		_, _ = hash.Write(data)
	}
	// File identity rejects a destroy/recreate with identical YAML, as well as
	// ordinary changes. Both metadata records refer to the read descriptors.
	for _, item := range []os.FileInfo{info, sInfo} {
		stat, ok := item.Sys().(*syscall.Stat_t)
		if !ok {
			return doc, errors.New("declaration filesystem identity unavailable")
		}
		_, _ = fmt.Fprintf(hash, "%d:%d:%d;", stat.Dev, stat.Ino, item.ModTime().UnixNano())
	}
	return editDocument{Revision: hex.EncodeToString(hash.Sum(nil)), Data: raw}, nil
}

func runControllerDocument(ctx context.Context, req controller.Request, catalogDigest string) (handled bool, result error) {
	if req.Method == "space.validate-grants" {
		var mounts []spaceconfig.Mount
		if err := decodeBoundedJSON(bytes.NewReader(req.Payload), &mounts, controller.MaxDocument); err != nil {
			return true, err
		}
		return true, validateControllerGrants(mounts)
	}
	if req.Method == "space.settings" {
		cfg, err := spaceconfig.Load(req.Space)
		if err != nil {
			return true, err
		}
		return true, json.NewEncoder(os.Stdout).Encode(cfg)
	}
	if req.Method == "box.copy-source" {
		if len(req.Args) != 1 {
			return true, errors.New("source box is required")
		}
		cfg, err := agentbox.Load(req.Space, req.Args[0])
		if err != nil {
			return true, err
		}
		raw, err := yaml.Marshal(cfg)
		if err != nil {
			return true, err
		}
		_, err = os.Stdout.Write(raw)
		return true, err
	}
	if req.Method == "account.auth.import" {
		return true, applyControllerAuthImport(ctx, req)
	}
	kind, phase, special := "", "", false
	for _, candidate := range []string{"box", "account"} {
		for _, action := range []string{"get", "apply"} {
			if req.Method == candidate+".edit."+action {
				kind, phase, special = candidate, action, true
			}
		}
	}
	if !special {
		return false, nil
	}
	if len(req.Args) != 1 {
		return true, errors.New("one declaration name is required")
	}
	name := req.Args[0]
	var box *agentsession.BoxLock
	lockEntity := func() (func(), error) {
		l, err := lockSessionBoxFn(req.Space, name)
		if err != nil {
			return nil, err
		}
		box = l
		return l.Release, nil
	}
	if kind == "account" {
		lockEntity = func() (func(), error) { return account.Lock(req.Space, name) }
	}
	release, err := lockManifestEdit(ctx, os.Stderr, req.Space, lockEntity)
	if err != nil {
		return true, err
	}
	defer release()
	roleSyncFailed := false
	if phase == "apply" {
		defer completeBeforeUnlock(ctx, &result, &roleSyncFailed)
	}
	if kind == "box" && phase == "apply" {
		if err := revalidateOperatorFn(ctx); err != nil {
			return true, err
		}
	}
	doc, err := controllerDocumentFn(kind, name, catalogDigest)
	if err != nil {
		return true, err
	}
	if phase == "get" {
		return true, json.NewEncoder(os.Stdout).Encode(doc)
	}
	var edited editDocument
	if err := decodeBoundedJSON(bytes.NewReader(req.Payload), &edited, 2*controller.MaxDocument); err != nil {
		return true, err
	}
	if len(edited.Data) > controller.MaxDocument || edited.Revision == "" || edited.Revision != doc.Revision {
		return true, asUserError(errors.New("declaration, space or catalogs changed while editing; fetch current state before saving"))
	}
	if kind == "account" {
		cfg, err := account.ParseManifest(edited.Data)
		if err != nil {
			return true, asUserError(errors.New("invalid edited account declaration"))
		}
		if cfg.Name != name || cfg.SchemaVersion != account.SchemaVersion {
			return true, asUserError(errors.New("account edit cannot change name or schema"))
		}
		cfg.Space = req.Space
		if err := controller.BeginScopedEffects(ctx, accountDocumentIntent(name, true)); err != nil {
			return true, err
		}
		if err := saveVerifiedAccount(ctx, cfg); err != nil {
			return true, err
		}
		_, _ = fmt.Fprintf(os.Stdout, "saved account %s\n", name)
		printSecretPickupNote(os.Stderr, name)
		return true, nil
	}
	cfg, err := agentbox.ParseManifest(edited.Data)
	if err != nil {
		return true, asUserError(fmt.Errorf("invalid edited box declaration: %w", err))
	}
	if cfg.Name != name || cfg.Space != req.Space {
		return true, asUserError(errors.New("box edit cannot change the box name or space; use box copy"))
	}
	if err := requireResolvableManifest(ctx, cfg); err != nil {
		return true, err
	}
	if _, err := canonicalizeUserMountSources(cfg); err != nil {
		return true, err
	}
	if err := rejectAccountRootBinds(cfg); err != nil {
		return true, err
	}
	// Same image mode as the reconcile's creation preflight below: a native box
	// must retain its preparation here, or that later check finds none after
	// apply began and refuses every edit.
	if err := warmBoxBeforeEffects(defaultCreationContext(ctx, cfg), container.New(req.Space), cfg, true); err != nil {
		return true, err
	}
	// Permission precedes the save, the reconcile and the deferred role
	// publication; a refused or canceled gate changes nothing.
	if err := controller.BeginScopedEffects(ctx, boxEditIntent(ctx, req.Space, cfg)); err != nil {
		return true, err
	}
	// Entity/space locks remain held through role publication, including
	// failures after a declaration save or partial lifecycle change. A failed
	// publication withholds the completion checkpoint.
	defer func() {
		syncErr := syncNativeChatRolesFn(ctx, req.Space)
		roleSyncFailed = syncErr != nil
		result = errors.Join(result, syncErr)
	}()
	if err := saveRepointableBoxManifest(cfg); err != nil {
		return true, err
	}
	return true, reconcileAndPrint(ctx, box, os.Stdout, container.New(req.Space), cfg, name)
}

func validateControllerGrants(mounts []spaceconfig.Mount) error {
	space := spaceenv.Current().Space
	before, err := spaceconfig.Load(space)
	if err != nil {
		return err
	}
	after := *before
	after.Mounts = mounts
	if err := after.Validate(); err != nil {
		return err
	}
	names, err := agentbox.ListNames(space)
	if err != nil {
		return err
	}
	for _, name := range names {
		cfg, err := agentbox.Load(space, name)
		if err != nil {
			return err
		}
		for _, bind := range agentboxHostMounts(cfg) {
			if classifyBindGrant(before, bind.Path, bind.Writable) == bindGrantOK && classifyBindGrant(&after, bind.Path, bind.Writable) != bindGrantOK {
				return fmt.Errorf("mount change would revoke %s from box %s; edit or remove that declaration first", bind.Path, name)
			}
		}
	}
	return nil
}

func applyControllerAuthImport(ctx context.Context, req controller.Request) (result error) {
	if len(req.Args) != 2 {
		return errors.New("account and agent names are required")
	}
	target, err := resolveAccountAuthTarget(req.Space, req.Args)
	if err != nil {
		return err
	}
	var doc authImportDocument
	if err := decodeBoundedJSON(bytes.NewReader(req.Payload), &doc, 2*controller.MaxDocument); err != nil {
		return err
	}
	raw, err := yaml.Marshal(target.profile)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if doc.ProfileDigest != hex.EncodeToString(digest[:]) {
		return errors.New("agent import conventions changed; retry with current catalogs")
	}
	writes, err := nativeAuthWrites(target, doc)
	if err != nil {
		return err
	}
	releaseSpace, err := spacelock.Acquire(ctx, req.Space, true)
	if err != nil {
		return err
	}
	defer releaseSpace()
	defer completeBeforeUnlock(ctx, &result, nil)
	release, err := account.Lock(target.space, target.name)
	if err != nil {
		return err
	}
	defer release()
	if err := ensureAccountSelectableLocked(target.space, target.name); err != nil {
		return err
	}
	// Only the secret branch is one verifiable declaration write; a native
	// login import keeps its account held when interrupted.
	if err := controller.BeginScopedEffects(ctx, accountDocumentIntent(target.name, doc.SecretValue != "")); err != nil {
		return err
	}
	if doc.SecretValue != "" {
		rule := target.profile.Auth.ImportSecret
		if rule == nil {
			return errors.New("agent has no declared API-key import")
		}
		acct, err := account.Load(target.space, target.name)
		if err != nil {
			return err
		}
		acct.SetSecret(rule.Secret, doc.SecretValue)
		if err := saveVerifiedAccount(ctx, acct); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(os.Stdout, "imported %s API key into account %s (secret %s)\n", target.profile.Name, target.name, rule.Secret)
		printSecretPickupNote(os.Stderr, target.name)
		return nil
	}
	if err := accountStopUsers(ctx, target.space, target.name, false, os.Stderr); err != nil {
		return err
	}
	for _, write := range writes {
		if _, err := accountStateOperation(ctx, target.space, accountstate.Request{Action: "write", Account: target.name, Agent: target.profile.Name, Path: write.Path, Data: write.Data}); err != nil {
			return err
		}
	}
	destination := target.authFilePath()
	if len(doc.NativeFiles) > 0 {
		destination = target.stateDir + "/" + target.profile.Auth.ProfilesDir
	}
	_, _ = fmt.Fprintf(os.Stdout, "imported %s login into account %s (%s)\n", target.profile.Name, target.name, destination)
	return nil
}

func prepareAuthImport(args []string, file, anthropicDir, space string) (authImportDocument, error) {
	var doc authImportDocument
	target, err := resolveAccountAuthTarget(space, args)
	if err != nil {
		return doc, err
	}
	profile, err := yaml.Marshal(target.profile)
	if err != nil {
		return doc, err
	}
	digest := sha256.Sum256(profile)
	doc.ProfileDigest = hex.EncodeToString(digest[:])
	if file != "" && anthropicDir != "" {
		return doc, errors.New("choose --file or --anthropic-dir, not both")
	}
	if anthropicDir != "" {
		bundle, err := prepareAnthropicImport(target, anthropicDir)
		bundle.ProfileDigest = doc.ProfileDigest
		return bundle, err
	}
	if file != "" {
		file, err = expandUserPath(file)
		if err != nil {
			return doc, err
		}
	} else {
		candidates, err := defaultHostAuthPaths(target.profile)
		if err != nil {
			return doc, err
		}
		file = firstExistingFile(candidates)
		dir, err := defaultAnthropicImportDir(target)
		if err != nil {
			return doc, err
		}
		if dir != "" {
			if file != "" {
				return doc, errors.New("both legacy and Anthropic login state exist; choose --file PATH or --anthropic-dir DIR")
			}
			bundle, err := prepareAnthropicImport(target, dir)
			bundle.ProfileDigest = doc.ProfileDigest
			return bundle, err
		}
	}
	read := func(path string) ([]byte, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		data, err := io.ReadAll(io.LimitReader(f, controller.MaxDocument+1))
		if err != nil {
			return nil, err
		}
		if len(data) > controller.MaxDocument {
			return nil, errors.New("login file exceeds size limit")
		}
		return data, nil
	}
	if file != "" {
		doc.Source = file
		doc.Native, err = read(file)
		if err == nil && len(bytes.TrimSpace(doc.Native)) == 0 {
			err = errors.New("login file is empty")
		}
		if err == nil {
			err = rejectMisplacedAnthropicFile(target, doc.Native)
		}
		return doc, err
	}
	rule := target.profile.Auth.ImportSecret
	if rule == nil {
		return doc, errors.New("no login file found; pass --file PATH or log in inside a box")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return doc, err
	}
	doc.Source = home + "/" + rule.File
	data, err := read(doc.Source)
	if err != nil {
		return doc, err
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(data, &source); err != nil {
		return doc, errors.New("login source is not valid JSON")
	}
	if err := json.Unmarshal(source[rule.Field], &doc.SecretValue); err != nil || strings.TrimSpace(doc.SecretValue) == "" {
		return doc, fmt.Errorf("login source has no nonempty %s field", rule.Field)
	}
	return doc, nil
}
