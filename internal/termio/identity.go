package termio

import "os"

// TerminalIdentity preserves the host terminal's name/version for application
// preferences such as copy-on-select. It is not a claim about the embedded VT's
// capabilities: TERM and terminal query replies still belong to that VT.
type TerminalIdentity struct {
	Program string `json:"program,omitempty"`
	Version string `json:"version,omitempty"`
}

func CurrentTerminalIdentity() TerminalIdentity {
	id := TerminalIdentity{Program: os.Getenv("TERM_PROGRAM"), Version: os.Getenv("TERM_PROGRAM_VERSION")}
	if id.Program == "" || !id.Valid() {
		return TerminalIdentity{}
	}
	return id
}

func (id TerminalIdentity) Valid() bool {
	for _, value := range []string{id.Program, id.Version} {
		if len(value) > 128 {
			return false
		}
		for _, r := range value {
			if r < 0x20 || r > 0x7e {
				return false
			}
		}
	}
	return id.Program != "" || id.Version == ""
}

// Command adds only these two presentation variables to the confined child.
// Values remain argv elements, never shell syntax or controller environment.
func (id TerminalIdentity) Command(argv []string) []string {
	if id.Program == "" || !id.Valid() {
		return argv
	}
	return append([]string{"env", "TERM_PROGRAM=" + id.Program, "TERM_PROGRAM_VERSION=" + id.Version}, argv...)
}
