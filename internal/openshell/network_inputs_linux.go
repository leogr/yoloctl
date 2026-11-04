package openshell

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"os"
	"syscall"
)

// Stable Linux UAPI values not exported by Go's frozen syscall package;
// include/uapi/linux/rtnetlink.h enum rtattr_type_t.
const routeViaAttribute uint16 = 18

// discoverLocalPrefixes runs only in native root infrastructure, with its
// thread already in the intended namespace. No image-provided ip executable,
// workload route text or editable resolv.conf supplies these destinations.
func discoverLocalPrefixes() ([]netip.Prefix, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("infrastructure route discovery requires root")
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var prefixes []netip.Prefix
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err != nil {
			return nil, errors.New("infrastructure interface address is not an IP prefix")
		}
		if prefix.Addr().IsLoopback() {
			continue
		}
		if !prefix.IsValid() || prefix.Bits() == 0 || prefix.Addr().Is4In6() || prefix.Addr().IsUnspecified() {
			return nil, errors.New("invalid infrastructure interface prefix")
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	raw, err := syscall.NetlinkRIB(syscall.RTM_GETROUTE, syscall.AF_UNSPEC)
	if err != nil {
		return nil, err
	}
	if len(raw) > 4<<20 {
		return nil, errors.New("infrastructure route dump exceeds bound")
	}
	messages, err := syscall.ParseNetlinkMessage(raw)
	if err != nil {
		return nil, err
	}
	routes, err := infrastructureRoutePrefixes(messages)
	if err != nil {
		return nil, err
	}
	prefixes = append(prefixes, routes...)
	if len(prefixes) == 0 || len(prefixes) > 4096 {
		return nil, errors.New("missing or unbounded infrastructure route/interface inventory")
	}
	return compactPrefixes(prefixes), nil
}

// Only connected/local/broadcast route destinations are infrastructure. A
// public route reached through a gateway is not a local subnet deny; its
// exact gateway is infrastructure even when publicly numbered. Defaults are
// never converted to /0 denies. Multipath/VIA gateways get the same treatment.
func infrastructureRoutePrefixes(messages []syscall.NetlinkMessage) ([]netip.Prefix, error) {
	var result []netip.Prefix
	for _, message := range messages {
		if message.Header.Type == syscall.NLMSG_DONE {
			continue
		}
		if message.Header.Type != syscall.RTM_NEWROUTE || len(message.Data) < syscall.SizeofRtMsg {
			return nil, errors.New("incomplete infrastructure route inventory")
		}
		family, bits, scope, kind := int(message.Data[0]), int(message.Data[1]), message.Data[6], message.Data[7]
		if family != syscall.AF_INET && family != syscall.AF_INET6 {
			continue
		}
		attrs, err := routeAttributes(message.Data[syscall.SizeofRtMsg:])
		if err != nil {
			return nil, err
		}
		var destination netip.Addr
		seen := map[uint16]bool{}
		for _, attribute := range attrs {
			if attribute.kind == syscall.RTA_DST || attribute.kind == syscall.RTA_GATEWAY || attribute.kind == syscall.RTA_MULTIPATH || attribute.kind == routeViaAttribute {
				if seen[attribute.kind] {
					return nil, errors.New("duplicate infrastructure route identity attribute")
				}
				seen[attribute.kind] = true
			}
			switch attribute.kind {
			case syscall.RTA_DST:
				destination, err = routeDestinationAddress(attribute.data, family)
			case syscall.RTA_GATEWAY:
				var gateway netip.Addr
				gateway, err = routeAddress(attribute.data, family)
				if err == nil {
					result = append(result, netip.PrefixFrom(gateway, gateway.BitLen()))
				}
			case routeViaAttribute:
				var gateway netip.Addr
				gateway, err = routeViaAddress(attribute.data)
				if err == nil {
					result = append(result, netip.PrefixFrom(gateway, gateway.BitLen()))
				}
			case syscall.RTA_MULTIPATH:
				var gateways []netip.Prefix
				gateways, err = multipathGatewayPrefixes(attribute.data, family)
				result = append(result, gateways...)
			case 30: // Linux RTA_NH_ID refers to a separate nexthop inventory.
				return nil, errors.New("infrastructure route has an unresolved nexthop object")
			}
			if err != nil {
				return nil, err
			}
		}
		maxBits := 32
		if family == syscall.AF_INET6 {
			maxBits = 128
		}
		if bits < 0 || bits > maxBits {
			return nil, errors.New("invalid infrastructure route prefix length")
		}
		if bits == 0 {
			continue
		}
		if !destination.IsValid() {
			return nil, errors.New("infrastructure route lacks its explicit destination")
		}
		if scope == syscall.RT_SCOPE_LINK || scope == syscall.RT_SCOPE_HOST || kind == syscall.RTN_LOCAL || kind == syscall.RTN_BROADCAST {
			result = append(result, netip.PrefixFrom(destination, bits).Masked())
		}
	}
	return result, nil
}

type routeAttribute struct {
	kind uint16
	data []byte
}

func routeAttributes(raw []byte) ([]routeAttribute, error) {
	var attributes []routeAttribute
	for len(raw) > 0 {
		if len(raw) < 4 {
			return nil, errors.New("truncated infrastructure route attribute")
		}
		size := int(binary.NativeEndian.Uint16(raw[:2]))
		aligned := (size + 3) &^ 3
		if size < 4 || size > len(raw) || aligned > len(raw) {
			return nil, errors.New("invalid infrastructure route attribute length")
		}
		attributes = append(attributes, routeAttribute{binary.NativeEndian.Uint16(raw[2:4]), raw[4:size]})
		raw = raw[aligned:]
	}
	return attributes, nil
}
func routeAddress(raw []byte, family int) (netip.Addr, error) {
	address, err := routeDestinationAddress(raw, family)
	if err != nil {
		return netip.Addr{}, err
	}
	if address.Is4In6() || address.IsUnspecified() || address.IsMulticast() {
		return netip.Addr{}, errors.New("invalid infrastructure gateway address")
	}
	return address, nil
}
func routeDestinationAddress(raw []byte, family int) (netip.Addr, error) {
	expected := 4
	if family == syscall.AF_INET6 {
		expected = 16
	} else if family != syscall.AF_INET {
		return netip.Addr{}, errors.New("unsupported infrastructure gateway family")
	}
	if len(raw) != expected {
		return netip.Addr{}, errors.New("invalid infrastructure route address width")
	}
	address, ok := netip.AddrFromSlice(raw)
	if !ok {
		return netip.Addr{}, errors.New("invalid infrastructure route address")
	}
	return address, nil
}
func routeViaAddress(raw []byte) (netip.Addr, error) {
	if len(raw) < 2 {
		return netip.Addr{}, errors.New("truncated infrastructure route VIA")
	}
	return routeAddress(raw[2:], int(binary.NativeEndian.Uint16(raw[:2])))
}
func multipathGatewayPrefixes(raw []byte, family int) ([]netip.Prefix, error) {
	var result []netip.Prefix
	for len(raw) > 0 {
		if len(raw) < 8 {
			return nil, errors.New("truncated infrastructure nexthop")
		}
		size := int(binary.NativeEndian.Uint16(raw[:2]))
		aligned := (size + 3) &^ 3
		if size < 8 || size > len(raw) || aligned > len(raw) {
			return nil, errors.New("invalid infrastructure nexthop length")
		}
		attrs, err := routeAttributes(raw[8:size])
		if err != nil {
			return nil, err
		}
		for _, attribute := range attrs {
			var address netip.Addr
			switch attribute.kind {
			case syscall.RTA_GATEWAY:
				address, err = routeAddress(attribute.data, family)
			case routeViaAttribute:
				address, err = routeViaAddress(attribute.data)
			default:
				continue
			}
			if err != nil {
				return nil, err
			}
			result = append(result, netip.PrefixFrom(address, address.BitLen()))
		}
		raw = raw[aligned:]
	}
	return result, nil
}
