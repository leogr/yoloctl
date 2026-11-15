package main

import (
	"fmt"

	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/storagegc"
)

type buildReserveRead struct {
	Reserve storagegc.Reserve
	Setting *string
	Err     error
}

var buildReserveConfig = spaceconfig.Load

// An unavailable setting preserves the previous uncapped admission rule.
// Callers disclose the fallback; cleanup policy never reads this setting.
func readBuildReserve(space string) buildReserveRead {
	cfg, err := buildReserveConfig(space)
	if err != nil {
		return buildReserveRead{Err: err}
	}
	reserve, err := cfg.Storage.BuildReserve()
	if err != nil {
		return buildReserveRead{Err: err}
	}
	var value *string
	if cfg.Storage != nil {
		value = cfg.Storage.BuildReserveMax
	}
	return buildReserveRead{Reserve: reserve, Setting: value}
}

func (r buildReserveRead) description() string {
	if r.Err != nil {
		return "storage.build_reserve_max cap could not be read; using uncapped 20%"
	}
	value := "none (20% uncapped)"
	if r.Reserve.Cap != nil {
		value = "10GiB (default)"
	}
	if r.Setting != nil {
		value = *r.Setting
		if value == "none" {
			value += " (20% uncapped)"
		}
	}
	return "storage.build_reserve_max=" + value
}

type storageBuildReserve struct {
	Required     uint64  `json:"required"`
	FractionFree uint64  `json:"fraction_free"`
	Cap          *uint64 `json:"cap"`
}

func (r buildReserveRead) report(d storagegc.Disk) *storageBuildReserve {
	return &storageBuildReserve{Required: r.Reserve.AdmissionFree(d), FractionFree: d.TargetFree(), Cap: r.Reserve.Cap}
}

func describeBuildReserve(r *storageBuildReserve) string {
	if r == nil {
		return "unavailable"
	}
	cap := "none (uncapped)"
	if r.Cap != nil {
		cap = fmt.Sprintf("%.1f GiB", gib(*r.Cap))
	}
	return fmt.Sprintf("%.1f GiB free required (20%% target %.1f GiB; storage.build_reserve_max %s)", gib(r.Required), gib(r.FractionFree), cap)
}
