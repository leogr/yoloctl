// Package terminal embeds a pinned libghostty-vt core. All access to a Terminal
// must be serialized by its owner; independent terminals can run concurrently.
// The WASM instance has bounded memory and no filesystem, network or WASI imports.
package terminal

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"image/color"
	"runtime"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// Include the third-party notices in distributed executables as well as source.
//
//go:embed wasm/ghostty-vt.wasm wasm/LICENSE*
var assets embed.FS

var artifact, _ = assets.ReadFile("wasm/ghostty-vt.wasm")

const (
	scratchSize = 64 * 1024
	bridgeABI   = 6
)

type contextKey struct{}

type compiledCore struct {
	runtime wazero.Runtime
	module  wazero.CompiledModule
}

var core = sync.OnceValues(func() (*compiledCore, error) {
	ctx := context.Background()
	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(4096).WithCloseOnContextDone(true))
	b := r.NewHostModuleBuilder("yolo_vt")
	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, ptr, n uint32) {
		t := ctx.Value(contextKey{}).(*Terminal)
		p, ok := m.Memory().Read(ptr, n)
		if !ok {
			panic("terminal reply outside linear memory")
		}
		if t.filtering {
			t.filtered = append(t.filtered, p...)
		} else if t.reply != nil {
			t.reply(p)
		}
	}).Export("reply")
	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, index, ptr, n, start, end uint32) {
		t := ctx.Value(contextKey{}).(*Terminal)
		p, ok := m.Memory().Read(ptr, n)
		if !ok || int(index) >= len(t.lines) {
			panic("terminal row outside frame")
		}
		t.lines[index] = t.renderPalette(string(p))
		t.spans[index] = SelectionSpan{int(start), int(end)}
	}).Export("row")
	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, kind, value uint32) {
		t := ctx.Value(contextKey{}).(*Terminal)
		switch kind {
		case 1:
			t.bells++
		case 2:
			t.hold = value != 0
			if t.onHold != nil {
				t.onHold(t.hold)
			}
		case 3:
			t.ScrollToBottom() // RIS invalidates the old viewport's meaning
		case 4:
			t.addPaletteOverride(value)
		}
	}).Export("event")
	if _, err := b.Instantiate(ctx); err != nil {
		_ = r.Close(ctx)
		return nil, err
	}
	c, err := r.CompileModule(ctx, artifact)
	if err == nil {
		for _, f := range c.ImportedFunctions() {
			mod, name, _ := f.Import()
			if mod != "yolo_vt" || (name != "reply" && name != "row" && name != "event") {
				err = fmt.Errorf("unexpected terminal import %s.%s", mod, name)
				break
			}
		}
		if len(c.ImportedMemories()) != 0 {
			err = errors.New("terminal imports shared memory")
		}
	}
	if err != nil {
		_ = r.Close(ctx)
		return nil, err
	}
	return &compiledCore{r, c}, nil
})

// Terminal owns one isolated core and its VT parser state.
type Terminal struct {
	mod            api.Module
	ctx            context.Context
	ptr, scratch   uint64
	functions      map[string]api.Function
	reply          func([]byte)
	lines          []string
	spans          []SelectionSpan
	palettePairs   []string
	paletteStyle   *strings.Replacer
	passthrough    uint64
	filtering      bool
	filtered       []byte
	bellFilter     uint64
	selection      uint64
	err            error
	hold           bool
	bells          uint64
	history        int // native retention policy, carried by snapshots
	visibleHistory int // local view cap; never changes native retention
	onHold         func(bool)
	anchor         uint64
	anchorScreen   int
	cleanup        runtime.Cleanup
}

func validSize(rows, cols int) bool {
	return rows > 0 && cols > 0 && rows <= 4096 && cols <= 4096 && rows*cols <= 1024*1024
}

// MaxHistoryLines is the native 32-bit line limit. The independent 64 MiB
// history budget still applies when a holder uses this to retain shared history.
const MaxHistoryLines = 1<<32 - 1

func New(rows, cols, history int, reply func([]byte)) (*Terminal, error) {
	if !validSize(rows, cols) || history < 0 || uint64(history) > MaxHistoryLines {
		return nil, errors.New("invalid terminal geometry or history limit")
	}
	t, err := newInstance(reply)
	if err != nil {
		return nil, err
	}
	t.history = history
	t.visibleHistory = history
	t.ptr = t.call("yolo_vt_new", uint64(cols), uint64(rows), uint64(history))
	if t.err != nil || t.ptr == 0 {
		err = t.err
		_ = t.Close()
		if err == nil {
			err = errors.New("allocation failed")
		}
		return nil, fmt.Errorf("initialize terminal core: %w", err)
	}
	return t, nil
}

// A restore gets its own instance, so a failed decode cannot corrupt the live
// terminal or its local viewport/selection. This allocates no native terminal.
func newInstance(reply func([]byte)) (*Terminal, error) {
	core, err := core()
	if err != nil {
		return nil, fmt.Errorf("compile terminal core: %w", err)
	}
	t := &Terminal{reply: reply, functions: make(map[string]api.Function)}
	t.ctx = context.WithValue(context.Background(), contextKey{}, t)
	t.mod, err = core.runtime.InstantiateModule(t.ctx, core.module, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return nil, fmt.Errorf("instantiate terminal core: %w", err)
	}
	if t.call("yolo_vt_abi") != bridgeABI {
		_ = t.mod.Close(t.ctx)
		return nil, errors.New("terminal bridge ABI mismatch")
	}
	t.scratch = t.call("ghostty_wasm_alloc", scratchSize)
	if t.err != nil || t.scratch == 0 {
		_ = t.mod.Close(t.ctx)
		if t.err == nil {
			t.err = errors.New("allocation failed")
		}
		return nil, fmt.Errorf("initialize terminal core: %v", t.err)
	}
	// Owners close explicitly. The cleanup also releases abandoned test instances
	// and constructor failures held by the shared runtime's module registry.
	t.cleanup = runtime.AddCleanup(t, func(m api.Module) { _ = m.Close(context.Background()) }, t.mod)
	return t, nil
}

func (t *Terminal) call(name string, args ...uint64) uint64 {
	if t.err != nil {
		return 0
	}
	f := t.functions[name]
	if f == nil {
		f = t.mod.ExportedFunction(name)
		if f == nil {
			t.err = fmt.Errorf("terminal bridge missing %s", name)
			return 0
		}
		t.functions[name] = f
	}
	v, err := f.Call(t.ctx, args...)
	if err != nil {
		t.err = fmt.Errorf("terminal %s: %w", name, err)
		return 0
	}
	if len(v) == 0 {
		return 0
	}
	return v[0]
}

func (t *Terminal) Err() error { return t.err }

func (t *Terminal) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 && t.err == nil {
		chunk := min(len(p), scratchSize)
		if !t.mod.Memory().Write(uint32(t.scratch), p[:chunk]) {
			t.err = errors.New("terminal input outside scratch memory")
			break
		}
		t.call("ghostty_terminal_vt_write", t.ptr, t.scratch, uint64(chunk))
		if t.err != nil {
			break
		}
		n += chunk
		p = p[chunk:]
	}
	return n, t.err
}

func (t *Terminal) Resize(rows, cols int) error {
	g := t.Geometry()
	g.Rows, g.Cols = rows, cols
	return t.ResizeGeometry(g)
}

func (t *Terminal) Reset() {
	t.ScrollToBottom()
	t.call("ghostty_terminal_reset", t.ptr)
}
func (t *Terminal) Get(key uint32) int {
	value := int(t.call("yolo_vt_get", t.ptr, uint64(key)))
	if key == 9 {
		return min(value, t.visibleHistory)
	}
	return value
}
func (t *Terminal) Mode(mode uint16) bool { return t.call("yolo_vt_mode", t.ptr, uint64(mode)) != 0 }
func (t *Terminal) Holding() bool         { return t.hold }
func (t *Terminal) Bells() uint64         { return t.bells }

func (t *Terminal) SetColor(slot int, value color.Color) {
	var rgb uint64
	if value != nil {
		r, g, b, _ := value.RGBA()
		rgb = 1<<24 | uint64(r>>8)<<16 | uint64(g>>8)<<8 | uint64(b>>8)
	}
	t.call("yolo_vt_color", t.ptr, uint64(slot), rgb)
}

func (t *Terminal) Title() string {
	n := t.call("yolo_vt_title", t.ptr, t.scratch, scratchSize)
	p, ok := t.mod.Memory().Read(uint32(t.scratch), uint32(n))
	if !ok {
		return ""
	}
	return string(p)
}

func (t *Terminal) Lines(history, plain bool, start, count int) ([]string, error) {
	limit := t.Get(2)
	if history {
		limit = t.Get(9)
	}
	if start < 0 || count < 0 || start > limit || count > limit-start {
		return nil, errors.New("terminal frame outside grid")
	}
	t.lines = make([]string, count)
	t.spans = make([]SelectionSpan, count)
	t.palettePairs, t.paletteStyle = nil, nil
	var h, p uint64
	if history {
		h = 1
		start += t.historyBase()
	}
	if plain {
		p = 1
	}
	status := t.call("yolo_vt_rows", t.ptr, h, uint64(start), uint64(count), p)
	lines := t.lines
	t.lines = nil
	if t.err != nil {
		return nil, t.err
	}
	if status != 0 {
		return nil, fmt.Errorf("terminal frame failed (%d)", status)
	}
	for i := range lines {
		lines[i] = strings.TrimSuffix(strings.TrimSuffix(lines[i], "\n"), "\r")
		if !plain {
			lines[i] = "\x1b[0m" + lines[i] + "\x1b[0m\x1b]8;;\x1b\\"
		}
	}
	return lines, nil
}

func (t *Terminal) Close() error {
	t.ScrollToBottom()
	t.cleanup.Stop()
	err := t.mod.Close(context.Background())
	if t.err == nil {
		t.err = errors.New("terminal closed")
	}
	return err
}
