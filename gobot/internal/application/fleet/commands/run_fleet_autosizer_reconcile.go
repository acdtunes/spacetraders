package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// autosizerRunConfig is the launch command with every default resolved, so the reconcile logic
// never repeats the "<= 0 → default" fallback (RULINGS #5, the siting resolveConfig idiom). It
// holds ALL knobs (not just the ones M1 reads) so resolveFleetAutosizerConfig is written once and
// the later-milestone guard/demand math reads resolved values directly.
type autosizerRunConfig struct {
	Disabled              bool
	DryRun                bool
	LightsDisabled        bool
	HeaviesDisabled       bool
	WarehouseHullsEnabled bool

	Tick               time.Duration
	PurchaseCapPerTick int

	FleetCeilingTotal     int
	FleetCeilingLights    int
	FleetCeilingHeavies   int
	FleetCeilingWarehouse int

	PurchaseMarginOverFloor int64
	Reserve                 int64
	ReserveTreasuryPct      int

	LightRotationSlots float64

	HeavyMarginalRateFloor      float64
	HeavyUnservedLanesMin       int
	HeavyTreasuryPctPerPurchase int

	APIUtilizationCeilingPct int

	PaybackSafetyFactor      float64
	PurchaseCutoffAtEraMinus time.Duration

	MaxPriceLights            int64
	MaxPriceHeavies           int64
	MaxPremiumOverCheapestPct int
	PreferDemandProximalYard  bool

	ShipTypeLights  string
	ShipTypeHeavies string

	ZeroEffectAlarmTicks int

	WarehouseMinChainRealizedPerHour float64
	WarehouseMinChainTickPersistence int
	MaxWarehouseHulls                int
	StockerHullsPerWarehouseGroup    int
	WarehouseCapacityTargetHours     float64
	MaxModuleSpendPerHull            int64
	WarehouseFrameClassCeiling       string
}

func resolveFleetAutosizerConfig(cmd *RunFleetAutosizerCoordinatorCommand) autosizerRunConfig {
	c := autosizerRunConfig{
		Disabled:                         cmd.Disabled,
		DryRun:                           cmd.DryRun,
		LightsDisabled:                   cmd.LightsDisabled,
		HeaviesDisabled:                  cmd.HeaviesDisabled,
		WarehouseHullsEnabled:            cmd.WarehouseHullsEnabled,
		Tick:                             time.Duration(cmd.TickIntervalSecs) * time.Second,
		PurchaseCapPerTick:               cmd.PurchaseCapPerTick,
		FleetCeilingTotal:                cmd.FleetCeilingTotal,
		FleetCeilingLights:               cmd.FleetCeilingLights,
		FleetCeilingHeavies:              cmd.FleetCeilingHeavies,
		FleetCeilingWarehouse:            cmd.FleetCeilingWarehouse,
		PurchaseMarginOverFloor:          cmd.PurchaseMarginOverFloor,
		Reserve:                          cmd.Reserve,
		ReserveTreasuryPct:               cmd.ReserveTreasuryPct,
		LightRotationSlots:               cmd.LightRotationSlots,
		HeavyMarginalRateFloor:           cmd.HeavyMarginalRateFloor,
		HeavyUnservedLanesMin:            cmd.HeavyUnservedLanesMin,
		HeavyTreasuryPctPerPurchase:      cmd.HeavyTreasuryPctPerPurchase,
		APIUtilizationCeilingPct:         cmd.APIUtilizationCeilingPct,
		PaybackSafetyFactor:              cmd.PaybackSafetyFactor,
		PurchaseCutoffAtEraMinus:         time.Duration(cmd.PurchaseCutoffAtEraMinusHours * float64(time.Hour)),
		MaxPriceLights:                   cmd.MaxPriceLights,
		MaxPriceHeavies:                  cmd.MaxPriceHeavies,
		MaxPremiumOverCheapestPct:        cmd.MaxPremiumOverCheapestPct,
		ShipTypeLights:                   cmd.ShipTypeLights,
		ShipTypeHeavies:                  cmd.ShipTypeHeavies,
		ZeroEffectAlarmTicks:             cmd.ZeroEffectAlarmTicks,
		WarehouseMinChainRealizedPerHour: cmd.WarehouseMinChainRealizedPerHour,
		WarehouseMinChainTickPersistence: cmd.WarehouseMinChainTickPersistence,
		MaxWarehouseHulls:                cmd.MaxWarehouseHulls,
		StockerHullsPerWarehouseGroup:    cmd.StockerHullsPerWarehouseGroup,
		WarehouseCapacityTargetHours:     cmd.WarehouseCapacityTargetHours,
		MaxModuleSpendPerHull:            cmd.MaxModuleSpendPerHull,
		WarehouseFrameClassCeiling:       cmd.WarehouseFrameClassCeiling,
	}

	if c.Tick <= 0 {
		c.Tick = defaultAutosizerTickSeconds * time.Second
	}
	if c.PurchaseCapPerTick <= 0 {
		c.PurchaseCapPerTick = defaultPurchaseCapPerTick
	}
	if c.FleetCeilingTotal <= 0 {
		c.FleetCeilingTotal = defaultFleetCeilingTotal
	}
	if c.FleetCeilingLights <= 0 {
		c.FleetCeilingLights = defaultFleetCeilingLights
	}
	if c.FleetCeilingHeavies <= 0 {
		c.FleetCeilingHeavies = defaultFleetCeilingHeavies
	}
	if c.FleetCeilingWarehouse <= 0 {
		c.FleetCeilingWarehouse = defaultFleetCeilingWarehouse
	}
	if c.MaxWarehouseHulls <= 0 {
		c.MaxWarehouseHulls = c.FleetCeilingWarehouse
	}
	if c.PurchaseMarginOverFloor <= 0 {
		c.PurchaseMarginOverFloor = defaultPurchaseMarginOverFloor
	}
	if c.LightRotationSlots <= 0 {
		c.LightRotationSlots = defaultLightRotationSlots
	}
	if c.HeavyMarginalRateFloor <= 0 {
		c.HeavyMarginalRateFloor = defaultHeavyMarginalRateFloor
	}
	if c.HeavyUnservedLanesMin <= 0 {
		c.HeavyUnservedLanesMin = defaultHeavyUnservedLanesMin
	}
	if c.HeavyTreasuryPctPerPurchase <= 0 {
		c.HeavyTreasuryPctPerPurchase = defaultHeavyTreasuryPctPerPurchase
	}
	if c.APIUtilizationCeilingPct <= 0 {
		c.APIUtilizationCeilingPct = defaultAPIUtilCeilingPct
	}
	if c.PaybackSafetyFactor <= 0 {
		c.PaybackSafetyFactor = defaultPaybackSafetyFactor
	}
	if c.PurchaseCutoffAtEraMinus <= 0 {
		c.PurchaseCutoffAtEraMinus = time.Duration(defaultPurchaseCutoffEraMinusHours * float64(time.Hour))
	}
	if c.MaxPremiumOverCheapestPct <= 0 {
		c.MaxPremiumOverCheapestPct = defaultMaxPremiumOverCheapestPct
	}
	if c.ShipTypeLights == "" {
		c.ShipTypeLights = defaultShipTypeLights
	}
	if c.ShipTypeHeavies == "" {
		c.ShipTypeHeavies = defaultShipTypeHeavies
	}
	if c.ZeroEffectAlarmTicks <= 0 {
		c.ZeroEffectAlarmTicks = defaultZeroEffectAlarmTicks
	}
	if c.WarehouseMinChainTickPersistence <= 0 {
		c.WarehouseMinChainTickPersistence = defaultWarehouseMinChainTickPersistence
	}
	if c.WarehouseCapacityTargetHours <= 0 {
		c.WarehouseCapacityTargetHours = defaultWarehouseCapacityTargetHours
	}
	if c.WarehouseFrameClassCeiling == "" {
		c.WarehouseFrameClassCeiling = defaultWarehouseFrameClassCeiling
	}
	// PreferDemandProximalYard defaults TRUE: nil (unset) → true; the *bool distinguishes an
	// explicit false from "not configured".
	c.PreferDemandProximalYard = true
	if cmd.PreferDemandProximalYard != nil {
		c.PreferDemandProximalYard = *cmd.PreferDemandProximalYard
	}
	return c
}

// reconcileResult tallies one tick's effect for the zero-effect alarm and metrics.
type reconcileResult struct {
	ClassesEvaluated int
	ShortfallClasses int
	Purchased        int
}

// reconcileOnce runs one full sizing pass: read the tick's shared inputs once, then for every
// enabled class read demand and buy the shortfall through the fail-closed guard stack (bounded by
// the per-tick cap, accounting each in-tick buy against the total so the next class sees the
// updated fleet size). It is the unit the tests drive directly; Handle just calls it on the tick.
func (h *RunFleetAutosizerCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunFleetAutosizerCoordinatorCommand) (reconcileResult, error) {
	cfg := resolveFleetAutosizerConfig(cmd)
	logger := common.LoggerFromContext(ctx)
	res := reconcileResult{}

	// Master boot-gate (RULINGS #5): the container stays resident when disabled so a config flip
	// + restart re-arms it with no manual relaunch, but it takes no action while stood down.
	if cfg.Disabled {
		return res, nil
	}

	// No-silent-dry-run (sp-1txd guard): dry-run WARNs every tick — it is opt-in watch mode, not
	// a silent no-op (the f5pr lesson: a coordinator sat in silent dry-run for a day).
	if cfg.DryRun {
		logger.Log("WARN", "Fleet autosizer in DRY-RUN — every buy decision is evaluated and logged but NOTHING is spent (set dry_run=false to arm)", map[string]interface{}{
			"action":       "autosizer_dry_run",
			"container_id": cmd.ContainerID,
		})
	}

	st := h.coordinatorState(cmd.ContainerID)
	in := h.readTickInputs(ctx, cmd.PlayerID)

	purchasesThisTick := 0
	anyUnmetNoBuy := false

	for _, p := range h.providers {
		class := p.Class()
		if cfg.classDisabled(class) {
			continue
		}
		d, err := p.Demand(ctx, cmd.PlayerID, DemandParams{LightRotationSlots: cfg.LightRotationSlots})
		if err != nil {
			// An infra fault reading one class must not abort the whole tick — log and move on;
			// the class simply does not size this pass (fail-safe: no buy).
			logger.Log("ERROR", fmt.Sprintf("Autosizer %s demand read failed: %v", class, err), map[string]interface{}{
				"action":       "autosizer_demand_error",
				"container_id": cmd.ContainerID,
				"class":        string(class),
			})
			continue
		}
		res.ClassesEvaluated++
		if d.Readable && d.Shortfall() > 0 {
			res.ShortfallClasses++
		}

		bought, unmetNoBuy := h.sizeClass(ctx, cmd, cfg, d, in, st, purchasesThisTick)
		if bought {
			purchasesThisTick++
			res.Purchased++
			in.totalHulls++ // account for the in-tick buy so the next class sees the updated total
		}
		if unmetNoBuy {
			anyUnmetNoBuy = true
		}
	}

	// Zero-effect alarm (no-silent-dry-run corollary): demand persisted but nothing was bought.
	h.runZeroEffectAlarm(ctx, cmd, cfg, st, anyUnmetNoBuy, res.Purchased)

	logger.Log("INFO", fmt.Sprintf("Autosizer tick: %d classes evaluated, %d with shortfall, %d purchased", res.ClassesEvaluated, res.ShortfallClasses, res.Purchased), map[string]interface{}{
		"action":            "autosizer_tick",
		"container_id":      cmd.ContainerID,
		"classes_evaluated": res.ClassesEvaluated,
		"shortfall_classes": res.ShortfallClasses,
		"purchased":         res.Purchased,
	})
	return res, nil
}

// runZeroEffectAlarm raises ONE edge-triggered WARN when demand has persisted for
// zero_effect_alarm_ticks consecutive ticks with zero purchases — the mechanized f5pr silent-dry
// -run lesson. A purchase (or a tick with no demand pressure at all) resets the streak and re-arms
// the alarm for the next episode.
func (h *RunFleetAutosizerCoordinatorHandler) runZeroEffectAlarm(ctx context.Context, cmd *RunFleetAutosizerCoordinatorCommand, cfg autosizerRunConfig, st *autosizerState, anyUnmetNoBuy bool, purchased int) {
	logger := common.LoggerFromContext(ctx)
	if purchased > 0 || !anyUnmetNoBuy {
		st.noEffectStreak = 0
		st.noEffectPaged = false
		return
	}
	st.noEffectStreak++
	if st.noEffectStreak >= cfg.ZeroEffectAlarmTicks && !st.noEffectPaged {
		st.noEffectPaged = true
		if h.metrics != nil {
			h.metrics.RecordZeroEffectAlarm()
		}
		logger.Log("WARN", fmt.Sprintf("Autosizer ZERO-EFFECT ALARM: unmet demand has produced NO purchase for %d consecutive ticks — a guard is persistently blocking (see the per-decision arithmetic above) or the purchaser is unwired/dry-run", st.noEffectStreak), map[string]interface{}{
			"action":       "autosizer_zero_effect_alarm",
			"container_id": cmd.ContainerID,
			"streak":       st.noEffectStreak,
		})
	}
}
