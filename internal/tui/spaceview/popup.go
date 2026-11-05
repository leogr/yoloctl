package spaceview

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/leogr/yoloctl/internal/spacetelemetry"
)

// popup is a modal detail view. Details that a CLI command already prints
// come from that same command's --json output; live details (VM, disk,
// rooms) re-render from the stream on every frame.
type popup struct {
	id      int
	icon    string
	iconFG  Color
	title   string
	titleFG Color
	kind    string
	command []string // the CLI command the details come from
	decode  func(m *Model, raw []byte) ([]line, error)
	local   func(m *Model) []line
	lines   []line
	loading bool
	err     string
	scroll  int
	actions []popupAction
	cmd     tea.Cmd
}

type popupAction struct {
	key   string
	label string
	do    func(m *Model) tea.Cmd
}

type popupLoadedMsg struct {
	id  int
	raw []byte
	err error
}

var popupSeq int

const popupTimeout = 30 * time.Second

func (m *Model) show(p *popup) {
	popupSeq++
	p.id = popupSeq
	if p.command != nil && m.opts.Run != nil {
		p.loading = true
		id, args, run, ctx := p.id, p.command, m.opts.Run, m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		p.cmd = func() tea.Msg {
			c, cancel := context.WithTimeout(ctx, popupTimeout)
			defer cancel()
			raw, err := run(c, args...)
			return popupLoadedMsg{id: id, raw: raw, err: err}
		}
	}
	m.popup = p
	m.hover = "" // the dashboard behind a popup is inert
}

func (p *popup) load(m *Model, msg popupLoadedMsg) {
	p.loading = false
	if msg.err != nil {
		p.err = msg.err.Error()
		return
	}
	lines, err := p.decode(m, msg.raw)
	if err != nil {
		p.err = err.Error()
		return
	}
	p.lines = lines
}

func (p *popup) scrollBy(n int) { p.scroll = max(0, p.scroll+n) }

func (p *popup) run(m *Model, id string) tea.Cmd {
	for i, a := range p.actions {
		if fmt.Sprint(i) == id {
			return a.do(m)
		}
	}
	return nil
}

func (m *Model) popupKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.popup
	switch k.String() {
	case "esc", "q", "ctrl+c":
		m.popup = nil
		if k.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case "up", "k":
		p.scrollBy(-1)
	case "down", "j":
		p.scrollBy(1)
	case "pgup":
		p.scrollBy(-10)
	case "pgdown", "space":
		p.scrollBy(10)
	case "enter":
		if len(p.actions) > 0 {
			return m, p.actions[0].do(m)
		}
		m.popup = nil
	default:
		for _, a := range p.actions {
			if a.key != "" && a.key == k.String() {
				return m, a.do(m)
			}
		}
	}
	return m, nil
}

func closeAnd(cmd func(m *Model) tea.Cmd) func(m *Model) tea.Cmd {
	return func(m *Model) tea.Cmd {
		m.popup = nil
		return cmd(m)
	}
}

// ---- opening popups ------------------------------------------------------------------

func (m *Model) openTarget(tg target) {
	switch tg.kind {
	case tAccount:
		m.openAccount(tg.id)
	case tAgent:
		m.openAgent(tg.id)
	case tFeature:
		m.openCatalog("feature", tg.id)
	case tCapability:
		m.openCatalog("capability", tg.id)
	case tCaps:
		m.openCaps(tg.id)
	case tPort:
		m.openPort(tg.id)
	case tWorkdir:
		m.openWorkdir(tg.id)
	case tGrant:
		m.openGrant(tg.id)
	case tRoom:
		m.openRoom(tg.id)
	case tVMCPU, tVMMem:
		m.openVM()
	case tBoxMem:
		m.openBoxMemory(tg.id)
	case tDisk:
		m.openDisk()
	case tSystemDisk:
		m.openSystemDisk()
	case tUnconfined:
		m.openUnconfined(tg.id)
	case tPrivileged:
		if tg.id != "" {
			m.openInspect(tg.id)
		}
	}
}

func (m *Model) spaceArgs(args ...string) []string {
	return append(args, "--space", m.opts.Space)
}

func (m *Model) openInspect(name string) {
	box, _ := m.box(name)
	p := &popup{icon: m.icon(box), iconFG: m.theme.agent(box.Agent), title: name, titleFG: m.accent(name), kind: "box",
		command: m.spaceArgs("box", "inspect", name, "--json")}
	p.decode = func(m *Model, raw []byte) ([]line, error) { return m.inspectLines(raw, false) }
	if verb := attachVerb(box); verb != "" {
		p.actions = append(p.actions, popupAction{"a", verb, closeAnd(func(m *Model) tea.Cmd { return m.activateBox(name) })})
	}
	m.show(p)
}

func (m *Model) openCaps(name string) {
	box, _ := m.box(name)
	p := &popup{icon: "⚑", iconFG: m.theme.Capability, title: name + " · capabilities", titleFG: m.accent(name), kind: "caps",
		command: m.spaceArgs("box", "inspect", name, "--json")}
	p.decode = func(m *Model, raw []byte) ([]line, error) { return m.inspectLines(raw, true) }
	if verb := attachVerb(box); verb != "" {
		p.actions = append(p.actions, popupAction{"a", verb, closeAnd(func(m *Model) tea.Cmd { return m.activateBox(name) })})
	}
	m.show(p)
}

func (m *Model) openAccount(name string) {
	p := &popup{icon: "@", iconFG: m.theme.Account, title: name, titleFG: m.theme.Account, kind: "account",
		command: m.spaceArgs("account", "show", name, "--json")}
	p.decode = func(m *Model, raw []byte) ([]line, error) { return m.accountLines(name, raw) }
	m.show(p)
}

func (m *Model) openCatalog(kind, name string) {
	icon, c := "+", m.theme.Feature
	if kind == "capability" {
		icon, c = "⚑", m.theme.Capability
	}
	if isYolo(name) {
		icon, c = "❯", m.theme.Brand
	}
	if isOperator(name) {
		icon, c = "▲", m.theme.OperatorFG
	}
	p := &popup{icon: icon, iconFG: c, title: name, titleFG: c, kind: kind, command: []string{kind, "show", name, "--json"}}
	p.decode = func(m *Model, raw []byte) ([]line, error) { return m.catalogLines(kind, name, raw) }
	m.show(p)
}

func (m *Model) openAgent(name string) {
	c := m.theme.agent(name)
	p := &popup{icon: m.agentIcon(name), iconFG: c, title: name, titleFG: c, kind: "agent", command: []string{"agent", "show", name, "--json"}}
	p.decode = func(m *Model, raw []byte) ([]line, error) { return m.agentLines(name, raw) }
	m.show(p)
}

func (m *Model) agentLines(name string, raw []byte) ([]line, error) {
	var a struct {
		Source             string   `json:"source"`
		Image              string   `json:"image"`
		Command            string   `json:"command"`
		DefaultArgs        []string `json:"default_args"`
		NpmPackage         string   `json:"npm_package"`
		NativeRelease      string   `json:"native_release"`
		StateContainerPath string   `json:"state_container_path"`
		StateMounts        []struct {
			Source      string `json:"source"`
			Destination string `json:"destination"`
		} `json:"state_mounts"`
		RequiresFeatures []string          `json:"requires_features"`
		LaunchEnv        map[string]string `json:"launch_env"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("unexpected agent show output: %w", err)
	}
	t := m.theme
	out := []line{kv(t, "Source", txt(a.Source, t.Text)), kv(t, "Image", txt(a.Image, t.Text)),
		kv(t, "Command", txt(strings.TrimSpace(a.Command+" "+strings.Join(a.DefaultArgs, " ")), t.Text))}
	switch {
	case a.NpmPackage != "":
		out = append(out, kv(t, "Installs", txt("npm "+a.NpmPackage+"@latest", t.Text)))
	case a.NativeRelease != "":
		out = append(out, kv(t, "Installs", txt("native release "+a.NativeRelease, t.Text)))
	}
	if a.StateContainerPath != "" {
		out = append(out, kv(t, "State path", txt(a.StateContainerPath, t.Text), txt("  native state from the box's account", t.Dim)))
		for _, mount := range a.StateMounts {
			out = append(out, kv(t, "State view", txt(mount.Source+" -> "+mount.Destination, t.Text)))
		}
	}
	if len(a.RequiresFeatures) > 0 {
		out = append(out, kv(t, "Features", txt(strings.Join(a.RequiresFeatures, ", "), t.Feature)))
	}
	keys := make([]string, 0, len(a.LaunchEnv))
	for k := range a.LaunchEnv {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		out = append(out, kv(t, "Launch env", txt(k+"="+a.LaunchEnv[k], t.Text)))
	}
	out = append(out, nil, append(kv(t, "Boxes"), m.boxChips(func(b spacetelemetry.Box) bool { return b.Agent == name })...))
	return out, nil
}

func (m *Model) openPort(id string) {
	boxName, published, _ := strings.Cut(id, "/")
	p := &popup{icon: "⇡", iconFG: m.theme.Port, title: ":" + published + " · " + boxName, titleFG: m.theme.Port, kind: "port",
		command: m.spaceArgs("port", "list", boxName, "--json")}
	p.decode = func(m *Model, raw []byte) ([]line, error) { return m.portLines(boxName, published, raw) }
	box, _ := m.box(boxName)
	for _, port := range box.Ports {
		if fmt.Sprint(port.Published) == published && (port.Protocol == "" || port.Protocol == "tcp") && m.opts.OpenURL != nil {
			url := "http://" + urlHost(port.HostIP) + ":" + published
			p.actions = append(p.actions, popupAction{"o", "open " + url, func(m *Model) tea.Cmd {
				return func() tea.Msg {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					return urlOpenedMsg{url: url, err: m.opts.OpenURL(ctx, url)}
				}
			}})
		}
	}
	m.show(p)
}

type urlOpenedMsg struct {
	url string
	err error
}

func urlHost(ip string) string {
	switch ip {
	case "", "0.0.0.0", "::":
		return "127.0.0.1"
	}
	if strings.Contains(ip, ":") {
		return "[" + ip + "]"
	}
	return ip
}

func (m *Model) openWorkdir(path string) {
	title := homeShort(path, m.opts.Home)
	if path == "" {
		title = "ephemeral workdirs"
	}
	m.show(&popup{icon: "⌂", iconFG: m.theme.Workdir, title: title, titleFG: m.theme.Workdir, kind: "workdir",
		local: func(m *Model) []line { return m.workdirLines(path) }})
}

func (m *Model) openGrant(path string) {
	m.show(&popup{icon: "⚑", iconFG: m.theme.Capability, title: homeShort(path, m.opts.Home), titleFG: m.theme.Capability, kind: "grant",
		local: func(m *Model) []line { return m.grantLines(path) }})
}

func (m *Model) openRoom(server string) {
	i, _ := m.roomOf(server)
	c, _ := m.roomColor(i)
	m.show(&popup{icon: ChatIcon, iconFG: c, title: server, titleFG: c, kind: "chat room",
		local: func(m *Model) []line { return m.roomLines(server) }})
}

func (m *Model) openVM() {
	title := "VM"
	if m.opts.VM.Backend != "" {
		title += " · " + m.opts.VM.Backend
	}
	m.show(&popup{icon: "▣", iconFG: m.theme.Label, title: title, titleFG: m.theme.Label, kind: "space " + m.opts.Space,
		local: func(m *Model) []line { return m.vmLines() }})
}

func (m *Model) openDisk() {
	m.show(&popup{icon: "▤", iconFG: m.theme.Label, title: "data disk", titleFG: m.theme.Label, kind: "space " + m.opts.Space,
		local: func(m *Model) []line { return m.diskLines() }})
}

func (m *Model) openUnconfined(name string) {
	t := m.theme
	m.show(&popup{icon: "▲", iconFG: t.Bad, title: name + " · unconfined network", titleFG: t.Bad, kind: "box",
		local: func(m *Model) []line {
			return []line{
				{txt("This box runs with --unconfined-network.", t.Text)},
				nil,
				{txt("OpenShell's inner network namespace and egress proxy are removed, so", t.Dim)},
				{txt("its network.* capabilities are not enforced and processes can reach", t.Dim)},
				{txt("anything the VM can reach. Filesystem and process confinement stay on.", t.Dim)},
				nil,
				{txt("A chat server needs it today to accept connections from other boxes.", t.Dim)},
			}
		}})
}

func (m *Model) openHelp() {
	t := m.theme
	key := func(k, what string) line {
		return line{txt(fmt.Sprintf("  %-14s", k), t.Brand).bold(), txt(what, t.Text)}
	}
	m.show(&popup{icon: "?", iconFG: t.Brand, title: "space view", titleFG: t.Brand, kind: "help",
		local: func(m *Model) []line {
			return []line{
				{txt("Mouse", t.Label).bold()},
				key("click box", "attach (asks first when that starts the box or a session)"),
				key("right-click", "inspect a box"),
				key("click item", "details: account, feature, capability, port, workdir, room, VM, disk, box memory"),
				key("hover "+ChatIcon, "spotlight the boxes connected to that chat room"),
				key("wheel", "scroll"),
				nil,
				{txt("Keys", t.Label).bold()},
				key("arrows hjkl", "move between boxes"),
				key("enter", "attach to the selected box"),
				key("i", "inspect the selected box"),
				key("/", "filter boxes by name, agent, account, workdir or feature"),
				key("s", "sort by name, CPU or memory"),
				key("p", "pause updates"),
				key("m", "motion on/off"),
				key("pgup pgdn g G", "scroll"),
				key("q", "quit (boxes keep running)"),
				nil,
				{txt("Legend", t.Label).bold()},
				{txt("  ● pulsing", t.OK).bold(), txt("  running, with terminal output or 3 s of CPU/disk load (not input or network)", t.Text)},
				{txt("  ● steady", t.OK).bold(), txt("   running, no such activity in the last samples", t.Text)},
				{txt("  ● dim", t.Dim).bold(), txt("      running, activity unknown", t.Text)},
				{txt("  ❯ yolo.*", t.Brand).bold(), txt("  yoloctl integration features", t.Text)},
				{txt("  ▲ operator", t.OperatorFG).bold(), txt(" authority over every box in this space", t.Text)},
				{txt("  "+ChatIcon+" room", t.Room).bold(), txt("     chat server and boxes observed connected to it", t.Text)},
				{txt("  ⌂ workdir  ", t.Workdir), txt("⇡ port  ", t.Port), txt("@ account  ", t.Account), txt("+ feature  ", t.Feature), txt("⚑ capability", t.Capability)},
			}
		}})
}

func (m *Model) openConfirm(box spacetelemetry.Box) {
	t := m.theme
	verb := attachVerb(box)
	why := "Its container is stopped. Attaching starts the box, then a new session."
	switch {
	case box.Runtime == "running":
		why = "Its container is running but has no live session. Attaching starts a new one."
	case box.State == "configured":
		why = "It is declared but has no container yet. Attaching creates and starts it."
	}
	name := box.Name
	m.show(&popup{icon: m.icon(box), iconFG: t.agent(box.Agent), title: name, titleFG: m.accent(name), kind: "confirm",
		local: func(m *Model) []line {
			return []line{{txt(why, t.Text)}, nil, {txt("This runs: yoloctl box attach "+name+" --space "+m.opts.Space, t.Dim)}}
		},
		actions: []popupAction{{"y", verb, closeAnd(func(m *Model) tea.Cmd { return m.attach(name) })}},
	})
}

// ---- decoders for CLI JSON output ---------------------------------------------------------

func kv(t Theme, key string, value ...seg) line {
	return append(line{txt(fmt.Sprintf("%-14s", key), t.Label)}, value...)
}

func (m *Model) inspectLines(raw []byte, capsOnly bool) ([]line, error) {
	var r struct {
		Identity struct {
			State, Declaration, Runtime, Session, Agent string
			DriftState                                  string `json:"drift_state"`
		} `json:"identity"`
		Manifest *struct {
			Image        string `json:"image"`
			RuntimeImage string `json:"runtime_image"`
			Composed     bool   `json:"composed"`
			Workdir      struct {
				Mode, Source  string
				ContainerPath string `json:"container_path"`
			} `json:"workdir"`
			WorkdirWritable   bool     `json:"workdir_writable"`
			UnconfinedNetwork bool     `json:"unconfined_network"`
			SessionCommand    []string `json:"session_command"`
			AgentExtraArgs    []string `json:"agent_extra_args"`
			Features          []string `json:"features"`
			Capabilities      []string `json:"capabilities"`
			Ports             []spacetelemetry.Port
			Env               []struct{ Key string } `json:"env"`
			ExtraMounts       []struct {
				Source, Destination string
				ReadOnly            bool `json:"read_only"`
			} `json:"extra_mounts"`
			CPUs   string `json:"cpus"`
			Memory string `json:"memory"`
		} `json:"manifest"`
		ManifestError string `json:"manifest_error"`
		Capabilities  []struct {
			Name    string
			Origins []string
		} `json:"effective_capabilities"`
		CapsError         string                   `json:"effective_capabilities_error"`
		OperatorAuthority string                   `json:"operator_authority"`
		Policy            *struct{ SHA256 string } `json:"computed_policy"`
		Runtime           *struct {
			Status         string `json:"status"`
			MemoryLimit    string `json:"memory_limit_bytes"`
			WorkloadMemory *struct {
				Current uint64            `json:"current_bytes"`
				Peak    uint64            `json:"peak_bytes"`
				Max     uint64            `json:"max_bytes"`
				Events  map[string]uint64 `json:"events"`
			} `json:"workload_memory"`
			ActivePolicyMatches *bool `json:"active_policy_matches_computed"`
		} `json:"runtime"`
		RuntimeError string `json:"runtime_error"`
		Drift        struct {
			State, Reason string
		} `json:"drift"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("unexpected box inspect output: %w", err)
	}
	t := m.theme
	var out []line
	caps := func() {
		out = append(out, line{txt("Effective capabilities", t.Label).bold(), txt("  (origin: explicit | feature)", t.Dim)})
		if r.CapsError != "" {
			out = append(out, line{txt("  "+r.CapsError, t.Bad)})
		}
		for _, c := range r.Capabilities {
			color := t.Capability
			switch {
			case isOperator(c.Name):
				color = t.OperatorFG
			case isYolo(c.Name):
				color = t.Brand
			}
			out = append(out, line{txt(fmt.Sprintf("  %-26s", c.Name), color).bold(), txt(strings.Join(c.Origins, ", "), t.Dim)})
		}
		if r.OperatorAuthority != "" {
			out = append(out, nil, line{txt("▲ ", t.OperatorFG).bold(), txt(r.OperatorAuthority, t.OperatorFG)})
		}
	}
	if capsOnly {
		caps()
		return out, nil
	}
	id := r.Identity
	out = append(out, kv(t, "State", txt(id.State, t.Bright).bold(), txt(fmt.Sprintf("   declaration %s · runtime %s · session %s", id.Declaration, id.Runtime, id.Session), t.Dim)))
	if r.ManifestError != "" {
		out = append(out, kv(t, "Manifest", txt(r.ManifestError, t.Bad)))
	}
	if man := r.Manifest; man != nil {
		img := man.Image
		if man.Composed && man.RuntimeImage != "" {
			img += " → " + man.RuntimeImage
		}
		out = append(out, kv(t, "Image", txt(img, t.Text)))
		wd := man.Workdir.Mode
		if man.Workdir.Source != "" {
			wd = homeShort(man.Workdir.Source, m.opts.Home)
		}
		mode := "read-only"
		if man.WorkdirWritable {
			mode = "writable"
		}
		out = append(out, kv(t, "Workdir", txt(wd, t.Workdir), txt(" → "+man.Workdir.ContainerPath+" ("+mode+")", t.Dim)))
		res := fmt.Sprintf("cpus %s · memory %s", dash(man.CPUs), dash(man.Memory))
		out = append(out, kv(t, "Resources", txt(res, t.Text)))
		for _, p := range man.Ports {
			out = append(out, kv(t, "Port", txt(fmt.Sprintf("%s:%d → %d/%s", p.HostIP, p.Published, p.Target, p.Protocol), t.Port)))
		}
		if len(man.Features) > 0 {
			out = append(out, kv(t, "Features", txt(strings.Join(man.Features, ", "), t.Feature)))
		}
		if len(man.SessionCommand) > 0 {
			out = append(out, kv(t, "Session cmd", txt(strings.Join(man.SessionCommand, " "), t.Text)))
		}
		if len(man.AgentExtraArgs) > 0 {
			out = append(out, kv(t, "Agent args", txt(strings.Join(man.AgentExtraArgs, " "), t.Text)))
		}
		for _, e := range man.Env {
			out = append(out, kv(t, "Env", txt(e.Key, t.Text), txt(" (value hidden)", t.Dim)))
		}
		for _, em := range man.ExtraMounts {
			mode := "rw"
			if em.ReadOnly {
				mode = "ro"
			}
			out = append(out, kv(t, "Mount", txt(homeShort(em.Source, m.opts.Home)+" → "+em.Destination+" ("+mode+")", t.Workdir)))
		}
		if man.UnconfinedNetwork {
			out = append(out, kv(t, "Network", txt("▲ unconfined: no egress allow-list", t.Bad).bold()))
		}
	}
	out = append(out, nil)
	caps()
	out = append(out, nil, line{txt("Runtime", t.Label).bold()})
	if r.RuntimeError != "" {
		out = append(out, line{txt("  "+r.RuntimeError, t.Bad)})
	}
	if rt := r.Runtime; rt != nil {
		out = append(out, kv(t, "  Status", txt(rt.Status, t.Text)))
		limit := "shared workload pool"
		if rt.MemoryLimit != "" && rt.MemoryLimit != "0" {
			limit = rt.MemoryLimit + " bytes"
		}
		out = append(out, kv(t, "  Memory", txt(limit, t.Text)))
		if wm := rt.WorkloadMemory; wm != nil {
			out = append(out, kv(t, "  Pool", txt(fmt.Sprintf("%s/%s, peak %s; OOM kills since pool creation: %d", humanBytes(wm.Current), humanBytes(wm.Max), humanBytes(wm.Peak), wm.Events["oom_kill"]), t.Text)))
		}
		if rt.ActivePolicyMatches != nil {
			match, c := "matches the computed policy", t.OK
			if !*rt.ActivePolicyMatches {
				match, c = "differs from the computed policy", t.Bad
			}
			out = append(out, kv(t, "  Policy", txt(match, c)))
		}
	}
	if r.Drift.State != "" {
		out = append(out, nil, kv(t, "Drift", txt(r.Drift.State, t.Text), txt(" "+r.Drift.Reason, t.Dim)))
	}
	return out, nil
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func (m *Model) accountLines(name string, raw []byte) ([]line, error) {
	var a struct {
		Space          string   `json:"space"`
		StateAvailable bool     `json:"state_available"`
		Name           string   `json:"name"`
		ManifestPath   string   `json:"manifest_path"`
		StateHome      string   `json:"state_home"`
		Secrets        []string `json:"secrets"`
		StateLeaves    []struct {
			Agent     string `json:"agent"`
			StatePath string `json:"state_path"`
		} `json:"state_leaves"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("unexpected account show output: %w", err)
	}
	t := m.theme
	nameLine := kv(t, "Name", txt(a.Name, t.Bright).bold())
	if m.d.inv != nil && m.d.inv.DefaultAccount == a.Name {
		nameLine = append(nameLine, txt("   ★ space default", t.Brand))
	}
	out := []line{kv(t, "Space", txt(a.Space, t.Text)), nameLine,
		kv(t, "Manifest", txt(a.ManifestPath, t.Text)), kv(t, "State home", txt(a.StateHome, t.Text))}
	if len(a.Secrets) == 0 {
		out = append(out, kv(t, "Secrets", txt("(none)", t.Dim)))
	}
	for i, s := range a.Secrets {
		label := "Secrets"
		if i > 0 {
			label = ""
		}
		value := []seg{txt(s, t.Text)}
		if i == 0 {
			value = append(value, txt("   names only; values are never shown", t.Dim))
		}
		out = append(out, kv(t, label, value...))
	}
	switch {
	case !a.StateAvailable:
		out = append(out, kv(t, "State leaves", txt("unavailable (space stopped or storage inaccessible)", t.Bad)))
	case len(a.StateLeaves) == 0:
		out = append(out, kv(t, "State leaves", txt("(none)", t.Dim)))
	}
	for i, l := range a.StateLeaves {
		label := "State leaves"
		if i > 0 {
			label = ""
		}
		out = append(out, kv(t, label, txt(m.agentIcon(l.Agent)+" "+fmt.Sprintf("%-10s", l.Agent), t.agent(l.Agent)), txt(l.StatePath, t.Text)))
	}
	out = append(out, nil, append(kv(t, "Used by"), m.boxChips(func(b spacetelemetry.Box) bool { return b.Account == name })...))
	return out, nil
}

// boxChips lists the boxes matching keep with their state glyph.
func (m *Model) boxChips(keep func(spacetelemetry.Box) bool) line {
	t := m.theme
	var out line
	if m.d.inv == nil {
		return line{txt("—", t.Dim)}
	}
	for _, b := range m.sortedBoxes() {
		if !keep(b) {
			continue
		}
		if len(out) > 0 {
			out = append(out, txt("   ", t.Dim))
		}
		glyph, c := "◌", t.Dim
		switch b.State {
		case "running":
			glyph, c = "●", t.OK
		case "exited", "lost", "orphaned", "invalid":
			glyph, c = "◇", t.Bad
		}
		out = append(out, txt(m.icon(b)+" ", t.agent(b.Agent)), txt(b.Name, m.accent(b.Name)).bold(), txt(" "+glyph, c))
	}
	if len(out) == 0 {
		return line{txt("no boxes", t.Dim)}
	}
	return out
}

func (m *Model) catalogLines(kind, name string, raw []byte) ([]line, error) {
	t := m.theme
	var out []line
	var c struct {
		Name                 string   `json:"name"`
		Source               string   `json:"source"`
		Description          string   `json:"description"`
		RequiresCapabilities []string `json:"requires_capabilities"`
		Install              *struct {
			Kind, Package, Version string
		} `json:"install"`
		Network []struct {
			PolicyName string `json:"policy_name"`
			Endpoints  []struct {
				Host  string
				Ports []int
			}
			Binaries []string
		} `json:"network"`
		Filesystem []struct{ Path, Access string } `json:"filesystem"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("unexpected %s show output: %w", kind, err)
	}
	out = append(out, kv(t, "Source", txt(c.Source, t.Text)))
	if c.Description != "" {
		for i, l := range wrapText(c.Description, 64) {
			label := "Description"
			if i > 0 {
				label = ""
			}
			out = append(out, kv(t, label, txt(l, t.Text)))
		}
	}
	if isOperator(name) {
		out = append(out, nil,
			line{txt("▲ ", t.OperatorFG).bold(), txt("Authority over every box in this space, including boxes of other", t.OperatorFG)},
			line{txt("  accounts, stronger grants, delegation and destructive operations.", t.OperatorFG)})
	}
	if len(c.RequiresCapabilities) > 0 {
		out = append(out, kv(t, "Requires", txt(strings.Join(c.RequiresCapabilities, ", "), t.Capability)))
	}
	if in := c.Install; in != nil {
		what := in.Kind
		if in.Package != "" {
			what += " " + in.Package
			if in.Version != "" {
				what += "@" + in.Version
			}
		}
		out = append(out, kv(t, "Installs", txt(what, t.Text)))
	}
	for _, n := range c.Network {
		for _, e := range n.Endpoints {
			ports := make([]string, len(e.Ports))
			for i, p := range e.Ports {
				ports[i] = fmt.Sprint(p)
			}
			out = append(out, kv(t, "Egress", txt(e.Host+":"+strings.Join(ports, ","), t.Port), txt("  "+n.PolicyName, t.Dim)))
		}
		if len(n.Binaries) > 0 {
			out = append(out, kv(t, "  binaries", txt(strings.Join(n.Binaries, " "), t.Dim)))
		}
	}
	for _, f := range c.Filesystem {
		out = append(out, kv(t, "Filesystem", txt(f.Path, t.Workdir), txt(" ("+f.Access+")", t.Dim)))
	}
	usedBy := "Used by"
	keep := func(b spacetelemetry.Box) bool { return slices.Contains(b.Features, name) }
	if kind == "capability" {
		usedBy = "Granted to"
		keep = func(b spacetelemetry.Box) bool { return slices.Contains(b.Capabilities, name) }
	}
	out = append(out, nil, append(kv(t, usedBy), m.boxChips(keep)...))
	return out, nil
}

func (m *Model) portLines(boxName, published string, raw []byte) ([]line, error) {
	var forwards []struct {
		Box       string `json:"box"`
		HostIP    string `json:"host_ip"`
		Published int    `json:"published"`
		Target    int    `json:"target"`
		Protocol  string `json:"protocol"`
		Unit      string `json:"unit"`
	}
	if err := json.Unmarshal(raw, &forwards); err != nil {
		return nil, fmt.Errorf("unexpected port list output: %w", err)
	}
	t := m.theme
	box, _ := m.box(boxName)
	var out []line
	for _, p := range box.Ports {
		if fmt.Sprint(p.Published) != published {
			continue
		}
		out = append(out, kv(t, "Declared", txt(fmt.Sprintf("host %s:%d → box %s:%d/%s", p.HostIP, p.Published, boxName, p.Target, p.Protocol), t.Port)))
	}
	active := false
	for _, f := range forwards {
		if fmt.Sprint(f.Published) != published {
			continue
		}
		active = true
		out = append(out, kv(t, "Forward", txt("active", t.OK).bold(), txt("  "+f.Unit, t.Dim)))
	}
	if !active {
		out = append(out, kv(t, "Forward", txt("not active", t.Bad), txt("  starts with the box", t.Dim)))
	}
	if live := m.d.boxes[boxName]; live != nil && live.Rated {
		out = append(out, kv(t, "Box traffic", txt("↓"+humanRate(live.NetRx)+"/s ↑"+humanRate(live.NetTx)+"/s", t.Text), txt("  all of the box's interfaces", t.Dim)))
	}
	return out, nil
}

// ---- live local views ---------------------------------------------------------------------

func (m *Model) workdirLines(path string) []line {
	t := m.theme
	var out []line
	if path == "" {
		out = append(out, line{txt("VM-local and disposable: lost when the box is destroyed or the", t.Text)},
			line{txt("space is recreated. Each box gets its own directory at /workdir.", t.Text)})
		out = append(out, nil, append(kv(t, "Boxes"), m.boxChips(func(b spacetelemetry.Box) bool { return b.WorkdirMode == "ephemeral" })...))
		return out
	}
	out = append(out, kv(t, "Host path", txt(path, t.Workdir)))
	out = append(out, kv(t, "In boxes", txt(filepath.Join("/workdir", path), t.Text)))
	if g, ok := m.grantFor(path); ok {
		mode := "read-only"
		if g.Writable {
			mode = "writable"
		}
		out = append(out, kv(t, "Grant", txt(homeShort(g.Path, m.opts.Home), t.Capability), txt(" ("+mode+")", t.Dim)))
	} else {
		out = append(out, kv(t, "Grant", txt("not covered by a current grant", t.Bad)))
	}
	out = append(out, append(kv(t, "Shared by"), m.boxChips(func(b spacetelemetry.Box) bool { return b.Workdir == path })...))
	var r, w float64
	for _, b := range m.sortedBoxes() {
		if live := m.d.boxes[b.Name]; b.Workdir == path && live != nil {
			r += live.IORead
			w += live.IOWrite
		}
	}
	out = append(out, kv(t, "Box disk I/O", txt("r"+humanRate(r)+"/s w"+humanRate(w)+"/s", t.Text), txt("  all I/O of these boxes", t.Dim)))
	return out
}

func (m *Model) grantFor(path string) (spacetelemetry.Grant, bool) {
	var best spacetelemetry.Grant
	found := false
	if m.d.inv == nil {
		return best, false
	}
	for _, g := range m.d.inv.Grants {
		if path == g.Path || strings.HasPrefix(path, strings.TrimSuffix(g.Path, "/")+"/") {
			if !found || len(g.Path) > len(best.Path) {
				best, found = g, true
			}
		}
	}
	return best, found
}

func (m *Model) grantLines(path string) []line {
	t := m.theme
	var g spacetelemetry.Grant
	for _, cand := range m.d.inv.Grants {
		if cand.Path == path {
			g = cand
		}
	}
	mode := "read-only"
	if g.Writable {
		mode = "writable"
	}
	return []line{
		kv(t, "Host path", txt(path, t.Capability)),
		kv(t, "Access", txt(mode, t.Text)),
		nil,
		{txt("Box workdirs and mounts must fit inside a grant. Change grants with", t.Dim)},
		{txt("yoloctl space mount add|remove; a box command never widens them.", t.Dim)},
		nil,
		append(kv(t, "Workdirs here"), m.boxChips(func(b spacetelemetry.Box) bool {
			return b.Workdir == path || strings.HasPrefix(b.Workdir, strings.TrimSuffix(path, "/")+"/")
		})...),
	}
}

func (m *Model) roomLines(server string) []line {
	t := m.theme
	var room spacetelemetry.Room
	for _, r := range m.d.inv.Rooms {
		if r.Server == server {
			room = r
		}
	}
	out := []line{append(kv(t, "Server"), m.boxChips(func(b spacetelemetry.Box) bool { return b.Name == server })...)}
	if room.Error != "" {
		return append(out, kv(t, "Connections", txt(room.Error, t.Bad)))
	}
	ports := make([]string, len(room.Ports))
	for i, p := range room.Ports {
		ports[i] = fmt.Sprint(p)
	}
	if len(ports) == 0 {
		out = append(out, kv(t, "Listening", txt("not reachable from other boxes", t.Dim)))
	} else {
		out = append(out, kv(t, "Listening", txt(strings.Join(ports, ", "), t.Port)))
	}
	out = append(out, append(kv(t, "Connected"), m.boxChips(func(b spacetelemetry.Box) bool { return slices.Contains(room.Members, b.Name) })...))
	if room.External > 0 || room.Local > 0 {
		out = append(out, kv(t, "Other clients", txt(fmt.Sprintf("%d outside this space · %d inside the server box", room.External, room.Local), t.Text)))
	}
	out = append(out, nil,
		line{txt("Links come from established TCP connections to the server's listening", t.Dim)},
		line{txt("ports, refreshed every few seconds. A box with yolo.chat.skill that is", t.Dim)},
		line{txt("not connected right now is not shown as a member.", t.Dim)})
	return out
}

func (m *Model) vmLines() []line {
	t := m.theme
	var out []line
	s := m.d.last
	if m.d.inv != nil {
		out = append(out, kv(t, "Space", txt(m.d.inv.Space, t.Bright).bold()))
		out = append(out, kv(t, "vCPUs", txt(fmt.Sprint(m.d.inv.CPUs), t.Text)))
	}
	if m.opts.VM.Backend != "" {
		out = append(out, kv(t, "Backend", txt(m.opts.VM.Backend, t.Text)))
	}
	if s == nil {
		return append(out, line{txt("waiting for the first sample…", t.Dim)})
	}
	if ok, why := m.sampleCurrent(); !ok {
		out = append(out, line{txt("◌ last sample is not current ("+why+"); the values below may be old", t.Bad)})
	}
	out = append(out, kv(t, "CPU busy", txt(fmt.Sprintf("%.0f%%", m.d.vmBusy*100), t.Text), txt(fmt.Sprintf("   load %.2f %.2f %.2f", s.Load[0], s.Load[1], s.Load[2]), t.Dim)))
	if s.MemTotal > 0 {
		out = append(out, kv(t, "Memory", txt(gib(s.MemTotal-min(s.MemAvailable, s.MemTotal))+" used of "+gib(s.MemTotal)+" GiB", t.Text), txt("   "+gib(s.MemAvailable)+" GiB available", t.Dim)))
	} else {
		out = append(out, kv(t, "Memory", txt("unavailable", t.Bad), txt("   /proc/meminfo could not be read", t.Dim)))
	}
	out = append(out, kv(t, "Uptime", txt(humanDuration(time.Duration(s.Uptime)*time.Second), t.Text)))
	out = append(out, line{txt("Used is MemTotal − MemAvailable: the kernel's estimate for the VM.", t.Dim)},
		line{txt("Box and pool charges include file cache that it counts as available,", t.Dim)},
		line{txt("so they need not add up to it.", t.Dim)})
	out = append(out, nil, line{txt("Workload pool", t.Label).bold(), txt("  every box and build shares it", t.Dim)})
	if p := s.Pool; p != nil {
		limit := "no limit"
		if p.Max > 0 {
			limit = gib(p.Max) + " GiB limit"
		}
		out = append(out, kv(t, "  Charged", txt(gib(p.Current)+" GiB", t.Text), txt(" of "+limit+", file cache included", t.Dim)))
		if p.Peak > 0 {
			out = append(out, kv(t, "  Peak", txt(gib(p.Peak)+" GiB", t.Text)))
		}
		oom := fg(t.OK)
		if p.OOMKills > 0 {
			oom = fg(t.Bad)
		}
		out = append(out, kv(t, "  OOM kills", seg{text: fmt.Sprint(p.OOMKills), style: oom.bold()}))
		out = append(out, kv(t, "  CPU", txt(fmt.Sprintf("%.2f cores", m.d.poolCPU), t.Text)))
		out = append(out, nil, line{txt("Peak and OOM kills count since the pool was created and are not", t.Dim)},
			line{txt("attributed to any one box. Kills under a box's own limit can count too.", t.Dim)})
	} else if s.PoolError != "" {
		out = append(out, line{txt("  "+s.PoolError, t.Bad)})
	}
	return out
}

func (m *Model) diskLines() []line {
	t := m.theme
	out := m.usageLines(m.disks()[1].usage)
	out = append(out, nil, line{txt("Holds accounts and box settings: declarations and native account state.", t.Dim)},
		line{txt("It is kept across VM rebuilds (space recreate); space destroy removes it.", t.Dim)}, nil)
	if inv := m.d.inv; inv != nil {
		out = append(out, line{txt("Accounts", t.Label).bold()})
		if inv.AccountsError != "" {
			out = append(out, line{txt("  "+inv.AccountsError, t.Bad)})
		}
		for _, a := range inv.Accounts {
			l := line{txt("  ", t.Text), txt(fmt.Sprintf("@%-14s", a.Name), t.Account).bold()}
			if len(a.Agents) == 0 {
				l = append(l, txt("no native state", t.Dim))
			}
			for _, agent := range a.Agents {
				l = append(l, txt(m.agentIcon(agent)+" "+agent+"  ", t.agent(agent)))
			}
			if a.Name == inv.DefaultAccount {
				l = append(l, txt("★ default", t.Brand))
			}
			out = append(out, l)
		}
	}
	return out
}

// ---- drawing ---------------------------------------------------------------------------------

func (b *builder) popup(f *frame) {
	m, t := b.m, b.t
	p := m.popup
	w, h := f.c.w, f.c.h
	pw := min(w-2, 90)
	inner := pw - 4
	bg := Style{FG: t.Text, BG: t.Popup}
	in := b.hit(tPopup, "")
	content := p.lines
	switch {
	case p.local != nil:
		content = p.local(m)
	case p.loading:
		frames := []string{"◐", "◓", "◑", "◒"}
		content = []line{{txt(frames[m.pulse%4]+" loading…", t.Dim)}}
	case p.err != "":
		content = nil
		for _, l := range wrapText(p.err, inner) {
			content = append(content, line{txt(l, t.Bad)})
		}
	}
	wrapped := slices.Clone(content)
	if len(p.command) > 0 {
		wrapped = append(wrapped, nil, line{txt("same data as ", t.Dim), txt("yoloctl "+strings.Join(p.command, " "), t.Capability)})
	}
	// Rows: title border, content, spacer, actions, bottom border.
	ph := min(h-2, len(wrapped)+4)
	view := ph - 4
	p.scroll = min(p.scroll, max(len(wrapped)-view, 0))
	x0, y0 := (w-pw)/2, max((h-ph)/2, 1)
	f.c.fill(x0, y0, pw, ph, bg, in)
	border := Style{FG: t.Frame, BG: t.Popup}
	on := func(st Style) Style {
		if st.BG == 0 {
			st.BG = t.Popup
		}
		return st
	}
	title := line{{text: "┌─ ", style: border, hit: in}, {text: p.icon + " ", style: on(fg(p.iconFG)), hit: in},
		{text: truncate(p.title, pw-24), style: on(fg(p.titleFG).bold()), hit: in}, {text: " · " + p.kind + " ", style: on(fg(t.Dim)), hit: in}}
	closeCtl := line{{text: " esc ", style: on(fg(t.Dim)), hit: b.hit(tPopupClose, "")}, {text: "✕", style: on(fg(t.Brand).bold()), hit: b.hit(tPopupClose, "")}, {text: " ─┐", style: border, hit: in}}
	title = append(title, seg{text: strings.Repeat("─", max(pw-title.width()-closeCtl.width(), 0)), style: border, hit: in})
	f.c.put(x0, y0, append(title, closeCtl...))
	for i := range view {
		row := line{{text: "│ ", style: border, hit: in}}
		var body line
		if j := p.scroll + i; j < len(wrapped) {
			body = wrapped[j]
		}
		for _, s := range body.fit(inner) {
			s.style = on(s.style)
			if s.hit == 0 {
				s.hit = in
			}
			row = append(row, s)
		}
		row = append(row, seg{text: " │", style: border, hit: in})
		f.c.put(x0, y0+1+i, row)
	}
	f.c.put(x0, y0+1+view, line{{text: "│" + strings.Repeat(" ", pw-2) + "│", style: border, hit: in}})
	actions := line{{text: "│ ", style: border, hit: in}}
	for i, a := range p.actions {
		key := a.key
		if i == 0 {
			key = "⏎"
		}
		actions = append(actions, seg{text: key, style: on(fg(t.Brand).bold()), hit: b.hit(tAction, fmt.Sprint(i))},
			seg{text: " " + a.label + "   ", style: on(fg(t.Text)), hit: b.hit(tAction, fmt.Sprint(i))})
	}
	actions = append(actions, seg{text: "esc", style: on(fg(t.Brand).bold()), hit: b.hit(tPopupClose, "")}, seg{text: " close", style: on(fg(t.Dim)), hit: b.hit(tPopupClose, "")})
	if len(wrapped) > view {
		actions = append(actions, seg{text: fmt.Sprintf("   ↑↓ %d/%d", p.scroll+view, len(wrapped)), style: on(fg(t.Dim)), hit: in})
	}
	actions = actions.fit(pw - 1)
	for i := range actions {
		actions[i].style = on(actions[i].style)
		if actions[i].hit == 0 {
			actions[i].hit = in
		}
	}
	f.c.put(x0, y0+2+view, append(actions, seg{text: "│", style: border, hit: in}))
	f.c.put(x0, y0+3+view, line{{text: "└" + strings.Repeat("─", pw-2) + "┘", style: border, hit: in}})
}
