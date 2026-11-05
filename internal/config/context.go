package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"gopkg.in/yaml.v3"
)

// DefaultSpaceName is the literal space-name fallback when neither a
// --space flag nor a context pin is set. Exported so the CLI can use
// it as the Cobra flag default.
const DefaultSpaceName = "default"

const (
	contextFileMode os.FileMode = 0o644
	contextDirMode  os.FileMode = 0o700
)

// Context is the persistent "current" pin: an optional Space that fills
// in the implicit target when the user omits --space. A missing Space
// falls through to the literal "default".
type Context struct {
	// path is the absolute path of the YAML file backing this Context.
	// Empty for in-memory Contexts that have not been saved yet.
	path string

	Space string `yaml:"space,omitempty"`
}

// LoadContext reads the context file. Returns an empty Context (no
// error) if the file doesn't exist — "no pin" is the normal startup
// state, and treating it as an error would force every CLI command to
// special-case fs.ErrNotExist.
func LoadContext() (*Context, error) {
	path, err := layout.ContextPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Context{path: path}, nil
	}
	if err != nil {
		return nil, err
	}
	c := &Context{path: path}
	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	return c, nil
}

// Path returns the YAML file path backing this Context.
func (c *Context) Path() string { return c.path }

// Save writes the Context to its file atomically (temp + rename). If the
// space pin is empty, removes the file instead — there's no point keeping
// an empty pin file around, and `yoloctl space use --reset` should leave
// a clean state.
func (c *Context) Save() error {
	path := c.path
	if path == "" {
		resolved, err := layout.ContextPath()
		if err != nil {
			return err
		}
		path = resolved
	}
	if c.Space != "" {
		if err := layout.ValidateExistingSpaceName(c.Space); err != nil {
			return err
		}
	}
	if c.Space == "" {
		err := os.Remove(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		c.path = path
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), contextDirMode); err != nil {
		return err
	}
	raw, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(path, raw, contextFileMode); err != nil {
		return err
	}
	c.path = path
	return nil
}

// ResolutionSource annotates where a resolved name came from. Used for
// the `yoloctl space use` print and the cwd-overridden-by-flag stderr note.
type ResolutionSource string

const (
	SourceFlag    ResolutionSource = "flag"    // explicit --space
	SourceContext ResolutionSource = "context" // ~/.config/yoloctl/context.yaml pin
	SourceDefault ResolutionSource = "default" // hardcoded fallback ("default")
)

// ResolveSpace applies the space resolution order:
//
//	flag > context pin > "default"
//
// flagValue is the raw --space string; flagChanged is true if the user
// actually passed --space (Cobra's Flag.Changed). Returns the resolved
// name plus a source annotation.
//
// Validates whatever name lands so callers can trust it for shell /
// VM-path interpolation without a second pass.
func ResolveSpace(flagValue string, flagChanged bool) (string, ResolutionSource, error) {
	if flagChanged {
		if err := layout.ValidateExistingSpaceName(flagValue); err != nil {
			return "", "", err
		}
		return flagValue, SourceFlag, nil
	}
	ctx, err := LoadContext()
	if err != nil {
		return "", "", err
	}
	if ctx.Space != "" {
		return ctx.Space, SourceContext, nil
	}
	return DefaultSpaceName, SourceDefault, nil
}
