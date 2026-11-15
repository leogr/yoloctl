package termio

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
)

// StepClickFDEnv names the private channel a TUI gives each command it
// starts in a pane: the TUI writes a click on the pane's steps block, as
// the clicked row's offset from the pane's cursor row. It carries no
// terminal input and no command line; keystrokes never travel on it.
const StepClickFDEnv = "YOLOCTL_TUI_STEP_FD"

// maxStepClickOffset bounds an offset: a block is never taller than a pane.
const maxStepClickOffset = 4096

// StepClickPair returns the TUI end and the child end of a new channel.
func StepClickPair() (*os.File, *os.File, error) { return ViewPair() }

// TakeInheritedStepClicks opens the channel named by StepClickFDEnv and
// removes the name from the environment, so no descendant can claim it; the
// descriptor is close-on-exec once opened. It returns nil without one.
func TakeInheritedStepClicks() (*os.File, error) {
	value, ok := os.LookupEnv(StepClickFDEnv)
	if !ok {
		return nil, nil
	}
	_ = os.Unsetenv(StepClickFDEnv)
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 || fd > 1024 {
		return nil, errors.New("invalid step click descriptor")
	}
	return ViewFile(fd)
}

// WriteStepClick sends one click: offset 0 is the cursor row, -1 the row above.
func WriteStepClick(w io.Writer, offset int) error {
	if offset > 0 || offset < -maxStepClickOffset {
		return errors.New("step click outside a block")
	}
	_, err := io.WriteString(w, strconv.Itoa(offset)+"\n")
	return err
}

// ReadStepClicks delivers clicks until the channel closes or a line is
// malformed.
func ReadStepClicks(r io.Reader, handle func(offset int)) error {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 16), 16)
	for s.Scan() {
		line := s.Text()
		offset, err := strconv.Atoi(line)
		if err != nil || strings.HasPrefix(line, "+") || offset > 0 || offset < -maxStepClickOffset {
			return errors.New("invalid step click")
		}
		handle(offset)
	}
	return s.Err()
}
