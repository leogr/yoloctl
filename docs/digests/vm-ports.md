# Firecracker port forwarding

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] The host helper polls guest port inventory every second over one pinned
SSH connection, opening a session per poll. A poll has a three-second deadline
and an 8 MiB reply cap. Transport failure or timeout closes the connection;
remote command failure or invalid JSON retains it. Failed inventory leaves
existing forwards intact; a successful inventory removes withdrawn ports.
Storage-paused VMs are not polled. [1](#source-1) [8](#source-8)

[FACT] Discovery reads guest TCP listeners, unconnected UDP sockets and CNI
DNAT rules. Exposed ports bind host `127.0.0.1`. Data uses a separate vsock
connection to guest port 5254, where the bridge accepts only host-CID peers and
dials guest loopback. TCP negotiates framed streams; UDP uses length-prefixed
datagrams. The inventory SSH connection does not carry forwarded traffic.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] New peers negotiate a bounded TCP byte-stream protocol with an in-band
end-of-write marker. The duplex bridge waits for both directions before closing,
so either endpoint can send a reply after receiving EOF. Cancellation or transport
failure closes both sides. Header/payload truncation and oversized frames fail.
[1](#source-1) [2](#source-2) [9](#source-9) [10](#source-10)

[FACT] Negotiation is additive: older images acknowledge raw forwarding. That
fallback retains the half-close limitation: Firecracker 1.17.0 maps carrier EOF
to both shutdown flags. Both the host CLI/helper and guest image must be updated
for framed forwarding; updating only the controller does not replace the guest
bridge. [1](#source-1) [2](#source-2) [4](#source-4) [5](#source-5) [6](#source-6)

[FACT] SSH session input uses `ssh.Session.StdinPipe`; input completion closes
that pipe, while `Run` waits for the session before closing its SSH client.
[7](#source-7)

## Sources

1. <a id="source-1"></a> [Host forwarding](../../internal/firecracker/ports_linux.go#L44) — `portInventory.query`, `forwardPorts`, `exposePort`.
2. <a id="source-2"></a> [Guest discovery and bridge](../../internal/vmguest/ports_linux.go#L22) — `portInventory`, `forwardBridge`, `copyDatagrams`.
3. <a id="source-3"></a> [Guest vsock endpoints](../../internal/vmguest/transport_linux.go#L13) — `hostPeer`, `listenForward`.
4. <a id="source-4"></a> [Host vsock carrier](../../internal/firecracker/api.go#L44) — `DialGuest`, `bufferedConn.CloseWrite`.
5. <a id="source-5"></a> [Firecracker 1.17.0 connection state](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/src/vmm/src/devices/virtio/vsock/csm/connection.rs#L207-L221) — `recv_pkt` maps host EOF to both shutdown flags.
6. <a id="source-6"></a> [Firecracker version pin](../../assets/vm/firecracker/pins.json#L2).
7. <a id="source-7"></a> [SSH session transport](../../internal/sshtransport/client.go#L18) — `Run` closes the session stdin pipe independently of the client connection.
8. <a id="source-8"></a> [Pinned guest identity](../../internal/firecracker/ssh_linux.go#L64) — `guestClientConfig` reads the selected VM's private client key and pinned host key.
9. <a id="source-9"></a> [TCP framing](../../internal/vmwire/tcp.go#L11) — `ForwardRequest`, `TCPStream`, `BridgeTCP`.
10. <a id="source-10"></a> [Half-close tests](../../internal/vmwire/tcp_test.go#L13) — replies after EOF in either direction, frame validation and cancellation.
