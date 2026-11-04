package openshell

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// This private getter only names SDK-owned original descriptor sources. It performs
// no opens/stats/dups, obtains no PID authority, and grants no exemption. The
// parent matcher must bind original slots/OFDs/identities/purpose/lifetime at
// acquisition and revoke them on the actual close transitions. Never register
// current path lookups or infer custody merely because this getter returns a
// nonnil *os.File with a matching inode. A returned pointer alone does not even
// prove that its original FD slot remains open. No production matcher calls
// this enumeration yet, and no actor/root/self exclusion is installed here.
type originalRemainingDescriptor struct {
	file  *os.File // borrowed exact original owner, never ownership transfer
	roles []string
}

func originalRemainingDescriptors(scope *nativeRemovalReadScope, retirement *nativeContainerRetirement, namespaceCleanup *nativeNamespaceCleanup, runtimeCleanup *nativeRuntimeCleanup) ([]originalRemainingDescriptor, error) {
	if scope == nil || scope.closed || scope.kernel == nil || scope.ownership == nil || scope.ownership.view == nil || retirement == nil || retirement.closed || retirement.scope != scope || !retirement.retired || retirement.full == nil || namespaceCleanup == nil || namespaceCleanup.retirement != retirement || namespaceCleanup.failure != nil || runtimeCleanup == nil || runtimeCleanup.namespace != namespaceCleanup || runtimeCleanup.full != retirement.full || runtimeCleanup.parent != scope.containers || runtimeCleanup.failure != nil {
		return nil, errors.New("remaining descriptor inventory lacks original post-retirement owners")
	}
	var result []originalRemainingDescriptor
	byFile := map[*os.File]int{}
	add := func(role string, file *os.File) error {
		if file == nil {
			return errors.New("remaining descriptor inventory lost an original owner")
		}
		if index, exists := byFile[file]; exists {
			result[index].roles = append(result[index].roles, role)
			return nil
		}
		if len(result) >= 8192 {
			return errors.New("remaining descriptor inventory exceeds source bound")
		}
		byFile[file] = len(result)
		result = append(result, originalRemainingDescriptor{file: file, roles: []string{role}})
		return nil
	}
	for _, entry := range []struct {
		role string
		file *os.File
	}{
		{"kernel/proc", scope.kernel.proc},
		{"kernel/vm-init", scope.kernel.init},
		{"kernel/vm-mount-namespace", scope.kernel.mount},
		{"kernel/vm-network-namespace", scope.kernel.network},
		{"scope/control", scope.control},
		{"scope/containers", scope.containers},
		// This is the separate lexical root opened in the outer read callback.
		// It is NOT ownership.view.root or ownership.view.tree.file.
		{"scope/original-read-root", scope.originalReadRoot},
	} {
		if err := add(entry.role, entry.file); err != nil {
			return nil, err
		}
	}
	inventory := func(role string, i *NativeContainerInventory) error {
		if i == nil || i.root == nil || i.tree == nil || i.tree.file == nil {
			return errors.New("remaining descriptor inventory lost original runtime tree")
		}
		if err := add(role+"/root", i.root); err != nil {
			return err
		}
		seen := map[*reservationNode]bool{}
		var visit func(*reservationNode, string, int) error
		visit = func(node *reservationNode, path string, depth int) error {
			if node == nil || seen[node] || len(seen) >= 1025 || depth > 16 {
				return errors.New("remaining descriptor tree is missing, repeated or unbounded")
			}
			seen[node] = true
			// Actual inventoryReservationView owns directory FDs only. A nil
			// directory field is lost source coverage, not a harmless leaf.
			// Regular/socket leaves retain identities and borrowed parent ints.
			switch node.identity.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				if node.file == nil {
					return errors.New("original remaining directory source was lost")
				}
				if err := add(role+"/tree/"+path, node.file); err != nil {
					return err
				}
			case unix.S_IFREG, unix.S_IFSOCK:
				if node.file != nil || len(node.children) != 0 {
					return errors.New("remaining leaf has unexpected descriptor/tree ownership")
				}
			default:
				return errors.New("remaining source tree has unsupported original type")
			}
			for _, child := range node.children {
				if child == nil {
					return errors.New("remaining descriptor tree has nil child")
				}
				if err := visit(child, path+"/"+child.name, depth+1); err != nil {
					return err
				}
			}
			return nil
		}
		return visit(i.tree, ".", 0)
	}
	if err := inventory("ownership", scope.ownership.view); err != nil {
		return nil, err
	}
	if err := inventory("post-stop-full", retirement.full); err != nil {
		return nil, err
	}

	acknowledgedGone := func(key string) bool {
		if !namespaceCleanup.attempted || namespaceCleanup.expected == nil || namespaceCleanup.failure != nil {
			return false
		}
		_, exists := namespaceCleanup.expected.members[key]
		return !exists
	}
	originalPins := map[*namespacePinFiles]bool{}
	if scope.selected.Launch.UnconfinedNetwork {
		if scope.lock == nil || len(scope.lock.directories) != 5 || scope.namespaces == nil || scope.namespaces.lock != scope.lock || namespaceCleanup.lock != scope.lock {
			return nil, errors.New("remaining descriptor inventory lost original namespace owners")
		}
		for index, directory := range scope.lock.directories {
			if err := add(fmt.Sprintf("namespace-lock/directory/%d", index), directory.file); err != nil {
				return nil, err
			}
		}
		if err := add("namespace-lock/file", scope.lock.file); err != nil {
			return nil, err
		}
		for index, pin := range scope.namespaces.pins {
			if pin == nil || pin.lock != scope.lock || originalPins[pin] {
				return nil, errors.New("remaining descriptor inventory has foreign/repeated pin owner")
			}
			originalPins[pin] = true
			if pin.directory == nil {
				if !acknowledgedGone("pins/" + pin.record.Generation) {
					return nil, errors.New("original pin descriptor disappeared before its acknowledged close")
				}
				continue // exact owner was closed by the original directory step
			}
			if err := add(fmt.Sprintf("namespace-inventory/pin/%d", index), pin.directory); err != nil {
				return nil, err
			}
		}
		for index, marker := range scope.namespaces.noInitialization {
			if marker == nil || marker.lock != scope.lock {
				return nil, errors.New("remaining descriptor inventory has foreign no-init owner")
			}
			generation := ""
			for _, record := range namespaceCleanup.records {
				if recordIdentity(record) == marker.marker.RecordSHA256 {
					if generation != "" {
						return nil, errors.New("repeated no-init record identity")
					}
					generation = record.Generation
				}
			}
			if generation == "" {
				return nil, errors.New("no-init descriptor lost original record")
			}
			if marker.directory == nil {
				if !acknowledgedGone(noInitializationDirectory + "/" + generation + ".json") {
					return nil, errors.New("no-init descriptor disappeared before its acknowledged close")
				}
				continue
			}
			if err := add(fmt.Sprintf("namespace-inventory/no-init/%d", index), marker.directory); err != nil {
				return nil, err
			}
		}
	} else if scope.lock != nil || scope.namespaces != nil || namespaceCleanup.lock != nil || len(namespaceCleanup.steps) != 0 || len(retirement.entries) != 0 {
		return nil, errors.New("confined remaining descriptor inventory acquired namespace owners")
	}
	for index, entry := range retirement.entries {
		if entry == nil || !entry.progress.completed {
			return nil, errors.New("remaining descriptor inventory precedes complete retirement")
		}
		switch action := entry.action.(type) {
		case *nativePreparedPinRetirement:
			if action == nil || action.closed || !action.consumed || !entry.sameBoot || entry.pin == nil || !originalPins[entry.pin] || action.remainingNamespace == nil {
				return nil, errors.New("remaining descriptor inventory has unsupported same-boot action")
			}
			// Successful same-boot retirement closes its own retained NSFS
			// descriptor before non-lazy unmount. It is NEVER an exemption
			// after rm, including the initially already-retired branch.
			if action.remainingNamespace() != nil {
				return nil, errors.New("same-boot retirement still retains a namespace descriptor")
			}
		case *nativePreparedObservationRetirement:
			if action == nil || action.closed || !action.consumed || entry.sameBoot {
				return nil, errors.New("remaining descriptor inventory has unsupported observed action")
			}
			if entry.pin == nil {
				if action.originalStaleFiles != nil {
					return nil, errors.New("no-init action acquired an independent stale descriptor")
				}
				continue // selected no-init directory is borrowed, enumerated above
			}
			// This EXTRA original directory lives in selected.Close/verify
			// closures. It is distinct from entry.pin.directory even after
			// namespace metadata cleanup has closed the latter.
			if !originalPins[entry.pin] || action.originalStaleFiles == nil || action.originalStaleFiles.lock != scope.lock {
				return nil, errors.New("stale action lost its original retained metadata")
			}
			if err := add(fmt.Sprintf("retirement/stale-original/%d", index), action.originalStaleFiles.directory); err != nil {
				return nil, err
			}
		default:
			return nil, errors.New("remaining descriptor inventory has unknown retirement action")
		}
	}
	// Step parents are aliases, not additional descriptor owners. Closed
	// namespace step pointers must never be registered again as live slots.
	for index, step := range namespaceCleanup.steps {
		if _, live := byFile[step.parent]; live {
			if err := add(fmt.Sprintf("namespace-cleanup/step-parent/%d", index), step.parent); err != nil {
				return nil, err
			}
			continue
		}
		closedPin := strings.HasPrefix(step.parentKey, "pins/") && acknowledgedGone(step.parentKey)
		closedNoInit := step.parentKey == noInitializationDirectory && acknowledgedGone(step.key)
		if step.parent == nil || !closedPin && !closedNoInit {
			return nil, errors.New("namespace step parent is not an original live or acknowledged-closed owner")
		}
		// This is only a source-lifetime omission. The parent matcher must
		// independently require the original registered close/revoke event.
	}
	if err := add("runtime-cleanup/containers-parent", runtimeCleanup.parent); err != nil {
		return nil, err
	}
	for index, step := range runtimeCleanup.steps {
		if _, live := byFile[step.parent]; !live {
			return nil, errors.New("runtime cleanup parent is not an original retained tree owner")
		}
		if err := add(fmt.Sprintf("runtime-cleanup/step-parent/%d", index), step.parent); err != nil {
			return nil, err
		}
	}
	return result, nil
}
