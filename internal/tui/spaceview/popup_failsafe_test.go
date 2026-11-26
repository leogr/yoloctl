package spaceview

import (
	"context"
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestPopupFailSafeIsTwoMinutes(t *testing.T) {
	if popupFailSafe != 2*time.Minute {
		t.Fatalf("fail-safe %s", popupFailSafe)
	}
}

// A popup's details command had 30 s; it now has the fail-safe, and closing or
// replacing the popup cancels it, since nobody waits for it any more.
func TestPopupCommandEndsWithItsPopup(t *testing.T) {
	for _, close := range []struct {
		name string
		do   func(m *Model)
	}{
		{"esc", func(m *Model) { m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})) }},
		{"replaced", func(m *Model) { m.openAccount("other") }},
	} {
		t.Run(close.name, func(t *testing.T) {
			m := fixtureModel(t, 100, 40)
			started := make(chan time.Duration, 2)
			ended := make(chan error, 2)
			m.opts.Run = func(ctx context.Context, _ ...string) ([]byte, error) {
				d, _ := ctx.Deadline()
				started <- time.Until(d)
				<-ctx.Done()
				ended <- ctx.Err()
				return nil, ctx.Err()
			}
			m.openAccount("x")
			cmd := m.popup.cmd
			go cmd()
			if left := <-started; left < popupFailSafe-10*time.Second || left > popupFailSafe {
				t.Fatalf("details command deadline in %v, want the %v fail-safe", left, popupFailSafe)
			}
			close.do(m)
			select {
			case err := <-ended:
				if err != context.Canceled {
					t.Fatalf("command ended with %v, want canceled by its popup", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the popup's command outlived the popup")
			}
		})
	}
}

// Opening a port URL adds no bound of its own: OpenURL has the fail-safe.
func TestPortURLOpenLeavesTheBoundToTheOpener(t *testing.T) {
	m := fixtureModel(t, 100, 40)
	bounded := true
	m.opts.OpenURL = func(ctx context.Context, _ string) error {
		_, bounded = ctx.Deadline()
		return nil
	}
	for _, b := range m.d.inv.Boxes {
		for _, port := range b.Ports {
			if port.Protocol == "" || port.Protocol == "tcp" {
				m.openPort(b.Name + "/" + fmt.Sprint(port.Published))
				for _, a := range m.popup.actions {
					if a.key == "o" {
						a.do(m)()
						if bounded {
							t.Fatal("the URL open has its own deadline")
						}
						return
					}
				}
			}
		}
	}
	t.Skip("fixture has no TCP port with an open action")
}
