package agentboximage

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/features"
)

func TestAWSInstallerAndKeyChangesInvalidateOnlyTheirImage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	aws, err := features.Lookup("aws")
	if err != nil {
		t.Fatal(err)
	}
	original := aws.Install.Dockerfile
	aws.Name = "aws-recipe-fixture"
	install := *aws.Install
	aws.Install = &install
	if err := features.SaveUser(aws); err != nil {
		t.Fatal(err)
	}
	before, _, _, err := ComposedImageRef(RefAgentbox, []string{aws.Name})
	if err != nil {
		t.Fatal(err)
	}
	other, _, _, err := ComposedImageRef(RefAgentbox, []string{"go"})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"aws-install.sh", "aws-cli-key.asc"} {
		raw, err := os.ReadFile("../features/" + file)
		if err != nil {
			t.Fatal(err)
		}
		encoded := base64.StdEncoding.EncodeToString(raw)
		if !strings.Contains(original, encoded) {
			t.Fatal("production AWS recipe does not contain its source input", file)
		}
		changed := append(append([]byte(nil), raw...), '\n')
		aws.Install.Dockerfile = strings.Replace(original, encoded, base64.StdEncoding.EncodeToString(changed), 1)
		if err := features.SaveUser(aws); err != nil {
			t.Fatal(err)
		}
		after, _, _, err := ComposedImageRef(RefAgentbox, []string{aws.Name})
		if err != nil || after == before {
			t.Fatal("AWS source input did not invalidate composed image", file, err)
		}
		unaffected, _, _, err := ComposedImageRef(RefAgentbox, []string{"go"})
		if err != nil || unaffected != other {
			t.Fatal("AWS input invalidated an unrelated image", err)
		}
	}
}
