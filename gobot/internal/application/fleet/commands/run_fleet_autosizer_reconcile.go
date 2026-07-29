package commands

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// autosizerRunConfig is the launch command with every default resolved, so the reconcile logic
// never repeats the "<= 0 → default" fallback (RULINGS #5, the siting resolveConfig idiom). It
// holds ALL knobs so resolveFleetAutosizerConfig is written once and the guard/demand math reads
// resolved values directly.
type autosizerRunConfig struct {
	Tick               time.Duration
	PurchaseCapPerTick int

	FleetCeilingLights  int
	FleetCeilingHeavies int
	// HeavyCap is the resolved heavy-HULL cap (capital exposure), distinct from
	// FleetCeilingHeavies (trade-pool size). Both bind.
	HeavyCap int

	PurchaseMarginOverFloor int64

	LightRotationSlots float64

	HeavyUnservedLanesMin       int
	HeavyTreasuryPctPerPurchase int

	APIUtilizationCeilingPct int

	MaxPriceLights            int64
	MaxPriceHeavies           int64
	MaxPremiumOverCheapestPct int
	PreferDemandProximalYard  bool

	ShipTypeLights  string
	ShipTypeHeavies string

	ZeroEffectAlarmTicks int

	// Explorer class.
	ExplorerHullsEnabled           bool
	FleetCeilingExplorer           int
	ExplorerTreasuryPctPerPurchase int
	MaxPriceExplorer               int64
	ShipTypeExplorer               string

	// sp-y2ptq: the contract-delivery class fields were removed (dedicated scaler owns contract capacity).
}

func resolveFleetAutosizerConfig(cmd *RunFleetAutosizerCoordinatorCommand) autosizerRunConfig {
	c := autosizerRunConfig{
		Tick:                        time.Duration(cmd.TickIntervalSecs) * time.Second,
		PurchaseCapPerTick:          cmd.PurchaseCapPerTick,
		FleetCeilingLights:          cmd.FleetCeilingLights,
		FleetCeilingHeavies:         cmd.FleetCeilingHeavies,
		PurchaseMarginOverFloor:     cmd.PurchaseMarginOverFloor,
		LightRotationSlots:          cmd.LightRotationSlots,
		HeavyUnservedLanesMin:       cmd.HeavyUnservedLanesMin,
		HeavyTreasuryPctPerPurchase: cmd.HeavyTreasuryPctPerPurchase,
		HeavyCap:                    resolveHeavyCap(cmd.HeavyCap),
		APIUtilizationCeilingPct:    cmd.APIUtilizationCeilingPct,
		MaxPriceLights:              cmd.MaxPriceLights,
		MaxPriceHeavies:             cmd.MaxPriceHeavies,
		MaxPremiumOverCheapestPct:   cmd.MaxPremiumOverCheapestPct,
		ShipTypeLights:              cmd.ShipTypeLights,
		ShipTypeHeavies:             cmd.ShipTypeHeavies,
		ZeroEffectAlarmTicks:        cmd.ZeroEffectAlarmTicks,

		ExplorerHullsEnabled:           cmd.ExplorerHullsEnabled,
		FleetCeilingExplorer:           cmd.FleetCeilingExplorer,
		ExplorerTreasuryPctPerPurchase: cmd.ExplorerTreasuryPctPerPurchase,
		MaxPriceExplorer:               cmd.MaxPriceExplorer,
		ShipTypeExplorer:               cmd.ShipTypeExplorer,
	}

	if c.Tick <= 0 {
		c.Tick = defaultAutosizerTickSeconds * time.Second
	}
	if c.PurchaseCapPerTick <= 0 {
		c.PurchaseCapPerTick = defaultPurchaseCapPerTick
	}
	if c.FleetCeilingLights <= 0 {
		c.FleetCeilingLights = defaultFleetCeilingLights
	}
	if c.FleetCeilingHeavies <= 0 {
		c.FleetCeilingHeavies = defaultFleetCeilingHeavies
	}
	if c.PurchaseMarginOverFloor <= 0 {
		c.PurchaseMarginOverFloor = defaultPurchaseMarginOverFloor
	}
	if c.LightRotationSlots <= 0 {
		c.LightRotationSlots = defaultLightRotationSlots
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
	// Explorer defaults. ExplorerHullsEnabled has NO fallback — its false zero value IS the
	// default (disarmed), so nothing boot-arms it. MaxPriceExplorer resolves to a REAL default (never
	// 0=off, unlike MaxPrice{Lights,Heavies}) because the explorer's price ceiling is a required guard.
	if c.FleetCeilingExplorer <= 0 {
		c.FleetCeilingExplorer = defaultFleetCeilingExplorer
	}
	if c.ExplorerTreasuryPctPerPurchase <= 0 {
		c.ExplorerTreasuryPctPerPurchase = defaultExplorerTreasuryPctPerPurchase
	}
	if c.MaxPriceExplorer <= 0 {
		c.MaxPriceExplorer = defaultMaxPriceExplorer
	}
	if c.ShipTypeExplorer == "" {
		c.ShipTypeExplorer = defaultShipTypeExplorer
	}
	// sp-y2ptq: the contract-delivery class default resolution was removed with the class.
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
// the per-tick cap). It is the unit the tests drive directly; Handle just calls it on the tick.
func (h *RunFleetAutosizerCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunFleetAutosizerCoordinatorCommand) (reconcileResult, error) {
	cfg := resolveFleetAutosizerConfig(cmd)
	// The ONE live-tunable knob: re-read from persisted config each tick so a `tune` applies
	// on the next reconcile with no container rebuild.
	cfg.HeavyCap = h.liveHeavyCap(ctx, cmd, cfg.HeavyCap)
	logger := common.LoggerFromContext(ctx)
	res := reconcileResult{}

	st := h.coordinatorState(cmd.ContainerID)
	in := h.readTickInputs(ctx, cmd.PlayerID, cfg)
	// Emitted every tick regardless of outcome: this is the series that distinguishes
	// "saving for a heavy" from "the buyer is stuck", and it is only useful if it is
	// always present.
	if h.metrics != nil {
		h.metrics.RecordHeavyReserve(strconv.Itoa(cmd.PlayerID), in.heavyReserve, in.heaviesOwned, cfg.HeavyCap)
	}

	// The heavy-yard PRICING ERRAND, run BEFORE any class sizes.
	//
	// It is deliberately not part of the class loop: it is not a purchase and has no demand
	// signal, it spends no credits, and its whole job is to make a LATER tick's price readable.
	// Running it first also means a tick that dispatches an errand still evaluates every class
	// normally — the errand never displaces a buy that could happen today.
	h.runHeavyPricingErrand(ctx, cmd, cfg, in)

	// The live-resolved params every provider reads this tick (the live-config discipline): the
	// providers are constructed once at boot but see the current config.yaml value through here.
	params := DemandParams{
		LightRotationSlots:   cfg.LightRotationSlots,
		ExplorerHullsEnabled: cfg.ExplorerHullsEnabled,
		MaxExplorerHulls:     cfg.FleetCeilingExplorer,
	}

	// sp-y2ptq: the contract-delivery graduation gate was removed with the autosizer's contract class
	// (the dedicated scaler owns contract capacity and carries its own graduation handling).

	purchasesThisTick := 0
	anyUnmetNoBuy := false

	for _, p := range h.providers {
		class := p.Class()
		if cfg.classDisabled(class) {
			continue
		}
		d, err := p.Demand(ctx, cmd.PlayerID, params)
		if err != nil {
			// An infra fault reading one class must not abort the whole tick — log and move on;
			// the class simply does not size this pass (fail-safe: no buy).
			logger.Log("ERROR", fmt.Sprintf("Autosizer %s demand read failed: %v", class, err), map[string]interface{}{
				"action":       "autosizer_demand_error",
				"container_id": cmd.ContainerID,
				"class":        string(class),
			})
			// A class that could not even read its demand did NOT get to decide: BLOCKED, not
			// idle. Reported before the continue so a permanently broken provider escalates
			// instead of reading as a fleet with nothing to buy.
			h.observeClassStall(ctx, cmd, class, classStallVerdict(d, err, false, false, "", false))
			continue
		}
		res.ClassesEvaluated++
		if d.Readable && d.Shortfall() > 0 {
			res.ShortfallClasses++
		}

		// Claim this class's tap slot so the blocking guard the ACT step publishes on the
		// metrics seam can be named in this tick's stall verdict. Pure observation: the claim
		// touches no port and no guard.
		h.expectBlockedGuard(cmd, class)
		bought, unmetNoBuy := h.sizeClass(ctx, cmd, cfg, d, in, st, purchasesThisTick)
		guard, guardKnown := h.takeBlockedGuard(cmd, class)
		h.observeClassStall(ctx, cmd, class, classStallVerdict(d, nil, bought, unmetNoBuy, guard, guardKnown))
		if bought {
			purchasesThisTick++
			res.Purchased++
		}
		if unmetNoBuy {
			anyUnmetNoBuy = true
		}
	}

	// Zero-effect alarm: demand persisted but nothing was bought.
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
// zero_effect_alarm_ticks consecutive ticks with zero purchases. A purchase (or a tick with no
// demand pressure at all) resets the streak and re-arms the alarm for the next episode.
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
		logger.Log("WARN", fmt.Sprintf("Autosizer ZERO-EFFECT ALARM: unmet demand has produced NO purchase for %d consecutive ticks — a guard is persistently blocking (see the per-decision arithmetic above) or the purchaser is unwired", st.noEffectStreak), map[string]interface{}{
			"action":       "autosizer_zero_effect_alarm",
			"container_id": cmd.ContainerID,
			"streak":       st.noEffectStreak,
		})
	}
}

// resolveHeavyCap applies the heavy cap's pointer semantics: nil (absent) defers to the
// documented default, while an explicit value — INCLUDING 0 — is the operator's choice.
// 0 is a legitimate hold ("own no heavies"), not an unset knob, which is exactly why the
// field is a *int rather than following the codebase's usual "<=0 ⇒ default" resolve.
//
// A negative value is nonsense rather than a hold; it resolves to the default so a typo
// cannot silently disable heavy buying in a way that reads as intentional.
func resolveHeavyCap(configured *int) int {
	if configured == nil {
		return defaultHeavyCap
	}
	if *configured < 0 {
		return defaultHeavyCap
	}
	return *configured
}
