package chatstream

import (
	_ "embed"
	"errors"
)

//go:embed client_config.py
var clientConfigScript string

// One human session holds the saved nickname at a time: AIRC refuses a
// nickname another live connection holds. A stream registers the saved
// nickname itself; while another session of the human account is connected
// it refuses before any effect, and replacing that session is an explicit
// choice (TakeOverFlag) that AIRC performs with its same-account REGAIN.

// ErrNickHeld is the stream's typed refusal: another session of the human
// account is connected, so the saved nickname may be held.
var ErrNickHeld = errors.New("your nickname is in use by another chat session of your account")

// TakenOverReason is AIRC's ERROR when another session of this account
// regained the nickname; ErrTakenOver is how the replaced view ends.
const TakenOverReason = "Session taken over"

var ErrTakenOver = errors.New("another chat session of your account took over this nickname")

// NickHeldEvent is the one frame a refusing stream sends instead of its header.
const NickHeldEvent = "nick-held"

// TakeOverFlag is the chat.stream request flag (value "true") that registers
// with REGAIN, closing this account's other session holding the nickname.
const TakeOverFlag = "take-over"

// RegainFeature is the AIRC client feature the stream requires.
const RegainFeature = "nick-regain"

// ClientArgv is the fixed confined launch of one human stream's AIRC JSONL v2
// client: python runs the embedded launcher, which execs airc on a private
// copy of config with the nickname replaced and, only for takeOver, the
// same-account regain enabled (client_config.py).
func ClientArgv(python, airc, config, nick string, takeOver bool) []string {
	mode := "join"
	if takeOver {
		mode = "take-over"
	}
	return []string{python, "-I", "-c", clientConfigScript, airc, config, nick, mode}
}
