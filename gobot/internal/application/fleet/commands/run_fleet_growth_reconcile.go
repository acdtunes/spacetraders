package commands

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
)

// growthRunConfig is the launch command with every default resolved, so the reconcile logic never
// repeats the "<= 0 → default" fallback (RULINGS #5, the siting resolveConfig idiom).
type growthRunConfig struct {
	Tick time.Duration

	// HeavyCap is the resolved heavy-HULL cap (capital exposure) — the only count-based bound on
	// the class; every other bound is economic.
	HeavyCap int

	UnservedLanesMin int
	// RunwayMilliHours is the working-capital term's multiplier on the observed cargo outflow.
	RunwayMilliHours int

	PurchaseMarginOverFloor int64
	// TreasuryPctPerPurchase is the single-hull percentage-of-treasury ceiling. It resolves with NO
	// "<= 0 ⇒ default" fallback: 0 leaves the term unapplied, any positive value applies it. A
	// non-zero default would make the ceiling the ONLY reachable state, because `tune <key> 0`
	// deletes a key rather than setting it.
	TreasuryPctPerPurchase int

	APIUtilizationCeilingPct int

	MaxPriceHeavies           int64
	MaxPremiumOverCheapestPct int
	PreferDemandProximalYard  bool

	ShipTypeHeavies string

	ZeroEffectAlarmTicks int
}

func resolveFleetGrowthConfig(cmd *RunFleetGrowthCoordinatorCommand) growthRunConfig {
	c := growthRunConfig{
		Tick:                      time.Duration(cmd.TickIntervalSecs) * time.Second,
		HeavyCap:                  resolveGrowthHeavyCap(cmd.HeavyCap),
		UnservedLanesMin:          cmd.UnservedLanesMin,
		RunwayMilliHours:          cmd.RunwayMilliHours,
		PurchaseMarginOverFloor:   cmd.PurchaseMarginOverFloor,
		TreasuryPctPerPurchase:    cmd.TreasuryPctPerPurchase,
		APIUtilizationCeilingPct:  cmd.APIUtilizationCeilingPct,
		MaxPriceHeavies:           cmd.MaxPriceHeavies,
		MaxPremiumOverCheapestPct: cmd.MaxPremiumOverCheapestPct,
		ShipTypeHeavies:           cmd.ShipTypeHeavies,
		ZeroEffectAlarmTicks:      cmd.ZeroEffectAlarmTicks,
	}

	if c.Tick <= 0 {
		c.Tick = defaultGrowthTickSeconds * time.Second
	}
	if c.UnservedLanesMin <= 0 {
		c.UnservedLanesMin = defaultGrowthUnservedLanesMin
	}
	if c.RunwayMilliHours <= 0 {
		c.RunwayMilliHours = defaultGrowthRunwayMilliHours
	}
	if c.PurchaseMarginOverFloor <= 0 {
		c.PurchaseMarginOverFloor = defaultGrowthPurchaseMarginOverFloor
	}
	if c.APIUtilizationCeilingPct <= 0 {
		c.APIUtilizationCeilingPct = defaultGrowthAPIUtilCeilingPct
	}
	if c.MaxPremiumOverCheapestPct <= 0 {
		c.MaxPremiumOverCheapestPct = defaultGrowthMaxPremiumOverCheapestPct
	}
	if c.ShipTypeHeavies == "" {
		c.ShipTypeHeavies = defaultGrowthShipTypeHeavies
	}
	if c.ZeroEffectAlarmTicks <= 0 {
		c.ZeroEffectAlarmTicks = defaultGrowthZeroEffectAlarmTicks
	}
	// PreferDemandProximalYard defaults TRUE: nil (unset) → true; the *bool distinguishes an
	// explicit false from "not configured".
	c.PreferDemandProximalYard = true
	if cmd.PreferDemandProximalYard != nil {
		c.PreferDemandProximalYard = *cmd.PreferDemandProximalYard
	}
	return c
}

// resolveGrowthHeavyCap applies the heavy cap's pointer semantics: nil (absent) defers to the
// documented default, while an explicit value — INCLUDING 0 — is the operator's choice. 0 is a
// legitimate hold ("own no heavies"), not an unset knob, which is exactly why the field is a *int
// rather than following the codebase's usual "<=0 ⇒ default" resolve.
//
// A negative value is nonsense rather than a hold; it resolves to the default so a typo cannot
// silently disable heavy buying in a way that reads as intentional.
func resolveGrowthHeavyCap(configured *int) int {
	if configured == nil {
		return defaultGrowthHeavyCap
	}
	if *configured < 0 {
		return defaultGrowthHeavyCap
	}
	return *configured
}

// growthResult tallies one tick's effect for the zero-effect alarm and the tick log.
type growthResult struct {
	Wave      common.Wave
	Reason    common.WaveProbeReason
	Shortfall int
	Purchased int
}

// reconcileOnce runs one full growth pass. The ORDER is the whole design: resolve the live knobs,
// publish the switch, stop at the switch, read the tick's facts once, derive the wave from them,
// publish it, then buy only on HEAVY. It is the unit the tests drive directly; Handle just calls
// it on the tick.
func (h *RunFleetGrowthCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunFleetGrowthCoordinatorCommand) (growthResult, error) {
	cfg := resolveFleetGrowthConfig(cmd)
	// The live-tunable knobs, re-read from persisted config on ONE snapshot each tick so a `tune`
	// applies on the next reconcile with no container rebuild and no restart.
	liveCap, liveRunway, growthOn := h.liveKnobs(ctx, cmd, cfg.HeavyCap, cfg.RunwayMilliHours)
	cfg.HeavyCap, cfg.RunwayMilliHours = liveCap, liveRunway
	logger := common.LoggerFromContext(ctx)
	res := growthResult{}
	pid := strconv.Itoa(cmd.PlayerID)

	if h.metrics != nil {
		h.metrics.RecordGrowthEnabled(pid, growthOn)
	}

	// THE MASTER SWITCH, placed HERE because every port is read below this line — and the
	// expensive one is the shipyard price walk, which runs before any guard can block it.
	//
	// THE PAUSED PATH STILL PUBLISHES A WAVE, and it publishes PROBE. A switched-off buyer has
	// nothing to save for, so pausing probe buying for it would be a deadlock no spender could
	// clear; going silent instead would leave the drain reading a stale regime.
	if !growthOn {
		res.Wave, res.Reason = common.WaveProbe, common.WaveProbeReasonGrowthDisabled
		if h.metrics != nil {
			h.metrics.RecordWave(pid, res.Wave, res.Reason)
		}
		h.reportGrowthPaused(ctx, cmd)
		return res, nil
	}

	st := h.coordinatorState(cmd.ContainerID)
	in := h.readTickInputs(ctx, cmd.PlayerID, cfg)

	wave, reason := common.DeriveWave(common.WaveInputs{
		GrowthEnabled:           true,
		UnservedLanes:           in.laneDemand.UnservedLanes,
		UnservedLanesReadable:   in.laneDemandOK,
		TradeSaturated:          in.laneDemand.Saturated,
		TradeSaturationReadable: in.laneDemandOK,
		Target:                  in.heavyTarget,
		// The regime is judged on the fleet's DEMONSTRATED CAPACITY, never on the balance it
		// happens to hold this tick. The live balance is carried for the log and the gauges only.
		HighWaterTreasury:        in.highWater,
		HighWaterReadable:        in.highWaterOK,
		LiveTreasuryForReporting: in.treasury,
	})
	res.Wave, res.Reason = wave, reason
	if h.metrics != nil {
		h.metrics.RecordWave(pid, wave, reason)
		h.metrics.RecordHeavyReserve(pid, in.heavyReserve, int64(in.heavyTarget), in.heaviesOwned, cfg.HeavyCap)
		h.metrics.RecordWorkingCapital(pid, in.workingCapital)
	}

	// The pricing errand runs on the PROBE wave too: it spends no credits, buys nothing, and its
	// whole job is to make a LATER tick's price readable — which is what lets the wave ever flip.
	h.runHeavyPricingErrand(ctx, cmd, cfg, in)

	// The streak advances on the DEMAND, every tick, so it is not reset by a wave flip.
	demand := h.readHeavyDemand(in)
	res.Shortfall = demand.Shortfall()
	if h.metrics != nil {
		h.metrics.RecordDemand(HullClassHeavy, demand.Demand, demand.Current)
	}
	h.advanceShortfallStreak(st, demand)

	if wave != common.WaveHeavy {
		logger.Log("INFO", fmt.Sprintf("Fleet growth tick: PROBE wave (%s) — shortfall %d, no heavy considered", reason, res.Shortfall), map[string]interface{}{
			"action": "growth_tick", "container_id": cmd.ContainerID,
			"wave": string(wave), "probe_reason": string(reason), "shortfall": res.Shortfall, "purchased": 0,
		})
		// A paused class is idle BY REGIME — there is no work outstanding this tick — so it must
		// not read as BLOCKED and must clear any stale BLOCKED streak the escalator was holding.
		h.observeClassStall(ctx, cmd.ContainerID, cmd.PlayerID, HullClassHeavy, health.TickIdle())
		h.runZeroEffectAlarm(ctx, cmd, cfg, st, false, 0)
		return res, nil
	}

	bought, unmetNoBuy, guard, guardKnown := h.buyHeavy(ctx, cmd, cfg, demand, in, st)
	if bought {
		res.Purchased++
	}
	h.observeClassStall(ctx, cmd.ContainerID, cmd.PlayerID, HullClassHeavy,
		classStallVerdict(demand, nil, bought, unmetNoBuy, guard, guardKnown))
	h.runZeroEffectAlarm(ctx, cmd, cfg, st, unmetNoBuy, res.Purchased)

	logger.Log("INFO", fmt.Sprintf("Fleet growth tick: HEAVY wave — shortfall %d, %d purchased", res.Shortfall, res.Purchased), map[string]interface{}{
		"action": "growth_tick", "container_id": cmd.ContainerID,
		"wave": string(wave), "shortfall": res.Shortfall, "purchased": res.Purchased,
	})
	return res, nil
}

// reportGrowthPaused is the whole observable footprint of a paused tick.
//
// It logs EVERY tick rather than once on the edge: the coordinator's liveness signal is its
// growth_tick line, and a paused coordinator that simply went quiet would look exactly like a dead
// one. The line names the knob and the value that restores it, so an operator who finds a silent
// coordinator learns why from the log itself.
//
// It also reports IDLE, because paused is idle-BY-INSTRUCTION — the operator withdrew the work —
// so it must not read as BLOCKED, and reporting it clears any stale BLOCKED streak.
func (h *RunFleetGrowthCoordinatorHandler) reportGrowthPaused(ctx context.Context, cmd *RunFleetGrowthCoordinatorCommand) {
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Fleet growth PAUSED: %s is off, so the coordinator reads nothing and buys nothing this tick "+
			"(no shipyard scans, no lane read, no pricing errand) and publishes a PROBE wave so probe "+
			"buying resumes. Restore with `spacetraders tune --operation growth %s 1`",
		growthEnabledKey, growthEnabledKey), map[string]interface{}{
		"action":       "growth_paused",
		"container_id": cmd.ContainerID,
		"knob":         growthEnabledKey,
	})
	h.observeClassStall(ctx, cmd.ContainerID, cmd.PlayerID, HullClassHeavy, health.TickIdle())
}

// runZeroEffectAlarm raises ONE edge-triggered WARN when demand has persisted for
// zero_effect_alarm_ticks consecutive ticks with zero purchases. A purchase (or a tick with no
// demand pressure at all) resets the streak and re-arms the alarm for the next episode.
func (h *RunFleetGrowthCoordinatorHandler) runZeroEffectAlarm(ctx context.Context, cmd *RunFleetGrowthCoordinatorCommand, cfg growthRunConfig, st *growthState, unmetNoBuy bool, purchased int) {
	if purchased > 0 || !unmetNoBuy {
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
		common.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf("Fleet growth ZERO-EFFECT ALARM: unmet trade-capacity demand has produced NO purchase for %d consecutive ticks — a guard is persistently blocking (see the per-decision arithmetic above) or the purchaser is unwired", st.noEffectStreak), map[string]interface{}{
			"action":       "growth_zero_effect_alarm",
			"container_id": cmd.ContainerID,
			"streak":       st.noEffectStreak,
		})
	}
}
