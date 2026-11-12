package rotatelog

import (
	"io"
	"os"
)

// Pipe gives an external producer a real inherited descriptor, so exec.Wait
// still joins only that process, not a copier held open by its descendants.
// The long-lived supervisor owns the relay goroutine. After starting a child,
// close the parent's output descriptor; EOF then follows the last producer.
// The relay drains to EOF and calls afterEOF before closing done. A lifecycle
// caller may bound its wait on done without closing the reader or losing later
// descendant writes. Use a best-effort log sink so disk errors cannot stop the
// relay and cause SIGPIPE in the producer.
func Pipe(dst io.Writer, afterEOF func()) (output *os.File, done <-chan struct{}, err error) {
	input, output, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(dst, input)
		_ = input.Close()
		if afterEOF != nil {
			afterEOF()
		}
		close(drained)
	}()
	return output, drained, nil
}
