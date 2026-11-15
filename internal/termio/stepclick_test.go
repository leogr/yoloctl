package termio

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestStepClickFraming(t *testing.T) {
	var b bytes.Buffer
	for _, o := range []int{0, -3} {
		if err := WriteStepClick(&b, o); err != nil {
			t.Fatal(err)
		}
	}
	if WriteStepClick(&b, 1) == nil || WriteStepClick(&b, -maxStepClickOffset-1) == nil {
		t.Fatal("offsets outside a block are refused")
	}
	var got []int
	if err := ReadStepClicks(&b, func(o int) { got = append(got, o) }); err != nil || !slices.Equal(got, []int{0, -3}) {
		t.Fatal(got, err)
	}
	for _, bad := range []string{"1\n", "+0\n", "x\n", "-99999\n", strings.Repeat("9", 40) + "\n"} {
		if err := ReadStepClicks(strings.NewReader(bad), func(int) { t.Fatal("delivered", bad) }); err == nil {
			t.Fatalf("%q is refused", bad)
		}
	}
}

func TestTakeInheritedStepClicksClearsTheName(t *testing.T) {
	t.Setenv(StepClickFDEnv, "")
	t.Setenv(StepClickFDEnv, "2")
	if f, err := TakeInheritedStepClicks(); err == nil || f != nil {
		t.Fatal("stdio is never the channel")
	}
	if _, ok := lookup(); ok {
		t.Fatal("the name is removed even when refused")
	}
}

func lookup() (string, bool) { return os.LookupEnv(StepClickFDEnv) }
