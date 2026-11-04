package agentbox

import (
	"strings"
	"testing"
)

func TestForwardListenerKernelTableRequiresExactOwnedListeningSocket(t *testing.T) {
	// Fictional Linux procfs rows; no actual socket/root readiness claim.
	const header = "sl local_address rem_address st tx_queue rx_queue tr tm->when retrnsmt uid timeout inode\n"
	const row = "0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 12345\n"
	if err := verifyForwardTCPTable([]byte(header+row), 8080, 12345, 0); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{
		strings.Replace(header+row, "0100007F", "00000000", 1),
		strings.Replace(header+row, ":1F90", ":1F91", 1),
		strings.Replace(header+row, " 0A ", " 01 ", 1),
		strings.Replace(header+row, "00000000 0 0 12345", "00000000 1 0 12345", 1),
		strings.Replace(header+row, "12345", "12346", 1),
		header + row + row,
		header + "0: incomplete\n",
		row,
		"",
	} {
		if err := verifyForwardTCPTable([]byte(changed), 8080, 12345, 0); err == nil {
			t.Fatalf("different/malformed socket table accepted: %q", changed)
		}
	}
}
