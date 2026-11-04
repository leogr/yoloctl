package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestClassifyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil error → ok", nil, exitOK},
		{"user error → user", asUserError(errors.New("bad name")), exitUser},
		{"wrapped user error → user", fmt.Errorf("outer: %w", asUserError(errors.New("bad"))), exitUser},
		{"plain subprocess exit error → runtime", runWithExit(t, 10), exitRuntime},
		{"passthrough subprocess exit error → child status", asPassthroughExitError(runWithExit(t, 10)), 10},
		{"plain error → runtime", errors.New("kaboom"), exitRuntime},
	}
	for _, c := range cases {
		got := classifyError(c.err)
		if got != c.want {
			t.Errorf("%s: classifyError = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestAsUserError_NilPassthrough(t *testing.T) {
	if asUserError(nil) != nil {
		t.Fatal("asUserError(nil) should return nil")
	}
}

func TestAsPassthroughExitError_NilPassthrough(t *testing.T) {
	if asPassthroughExitError(nil) != nil {
		t.Fatal("asPassthroughExitError(nil) should return nil")
	}
}

func TestAsUserError_PreservesMessage(t *testing.T) {
	inner := errors.New("invalid name: must match X")
	wrapped := asUserError(inner)
	if wrapped.Error() != inner.Error() {
		t.Errorf("wrapped Error() = %q, want unchanged %q", wrapped.Error(), inner.Error())
	}
	if !IsUserError(wrapped) {
		t.Error("IsUserError on directly-wrapped should be true")
	}
	if !errors.Is(wrapped, inner) {
		t.Error("errors.Is should still find the inner error")
	}
}
