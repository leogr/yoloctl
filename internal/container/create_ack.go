package container

import (
	"errors"
	"strings"
)

// Drain unexpected output without allocating unbounded memory or terminating
// the manager before its result is known. Never search chatter for a plausible
// ID or publish malformed bytes to a structured caller's stdout/error message.
type createAcknowledgement struct {
	data     [128]byte
	size     int
	overflow bool
}

func (a *createAcknowledgement) Write(body []byte) (int, error) {
	written := copy(a.data[a.size:], body)
	a.size += written
	a.overflow = a.overflow || written != len(body)
	return len(body), nil
}

func (a *createAcknowledgement) containerID() (string, error) {
	id := strings.TrimSpace(string(a.data[:a.size]))
	if a.overflow || len(id) != 64 {
		return "", errors.New("container create acknowledgement lacks one bounded full ID; outcome is uncertain")
	}
	for _, c := range id {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return "", errors.New("container create acknowledgement has an invalid ID; outcome is uncertain")
		}
	}
	return id, nil
}
