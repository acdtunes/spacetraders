package autooutfit

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	domainOutfit "github.com/andrescamacho/spacetraders-go/internal/domain/outfitting"
)

// autoOutfitConfig is the launch command with every default resolved and, when a live
// snapshot exists, the tunable knobs overlaid.
type autoOutfitConfig struct {
	Selection              domainOutfit.SelectionConfig
	PriceCeiling           int
	MaxInstallsPerTick     int
	TreasuryReserve        int
	MaxTreasuryFractionPct int
	TelemetryWindow        time.Duration
	WantedModules          []string
}

// AutoOutfitTunableDefaults maps every LIVE-tunable auto-outfit knob to its documented
// default — the value that applies when neither the live container config nor the launch
// command carries a positive one. The daemon's tune bounds registry reads THIS map, so
// the defaults-of-record stay next to the consts they mirror. The KEY SET is also the
// contract for which keys resolveAutoOutfitConfig live-overlays.
//
// payback_horizon_hours defaults to 0 (the absolute payback gate is OFF): without a
// per-hull throughput signal the value-per-hour it needs is not yet measured, so the
// gate stays disabled until throughput is wired; the relative new-hull gate and the
// spend guards are the active payback protection meanwhile.
func AutoOutfitTunableDefaults() map[string]int {
	return map[string]int{
		"min_telemetry_samples":     defaultMinTelemetrySamples,
		"price_ceiling":             defaultPriceCeiling,
		"max_installs_per_tick":     defaultMaxInstallsPerTick,
		"payback_horizon_hours":     defaultPaybackHorizonHours,
		"treasury_reserve":          defaultTreasuryReserve,
		"max_treasury_fraction_pct": defaultMaxTreasuryFractionPct,
	}
}

// resolveAutoOutfitConfig resolves one tick's effective config. live is the tick-start
// snapshot of the container's persisted config column (nil when unwired/unreadable). For
// the TUNABLE knobs a non-nil snapshot is AUTHORITATIVE (a positive value is the live
// value; an absent/zeroed key means the documented default — the `tune <key> 0` revert).
// The non-tunable knobs (fee/hop cost model, telemetry window, wanted set) always resolve
// from the launch command.
func resolveAutoOutfitConfig(cmd *RunAutoOutfitCoordinatorCommand, live liveconfig.Snapshot) autoOutfitConfig {
	c := autoOutfitConfig{
		Selection: domainOutfit.SelectionConfig{
			MinTelemetrySamples: cmd.MinTelemetrySamples,
			InstallFeeEstimate:  cmd.InstallFeeEstimate,
			HopCost:             cmd.HopCost,
			PaybackHorizonHours: float64(cmd.PaybackHorizonHours),
		},
		PriceCeiling:           cmd.PriceCeiling,
		MaxInstallsPerTick:     cmd.MaxInstallsPerTick,
		TreasuryReserve:        cmd.TreasuryReserve,
		MaxTreasuryFractionPct: cmd.MaxTreasuryFractionPct,
		TelemetryWindow:        time.Duration(cmd.TelemetryWindowSecs) * time.Second,
		WantedModules:          cmd.WantedModules,
	}

	if live != nil {
		c.Selection.MinTelemetrySamples = live.PositiveIntOrZero("min_telemetry_samples")
		c.Selection.PaybackHorizonHours = float64(live.PositiveIntOrZero("payback_horizon_hours"))
		c.PriceCeiling = live.PositiveIntOrZero("price_ceiling")
		c.MaxInstallsPerTick = live.PositiveIntOrZero("max_installs_per_tick")
		c.TreasuryReserve = live.PositiveIntOrZero("treasury_reserve")
		c.MaxTreasuryFractionPct = live.PositiveIntOrZero("max_treasury_fraction_pct")
	}

	if c.Selection.MinTelemetrySamples <= 0 {
		c.Selection.MinTelemetrySamples = defaultMinTelemetrySamples
	}
	if c.Selection.InstallFeeEstimate <= 0 {
		c.Selection.InstallFeeEstimate = defaultInstallFeeEstimate
	}
	if c.Selection.HopCost <= 0 {
		c.Selection.HopCost = defaultHopCost
	}
	// PaybackHorizonHours is intentionally NOT forced to a positive default: 0 keeps the
	// absolute payback gate off (see AutoOutfitTunableDefaults).
	if c.PriceCeiling <= 0 {
		c.PriceCeiling = defaultPriceCeiling
	}
	if c.MaxInstallsPerTick <= 0 {
		c.MaxInstallsPerTick = defaultMaxInstallsPerTick
	}
	if c.TreasuryReserve <= 0 {
		c.TreasuryReserve = defaultTreasuryReserve
	}
	if c.MaxTreasuryFractionPct <= 0 {
		c.MaxTreasuryFractionPct = defaultMaxTreasuryFractionPct
	}
	if c.TelemetryWindow <= 0 {
		c.TelemetryWindow = defaultTelemetryWindowSecs * time.Second
	}
	if len(c.WantedModules) == 0 {
		c.WantedModules = defaultWantedModules
	}
	return c
}

// liveConfigSnapshot takes the tick's live-config snapshot. A nil reader (tests, minimal
// boots) or a read error yields nil, which resolveAutoOutfitConfig treats as "run this
// tick on the launch command" — the fail-safe launch behavior, never a half-applied
// config. The read is logged, not fatal.
func (h *RunAutoOutfitCoordinatorHandler) liveConfigSnapshot(ctx context.Context, cmd *RunAutoOutfitCoordinatorCommand) liveconfig.Snapshot {
	if h.liveConfig == nil {
		return nil
	}
	snap, err := h.liveConfig.Snapshot(ctx, cmd.ContainerID, cmd.PlayerID.Value())
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", "Auto-outfit live config unreadable — this tick runs on launch values", nil)
		return nil
	}
	return snap
}
