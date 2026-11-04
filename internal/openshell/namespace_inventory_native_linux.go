package openshell

import (
	"bytes"
	"context"
	"errors"
)

// Kept only by the original admitted inventory inside its locked-thread
// kernel lifetime. Metadata-only inventories have nil readers and retain the
// original strict device checks. Neither mode grants mutation/ACK authority.
type namespaceInventoryReaders struct {
	pin     func(context.Context, *namespacePinLock, RuntimeRecord, string) (*namespacePinFiles, error)
	retired func(context.Context, *namespacePinFiles) (bool, error)
}

func snapshotNativeNamespaceGenerations(ctx context.Context, kernel *kernelInfrastructure, lock *namespacePinLock, records []RuntimeRecord, cid string) (*namespaceGenerationInventory, error) {
	if lock == nil || lock.uid != 0 || lock.gid != 0 {
		return nil, errors.New("native namespace inventory requires protected root selection")
	}
	if err := kernel.revalidate(ctx); err != nil {
		return nil, err
	}
	readers := namespaceReadersForCurrentBoot(func(c context.Context) (string, error) {
		if err := kernel.revalidate(c); err != nil {
			return "", err
		}
		return kernel.bootID, nil
	}, func(c context.Context, l *namespacePinLock, r RuntimeRecord, id string) (*staleNamespacePinObservation, error) {
		return observeStaleNamespacePin(c, kernel, l, r, id)
	})
	s, err := readNamespaceGenerationInventoryWith(ctx, lock, records, cid, readers)
	if err != nil {
		return nil, err
	}
	if err := s.check(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Production supplies genuine protected current boot/stale-context readers
// above. The private project seam composes the same enumeration, file readers,
// marker verification and repeated stage reads with owned-file kernel fixtures.
func namespaceReadersForCurrentBoot(currentBoot func(context.Context) (string, error), stale func(context.Context, *namespacePinLock, RuntimeRecord, string) (*staleNamespacePinObservation, error)) *namespaceInventoryReaders {
	boot := func(c context.Context) (string, error) {
		if currentBoot == nil || stale == nil {
			return "", errors.New("namespace inventory lacks current admitted boot readers")
		}
		b, err := currentBoot(c)
		if err != nil {
			return "", err
		}
		if !canonicalUUID(b) {
			return "", errors.New("namespace inventory has unknown current boot")
		}
		return b, nil
	}
	classify := func(c context.Context, lock *namespacePinLock, record RuntimeRecord, cid string) (namespacePinBootRelation, error) {
		b, err := boot(c)
		if err != nil {
			return pinBootUnknown, err
		}
		if lock == nil {
			return pinBootUnknown, errors.New("namespace boot selection lacks original lock")
		}
		if err := lock.check(); err != nil {
			return pinBootUnknown, err
		}
		raw, _, err := readPinFileAt(int(lock.directories[3].file.Fd()), record.Generation+".json", lock.uid, lock.gid, 4<<20)
		if err != nil {
			return pinBootUnknown, err
		}
		origin, err := decodeNamespacePinOrigin(raw, record, cid)
		if err != nil {
			return pinBootUnknown, err
		}
		return origin.Record.Scope.classifyBoot(b)
	}
	r := &namespaceInventoryReaders{}
	r.pin = func(c context.Context, lock *namespacePinLock, record RuntimeRecord, cid string) (*namespacePinFiles, error) {
		relation, err := classify(c, lock, record, cid)
		if err != nil {
			return nil, err
		}
		switch relation {
		case pinSameBoot:
			return lock.read(record, cid)
		case pinDifferentBoot:
			observed, err := stale(c, lock, record, cid)
			if err != nil {
				return nil, err
			}
			if observed == nil || observed.files == nil {
				return nil, errors.New("stale inventory reader omitted actual selected files")
			}
			b, err := boot(c)
			if err != nil || observed.currentBoot != b {
				_ = observed.Close()
				return nil, errors.Join(err, errors.New("stale inventory reader changed current independent boot"))
			}
			return observed.files, nil // transfer its sole metadata FD, never a namespace FD
		default:
			return nil, errors.New("namespace inventory refuses unknown boot relation")
		}
	}
	r.retired = func(c context.Context, f *namespacePinFiles) (bool, error) {
		if f == nil || f.lock == nil {
			return false, errors.New("namespace marker lacks original selected files")
		}
		relation, err := classify(c, f.lock, f.record, f.cid)
		if err != nil {
			return false, err
		}
		if relation == pinSameBoot {
			return f.readRetired()
		}
		if relation != pinDifferentBoot {
			return false, errors.New("namespace marker has unknown current boot")
		}
		check := func() error {
			current, err := stale(c, f.lock, f.record, f.cid)
			if err != nil {
				return err
			}
			if current == nil || current.files == nil {
				return errors.New("stale marker reader omitted actual current files")
			}
			defer func() { _ = current.Close() }()
			b, err := boot(c)
			if err != nil {
				return err
			}
			if current.currentBoot != b {
				return errors.New("stale marker reader changed independent current boot")
			}
			if current.files.identity != f.identity || current.files.lock != f.lock || current.files.cid != f.cid || recordIdentity(current.files.record) != recordIdentity(f.record) || !bytes.Equal(current.files.origin, f.origin) || !bytes.Equal(current.files.current, f.current) {
				return errors.New("stale marker changed original current native selection")
			}
			return nil
		}
		if err := check(); err != nil {
			return false, err
		}
		found, err := f.readRetiredValue()
		if err != nil {
			return false, err
		}
		if err := check(); err != nil {
			return false, err
		}
		return found, nil
	}
	return r
}
