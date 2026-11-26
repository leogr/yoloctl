//go:build linux || darwin

package termio

import "golang.org/x/sys/unix"

// MakeRawWithSignals preserves terminal control signals while passing output
// bytes through unchanged. The caller restores the original terminal state
// when the controller read ends.
func MakeRawWithSignals(fd int) (restore func() error, err error) {
	old, err := unix.IoctlGetTermios(fd, readTermios)
	if err != nil {
		return nil, err
	}
	raw := *old
	// Apply the same flags as term.MakeRaw except ISIG, in one update so a
	// typed interrupt never gets buffered while the controller is starting.
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.IEXTEN
	raw.Lflag |= unix.ISIG
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, writeTermios, &raw); err != nil {
		return nil, err
	}
	return func() error { return unix.IoctlSetTermios(fd, writeTermios, old) }, nil
}
