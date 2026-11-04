package managedchat

import (
	"strings"
	"testing"
)

func TestCaseCollisionEnrollmentRemainsRenderable(t *testing.T) {
	s, i := fixture(t)
	r := ensure(t, s, i)
	var err error
	r, err = s.EnableRuntime(r.Generation, i)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PERSON", "Opus", "opus"} {
		id := NewID()
		i.Declarations[name] = id
		r, err = s.Enroll(Box{Name: name, ID: id}, r.Generation, i)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Render(r); err != nil {
			t.Fatal("accepted enrollment poisoned room", name, err)
		}
		if _, err = s.Expected(r); err != nil {
			t.Fatal("status cannot inspect accepted room", err)
		}
	}
	nicks, err := RoomNicknames(r)
	if err != nil {
		t.Fatal(err)
	}
	if nicks[r.Human.ID] != "person" || nicks[r.Members[1].ID] != "Opus" {
		t.Fatal("later enrollment stole earlier display identity", nicks)
	}
	for _, m := range []Member{r.Members[0], r.Members[2]} {
		a, _ := AuthAccount(m.ID)
		if nicks[m.ID] != a {
			t.Fatal("no immutable fallback", nicks)
		}
	}
	used := map[string]bool{}
	for _, nick := range nicks {
		folded := strings.ToLower(nick)
		if used[folded] {
			t.Fatal("duplicate nick", nick)
		}
		used[folded] = true
	}
}

func TestDisplayNameCannotOccupyAnotherImmutableFallback(t *testing.T) {
	a, b := Member{ID: NewID(), Name: "human"}, Member{ID: NewID(), Name: "9-invalid-nick"}
	a.Name, _ = AuthAccount(b.ID)
	r := &Room{Human: a, Members: []Member{b}}
	nicks, err := RoomNicknames(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []Member{a, b} {
		account, _ := AuthAccount(m.ID)
		if nicks[m.ID] != account {
			t.Fatal("reserved immutable account was occupied", nicks)
		}
	}
}
