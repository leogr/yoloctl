package agentboximage

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io/fs"
	"path"
)

// tarContext walks the embedded filesystem subtree rooted at root,
// producing an in-memory tar archive whose entries are root-relative
// paths (so the tar root contains "Dockerfile" + any sibling files,
// not "containers/agentbox/Dockerfile"). The output is the form
// `tar -xf -` accepts when extracted into the VM-side build dir
// where `nerdctl build .` will run.
//
// Why all entries get mode 0644: the Dockerfile chmod's executables
// after COPY (see assets/containers/agentbox/Dockerfile's
// `chmod 0755 /usr/local/bin/yoloctl-entrypoint`), so the source
// permission carried by the tar header is irrelevant for the final
// image. Keeping every entry at 0644 also matches what an
// `embed.FS` ReadFile would produce — these files have no on-disk
// permission metadata at build time anyway.
func tarContext(fsys fs.FS, root string) (*bytes.Buffer, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	wrote := 0
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := relPath(root, p)
		if err != nil {
			return err
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		if err := tw.WriteHeader(&tar.Header{
			Name: rel,
			Mode: 0o644,
			Size: int64(len(data)),
		}); err != nil {
			return fmt.Errorf("tar header %s: %w", rel, err)
		}
		if _, err := tw.Write(data); err != nil {
			return fmt.Errorf("tar write %s: %w", rel, err)
		}
		wrote++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("tar close: %w", err)
	}
	if wrote == 0 {
		return nil, fmt.Errorf("tar context %s: no files found", root)
	}
	return &buf, nil
}

// relPath returns p relative to root using forward-slash separators,
// which tar (and Docker) expect regardless of the host OS. The embed
// FS already uses forward slashes, but path.Rel-style trimming guards
// against an accidental leading separator.
func relPath(root, p string) (string, error) {
	root = path.Clean(root)
	p = path.Clean(p)
	if p == root {
		return "", fmt.Errorf("relPath: %s equals root %s (caller should skip dirs)", p, root)
	}
	prefix := root + "/"
	if len(p) <= len(prefix) || p[:len(prefix)] != prefix {
		return "", fmt.Errorf("relPath: %s not under %s", p, root)
	}
	return p[len(prefix):], nil
}
