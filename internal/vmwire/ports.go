package vmwire

const ForwardPort = 5254

// Port is a guest loopback service. It never contains a host address or path.
type Port struct {
	Protocol string `json:"protocol"`
	Number   uint16 `json:"number"`
}
