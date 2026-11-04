package operator

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// prepareLegacyIdentityAt preserves the legacy producer bytes and never
// replaces an existing identity. Parent admission/publication ownership must
// protect the registration mapping; this helper grants no publisher authority.
// The staging leaf is private to this original call and is never adopted.
func prepareLegacyIdentityAt(directory string, endpoint Endpoint) (err error) {
	return prepareLegacyIdentityWithPublisher(directory, endpoint, renameLegacyIdentityNoReplace)
}

// publish is private fault scheduling for an actual no-replace publication;
// production always supplies the platform's descriptor-relative operation.
func prepareLegacyIdentityWithPublisher(directory string, endpoint Endpoint, publish func(int, string, string) error) (err error) {
	return prepareRegistrationIdentityWithPublisher(directory, LegacyRegistrationIdentity(endpoint), true, publish)
}

// prepareNativeIdentityAt is a private filesystem primitive, not the native
// publisher/receipt protocol. Its future original parent caller must reserve
// scanner ownership BEFORE visibility and independently qualify origin/storage
// and effect permission. Cold restoration has different authority and must not
// call this to overwrite or adopt an existing registration.
func prepareNativeIdentityAt(directory string, identity RegistrationIdentity) error {
	if identity.Mode != RegistrationOpenShellNative {
		return errors.New("native identity publication requires its explicit native variant")
	}
	return prepareRegistrationIdentityWithPublisher(directory, identity, false, renameLegacyIdentityNoReplace)
}

func prepareRegistrationIdentityWithPublisher(directory string, identity RegistrationIdentity, reuseExisting bool, publish func(int, string, string) error) (err error) {
	if publish == nil {
		return errors.New("identity publication lacks its original operation")
	}
	if err := identity.Validate(); err != nil {
		return err
	}
	fd, err := openLegacyIdentityDirectory(directory)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unix.Close(fd)) }()
	checkExisting := func() error {
		err := checkRegistrationIdentityAt(fd, identity)
		if err == nil && !reuseExisting {
			return os.ErrExist // matching metadata is never a native create ACK
		}
		return err
	}
	if err := checkExisting(); !errors.Is(err, os.ErrNotExist) {
		return err // existing matching bytes remain unchanged
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := ".write-" + hex.EncodeToString(nonce[:])
	n, err := unix.Openat(fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	// Exact original temporary name, not wildcard cleanup or identity removal.
	published := false
	defer func() {
		if published {
			return
		} // the actual no-replace rename consumed this name
		if removeErr := unix.Unlinkat(fd, name, 0); removeErr != nil {
			err = errors.Join(err, removeErr)
		}
		err = errors.Join(err, unix.Fsync(fd))
	}()
	file := os.NewFile(uintptr(n), "operator identity staging")
	raw, err := json.Marshal(identity)
	if err != nil {
		return errors.Join(err, file.Close())
	}
	if n, err := file.Write(raw); err != nil || n != len(raw) {
		return errors.Join(err, io.ErrShortWrite, file.Close())
	}
	if err := file.Chmod(0o644); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	// Atomic no-replace publication keeps one link from first exposure. No
	// reader can mistake a temporary second link for foreign identity.
	if err := publish(fd, name, "identity.json"); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return checkExisting()
		}
		return err
	}
	published = true
	return unix.Fsync(fd)
}

// Walk every parent without following symlinks. Native owner/admission checks
// still belong to the real infrastructure caller, not this filesystem helper.
func openLegacyIdentityDirectory(path string) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, errors.New("operator identity requires a canonical directory")
	}
	// Ancestors need search/path custody only on Linux. Requiring directory
	// read permission here would reject an otherwise accessible publication.
	// The final handle is readable because its mutation requires fsync below.
	flags := legacyIdentityAncestorAccess() | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if path == "/" {
		flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	}
	fd, err := unix.Open("/", flags, 0)
	if err != nil {
		return -1, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, part := range parts {
		if part == "" {
			continue
		}
		flags := legacyIdentityAncestorAccess() | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if index == len(parts)-1 {
			flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		}
		next, err := unix.Openat(fd, part, flags, 0)
		closeErr := unix.Close(fd)
		if err != nil || closeErr != nil {
			if next >= 0 {
				_ = unix.Close(next)
			}
			return -1, errors.Join(err, closeErr)
		}
		fd = next
	}
	return fd, nil
}

func checkLegacyIdentityAt(directory int, expected Endpoint) error {
	return checkRegistrationIdentityAt(directory, LegacyRegistrationIdentity(expected))
}

func checkRegistrationIdentityAt(directory int, expected RegistrationIdentity) error {
	fd, err := unix.Openat(directory, "identity.json", unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), "operator identity guard")
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 {
		return errors.Join(errors.New("operator identity is not its single regular file"), err, file.Close())
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 4097))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(raw) > 4096 {
		return errors.Join(errors.New("operator identity guard read failed"), readErr, closeErr)
	}
	identity, err := DecodeRegistrationIdentity(raw)
	if err != nil {
		return err
	}
	if expected.Mode == RegistrationLegacy && identity.Mode == RegistrationOpenShellNative {
		return ErrNativeRegistration
	}
	if identity != expected {
		return errors.New("existing operator identity conflicts with original preparation")
	}
	return nil
}

func guardLegacyIdentityPreparation(directory string, endpoint Endpoint) error {
	fd, err := openLegacyIdentityDirectory(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	checkErr := checkLegacyIdentityAt(fd, endpoint)
	if errors.Is(checkErr, os.ErrNotExist) {
		checkErr = nil
	}
	return errors.Join(checkErr, unix.Close(fd))
}
