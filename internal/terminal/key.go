package terminal

import "errors"

// Key is a decoded host key. Names follow Ghostty's W3C physical-key enum.
// Mod bits: shift, ctrl, alt, super, caps-lock, num-lock. Action: release=0,
// press=1, repeat=2. Text/codepoint retain the host's logical keyboard layout.
type Key struct {
	Name      string
	Text      string
	Codepoint rune
	Mods      uint16
	Action    uint32
}

func (t *Terminal) EncodeKey(k Key) ([]byte, error) {
	if len(k.Name) > 128 || len(k.Text) > scratchSize/2-128 {
		return nil, errors.New("terminal key exceeds encoding limit")
	}
	if t.err != nil {
		return nil, t.err
	}
	if !t.mod.Memory().Write(uint32(t.scratch), []byte(k.Name+k.Text)) {
		return nil, errors.New("terminal key outside scratch memory")
	}
	n := t.call("yolo_vt_key", t.ptr, t.scratch, uint64(len(k.Name)), t.scratch+uint64(len(k.Name)), uint64(len(k.Text)), uint64(k.Codepoint), uint64(k.Mods), uint64(k.Action), t.scratch+scratchSize/2, scratchSize/2)
	if t.err != nil {
		return nil, t.err
	}
	if n > scratchSize/2 {
		return nil, errors.New("terminal cannot encode this key")
	}
	p, ok := t.mod.Memory().Read(uint32(t.scratch+scratchSize/2), uint32(n))
	if !ok {
		return nil, errors.New("terminal key result outside memory")
	}
	return append([]byte(nil), p...), nil
}
