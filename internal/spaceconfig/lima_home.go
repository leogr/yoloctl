package spaceconfig

import (
	"errors"
	"os"
	"runtime"
	"sort"

	"github.com/leogr/yoloctl/internal/layout"
)

// SelectNewLimaHome is only for creation/import. Never call it while loading,
// starting or recreating an existing space: an absent field means legacy.
func (b *Bootstrap) SelectNewLimaHome() error {
	b.VM.LimaHome = ""
	if runtime.GOOS != "darwin" || b.VM.Backend == "firecracker" {
		return nil
	}
	home, err := layout.NewLimaHome()
	if err != nil {
		return err
	}
	b.VM.LimaHome = home
	return nil
}

// LimaHome resolves this space's recorded host storage before any Lima command.
// Standalone/orphan Lima inspection without a bootstrap retains the old home.
func LimaHome(space string) (string, error) {
	b, err := LoadBootstrap(space)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrLegacySchema) {
		return layout.LimaHome()
	}
	if err != nil {
		return "", err
	}
	return layout.ResolveLimaHome(b.VM.LimaHome)
}

// BootstrapForDisk finds the unique declaring space; nil means standalone
// legacy disk tooling. Unreadable or duplicate ownership must not pick a home.
func BootstrapForDisk(id string) (*Bootstrap, error) {
	names, err := List()
	if err != nil {
		return nil, err
	}
	var found *Bootstrap
	for _, name := range names {
		b, err := LoadBootstrap(name)
		if errors.Is(err, ErrLegacySchema) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if b.DiskID != id {
			continue
		}
		if found != nil {
			return nil, errors.New("data disk declared by multiple spaces")
		}
		found = b
	}
	return found, nil
}

func LimaHomeForDisk(id string) (string, error) {
	b, err := BootstrapForDisk(id)
	if err != nil {
		return "", err
	}
	if b == nil {
		return layout.LimaHome()
	}
	return layout.ResolveLimaHome(b.VM.LimaHome)
}

// LimaHomes inventories legacy, new and explicitly recorded Lima roots. This
// serves both runtime listing and new host-grant exclusions; it creates nothing.
func LimaHomes() ([]string, error) { return limaHomes(true) }

// RecordedLimaHomes is the legacy root plus the roots existing bootstraps
// record: the storage that exists now. Revalidating an existing grant uses it,
// so the unused new-space root never stops an older space from starting.
func RecordedLimaHomes() ([]string, error) { return limaHomes(false) }

func limaHomes(withNew bool) ([]string, error) {
	legacy, err := layout.LimaHome()
	if err != nil {
		return nil, err
	}
	homes := map[string]bool{legacy: true}
	if withNew {
		next, err := layout.NewLimaHome()
		if err != nil {
			return nil, err
		}
		homes[next] = true
	}
	names, err := List()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		b, err := LoadBootstrap(name)
		if errors.Is(err, ErrLegacySchema) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if b.VM.Backend == "firecracker" {
			continue
		}
		home, err := layout.ResolveLimaHome(b.VM.LimaHome)
		if err != nil {
			return nil, err
		}
		homes[home] = true
	}
	out := make([]string, 0, len(homes))
	for home := range homes {
		out = append(out, home)
	}
	sort.Strings(out)
	return out, nil
}
