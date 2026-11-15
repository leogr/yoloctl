package spaceconfig

import "github.com/leogr/yoloctl/internal/storagegc"

// StorageSettings is VM-native desired state, never host boot authority.
type StorageSettings struct {
	BuildReserveMax *string `yaml:"build_reserve_max,omitempty" json:"build_reserve_max,omitempty"`
}

func (s *StorageSettings) BuildReserve() (storagegc.Reserve, error) {
	if s == nil {
		return storagegc.ParseReserve(nil)
	}
	return storagegc.ParseReserve(s.BuildReserveMax)
}
