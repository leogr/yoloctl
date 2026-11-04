package spacearchive

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/strictyaml"
)

func readDeclarations(disk string, boot *spaceconfig.Bootstrap) (*controller.StoreBundle, error) {
	root := filepath.Join(disk, "config")
	b := &controller.StoreBundle{Schema: 1, Boxes: map[string][]byte{}, Accounts: map[string][]byte{}}
	raw, err := safepath.ReadFile(root, "space.yaml", controller.MaxDocument)
	if err != nil {
		return nil, err
	}
	if err := strictyaml.Decode(raw, &b.Settings); err != nil {
		return nil, err
	}
	b.Chat, err = safepath.ReadFile(disk, layout.ChatManifest, managedchat.MaxDocument)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, kind := range []string{"accounts", "boxes"} {
		entries, err := os.ReadDir(filepath.Join(root, kind))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".lock") {
				continue
			}
			if !strings.HasSuffix(entry.Name(), ".yaml") {
				return nil, fmt.Errorf("unexpected %s declaration entry", kind)
			}
			name := strings.TrimSuffix(entry.Name(), ".yaml")
			raw, err := safepath.ReadFile(root, kind+"/"+entry.Name(), controller.MaxDocument)
			if err != nil {
				return nil, err
			}
			if kind == "accounts" {
				b.Accounts[name] = raw
			} else {
				b.Boxes[name] = raw
			}
		}
	}
	if _, err := b.Documents(boot); err != nil {
		return nil, err
	}
	return b, nil
}

// WriteState requires a verified disk, stopped workloads and closed controller
// admission. Never dereference account links or collect VM/host infrastructure.
func WriteState(ctx context.Context, w io.Writer, disk string, boot *spaceconfig.Bootstrap) error {
	b, err := readDeclarations(disk, boot)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if len(raw) > maxDeclarations {
		return errors.New("declaration inventory too large")
	}
	tw := tar.NewWriter(w)
	if err := tw.WriteHeader(&tar.Header{Name: "declarations.json", Mode: 0600, Typeflag: tar.TypeReg, Size: int64(len(raw))}); err != nil {
		return err
	}
	if _, err := tw.Write(raw); err != nil {
		return err
	}
	if err := writeTree(ctx, tw, filepath.Join(disk, "account-state"), "accounts", b.Accounts); err != nil {
		return err
	}
	if err := writeChatState(ctx, tw, disk, boot, b.Chat); err != nil {
		return err
	}
	return tw.Close()
}

type inode struct{ dev, ino uint64 }

func writeTree(ctx context.Context, tw *tar.Writer, dir, prefix string, accounts map[string][]byte) error {
	return writeTreeChecked(ctx, tw, dir, prefix, accounts, nil)
}

func writeTreeChecked(ctx context.Context, tw *tar.Writer, dir, prefix string, accounts map[string][]byte, check func(*tar.Header) error) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	hardlinks := map[inode]string{}
	var policy nativeExportPolicy
	return fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, walkErr error) (err error) {
		defer func() {
			if err != nil && !errors.Is(err, fs.SkipDir) {
				err = fmt.Errorf("native entry %q: %w", path.Join(prefix, rel), err)
			}
		}()
		if walkErr != nil {
			return walkErr
		}
		if rel == "." {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(rel)
		if err != nil {
			return err
		}
		if accounts != nil && policy.skip(rel, info) {
			if info.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = root.Readlink(rel)
			if err != nil {
				return err
			}
			if accounts != nil && path.IsAbs(link) {
				link, err = policy.relativeLink(dir, rel, link)
				if err != nil {
					return err
				}
			}
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name = path.Join(prefix, rel)
		h.Uid = 0
		h.Gid = 0
		h.Uname = ""
		h.Gname = ""
		if accounts == nil {
			h.ModTime = time.Unix(0, 0)
		}
		h.AccessTime = time.Time{}
		h.ChangeTime = time.Time{}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && info.Mode().IsRegular() && st.Nlink > 1 {
			if check != nil {
				return errors.New("managed chat data cannot contain hardlinks")
			}
			key := inode{uint64(st.Dev), st.Ino}
			if first, ok := hardlinks[key]; ok {
				h.Typeflag = tar.TypeLink
				h.Linkname = first
				h.Size = 0
			} else {
				hardlinks[key] = h.Name
			}
		}
		if accounts != nil {
			if err := validateEntry(h, accounts); err != nil {
				return err
			}
		}
		if check != nil {
			if err := check(h); err != nil {
				return err
			}
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			return nil
		}
		f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		current, err := f.Stat()
		if err != nil {
			return err
		}
		if !current.Mode().IsRegular() || !os.SameFile(info, current) || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
			return errors.New("native file changed while exporting")
		}
		_, err = io.CopyN(tw, contextReader{ctx, f}, info.Size())
		if err != nil {
			return err
		}
		after, err := f.Stat()
		if err != nil {
			return err
		}
		if after.Size() != current.Size() || after.ModTime() != current.ModTime() {
			return errors.New("native file changed while exporting")
		}
		return nil
	})
}

func validateEntry(h *tar.Header, accounts map[string][]byte) error {
	if h.Name != path.Clean(h.Name) || strings.ContainsAny(h.Name, "\\\x00") || strings.HasPrefix(h.Name, "/") || h.Size < 0 || h.Size > MaxState {
		return errors.New("unsafe native archive path or size")
	}
	parts := strings.Split(h.Name, "/")
	if len(parts) < 2 || parts[0] != "accounts" {
		return errors.New("native archive entry is outside accounts")
	}
	if err := account.ValidateName(parts[1]); err != nil {
		return err
	}
	if _, ok := accounts[parts[1]]; !ok {
		return errors.New("native archive references an undeclared account")
	}
	if len(parts) > 2 {
		if err := layout.ValidateName("agent", parts[2]); err != nil {
			return err
		}
	}
	if len(parts) < 4 && h.Typeflag != tar.TypeDir {
		return errors.New("native account/agent roots must be directories")
	}
	switch h.Typeflag {
	case tar.TypeReg, tar.TypeDir:
	case tar.TypeSymlink, tar.TypeLink:
		if strings.ContainsAny(h.Linkname, "\\\x00") {
			return errors.New("native links must remain inside their account/agent leaf")
		}
		if path.IsAbs(h.Linkname) {
			if h.Typeflag == tar.TypeSymlink && h.Linkname == path.Clean(h.Linkname) {
				return nil // Retain the link itself; never follow its outside target.
			}
			return errors.New("native absolute symlinks must be clean; hardlinks must remain inside their account/agent leaf")
		}
		target := h.Linkname
		if h.Typeflag == tar.TypeSymlink {
			target = path.Join(path.Dir(h.Name), target)
		}
		leaf := strings.Join(parts[:3], "/")
		if path.Clean(target) != leaf && !strings.HasPrefix(path.Clean(target), leaf+"/") {
			return errors.New("native link escapes its account/agent leaf")
		}
	default:
		return fmt.Errorf("unsupported native archive entry %q", h.Name)
	}
	return nil
}

// inspectState checks the complete inventory before activation. extract, when
// nonnil, receives only validated members under a new private staging tree.
func inspectState(ctx context.Context, r io.Reader, boot *spaceconfig.Bootstrap, extract func(*tar.Header, io.Reader) error) (*controller.StoreBundle, error) {
	r = contextReader{ctx, r}
	tr := tar.NewReader(r)
	h, err := tr.Next()
	if err != nil {
		return nil, err
	}
	if h.Name != "declarations.json" || h.Typeflag != tar.TypeReg || h.Size > maxDeclarations {
		return nil, errors.New("missing declaration inventory")
	}
	raw, err := io.ReadAll(tr)
	if err != nil {
		return nil, err
	}
	var b controller.StoreBundle
	if err := decode(raw, &b); err != nil {
		return nil, err
	}
	if boot != nil {
		if _, err := b.Documents(boot); err != nil {
			return nil, err
		}
	}
	var room *managedchat.Room
	if len(b.Chat) != 0 {
		room, err = managedchat.Parse(b.Chat)
		if err != nil {
			return nil, err
		}
	}
	seen := map[string]byte{"accounts": tar.TypeDir, "chat": tar.TypeDir}
	var total int64
	for {
		h, err = tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		validate := func() error {
			if strings.HasPrefix(h.Name, "chat/") {
				return validateChatEntry(h, room)
			}
			return validateEntry(h, b.Accounts)
		}
		if err := validate(); err != nil {
			return nil, fmt.Errorf("native archive entry %q: %w", h.Name, err)
		}
		if _, ok := seen[h.Name]; ok {
			return nil, errors.New("duplicate native archive member")
		}
		if seen[path.Dir(h.Name)] != tar.TypeDir {
			return nil, errors.New("native archive parent is absent or is a link")
		}
		if h.Typeflag == tar.TypeLink && seen[h.Linkname] != tar.TypeReg {
			return nil, errors.New("native hardlink target must be an earlier regular file")
		}
		seen[h.Name] = h.Typeflag
		total += h.Size
		if len(seen) > 1_000_000 || total > MaxState {
			return nil, errors.New("native archive inventory exceeds limit")
		}
		if extract != nil {
			if err := extract(h, tr); err != nil {
				return nil, err
			}
		} else {
			if _, err := io.Copy(io.Discard, tr); err != nil {
				return nil, err
			}
		}
	}
	if err := zeroTail(r); err != nil {
		return nil, err
	}
	if room != nil && seen[path.Join("chat", room.ID, "data")] != tar.TypeDir {
		return nil, errors.New("managed chat archive lacks its native data root")
	}
	return &b, nil
}

func treeDigest(ctx context.Context, dir string) (string, error) {
	h := sha256.New()
	tw := tar.NewWriter(h)
	if err := writeTree(ctx, tw, dir, "native", nil); err != nil {
		return "", err
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// RestoreState publishes whole account directories only after validating all
// bytes. A retried interrupted import accepts only identical previously moved
// directories. A durable completion marker makes a lost reply inspectable.
func RestoreState(ctx context.Context, r io.Reader, disk string, boot *spaceconfig.Bootstrap, sum string, size int64) error {
	if size <= 0 || size > MaxState {
		return errors.New("invalid import size")
	}
	meta, err := controller.PrivateDir(disk, ".controller")
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp(meta, ".restore-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := os.Mkdir(filepath.Join(stage, "accounts"), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(stage, "chat"), 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(stage)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	var dirs []*tar.Header
	hash := sha256.New()
	limited := io.LimitReader(r, size+1)
	count := &countReader{r: io.TeeReader(limited, hash)}
	bundle, err := inspectState(ctx, count, nil, func(h *tar.Header, data io.Reader) error {
		mode := os.FileMode(h.Mode) & 0777
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.Mkdir(h.Name, 0700); err != nil {
				return err
			}
			copy := *h
			dirs = append(dirs, &copy)
		case tar.TypeSymlink:
			return root.Symlink(h.Linkname, h.Name)
		case tar.TypeLink:
			return root.Link(h.Linkname, h.Name)
		case tar.TypeReg:
			f, err := root.OpenFile(h.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			if _, err := io.Copy(f, data); err != nil {
				return err
			}
			if err := f.Chown(boot.UID, boot.GID); err != nil {
				return err
			}
			if err := f.Chmod(mode); err != nil {
				return err
			}
			if err := root.Chtimes(h.Name, h.ModTime, h.ModTime); err != nil {
				return err
			}
			if err := f.Sync(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if count.n != size || hex.EncodeToString(hash.Sum(nil)) != sum {
		return errors.New("import payload size or checksum mismatch")
	}
	// Restrict account roots to root; agent-owned leaves/files use the importing
	// developer's identity. Apply directory modes only after writing children.
	for i := len(dirs) - 1; i >= 0; i-- {
		h := dirs[i]
		if strings.Count(h.Name, "/") >= 2 {
			if err := root.Chown(h.Name, boot.UID, boot.GID); err != nil {
				return err
			}
		}
		mode := os.FileMode(h.Mode) & 0777
		if strings.Count(h.Name, "/") == 1 {
			mode = 0700
		}
		if err := root.Chmod(h.Name, mode); err != nil {
			return err
		}
		if err := root.Chtimes(h.Name, h.ModTime, h.ModTime); err != nil {
			return err
		}
		if err := atomicfile.SyncDir(filepath.Join(stage, h.Name)); err != nil {
			return err
		}
	}
	if done, err := safepath.ReadFile(meta, "imported", 128); err == nil {
		if string(done) == sum {
			return nil
		}
		return errors.New("different import already completed")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state, err := controller.InspectStore(disk)
	if err != nil {
		return err
	}
	if state.Activated {
		return errors.New("refusing to import into an activated space")
	}
	if err := restoreChatState(ctx, stage, disk, boot, bundle.Chat); err != nil {
		return err
	}
	src := filepath.Join(stage, "accounts")
	dst := filepath.Join(disk, "account-state")
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, entry := range entries {
		expected[entry.Name()] = true
	}
	existing, err := os.ReadDir(dst)
	if err != nil {
		return err
	}
	for _, entry := range existing {
		if !expected[entry.Name()] {
			return errors.New("destination contains unrelated native account state")
		}
	}
	for _, entry := range entries {
		target := filepath.Join(dst, entry.Name())
		source := filepath.Join(src, entry.Name())
		if info, err := os.Lstat(target); err == nil {
			if !info.IsDir() {
				return errors.New("import destination is not a directory")
			}
			a, err := treeDigest(ctx, source)
			if err != nil {
				return err
			}
			b, err := treeDigest(ctx, target)
			if err != nil {
				return err
			}
			if a != b {
				return errors.New("partial import differs from archive; destination retained for inspection")
			}
		} else if errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(source, target); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	if err := atomicfile.SyncDir(dst); err != nil {
		return err
	}
	return safepath.WriteFile(meta, "imported", []byte(sum), 0600)
}

type countReader struct {
	r io.Reader
	n int64
}

func (r *countReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += int64(n)
	return n, err
}
