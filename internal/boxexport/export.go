// Package boxexport receives a container tar into private host staging and
// publishes a constrained file export only after the producer has succeeded.
package boxexport

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type Options struct{ Contents, Overwrite bool }
type entryKind byte

const (
	directoryEntry entryKind = iota
	regularEntry
	symlinkEntry
	hardlinkEntry
)

type entry struct {
	kind     entryKind
	explicit bool
	mode     fs.FileMode
	mod      time.Time
	link     string
}
type archiveTree struct {
	nodes map[string]*entry
	root  string
}

// Copy opens the existing destination parent before invoking produce. produce
// must write one uncompressed tar and return its actual transport/producer
// result. On cancellation or malformed input its exact attempt is cancelled and
// joined before staging cleanup. An existing-directory merge can publish a
// prefix; its error explicitly says so. It never retries a producer.
func Copy(ctx context.Context, destination string, o Options, produce func(context.Context, io.Writer) error) (result error) {
	if destination == "" || destination == "-" {
		return errors.New("filesystem export needs a host destination")
	}
	abs, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	parentName, base := filepath.Dir(abs), filepath.Base(abs)
	if abs == string(filepath.Separator) {
		parentName = abs
		base = "."
	}
	parent, err := openHostDirectory(parentName)
	if err != nil {
		return err
	}
	defer func() { _ = parent.close() }()
	existing, statErr := parent.stat(base)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	exists := statErr == nil
	if exists && !existing.directory() && !existing.regular() {
		return errors.New("destination is a link or special file; not followed or replaced")
	}
	if exists && existing.regular() && !o.Overwrite {
		return fmt.Errorf("destination %s already exists (use --overwrite for a regular file)", destination)
	}
	if !exists && strings.HasSuffix(destination, string(filepath.Separator)) {
		return errors.New("destination directory must already exist")
	}
	var target *directory
	if exists && existing.directory() {
		target, err = parent.open(base)
		if err != nil {
			return err
		}
		defer func() { _ = target.close() }()
	}
	stageName, stage, err := newStage(parent)
	if err != nil {
		return err
	}
	defer func() { _ = stage.close() }()
	defer func() {
		if err := cleanupStage(parent, stageName, stage); err != nil {
			result = errors.Join(result, fmt.Errorf("remove owned copy staging: %w", err))
		}
	}()
	payload, err := stage.mkdir("data")
	if err != nil {
		return err
	}
	defer func() { _ = payload.close() }()
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { e := produce(streamCtx, writer); _ = writer.CloseWithError(e); done <- e }()
	tree, extractErr := receive(streamCtx, reader, payload, o.Contents)
	_ = reader.CloseWithError(extractErr)
	if extractErr != nil {
		cancel()
	}
	producerErr := <-done
	if err := errors.Join(extractErr, producerErr, ctx.Err()); err != nil {
		return fmt.Errorf("copy archive not published: %w", err)
	}
	published := false
	if target != nil {
		if o.Contents {
			err = mergeTree(ctx, payload, target, ".", tree, o.Overwrite, &published)
		} else {
			err = publishEntry(ctx, payload, tree.root, target, tree.root, tree.root, tree, o.Overwrite, &published)
		}
	} else if o.Contents {
		if exists {
			return errors.New("directory contents cannot replace a file")
		}
		err = publishEntry(ctx, stage, "data", parent, base, ".", tree, false, &published)
	} else {
		err = publishEntry(ctx, payload, tree.root, parent, base, tree.root, tree, o.Overwrite, &published)
	}
	if err != nil && published {
		return fmt.Errorf("copy partially published to %s: %w", destination, err)
	}
	return err
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type zeroTrailer struct{}

func (zeroTrailer) Write(p []byte) (int, error) {
	for _, b := range p {
		if b != 0 {
			return 0, errors.New("nonzero data after tar end")
		}
	}
	return len(p), nil
}
func cleanEntry(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) || path.IsAbs(name) {
		return "", fmt.Errorf("unsafe archive name %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("archive traversal name %q", name)
		}
	}
	return path.Clean(name), nil
}
func (t *archiveTree) add(name string, e *entry) error {
	if name == "." && e.kind != directoryEntry {
		return errors.New("archive root is not a directory")
	}
	for p := path.Dir(name); p != "."; p = path.Dir(p) {
		if prior, ok := t.nodes[p]; ok {
			if prior.kind != directoryEntry {
				return fmt.Errorf("archive link/file parent %q", p)
			}
		} else {
			t.nodes[p] = &entry{kind: directoryEntry, mode: 0755}
		}
	}
	if prior, ok := t.nodes[name]; ok && prior.explicit {
		return fmt.Errorf("duplicate archive entry %q", name)
	} else if ok && prior.kind != e.kind {
		return fmt.Errorf("conflicting archive entry %q", name)
	}
	t.nodes[name] = e
	return nil
}
func receive(ctx context.Context, r io.Reader, stage *directory, contents bool) (*archiveTree, error) {
	t := &archiveTree{nodes: map[string]*entry{".": {kind: directoryEntry, mode: 0755}}}
	tr := tar.NewReader(contextReader{ctx, r})
	explicitEntries := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		explicitEntries++
		name, err := cleanEntry(h.Name)
		if err != nil {
			return nil, err
		}
		e := &entry{explicit: true, mode: fs.FileMode(h.Mode).Perm(), mod: h.ModTime, link: h.Linkname}
		switch h.Typeflag {
		case tar.TypeDir:
			e.kind = directoryEntry
		case tar.TypeReg, 0: // NUL is the original tar regular-file type.
			e.kind = regularEntry
		case tar.TypeSymlink:
			e.kind = symlinkEntry
		case tar.TypeLink:
			e.kind = hardlinkEntry
		default:
			return nil, fmt.Errorf("unsupported archive entry %q type %d", name, h.Typeflag)
		}
		if e.kind != regularEntry && h.Size != 0 {
			return nil, fmt.Errorf("non-file archive payload %q", name)
		}
		if err := t.add(name, e); err != nil {
			return nil, err
		}
		if name == "." {
			continue
		}
		parent, base, err := stage.parent(name, true)
		if err != nil {
			return nil, err
		}
		switch e.kind {
		case directoryEntry:
			child, err := parent.open(base)
			if errors.Is(err, os.ErrNotExist) {
				child, err = parent.mkdir(base)
			}
			if err == nil {
				err = child.close()
			}
			_ = parent.close()
			if err != nil {
				return nil, err
			}
		case regularEntry:
			f, err := parent.create(base)
			_ = parent.close()
			if err != nil {
				return nil, err
			}
			_, copyErr := io.CopyN(f, contextReader{ctx, tr}, h.Size)
			if copyErr == nil {
				copyErr = setAttributes(f, e.mode, e.mod)
			}
			if err := errors.Join(copyErr, f.Close()); err != nil {
				return nil, err
			}
		default:
			_ = parent.close() // Links are validated as a complete inventory first.
		}
	}
	if _, err := io.Copy(zeroTrailer{}, contextReader{ctx, r}); err != nil {
		return nil, err
	}
	if explicitEntries == 0 {
		return nil, errors.New("empty archive has no observed source")
	}
	tops := map[string]bool{}
	for name := range t.nodes {
		if name != "." {
			tops[strings.SplitN(name, "/", 2)[0]] = true
		}
	}
	if contents {
		t.root = "."
	} else {
		if len(tops) != 1 {
			return nil, errors.New("archive must contain exactly one selected source tree")
		}
		for name := range tops {
			t.root = name
		}
	}
	// Link targets must stay in the entire selected tree, not merely under a
	// convenient staging prefix. No archive link ever becomes a parent writer.
	for name, e := range t.nodes {
		switch e.kind {
		case symlinkEntry:
			if e.link == "" || strings.ContainsRune(e.link, 0) || path.IsAbs(e.link) {
				return nil, fmt.Errorf("unsafe symlink %q", name)
			}
			resolved := path.Clean(path.Join(path.Dir(name), e.link))
			if resolved == ".." || strings.HasPrefix(resolved, "../") || t.root != "." && resolved != t.root && !strings.HasPrefix(resolved, t.root+"/") {
				return nil, fmt.Errorf("escaping symlink %q", name)
			}
		case hardlinkEntry:
			target, err := cleanEntry(e.link)
			if err != nil {
				return nil, err
			}
			original, ok := t.nodes[target]
			if !ok || original.kind != regularEntry {
				return nil, fmt.Errorf("hardlink %q does not select an in-tree regular file", name)
			}
			e.link = target
		}
	}
	for name, e := range t.nodes {
		if e.kind == symlinkEntry {
			if err := t.checkLink(name, e.link); err != nil {
				return nil, err
			}
		}
	}
	for name, e := range t.nodes {
		if e.kind != symlinkEntry && e.kind != hardlinkEntry {
			continue
		}
		parent, base, err := stage.parent(name, false)
		if err != nil {
			return nil, err
		}
		if e.kind == symlinkEntry {
			err = unix.Symlinkat(e.link, parent.fd(), base)
		} else {
			source, leaf, e2 := stage.parent(e.link, false)
			if e2 == nil {
				err = unix.Linkat(source.fd(), leaf, parent.fd(), base, 0)
				_ = source.close()
			} else {
				err = e2
			}
		}
		_ = parent.close()
		if err != nil {
			return nil, err
		}
	}
	return t, nil
}
func mergeTree(ctx context.Context, source, target *directory, prefix string, t *archiveTree, overwrite bool, published *bool) error {
	names, err := source.names()
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		if err := publishEntry(ctx, source, name, target, name, path.Join(prefix, name), t, overwrite, published); err != nil {
			return err
		}
	}
	return nil
}
func publishEntry(ctx context.Context, from *directory, old string, to *directory, name, key string, t *archiveTree, overwrite bool, published *bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	node, ok := t.nodes[key]
	if !ok {
		return errors.New("archive publication has no validated entry")
	}
	dest, err := to.stat(name)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if node.kind == directoryEntry && exists {
		if !dest.directory() {
			return fmt.Errorf("directory cannot replace destination %q", name)
		}
		source, err := from.open(old)
		if err != nil {
			return err
		}
		defer func() { _ = source.close() }()
		target, err := to.open(name)
		if err != nil {
			return err
		}
		defer func() { _ = target.close() }()
		if err := mergeTree(ctx, source, target, key, t, overwrite, published); err != nil {
			return err
		}
		*published = true // Merged-directory metadata is a published change too.
		return setAttributes(target.file, node.mode, node.mod)
	}
	if err := moveEntry(from, old, to, name, overwrite); err != nil {
		return fmt.Errorf("publish %q: %w", name, err)
	}
	*published = true
	if err := errors.Join(from.file.Sync(), to.file.Sync()); err != nil {
		return err
	}
	if node.kind == directoryEntry {
		target, err := to.open(name)
		if err != nil {
			return err
		}
		defer func() { _ = target.close() }()
		return finishDirectory(target, key, t)
	}
	return nil
}
func finishDirectory(d *directory, prefix string, t *archiveTree) error {
	names, err := d.names()
	if err != nil {
		return err
	}
	for _, name := range names {
		key := path.Join(prefix, name)
		if e := t.nodes[key]; e != nil && e.kind == directoryEntry {
			child, err := d.open(name)
			if err != nil {
				return err
			}
			err = finishDirectory(child, key, t)
			err = errors.Join(err, child.close())
			if err != nil {
				return err
			}
		}
	}
	e := t.nodes[prefix]
	return setAttributes(d.file, e.mode, e.mod)
}

// Resolve staged symlinks component by component: cleaning the lexical target
// alone misses a/../ when a itself is a link back to the selected root.
func (t *archiveTree) checkLink(name, target string) error {
	at := path.Dir(name)
	todo := strings.Split(target, "/")
	links := 0
	for len(todo) > 0 {
		part := todo[0]
		todo = todo[1:]
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if at == t.root {
				return fmt.Errorf("escaping symlink %q", name)
			}
			at = path.Dir(at)
		} else {
			at = path.Join(at, part)
		}
		if at == ".." || strings.HasPrefix(at, "../") || t.root != "." && at != t.root && !strings.HasPrefix(at, t.root+"/") {
			return fmt.Errorf("escaping symlink %q", name)
		}
		if node := t.nodes[at]; node != nil && node.kind == symlinkEntry {
			links++
			if links > 40 {
				return fmt.Errorf("cyclic symlink %q", name)
			}
			todo = append(strings.Split(node.link, "/"), todo...)
			at = path.Dir(at)
		}
	}
	return nil
}
