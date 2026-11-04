//go:build linux

package vmguest

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/vmwire"
)

func portInventory() ([]vmwire.Port, error) {
	found := map[vmwire.Port]bool{}
	add := func(proto string, n uint64) {
		if n > 0 && n <= 65535 && n != 22 && n != 53 && n != 2222 && !testTransportPort(n) {
			found[vmwire.Port{Protocol: proto, Number: uint16(n)}] = true
		}
	}
	for _, proto := range []string{"tcp", "udp"} {
		for _, suffix := range []string{"", "6"} {
			raw, e := os.ReadFile("/proc/net/" + proto + suffix)
			if e != nil {
				return nil, e
			}
			for _, line := range strings.Split(string(raw), "\n")[1:] {
				f := strings.Fields(line)
				if len(f) < 4 {
					continue
				}
				if proto == "tcp" && f[3] != "0A" {
					continue
				}
				if proto == "udp" && !strings.HasSuffix(f[2], ":0000") {
					continue
				}
				_, hex, ok := strings.Cut(f[1], ":")
				if !ok {
					continue
				}
				n, e := strconv.ParseUint(hex, 16, 16)
				if e == nil {
					add(proto, n)
				}
			}
		}
	}
	// Rootful CNI publishes through NAT, without a listening process in /proc.
	raw, e := outputOwned(exec.Command("iptables-save", "-t", "nat"))
	if e != nil {
		return nil, e
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.Contains(line, "-j DNAT") {
			continue
		}
		f := strings.Fields(line)
		proto := ""
		var port uint64
		for i := 0; i+1 < len(f); i++ {
			switch f[i] {
			case "-p":
				proto = f[i+1]
			case "--dport":
				port, _ = strconv.ParseUint(f[i+1], 10, 16)
			}
		}
		if proto == "tcp" || proto == "udp" {
			add(proto, port)
		}
	}
	out := make([]vmwire.Port, 0, len(found))
	for p := range found {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Protocol == out[j].Protocol {
			return out[i].Number < out[j].Number
		}
		return out[i].Protocol < out[j].Protocol
	})
	return out, nil
}
func forwardBridge() error {
	ln, e := listenForward()
	if e != nil {
		return e
	}
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			if !hostPeer(c) {
				_ = c.Close()
				continue
			}
			go func() {
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				r := bufio.NewReaderSize(c, 256)
				line, e := r.ReadSlice('\n')
				if e != nil {
					return
				}
				var p vmwire.ForwardRequest
				if json.Unmarshal(line, &p) != nil || p.Number == 0 || (p.Protocol != "tcp" && p.Protocol != "udp") {
					return
				}
				target, e := net.DialTimeout(p.Protocol, net.JoinHostPort("127.0.0.1", strconv.Itoa(int(p.Number))), 2*time.Second)
				if e != nil {
					_, _ = c.Write([]byte{1})
					return
				}
				defer func() { _ = target.Close() }()
				ack := byte(vmwire.ForwardOK)
				if p.Protocol == "tcp" && p.TCPStream {
					ack = vmwire.ForwardStreamOK
				}
				if _, e = c.Write([]byte{ack}); e != nil {
					return
				}
				_ = c.SetDeadline(time.Time{})
				if p.Protocol == "tcp" {
					if p.TCPStream {
						vmwire.BridgeTCP(target, vmwire.NewTCPStream(c, r))
						return
					}
					go func() { _, _ = io.Copy(target, r); _ = target.(*net.TCPConn).CloseWrite() }()
					_, _ = io.Copy(c, target)
					return
				}
				go func() {
					defer func() { _ = c.Close() }()
					buf := make([]byte, 65535)
					for {
						_ = target.SetReadDeadline(time.Now().Add(60 * time.Second))
						n, e := target.Read(buf[2:])
						if e != nil {
							return
						}
						binary.BigEndian.PutUint16(buf[:2], uint16(n))
						if _, e = c.Write(buf[:n+2]); e != nil {
							return
						}
					}
				}()
				_ = copyDatagrams(target, r)
			}()
		}
	}()
	return nil
}
func copyDatagrams(dst io.Writer, src io.Reader) error {
	buf := make([]byte, 65535)
	var h [2]byte
	for {
		if _, e := io.ReadFull(src, h[:]); e != nil {
			return e
		}
		n := int(binary.BigEndian.Uint16(h[:]))
		if n > 65507 {
			return errors.New("oversize UDP datagram")
		}
		if _, e := io.ReadFull(src, buf[:n]); e != nil {
			return e
		}
		if _, e := dst.Write(buf[:n]); e != nil {
			return e
		}
	}
}
