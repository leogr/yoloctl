package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/leogr/yoloctl/internal/hostintegration"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/tui/panes"
	"github.com/leogr/yoloctl/internal/tui/term"
)

var readHostImage = hostintegration.ReadImage
var openHostURL = hostintegration.OpenURL

func (m *model) browserHandler() func(*term.Session, session.BrowserRequest, <-chan struct{}) {
	if m.cfg.BrowserPolicy() == "off" {
		return nil
	}
	return m.receiveBrowserRequest
}

type browserPrompt struct {
	surface *term.Session
	request session.BrowserRequest
	done    <-chan struct{}
}

type browserDialog struct {
	surface       *term.Session
	returnPalette bool
}

type browserRequestMsg browserPrompt
type browserFinishedMsg struct {
	surface *term.Session
	id      string
	err     error
}

func (m *model) receiveBrowserRequest(s *term.Session, request session.BrowserRequest, done <-chan struct{}) {
	m.program.Send(browserRequestMsg{surface: s, request: request, done: done})
}

func (m *model) browserRequest(msg browserRequestMsg) tea.Cmd {
	if m.browserPrompts == nil {
		m.browserPrompts = make(map[*term.Session]browserPrompt)
	}
	tab, pane := m.tabs.Owner(func(p *panes.Pane) bool { return p.Session == msg.surface && !p.Exited })
	if tab != nil {
		// The prompt still opens at once, as for a background tab: a tab
		// other than the current one is badged as before, and a pane other
		// than the focused one is marked.
		if tab != m.tabs.Current() {
			tab.Badge = true
		}
		if pane != tab.Focused() || tab != m.tabs.Current() {
			pane.Mark = true
		}
	}
	if tab == nil {
		return func() tea.Msg {
			_ = msg.surface.Host().ReplyBrowser(msg.request.ID, errors.New("requesting tab is no longer attached"))
			return nil
		}
	}
	prompt := browserPrompt(msg)
	m.browserPrompts[msg.surface] = prompt
	if m.cfg.BrowserPolicy() == "on" {
		return m.answerBrowser(prompt, true)
	}
	m.showBrowserDialog()
	return func() tea.Msg {
		<-msg.done
		return browserFinishedMsg{surface: msg.surface, id: msg.request.ID}
	}
}

func (m *model) currentBrowserPrompt() *browserPrompt {
	if m.browserDialog == nil {
		return nil
	}
	request, ok := m.browserPrompts[m.browserDialog.surface]
	if !ok {
		return nil
	}
	select {
	case <-request.done:
		return nil
	default:
		return &request
	}
}

// Ask prompts appear immediately, including for a background tab or while a
// palette command is running. Keep the current tab and palette draft intact;
// queued prompts are presented one at a time, in tab order.
func (m *model) showBrowserDialog() {
	if m.browserDialog != nil {
		return
	}
	_, p := m.tabs.Owner(func(p *panes.Pane) bool {
		prompt, ok := m.browserPrompts[p.Session]
		if !ok || p.Exited {
			return false
		}
		select {
		case <-prompt.done:
			return false
		default:
			return true
		}
	})
	if p != nil {
		m.cancelConfirm() // a browser prompt cancels an open close popup (Keep)
		m.closePicker()   // and an open tab picker
		m.disarmPrefix()  // a prefix never acts on the key after the prompt
		m.browserDialog = &browserDialog{surface: p.Session, returnPalette: m.focusPalette}
		m.focusPalette = true
	}
}

func (m *model) finishBrowserPrompt(surface *term.Session, id string) {
	if pending, ok := m.browserPrompts[surface]; !ok || pending.request.ID != id {
		return
	}
	delete(m.browserPrompts, surface)
	if dialog := m.browserDialog; dialog != nil && dialog.surface == surface {
		m.focusPalette = dialog.returnPalette
		m.browserDialog = nil
		m.showBrowserDialog()
	}
}

func (m *model) answerBrowser(prompt browserPrompt, accept bool) tea.Cmd {
	m.finishBrowserPrompt(prompt.surface, prompt.request.ID)
	return func() tea.Msg {
		var err error
		if !accept {
			err = errors.New("browser request declined on host")
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			select {
			case <-prompt.done:
				return browserFinishedMsg{surface: prompt.surface, id: prompt.request.ID, err: errors.New("browser request expired")}
			default:
			}
			go func() {
				select {
				case <-prompt.done:
					cancel()
				case <-ctx.Done():
				}
			}()
			err = openHostURL(ctx, prompt.request.URL)
		}
		replyErr := prompt.surface.Host().ReplyBrowser(prompt.request.ID, err)
		if err == nil {
			err = replyErr
		}
		return browserFinishedMsg{surface: prompt.surface, id: prompt.request.ID, err: err}
	}
}

func (m *model) browserKey(k tea.Key) (tea.Cmd, bool) {
	if m.browserDialog == nil {
		return nil, false
	}
	prompt := m.currentBrowserPrompt()
	if prompt == nil || k.IsRepeat {
		return nil, true
	}
	if k.Code == 'o' && k.Mod & ^(tea.ModCapsLock|tea.ModNumLock) == tea.ModCtrl {
		return m.answerBrowser(*prompt, true), true
	}
	if k.Code == tea.KeyEscape && k.Mod & ^(tea.ModCapsLock|tea.ModNumLock) == 0 {
		return m.answerBrowser(*prompt, false), true
	}
	// Ordinary typing, paste and Enter are not approval and must not reach the
	// obscured editor or child. Ctrl+O is deliberately an explicit choice.
	return nil, true
}

func (m *model) browserPopup(rows int) []string {
	prompt := m.currentBrowserPrompt()
	if prompt == nil || rows <= 0 {
		return nil
	}
	var lines []string
	if _, p := m.tabs.Owner(func(p *panes.Pane) bool { return p.Session == prompt.surface }); p != nil {
		label := strings.Join(strings.Fields(ansi.Strip(p.Title())), " ")
		lines = append(lines, ansi.Truncate("From: "+label, max(m.width-4, 1), "…"))
	}
	lines = append(lines, prompt.request.URL)
	return (popup{
		Title: "Open link in your browser?", Lines: lines,
		Footer: "Ctrl+O Open     Esc Cancel",
	}).render(m.width, rows, m.surfaceBg())
}

// imagePasteKey is a temporary input owner for one Ctrl+V press/release pair.
// Its fields are used only by the session's ordered input worker. An image
// consumes both events; a non-image clipboard preserves both encoded events.
type imagePasteKey struct {
	*term.Session
	started  bool
	consumed bool
	read     func(context.Context) (hostintegration.ClipboardImage, error)
}

func (k *imagePasteKey) SendInput(fallback []byte) error {
	fallback = bytes.Clone(fallback)
	return k.PrepareInput(func(ctx context.Context) ([]byte, error) {
		if k.started {
			if k.consumed {
				return nil, nil
			}
			return fallback, nil
		}
		k.started = true
		data, err := k.read(ctx)
		if errors.Is(err, hostintegration.ErrNoImage) {
			return fallback, nil
		}
		k.consumed = true
		if err != nil {
			return nil, fmt.Errorf("image paste: %w", err)
		}
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		path, err := k.Host().PasteImage(ctx, data)
		if err != nil {
			return nil, fmt.Errorf("image paste: %w", err)
		}
		return pasteBytes(hostintegration.ImagePathText(path), k.BracketedPaste()), nil
	})
}

func (m *model) imageKey(target keyTerminal, k tea.Key, release bool) keyTerminal {
	if release || !m.cfg.ClipboardEnabled() {
		return target
	}
	if k.IsRepeat {
		if owner, ok := m.keyOwners[keyIdentity(k)].(*imagePasteKey); ok {
			return owner
		}
		return target
	}
	mod := k.Mod & ^(tea.ModCapsLock | tea.ModNumLock)
	if (k.Code != 'v' || mod != tea.ModCtrl) && (k.Code != 0x16 || mod != 0) {
		return target
	}
	s, ok := target.(*term.Session)
	if !ok || s.Host() == nil || !s.Host().CanPasteImage() {
		return target
	}
	return &imagePasteKey{Session: s, read: readHostImage}
}
