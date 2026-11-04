package imagejob

import (
	"strings"
	"testing"
)

func TestCanonicalContextValidatesPathsAndIncludesModes(t *testing.T) {
	r := Recipe{Builder: "1", Platform: "linux/amd64", BaseDigest: "sha256:" + strings.Repeat("a", 64), Dockerfile: []byte("FROM scratch"), Files: map[string][]byte{"install.sh": []byte("echo hi")}}
	x, err := r.Identity()
	if err != nil {
		t.Fatal(err)
	}
	r.FileModes = map[string]uint32{"install.sh": 0755}
	y, err := r.Identity()
	if err != nil || x != y {
		t.Fatal("default mode is not canonical", err)
	}
	r.FileModes["install.sh"] = 0644
	y, _ = r.Identity()
	if x == y {
		t.Fatal("mode absent from input identity")
	}
	for _, name := range []string{"../escape", "/absolute", "Dockerfile", "a/../b", "a//b"} {
		bad := Copy(r)
		bad.Files[name] = []byte("data")
		if _, err := bad.Identity(); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}
