package openshell

import (
	"bytes"
	"context"
	"errors"
	"strings"
)

// This private reader accepts ONLY this original protocol's positive Stop +
// inactivity stage. The actual inactive callback must independently prove the
// fixed CID/process/native storage; an ACK or this metadata is not that proof.
// No public callback construction, file mutation or removal authority is added.
func refreshNativeContainerAfterStop(ctx context.Context, original *NativeContainerInventory, release QualifiedRelease, cid string, ack *nativeContainerStopACK) (_ *NativeContainerInventory, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if original == nil || original.root == nil || original.tree == nil {
		return nil, errors.New("native status refresh lacks its original retained inventory")
	}
	if err := ack.coversInventory(original, cid); err != nil {
		return nil, err
	}
	next, err := inventoryNativeContainerAt(ctx, original.root, original.current, release, original.owners)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = next.Close()
		}
	}()
	if err := ack.coversInventory(next, cid); err != nil {
		return nil, err
	}
	if err := compareRuntimeInventories(original, next); err != nil {
		return nil, err
	}
	if err := next.Check(ctx); err != nil {
		return nil, err
	}
	return next, nil
}
func compareRuntimeInventories(original, next *NativeContainerInventory) error {
	if original == nil || next == nil {
		return errors.New("native inventory comparison lacks original observations")
	}
	if next.identity != original.identity {
		return errors.New("native status refresh changed original root mapping")
	}
	if len(original.rawRecords) != len(next.rawRecords) {
		return errors.New("native status refresh changed immutable generation set")
	}
	for generation, raw := range original.rawRecords {
		if !bytes.Equal(raw, next.rawRecords[generation]) {
			return errors.New("native status refresh changed immutable record bytes")
		}
	}
	if err := compareRuntimeOwnershipAnchors("", original.tree, next.tree); err != nil {
		return err
	}
	return nil
}
func (a *nativeContainerStopACK) coversInventory(i *NativeContainerInventory, cid string) error {
	if a == nil || a.attempt == nil || i == nil || i.root == nil || len(i.records) != len(a.attempt.records) || i.current.Generation != a.attempt.currentGeneration {
		return errors.New("native Stop ACK does not cover the complete selected runtime inventory")
	}
	for _, record := range i.records {
		if err := a.covers(record, cid); err != nil {
			return err
		}
	}
	return nil
}

// Only these already described role-owned observations may appear/disappear
// or be atomically replaced at shutdown. Fresh schema inventory still refuses
// unknown paths, wrong roles/types/modes/links or ANY mount. No status value or
// inode here grants ownership of another root, generation, record or actor.
func mutableRuntimeObservation(path string) bool {
	parts := strings.Split(path, "/")
	if len(parts) < 4 || parts[0] != "generations" || !reservationGeneration(parts[1]) || parts[2] != "status" {
		return false
	}
	if len(parts) == 4 && runtimeAtomicTemporary(parts[3]) {
		return true
	}
	switch parts[3] {
	case "runtime.json":
		return len(parts) == 4
	case "runtime-control", "runtime-supervisor":
		return true
	case "forwards":
		// Cleanup removes publications, never the shared transaction lock or its
		// parent. Keep those anchors even when no worker remains observable.
		return len(parts) == 5 && (strings.HasSuffix(parts[4], ".json") || runtimeAtomicTemporary(parts[4]))
	}
	return false
}
func compareRuntimeOwnershipAnchors(path string, original, next *reservationNode) error {
	mutable := mutableRuntimeObservation(path)
	if original == nil || next == nil {
		if mutable {
			return nil
		} // fresh reader already validated any newly present leaf
		return errors.New("native status refresh changed immutable member coverage")
	}
	// Keep every surviving directory anchor, even inside a mutable subtree.
	// Directory link counts may change with permitted child directory lifetime;
	// complete child coverage and fresh no-follow/no-mount schema checks remain.
	directory := original.file != nil && next.file != nil
	if !mutable || directory {
		a, b := original.identity, next.identity
		if !sameReservationNode(a, b) || a.Mode != b.Mode || a.Uid != b.Uid || a.Gid != b.Gid || !directory && a.Nlink != b.Nlink {
			return errors.New("native status refresh replaced immutable ownership anchor")
		}
	}
	oldChildren := map[string]*reservationNode{}
	newChildren := map[string]*reservationNode{}
	for _, child := range original.children {
		oldChildren[child.name] = child
	}
	for _, child := range next.children {
		newChildren[child.name] = child
	}
	names := map[string]bool{}
	for name := range oldChildren {
		names[name] = true
	}
	for name := range newChildren {
		names[name] = true
	}
	for name := range names {
		childPath := name
		if path != "" {
			childPath = path + "/" + name
		}
		if err := compareRuntimeOwnershipAnchors(childPath, oldChildren[name], newChildren[name]); err != nil {
			return err
		}
	}
	return nil
}
