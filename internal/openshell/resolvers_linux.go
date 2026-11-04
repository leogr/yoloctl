package openshell

import (
	"bufio"
	"errors"
	"io"
	"net/netip"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/sys/unix"
)

type infrastructureInputs struct {
	resolvers []netip.Addr
	prefixes  []netip.Prefix
}

// nativeInfrastructureInputs reads only the verified VM's filesystem/network,
// before the dedicated child enters the target container namespace. It never
// reads a box's resolver file or accepts resolver authority from workload env.
func nativeInfrastructureInputs() (infrastructureInputs, error) {
	if !spaceenv.Native() || os.Geteuid() != 0 {
		return infrastructureInputs{}, errors.New("network inputs require the verified native root controller")
	}
	var resolvers []netip.Addr
	var err error
	if spaceenv.Current().ServiceManager == "microvm" {
		resolvers = []netip.Addr{netip.MustParseAddr("192.168.127.1")}
	} else {
		resolvers, err = trustedLimaResolvers()
		if err != nil {
			return infrastructureInputs{}, err
		}
	}
	prefixes, err := discoverLocalPrefixes()
	if err != nil {
		return infrastructureInputs{}, err
	}
	return infrastructureInputs{resolvers, prefixes}, nil
}

func trustedLimaResolvers() ([]netip.Addr, error) {
	// The adapter's Ubuntu guest uses a root-controlled regular resolv.conf or
	// systemd-resolved's upstream state. Its loopback stub cannot become an
	// exception in the container's separate namespace.
	raw, err := readResolverSource("/etc/resolv.conf", 0)
	if err == nil {
		addresses, stub, parseErr := parseTrustedResolvers(raw)
		if parseErr != nil {
			return nil, parseErr
		}
		if !stub {
			return addresses, nil
		}
	} else if !errors.Is(err, unix.ELOOP) {
		return nil, err
	} else {
		link, linkErr := rootResolverLink()
		if linkErr != nil {
			return nil, linkErr
		}
		if link != "/run/systemd/resolve/stub-resolv.conf" && link != "/run/systemd/resolve/resolv.conf" {
			return nil, errors.New("resolver symlink on Lima is not verified adapter-owned resolved state")
		}
	}
	uid, err := resolvedServiceUID()
	if err != nil {
		return nil, err
	}
	raw, err = readResolverSource("/run/systemd/resolve/resolv.conf", uid)
	if err != nil {
		return nil, err
	}
	addresses, stub, err := parseTrustedResolvers(raw)
	if err != nil {
		return nil, err
	}
	if stub {
		return nil, errors.New("upstream resolver state on Lima still contains a loopback stub")
	}
	return addresses, nil
}

// readResolverSource walks fixed VM paths with no-follow descriptors. Only
// resolved's exact runtime directory/file may belong to its verified service
// UID; all preceding components remain root-owned and not group/world writable.
func readResolverSource(filename string, serviceUID uint32) ([]byte, error) {
	if !path.IsAbs(filename) || path.Clean(filename) != filename {
		return nil, errors.New("invalid native resolver source path")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(filename, "/"), "/")
	current := ""
	for _, component := range parts[:len(parts)-1] {
		current += "/" + component
		next, err := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&0o022 != 0 || (stat.Uid != 0 && (current != "/run/systemd/resolve" || stat.Uid != serviceUID)) {
			_ = unix.Close(fd)
			return nil, errors.New("native resolver directory has an untrusted writer")
		}
	}
	fileFD, err := unix.Openat(fd, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	_ = unix.Close(fd)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fileFD), "native-resolver-source")
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(fileFD, &stat); err != nil {
		return nil, err
	}
	allowService := filename == "/run/systemd/resolve/resolv.conf" && stat.Uid == serviceUID
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o022 != 0 || stat.Nlink != 1 || stat.Uid != 0 && !allowService {
		return nil, errors.New("native resolver source is not a protected regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 64<<10 {
		return nil, errors.New("native resolver source exceeds bound")
	}
	return raw, nil
}

func rootResolverLink() (string, error) {
	fd, err := unix.Open("/etc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(fd) }()
	var directory, before, after unix.Stat_t
	if unix.Fstat(fd, &directory) != nil || directory.Uid != 0 || directory.Mode&0o022 != 0 {
		return "", errors.New("untrusted native resolver link directory")
	}
	if unix.Fstatat(fd, "resolv.conf", &before, unix.AT_SYMLINK_NOFOLLOW) != nil || before.Uid != 0 || before.Mode&unix.S_IFMT != unix.S_IFLNK {
		return "", errors.New("native resolver link is not root-owned")
	}
	buffer := make([]byte, 4096)
	n, err := unix.Readlinkat(fd, "resolv.conf", buffer)
	if err != nil || n == len(buffer) {
		return "", errors.New("invalid native resolver symlink")
	}
	if unix.Fstatat(fd, "resolv.conf", &after, unix.AT_SYMLINK_NOFOLLOW) != nil || before.Dev != after.Dev || before.Ino != after.Ino {
		return "", errors.New("native resolver link changed during discovery")
	}
	link := string(buffer[:n])
	if !path.IsAbs(link) {
		link = path.Join("/etc", link)
	}
	return path.Clean(link), nil
}

func resolvedServiceUID() (uint32, error) {
	raw, err := readResolverSource("/etc/passwd", 0)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) != 7 || fields[0] != "systemd-resolve" {
			continue
		}
		uid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil || uid == 0 || uid == uint64(^uint32(0)) {
			return 0, errors.New("invalid native resolved service identity")
		}
		return uint32(uid), nil
	}
	return 0, errors.New("native resolved service identity is unavailable")
}

func parseTrustedResolvers(raw []byte) ([]netip.Addr, bool, error) {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return nil, false, errors.New("missing or unbounded native resolver data")
	}
	var addresses []netip.Addr
	stub := false
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		line := strings.SplitN(scanner.Text(), "#", 2)[0]
		line = strings.SplitN(line, ";", 2)[0]
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "nameserver" {
			continue
		}
		if len(fields) != 2 {
			return nil, false, errors.New("invalid native nameserver entry")
		}
		address, err := netip.ParseAddr(fields[1])
		if err != nil || address.Zone() != "" || address.Is4In6() || address.IsUnspecified() || address.IsMulticast() {
			return nil, false, errors.New("native resolver is not a verified exact IP address")
		}
		if address.IsLoopback() {
			stub = true
			continue
		}
		addresses = append(addresses, address)
		if len(addresses) > 16 {
			return nil, false, errors.New("too many native resolver addresses")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, false, err
	}
	if len(addresses) == 0 && !stub {
		return nil, false, errors.New("native resolver source has no nameserver")
	}
	slices.SortFunc(addresses, func(a, b netip.Addr) int { return a.Compare(b) })
	return slices.Compact(addresses), stub, nil
}
