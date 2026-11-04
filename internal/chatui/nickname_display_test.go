package chatui

import (
	"maps"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func nicknameDisplayModel() (*Model, *[]Outgoing) {
	m, sends := readyModel()
	m.opts.Account, m.opts.Nick = "mh", "leogr_"
	m.opts.Identities = map[string]Identity{
		"mh": {Name: "leonardo.grasso", Nick: "leogr", Human: true},
		"mo": {Name: "opus-box", Nick: "opus", Box: true},
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 42})
	m.Update(MembersMsg{Members: []Member{{Nick: "leogr_", Account: "mh"}, {Nick: "opus_", Account: "mo"}}})
	arrive(m, "h", "mh", "leo", "human text")
	arrive(m, "o", "mo", "old-opus", "agent text")
	return m, sends
}

func assertNicknameLabels(t *testing.T, text string, nicks ...string) {
	t.Helper()
	for _, name := range []string{"leonardo.grasso", "opus-box"} {
		if strings.Contains(text, name) {
			t.Errorf("canonical identity name %q displayed:\n%s", name, text)
		}
	}
	for _, nick := range nicks {
		if !strings.Contains(text, nick) {
			t.Errorf("missing nickname %q:\n%s", nick, text)
		}
	}
}

func TestParticipantDisplaysUseSavedNicknames(t *testing.T) {
	for _, surface := range []struct {
		name   string
		render func(*Model) string
		nicks  []string
	}{
		{"authors", func(m *Model) string { m.rosterOpen = false; return view(m) }, []string{"leogr", "opus"}},
		{"panel cards", func(m *Model) string { m.messages = nil; return view(m) }, []string{"leogr", "opus"}},
		{"compact panel", func(m *Model) string {
			m.messages = nil
			m.Update(tea.WindowSizeMsg{Width: 120, Height: 12})
			return view(m)
		}, []string{"leogr", "opus"}},
		{"human card", func(m *Model) string { m.openCard("leogr_", "mh"); return view(m) }, []string{"leogr"}},
		{"box card", func(m *Model) string { m.openCard("opus_", "mo"); return view(m) }, []string{"opus"}},
		{"hover hints", func(m *Model) string {
			human, _ := m.participantHint("mh", "leogr_")
			box, _ := m.participantHint("mo", "opus_")
			return human + "\n" + box
		}, []string{"leogr", "opus"}},
		{"reply", func(m *Model) string { m.replyTo = "h"; return view(m) }, []string{"leogr"}},
		{"reference previews", func(m *Model) string {
			return m.previewLabel(&m.messages[0]) + "\n" + m.previewLabel(&m.messages[1])
		}, []string{"↗ leogr: human text", "↗ opus: agent text"}},
		{"message actions", func(m *Model) string {
			m.selectMessage(1)
			var labels []string
			for _, a := range m.messageActions() {
				labels = append(labels, a.label)
			}
			return strings.Join(labels, "\n") + "\n" + m.messageName(&m.messages[1]) + "\n" + view(m)
		}, []string{"Attach opus", "opus's message from"}},
		{"suggestions", func(m *Model) string { paste(m, "@"); return view(m) }, []string{"@leogr", "@opus"}},
		{"filter picker", func(m *Model) string { m.openFilterPicker(); return view(m) }, []string{"@opus"}},
		{"presence", func(m *Model) string {
			members := slices.Clone(m.members)
			m.Update(MembersMsg{Members: []Member{}})
			m.Update(MembersMsg{Members: members})
			return view(m)
		}, []string{"leogr, opus left", "leogr, opus joined"}},
	} {
		t.Run(surface.name, func(t *testing.T) {
			m, _ := nicknameDisplayModel()
			assertNicknameLabels(t, surface.render(m), surface.nicks...)
		})
	}
}

func TestNicknameCardsKeepSavedNickAndConnectionAlias(t *testing.T) {
	m, _ := nicknameDisplayModel()
	for _, member := range m.members {
		m.openCard(member.Nick, member.Account)
		out := view(m)
		token := m.opts.Identities[member.Account].Nick
		assertNicknameLabels(t, out, token)
		if !strings.Contains(out, "Nick          @"+token) || !strings.Contains(out, "Connection    @"+member.Nick) {
			t.Fatalf("saved nick and connection must remain distinct:\n%s", out)
		}
	}
	m.overlay = nil
	m.Update(MembersMsg{Members: []Member{{Nick: "opus", Account: "mo"}}})
	m.openCard("opus", "mo")
	if out := view(m); strings.Contains(out, "Connection") {
		t.Fatalf("the matching connection needs no alias row:\n%s", out)
	}
}

func TestNicknameFallbackAndRawSourceIdentity(t *testing.T) {
	m, _ := nicknameDisplayModel()
	id := m.opts.Identities["mh"]
	id.Nick = ""
	m.opts.Identities["mh"] = id
	if w := m.identify("mh", "mh"); w.name != "mh" || !w.human {
		t.Fatalf("without a saved nick, use the live nick even for a dotted name: %+v", w)
	}
	if w := m.identify("stranger", "unknown"); w.name != "stranger" || w.glyph != "" || w.trusted || w.color != m.theme.Dim {
		t.Fatalf("an untrusted session stays a dim live nick: %+v", w)
	}
	m.source, m.rosterOpen = true, false
	assertNicknameLabels(t, view(m), "leo(mh)", "old-opus(mo)")
}

func TestNicknameSaveReconnectRefreshesOpenLabels(t *testing.T) {
	for _, settings := range []bool{false, true} {
		name := "/nick"
		if settings {
			name = "Settings"
		}
		t.Run(name, func(t *testing.T) {
			m, sends := nicknameDisplayModel()
			// Retain an event-time label across the rename.
			m.Update(MembersMsg{Members: []Member{{Nick: "opus_", Account: "mo"}}})
			before := slices.Clone(m.presence[0].names)
			var saved []string
			reconnects := 0
			m.opts.SaveNick = func(nick string) tea.Cmd {
				saved = append(saved, nick)
				return func() tea.Msg { return NickSavedMsg{Nick: nick} }
			}
			ids := maps.Clone(m.opts.Identities)
			id := ids["mh"]
			id.Nick = "leonew"
			ids["mh"] = id
			m.opts.Reconnect = func() tea.Cmd {
				reconnects++
				return func() tea.Msg { return SessionMsg{Nick: "leonew", Identities: ids} }
			}
			if settings {
				paste(m, "draft @le")
				m.replyTo = "o"
				m.openSettings()
				press(m, tea.KeyEnter, 0)
				m.edit(m.overlay.nick.replace(0, len(m.overlay.nick.text), "leonew"))
			} else {
				paste(m, "/nick leonew")
			}
			cmd := press(m, tea.KeyEnter, 0)
			if cmd == nil || !slices.Equal(saved, []string{"leonew"}) || reconnects != 0 {
				t.Fatalf("one save must precede reconnect: saved=%v reconnects=%d", saved, reconnects)
			}
			if !settings && m.Draft() != "" {
				t.Fatalf("the submitted /nick draft must be consumed: %q", m.Draft())
			}
			_, reconnect := m.Update(cmd())
			if reconnect == nil || reconnects != 1 {
				t.Fatalf("one successful save must request exactly one reconnect: %d", reconnects)
			}
			// Open surfaces before the new session header arrives.
			m.Update(MembersMsg{Members: []Member{{Nick: "leogr_", Account: "mh"}, {Nick: "opus_", Account: "mo"}}})
			m.openCard("leogr_", "mh")
			if !settings {
				_ = m.SetDraft("draft @le")
			}
			m.refreshSuggest()
			if m.suggest == nil || m.suggest.items[0].label != "@leogr" {
				t.Fatal("fixture needs an open suggestion under the old nickname")
			}
			m.Update(reconnect())
			m.Update(ConnectionMsg{Connected: true})
			if m.overlay.who.account != "mh" || m.overlay.who.name != "leonew" || m.suggest == nil || m.suggest.items[0].label != "@leonew" {
				t.Fatalf("open card and suggestions must follow the session: card=%+v suggestions=%+v", m.overlay, m.suggest)
			}
			assertNicknameLabels(t, view(m), "leonew", "Nick          @leonew")
			m.overlay, m.suggest = nil, nil
			assertNicknameLabels(t, view(m), "leonew", "opus")
			if m.author(&m.messages[0]).name != "leonew" || !strings.Contains(m.previewLabel(&m.messages[0]), "leonew:") {
				t.Fatal("historical messages must display the current saved nickname")
			}
			if m.Draft() != "draft @le" || settings && m.replyTo != "o" || len(*sends) != 0 || reconnects != 1 || len(saved) != 1 {
				t.Fatalf("reconnect changed draft/reply or replayed a send: draft=%q reply=%q sends=%v saves=%v reconnects=%d", m.Draft(), m.replyTo, *sends, saved, reconnects)
			}
			if !slices.Equal(m.presence[0].names, before) || !slices.Contains(before, "leogr") {
				t.Fatal("retained notices must keep their event-time nickname")
			}
		})
	}
}

func TestCatalogRenameRefreshesOpenCardAndSuggestions(t *testing.T) {
	m, _ := nicknameDisplayModel()
	m.openCard("opus_", "mo")
	_ = m.SetDraft("@op")
	m.refreshSuggest()
	if m.suggest == nil || m.suggest.items[0].label != "@opus" {
		t.Fatal("fixture needs open suggestions")
	}
	ids := maps.Clone(m.opts.Identities)
	id := ids["mo"]
	id.Nick = "opus2"
	ids["mo"] = id
	m.Update(CatalogMsg{Generation: 1, Identities: ids})
	if m.overlay.who.account != "mo" || m.overlay.who.name != "opus2" || m.suggest == nil || m.suggest.items[0].label != "@opus2" {
		t.Fatalf("open labels must follow the catalog: card=%+v suggestions=%+v", m.overlay, m.suggest)
	}
	assertNicknameLabels(t, view(m), "opus2", "Nick          @opus2")
	m.overlay, m.suggest = nil, nil
	assertNicknameLabels(t, view(m), "opus2", "leogr")
	if m.author(&m.messages[1]).name != "opus2" || !strings.Contains(m.previewLabel(&m.messages[1]), "opus2:") {
		t.Fatal("author and preview must use the refreshed nickname")
	}
}

func TestAttachNoticesUseCurrentNicknameOrOriginalLiveNick(t *testing.T) {
	for _, state := range []string{"renamed", "empty", "missing"} {
		t.Run(state, func(t *testing.T) {
			m, _ := nicknameDisplayModel()
			m.opts.Identities["mtwo"] = Identity{Name: "second-box", Nick: "second", Box: true}
			m.opts.ResolveAttach = func(string) tea.Cmd { return func() tea.Msg { return nil } }
			m.requestAttach("original-live", "mo")
			assertNicknameLabels(t, m.notice, "Checking opus before attaching")
			want := "original-live"
			id := m.opts.Identities["mo"]
			id.Nick = ""
			if state == "renamed" {
				id.Nick, want = "opus2", "opus2"
			}
			m.opts.Identities["mo"] = id
			if state == "missing" {
				delete(m.opts.Identities, "mo")
			}
			m.requestAttach("second-live", "mtwo")
			assertNicknameLabels(t, m.notice, "Still checking "+want+"; try second again")
			m.cancelAttach("test finished")
			assertNicknameLabels(t, m.notice, "Stopped checking "+want+": test finished")
		})
	}
	t.Run("catalog removal cancels with original live nick", func(t *testing.T) {
		m, _ := nicknameDisplayModel()
		m.opts.ResolveAttach = func(string) tea.Cmd { return func() tea.Msg { return nil } }
		m.requestAttach("original-live", "mo")
		ids := maps.Clone(m.opts.Identities)
		delete(ids, "mo")
		m.Update(CatalogMsg{Generation: 1, Identities: ids})
		assertNicknameLabels(t, m.notice, "Stopped checking original-live: its room membership changed")
		if m.attaching != nil {
			t.Fatal("the changed membership must still cancel attachment")
		}
	})
}

func TestIdentityRefreshKeepsSuggestionSelection(t *testing.T) {
	for _, session := range []bool{false, true} {
		for _, renamed := range []bool{false, true} {
			for _, command := range []bool{false, true} {
				name := "catalog"
				if session {
					name = "session"
				}
				if renamed {
					name += "/renamed"
				} else {
					name += "/unchanged"
				}
				if command {
					name += "/command"
				} else {
					name += "/account"
				}
				t.Run(name, func(t *testing.T) {
					m, sends := nicknameDisplayModel()
					word, want := "@", "@opus"
					if command {
						word, want = "/", "/settings"
					}
					paste(m, word)
					press(m, tea.KeyDown, 0)
					if m.suggest == nil || m.suggest.items[m.suggest.index].label != want {
						t.Fatal("fixture must select the second suggestion")
					}
					ids := maps.Clone(m.opts.Identities)
					if renamed {
						id := ids["mo"]
						id.Nick = "opus2"
						ids["mo"] = id
						if !command {
							want = "@opus2"
						}
					}
					if session {
						m.Update(SessionMsg{Nick: m.opts.Nick, Identities: ids})
					} else {
						m.Update(CatalogMsg{Generation: 1, Identities: ids})
					}
					if m.suggest == nil || m.suggest.items[m.suggest.index].label != want {
						t.Fatalf("identity refresh changed the selected suggestion: %+v", m.suggest)
					}
					press(m, tea.KeyTab, 0)
					wantDraft := want + " "
					if command {
						wantDraft = want
					}
					if m.Draft() != wantDraft || len(*sends) != 0 {
						t.Fatalf("Tab must insert the selected item once: draft=%q sends=%v", m.Draft(), *sends)
					}
				})
			}
		}
	}
}

func TestCatalogRefreshTracksSelectedAccountWhenRowsMove(t *testing.T) {
	m, _ := nicknameDisplayModel()
	// This untrusted row shares the human's mention token and is replaced
	// by the trusted row. Its new identity will give it a separate row.
	m.Update(MembersMsg{Members: append(slices.Clone(m.members), Member{Nick: "leogr", Account: "mx"})})
	paste(m, "@")
	press(m, tea.KeyDown, 0)
	if m.suggest == nil || m.suggest.index != 1 || m.suggest.items[1].member.Account != "mo" {
		t.Fatal("fixture must select opus at index 1")
	}
	ids := maps.Clone(m.opts.Identities)
	ids["mx"] = Identity{Name: "new-member", Nick: "newbie", Box: true}
	m.Update(CatalogMsg{Generation: 1, Identities: ids})
	if m.suggest == nil || m.suggest.index != 2 || m.suggest.items[2].member.Account != "mo" {
		t.Fatalf("the account must stay selected when a new row precedes it: %+v", m.suggest)
	}
}

func TestCatalogRefreshClampsMissingSuggestion(t *testing.T) {
	for _, closeList := range []bool{false, true} {
		m, _ := readyModel()
		m.opts.Identities = map[string]Identity{
			"ma": {Name: "first", Nick: "oa"},
			"mb": {Name: "second", Nick: "ob"},
			"mc": {Name: "third", Nick: "oc"},
		}
		m.Update(MembersMsg{Members: []Member{{Nick: "a", Account: "ma"}, {Nick: "b", Account: "mb"}, {Nick: "c", Account: "mc"}}})
		paste(m, "@o")
		press(m, tea.KeyDown, 0)
		press(m, tea.KeyDown, 0)
		if m.suggest == nil || m.suggest.index != 2 {
			t.Fatal("fixture must select the last of three matches")
		}
		ids := maps.Clone(m.opts.Identities)
		for account, id := range ids {
			if account != "ma" || closeList {
				id.Nick = id.Name
				ids[account] = id
			}
		}
		m.Update(CatalogMsg{Generation: 1, Identities: ids})
		if closeList {
			if m.suggest != nil {
				t.Fatalf("no matching items must close the list: %+v", m.suggest)
			}
		} else if m.suggest == nil || m.suggest.index != 0 || m.suggest.items[0].label != "@oa" {
			t.Fatalf("a missing account must clamp the old index to the remaining list: %+v", m.suggest)
		}
	}
}
