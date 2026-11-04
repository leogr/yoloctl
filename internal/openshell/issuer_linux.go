package openshell

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

// nativeIssuer is called under native mutation admission after LoadRuntime's
// disk/ownership verification. There is no environment/host-path override, and
// the key root is outside every container's per-incarnation runtime projection.
func nativeIssuer() (ed25519.PrivateKey, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 {
		return nil, errors.New("OpenShell issuer requires VM-root controller infrastructure")
	}
	c := spaceenv.Current()
	if err := safepath.PrivateDirectory(c.DiskMount, "runtime", 0, 0); err != nil {
		return nil, err
	}
	if err := safepath.PrivateDirectory(c.DiskMount, "runtime/openshell", 0, 0); err != nil {
		return nil, err
	}
	root, err := layout.OpenShellControlDir()
	if err != nil {
		return nil, err
	}
	return issuerAt(root, 0, 0)
}

// issuerAt operates beneath a trusted private root, using descriptor-relative
// no-follow reads and atomic no-replace publication. It never adopts symlinks,
// hard links, wrong owners or permissive state, and never overwrites a key.
func issuerAt(root string, uid, gid int) (ed25519.PrivateKey, error) {
	if err := safepath.PrivateDirectory(root, "issuer", uid, gid); err != nil {
		return nil, err
	}
	issuerDir := filepath.Join(root, "issuer")
	read := func() (ed25519.PrivateKey, error) {
		raw, info, err := safepath.ReadFileInfo(issuerDir, "signing.key", 4096)
		if err != nil {
			return nil, err
		}
		if err := privateFileOwner(info, uid, gid); err != nil {
			return nil, err
		}
		block, rest := pem.Decode(raw)
		if block == nil || block.Type != "PRIVATE KEY" || len(rest) != 0 {
			return nil, errors.New("invalid issuer key encoding")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("invalid issuer signing key")
		}
		ed, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("issuer key must be Ed25519")
		}
		return ed, nil
	}
	key, err := read()
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return key, err
	}
	_, fresh, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(fresh)
	if err != nil {
		return nil, err
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := publishPrivateOnce(issuerDir, "signing.key", raw, uid, gid); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	// An atomic no-replace collision means another initializer published the
	// complete winner. Read it; never retry the write or replace that identity.
	return read()
}

func privateFileOwner(info os.FileInfo, uid, gid int) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(uid) || st.Gid != uint32(gid) || st.Nlink != 1 || st.Mode&0o7777 != 0o600 {
		return errors.New("runtime authority file is not private to its expected owner")
	}
	return nil
}

func publishPrivateOnce(dir, name string, raw []byte, uid, gid int) error {
	return publishPrivateOnceObserved(dir, name, raw, uid, gid, nil)
}

// The optional observer sees the actual exclusively created file descriptor,
// after its data sync and before Close/RENAME_NOREPLACE. A pathname readback
// cannot supply the identity of the file this call originally created. Ordinary
// publications retain exactly the original path when no observer is supplied.
func publishPrivateOnceObserved(dir, name string, raw []byte, uid, gid int, observe func(*os.File) error) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return errors.New("invalid private publication name")
	}
	parent, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(parent) }()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temporary := ".issuer-" + hex.EncodeToString(nonce[:]) + ".tmp"
	fd, err := unix.Openat(parent, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = unix.Unlinkat(parent, temporary, 0)
		}
	}()
	file := os.NewFile(uintptr(fd), temporary)
	defer func() { _ = file.Close() }()
	if uid != os.Geteuid() || gid != os.Getegid() {
		if err := file.Chown(uid, gid); err != nil {
			return err
		}
	}
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if observe != nil {
		if err := observe(file); err != nil {
			return err
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := unix.Renameat2(parent, temporary, parent, name, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	published = true
	if err := unix.Fsync(parent); err != nil {
		return &atomicfile.CommitError{Err: err}
	}
	return nil
}
