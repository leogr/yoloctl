package controller

import (
	"testing"

	"github.com/leogr/yoloctl/internal/vmwire"
)

// space logs --unit names the controller through vmwire, which cannot import
// this package; both names must stay the same unit.
func TestServiceNameMatchesTheLogUnitName(t *testing.T) {
	if ServiceName != vmwire.ControllerUnitName {
		t.Fatalf("%q != %q", ServiceName, vmwire.ControllerUnitName)
	}
}
