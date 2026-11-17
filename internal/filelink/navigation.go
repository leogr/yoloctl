// Package filelink owns pinned local Markdown navigation, separate from browsers.
package filelink

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/leogr/yoloctl/internal/layout"
)

const MaxTargets = 64

type Context struct{ Mode, Space, Base, Root string }
type Target struct{ Mode, Space, Path, Root string }

func Within(path, root string) bool {
	if !filepath.IsAbs(path) || !filepath.IsAbs(root) {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func CleanAbsolute(s string) bool {
	return filepath.IsAbs(s) && filepath.Clean(s) == s && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) || r == '\\' })
}
func (c Context) Validate() error {
	if c.Mode != "host" && c.Mode != "vm" || c.Mode == "host" && c.Space != "" || c.Mode == "vm" && layout.ValidateExistingSpaceName(c.Space) != nil || !CleanAbsolute(c.Root) || !CleanAbsolute(c.Base) || !Within(c.Base, c.Root) {
		return errors.New("local file navigation has no pinned workdir")
	}
	return nil
}
func ForFile(mode, space, path, workdir, root string) (Context, error) {
	if root == "" {
		root = filepath.Dir(path)
		if Within(path, workdir) {
			root = workdir
		}
	}
	c := Context{Mode: mode, Space: space, Base: filepath.Dir(path), Root: root}
	if err := c.Validate(); err != nil {
		return c, err
	}
	if !Within(path, root) {
		return c, errors.New("file is outside its navigation root")
	}
	return c, nil
}
func (c Context) Resolve(destination string) (Target, error) {
	if err := c.Validate(); err != nil {
		return Target{}, err
	}
	u, err := url.Parse(destination)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery {
		return Target{}, errors.New("not a relative file destination")
	}
	// url.Parse decodes Path exactly once. Apply every check after decoding too.
	p := u.Path
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || strings.ContainsAny(p, "\\:") || strings.ContainsFunc(p, unicode.IsControl) {
		return Target{}, errors.New("not a relative file destination")
	}
	path := filepath.Clean(filepath.Join(c.Base, p))
	if !Within(path, c.Root) {
		return Target{}, errors.New("file link leaves its navigation root")
	}
	return Target{Mode: c.Mode, Space: c.Space, Path: path, Root: c.Root}, nil
}
func (c Context) Accept(t Target) bool {
	return c.Validate() == nil && t.Mode == c.Mode && t.Space == c.Space && t.Root == c.Root && CleanAbsolute(t.Path) && Within(t.Path, c.Root)
}
func HTTP(s string) bool {
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && !strings.ContainsFunc(s, unicode.IsControl)
}

// Probe opens regular leaves without following symlinks. It runs only in owned
// background work, coalesced into a finite batch and rechecked on activation.
type Probe func(context.Context, []Target) map[Target]error
type Navigator struct {
	Context Context
	Probe   Probe
}

func (n *Navigator) Check(ctx context.Context, t Target) error {
	if n == nil || !n.Context.Accept(t) || n.Probe == nil {
		return errors.New("local file navigation unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	results := n.Probe(ctx, []Target{t})
	err, ok := results[t]
	if !ok {
		return errors.New("file readability was not measured")
	}
	return err
}

type Admissions map[string]Target

// AdmitDestinations probes only explicit destinations visible in a native view.
// Unknown/inert action metadata is collection data, never opener authority.
func (n *Navigator) AdmitDestinations(ctx context.Context, destinations []string) Admissions {
	admitted := Admissions{}
	if n == nil || n.Probe == nil || n.Context.Validate() != nil {
		return admitted
	}
	byDestination := map[string]Target{}
	unique := map[Target]bool{}
	var targets []Target
	for _, s := range destinations {
		t, e := n.Context.Resolve(s)
		if e != nil {
			continue
		}
		if len(byDestination) >= MaxTargets {
			break
		}
		byDestination[s] = t
		if !unique[t] {
			unique[t] = true
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		return admitted
	}
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	results := n.Probe(ctx, targets)
	for s, t := range byDestination {
		if e, ok := results[t]; ok && e == nil && ctx.Err() == nil {
			admitted[s] = t
		}
	}
	return admitted
}
