package openshell

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

type nativePinMount struct {
	id, parent, device uint64
	root, point        string
	filesystem, source string
	readOnly           bool
	optional           []string
}

func decodeNativeMountField(raw string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			out.WriteByte(raw[i])
			continue
		}
		if i+3 >= len(raw) {
			return "", errors.New("truncated native mount field escape")
		}
		switch raw[i+1 : i+4] {
		case "040":
			out.WriteByte(' ')
		case "011":
			out.WriteByte('\t')
		case "012":
			out.WriteByte('\n')
		case "134":
			out.WriteByte('\\')
		case "043":
			out.WriteByte('#')
		default:
			return "", errors.New("unsupported native mount field escape")
		}
		i += 3
	}
	return out.String(), nil
}

// parseNativePinMounts retains the fields needed for source/parent/propagation
// validation. NSFS roots are e.g. net:[123], not absolute filesystem paths.
// Unknown optional tags are retained; they can never prove private isolation.
func parseNativePinMounts(raw []byte) ([]nativePinMount, error) {
	if len(raw) == 0 || len(raw) > 4<<20 || strings.IndexByte(string(raw), 0) >= 0 {
		return nil, errors.New("missing or unbounded native mount inventory")
	}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	var result []nativePinMount
	seen := map[uint64]bool{}
	decimal := func(value string, bits int) (uint64, error) {
		n, err := strconv.ParseUint(value, 10, bits)
		if err != nil || strconv.FormatUint(n, 10) != value {
			return 0, errors.New("noncanonical native mount identifier")
		}
		return n, nil
	}
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			return nil, errors.New("incomplete native mount inventory entry")
		}
		separator := -1
		for i := 6; i < len(fields); i++ {
			if fields[i] == "-" {
				separator = i
				break
			}
		}
		if separator < 6 || len(fields) != separator+4 {
			return nil, errors.New("invalid native mount inventory separator")
		}
		id, idErr := decimal(fields[0], 64)
		parent, parentErr := decimal(fields[1], 64)
		device := strings.Split(fields[2], ":")
		if idErr != nil || parentErr != nil || id == 0 || parent == 0 || seen[id] || len(device) != 2 {
			return nil, errors.New("invalid or duplicate native mount identity")
		}
		seen[id] = true
		major, majorErr := decimal(device[0], 32)
		minor, minorErr := decimal(device[1], 32)
		if majorErr != nil || minorErr != nil {
			return nil, errors.New("invalid native mount device identity")
		}
		root, rootErr := decodeNativeMountField(fields[3])
		point, pointErr := decodeNativeMountField(fields[4])
		source, sourceErr := decodeNativeMountField(fields[separator+2])
		if rootErr != nil || pointErr != nil || sourceErr != nil || root == "" || source == "" || !path.IsAbs(point) || path.Clean(point) != point {
			return nil, errors.New("invalid native mount root/target/source")
		}
		ro, rw := false, false
		for _, option := range strings.Split(fields[5], ",") {
			ro = ro || option == "ro"
			rw = rw || option == "rw"
		}
		if ro == rw {
			return nil, errors.New("ambiguous native mount write mode")
		}
		result = append(result, nativePinMount{id, parent, unix.Mkdev(uint32(major), uint32(minor)), root, point, fields[separator+1], source, ro, append([]string(nil), fields[6:separator]...)})
		if len(result) > 16384 {
			return nil, errors.New("native mount inventory exceeds its count bound")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("empty native mount inventory")
	}
	return result, nil
}

type nativePinMountView struct {
	directoryMount   uint64
	directoryBound   bool
	directoryPrivate bool
	namespaceMount   uint64
	namespace        *os.File
}

func (v *nativePinMountView) Close() error {
	if v == nil || v.namespace == nil {
		return nil
	}
	err := v.namespace.Close()
	v.namespace = nil
	return err
}

// validateNativePinMountView compares opened identities with the complete
// current VM inventory. It supplies observations only, never a mount/unmount
// ACK, fresh creation authority or a historical receipt result.
func validateNativePinMountView(mounts []nativePinMount, target string, nativeMount uint64, directory, leaf pinFileIdentity, namespace bool) (nativePinMountView, error) {
	if !path.IsAbs(target) || path.Clean(target) != target || target == "/" || nativeMount == 0 || directory.device == 0 || directory.inode == 0 || directory.mount == 0 || leaf.device == 0 || leaf.inode == 0 || leaf.mount == 0 {
		return nativePinMountView{}, errors.New("invalid native pin mount selection")
	}
	var base, dir, pin *nativePinMount
	for i := range mounts {
		m := &mounts[i]
		if m.id == nativeMount {
			if base != nil {
				return nativePinMountView{}, errors.New("duplicate native pin base mount")
			}
			base = m
		}
		if m.point == target {
			if dir != nil {
				return nativePinMountView{}, errors.New("stacked mounts at native pin directory")
			}
			dir = m
		} else if strings.HasPrefix(m.point, target+"/") {
			if m.point != target+"/namespace" || pin != nil {
				return nativePinMountView{}, errors.New("unexpected or stacked child mount at native pin")
			}
			pin = m
		}
	}
	if base == nil || base.filesystem != "ext4" || base.readOnly || !path.IsAbs(base.root) || path.Clean(base.root) != base.root || directory.device != base.device {
		return nativePinMountView{}, errors.New("native pin lacks its selected writable native ext4 base")
	}
	relative, err := filepath.Rel(base.point, target)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
		return nativePinMountView{}, errors.New("native pin target is outside its selected filesystem mount")
	}
	view := nativePinMountView{directoryMount: directory.mount}
	if dir == nil {
		if pin != nil || namespace || directory.mount != nativeMount || leaf.mount != nativeMount || leaf.device != base.device {
			return nativePinMountView{}, errors.New("bare native pin has an unexpected opened mount")
		}
		return view, nil
	}
	if dir.id == nativeMount || dir.id != directory.mount || dir.parent != nativeMount || dir.device != base.device || dir.root != path.Join(base.root, relative) || dir.filesystem != base.filesystem || dir.source != base.source || dir.readOnly {
		return nativePinMountView{}, errors.New("native pin directory bind differs from its selected source/parent/target")
	}
	view.directoryBound, view.directoryPrivate = true, len(dir.optional) == 0
	if pin == nil {
		if namespace || leaf.mount != dir.id || leaf.device != base.device {
			return nativePinMountView{}, errors.New("native pin placeholder mount differs from its directory")
		}
		return view, nil
	}
	if !namespace || !view.directoryPrivate || pin.id == dir.id || pin.id != leaf.mount || pin.parent != dir.id || pin.device != leaf.device || pin.root != fmt.Sprintf("net:[%d]", leaf.inode) || pin.filesystem != "nsfs" || pin.source != "nsfs" || len(pin.optional) != 0 {
		return nativePinMountView{}, errors.New("native network pin lacks exact private NSFS mount identity")
	}
	view.namespaceMount = pin.id
	return view, nil
}

func nativePinDirectoryPath(f *namespacePinFiles) (string, error) {
	if f == nil || f.directory == nil {
		return "", errors.New("pin observation requires its retained directory")
	}
	return nativeNamespaceGenerationPath(f.lock, f.record)
}

func nativeNamespaceGenerationPath(lock *namespacePinLock, record RuntimeRecord) (string, error) {
	current := spaceenv.Current()
	if current == nil || os.Geteuid() != 0 || os.Getegid() != 0 {
		return "", errors.New("pin observation requires current native VM-root infrastructure")
	}
	binding := record.Launch.Binding
	if binding.Space != current.Space || binding.DiskID != current.DiskID || binding.DiskUUID != current.DiskUUID || record.Launch.UID != uint32(current.UID) || record.Launch.GID != uint32(current.GID) || !lowerHex(binding.Incarnation, 16) || !namespacePinGenerationName(record.Generation) {
		return "", errors.New("namespace pin belongs to another native disk/space/identity")
	}
	control, err := layout.OpenShellControlDir()
	if err != nil {
		return "", err
	}
	root, err := openNativeGenerationDirectory(control)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	identity, err := pinIdentity(int(root.Fd()), 0, 0, true)
	if err != nil || lock == nil || lock.check() != nil || identity != lock.directories[0].identity || binding.Incarnation != lock.incarnation {
		return "", errors.New("namespace pin metadata is outside the current native control root")
	}
	// An old controller generation is observation, never fresh authority.
	return filepath.Join(control, "namespace-pins", binding.Incarnation, "pins", record.Generation), nil
}

// readNativePinMountInventory reads only the current pinned infrastructure
// thread's genuine procfs. It performs no leaf lookup, namespace entry or effect.
func readNativePinMountInventory(ctx context.Context, kernel *kernelInfrastructure) ([]nativePinMount, error) {
	if err := kernel.revalidate(ctx); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(kernel.proc.Fd()), "thread-self/mountinfo", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	mountinfo := os.NewFile(uintptr(fd), "native-pin-mountinfo")
	defer func() { _ = mountinfo.Close() }()
	if err := verifyKernelProcFD(fd, false); err != nil {
		return nil, err
	}
	var procMount, infoMount unix.Statx_t
	if unix.Statx(int(kernel.proc.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &procMount) != nil || unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &infoMount) != nil || procMount.Mask&unix.STATX_MNT_ID == 0 || infoMount.Mask&unix.STATX_MNT_ID == 0 || procMount.Mnt_id == 0 || procMount.Mnt_id != infoMount.Mnt_id {
		return nil, errors.New("native pin mountinfo has an unexpected procfs overmount")
	}
	raw, err := io.ReadAll(io.LimitReader(mountinfo, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	mounts, err := parseNativePinMounts(raw)
	if err != nil {
		return nil, err
	}
	return mounts, nil
}

// observeNativePinMounts keeps current namespace FDs for same-boot owned
// retirement. It never looks up an old PID or enters a namespace. Holding the
// returned FD prolongs lifetime: the caller must close it before claiming full
// retirement. Decoded phases and this observation are not in-call effect ACKs.
func observeNativePinMounts(ctx context.Context, kernel *kernelInfrastructure, files *namespacePinFiles) (_ *nativePinMountView, err error) {
	if err := kernel.revalidate(ctx); err != nil {
		return nil, err
	}
	target, err := nativePinDirectoryPath(files)
	if err != nil {
		return nil, err
	}
	current, err := files.lock.read(files.record, files.cid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = current.Close() }()
	if current.identity != files.identity || !bytes.Equal(current.origin, files.origin) || !bytes.Equal(current.current, files.current) {
		return nil, errors.New("native pin metadata changed before mount observation")
	}
	var origin namespacePinOrigin
	if err := json.Unmarshal(files.origin, &origin); err != nil {
		return nil, err
	}
	scope := origin.Record.Scope
	if scope.KernelBootID != kernel.bootID || scope.InfrastructureMount != kernel.mountID || scope.Namespace == kernel.networkID {
		return nil, errors.New("same-boot pin observation refuses changed boot/VM namespace bindings")
	}
	mounts, err := readNativePinMountInventory(ctx, kernel)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(files.directory.Fd()), "namespace", unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	leaf := os.NewFile(uintptr(fd), "native-pin-namespace")
	keep := false
	defer func() {
		if !keep {
			_ = leaf.Close()
		}
	}()
	var st unix.Stat_t
	var fs unix.Statfs_t
	var sx unix.Statx_t
	if unix.Fstat(fd, &st) != nil || unix.Fstatfs(fd, &fs) != nil || unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &sx) != nil || sx.Mask&unix.STATX_MNT_ID == 0 || sx.Mnt_id == 0 {
		return nil, errors.New("native pin namespace lacks current file/mount identity")
	}
	isNamespace := fs.Type == unix.NSFS_MAGIC
	if isNamespace {
		identity, err := kernelNamespaceIdentity(fd, unix.CLONE_NEWNET)
		if err != nil || identity != scope.Namespace {
			return nil, errors.New("native pin names a foreign namespace")
		}
	} else {
		identity, err := pinIdentity(fd, 0, 0, false)
		if err != nil || identity.device != origin.Record.Placeholder.Device || identity.inode != origin.Record.Placeholder.Inode || st.Size != 0 {
			return nil, errors.New("native pin placeholder was replaced")
		}
	}
	view, err := validateNativePinMountView(mounts, target, files.lock.directories[4].identity.mount, files.identity, pinFileIdentity{uint64(st.Dev), st.Ino, sx.Mnt_id}, isNamespace)
	if err != nil {
		return nil, err
	}
	if err := kernel.revalidate(ctx); err != nil {
		return nil, err
	}
	if isNamespace {
		view.namespace, keep = leaf, true
	}
	return &view, nil
}
