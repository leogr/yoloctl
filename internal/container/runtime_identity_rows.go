package container

import "errors"

// Copied rows from this ACTUALLY captured complete configured-namespace read.
// No constructor or boolean current/empty-census promise is exposed.
type RuntimeIdentityRow struct{ ContainerID, Name string }

func (p *RuntimeIdentityInventory) Identities() ([]RuntimeIdentityRow, error) {
	if p == nil || !p.captured {
		return nil, errors.New("original runtime identity observation unavailable")
	}
	out := make([]RuntimeIdentityRow, len(p.entries))
	for i, row := range p.entries {
		out[i] = RuntimeIdentityRow{row.id, row.name}
	}
	return out, nil
}
