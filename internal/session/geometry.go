package session

import (
	"errors"

	"github.com/leogr/yoloctl/internal/terminal"
	"golang.org/x/crypto/ssh"
)

// RFC 4254's pixel dimensions are the measured text grid, or zero if unknown.
// ssh.Session's convenience methods instead assume an 8x8 pixel font.
type sshGeometry struct{ Cols, Rows, Width, Height uint32 }

func sshSize(g terminal.Geometry) sshGeometry {
	return sshGeometry{uint32(g.Cols), uint32(g.Rows), uint32(g.Cols * g.CellWidth), uint32(g.Rows * g.CellHeight)}
}

func requestPTY(s *ssh.Session, term string, g terminal.Geometry) error {
	if !g.Valid() {
		return errors.New("invalid PTY geometry")
	}
	req := struct {
		Term                      string
		Cols, Rows, Width, Height uint32
		Modes                     string
	}{Term: term, Cols: uint32(g.Cols), Rows: uint32(g.Rows),
		Width: uint32(g.Cols * g.CellWidth), Height: uint32(g.Rows * g.CellHeight), Modes: "\x00"}
	ok, err := s.SendRequest("pty-req", true, ssh.Marshal(&req))
	if err == nil && !ok {
		err = errors.New("SSH PTY request rejected")
	}
	return err
}

func resizePTY(s *ssh.Session, g terminal.Geometry) error {
	if !g.Valid() {
		return errors.New("invalid PTY geometry")
	}
	size := sshSize(g)
	_, err := s.SendRequest("window-change", false, ssh.Marshal(&size))
	return err
}
