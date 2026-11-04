package lima

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/leogr/yoloctl/assets"
	"github.com/leogr/yoloctl/internal/layout"
)

// TemplateWithAccountDisk renders the bundled Lima template with the supplied
// mounts (the space's host→VM grants) as the entire `mounts:` block. yoloctl
// declares NO blanket home mount — only these explicitly-granted paths — so
// a VM-level escape can't read the user's whole home. Pass the result to
// Client.Create when bringing up a new space so its grants are visible from
// first boot, or write the result over the instance's active lima.yaml
// (under LIMA_HOME — see layout.LimaHome) and restart the space to refresh mounts
// in place. An empty mount list renders an empty `mounts:` block.
//
// Lima selects the VM backend, guest architecture and mount transport from
// the host. Do not infer them from yoloctl's build architecture: an amd64 CLI
// can run under Rosetta on an arm64 Mac.
func TemplateWithAccountDisk(mounts []Mount, id string, format bool) []byte {
	return TemplateWithBootSettings(mounts, id, format, 4, "4GiB", "30GiB")
}

func TemplateWithBootSettings(mounts []Mount, id string, format bool, cpus int, memory, disk string) []byte {
	tmpl := template.Must(template.New("lima").Parse(assets.LimaTemplate))
	data := struct {
		AccountDisk       string
		FormatAccountDisk bool
		WritableMounts    string
		CPUs              int
		Memory            string
		Disk              string
	}{
		FormatAccountDisk: format,
		WritableMounts:    strings.TrimRight(MarshalMounts(mounts), "\n"),
		CPUs:              cpus, Memory: memory, Disk: disk,
	}
	if id != "" {
		data.AccountDisk = layout.AccountDiskName(id)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		panic(fmt.Sprintf("render lima template: %v", err))
	}
	return buf.Bytes()
}
