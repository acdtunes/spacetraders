package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
)

const (
	// Config defaults (RULINGS #5: every operational number is container config,
	// filled here only when the launch config leaves it unset).
	defaultSensingTickSeconds = 30
	defaultSensingWaitLowMs   = 50   // limiter wait at or under this: the brake recovers
	defaultSensingWaitHighMs  = 1000 // limiter wait at or past this: the brake bites

	// defaultParkedProbeCap is the hard ceiling on probe hulls the engine may
	// own. It is deliberately far above any fleet we expect to build: parked
	// probes are bought one placement at a time behind the dynamic buy floor,
	// so the BINDING constraint on fleet size is money, not this number. The cap
	// is the backstop against a runaway placement plan, not the growth dial.
	defaultParkedProbeCap = 3000

	// defaultExpansionEnabled is the expansion engine's master switch: ONE knob with
	// three states, 1=buy probes and dispatch charting seeds. It is NOT a bool-as-0/1
	// because `tune <key> 0` means "revert to the default" fleet-wide — a 0/1 encoding
	// would make a state unexpressible.
	defaultExpansionEnabled = 1
	// expansionDisabled buys nothing at all. expansionProbesOnly keeps buying coverage
	// probes and dispatches no charting seed — the two spends have different economics.
	expansionDisabled   = 2
	expansionProbesOnly = 3

	// defaultTargetUtilPct is the share of the rate-limiter ceiling the whole
	// fleet aims to occupy, leaving the remainder as burst headroom.
	defaultTargetUtilPct = 92
	// defaultMinScanRateMilli is the floor the pacer is clamped up to, in
	// thousandths of a request per second (100 = 0.1 req/s). It is what
	// guarantees planner data never goes fully dark under pressure.
	defaultMinScanRateMilli = 100
	// defaultExpansionMinBudgetMilli is the residual below which expansion yields, in
	// the same units. A KNOB OF ITS OWN: one number serving this AND the scan floor
	// couples them backwards, since the brake drives the residual DOWN while the pacer
	// re-imposes the floor. 20 is the brake's reach — floored at 0.1 against a 0.1
	// req/s clamp the deepest residual is 0.010, so only a storm yields.
	defaultExpansionMinBudgetMilli = 20
	// defaultValueClampR is the ceiling on how much more attention the hottest
	// market may earn than the baseline.
	defaultValueClampR = 4
	// defaultInflightCap bounds concurrent scans, and with it how hard a slow
	// API can push back on the pacer.
	defaultInflightCap = 3
	// defaultCapitalMultiplierKMilli is how many MILLI-hours of the trading
	// fleet's cargo runway the probe buy floor holds back on top of the
	// immutable reserve. 200 = 0.2h. Same convention as defaultMinScanRateMilli.
	//
	// A long runway prices expansion out of its own era: a fleet trading hard
	// enough to be worth expanding is the one whose cargo term lifts the floor
	// past its treasury, so the drain holds every probe behind the success that
	// was meant to fund it.
	defaultCapitalMultiplierKMilli = 200
	// defaultCapexReserveCredits is the credits held back for ship capex the
	// operation has already committed to elsewhere.
	defaultCapexReserveCredits = 100_000
	// defaultQuartermasterCadenceSecs is a yard slot's re-read interval. It is a
	// FLOOR on the scan interval, never a target: hull prices move on their own
	// schedule, so the budget may slow a yard down but never speed it past this.
	defaultQuartermasterCadenceSecs = 3600
	// defaultCoverageReserve ships the buy queue's coverage reserve OFF: unlike
	// every other knob here, zero is the documented default rather than a revert
	// to some positive one, so the saturate-first order runs unarmed until set.
	defaultCoverageReserve = 0

	// The probe-procurement pair, mirroring the engine's own values so the tune
	// registry publishes a number rather than a zero (as the charting-crew defaults do).
	defaultWalkAwayMult       = domainSensing.DefaultWalkAwayMult
	defaultJumpPenaltyCredits = int(domainSensing.DefaultJumpPenaltyCredits)

	// askFreshnessCadences is how many quartermaster cadences a stored yard price stays
	// comparable for, and the multiple is load-bearing: the cadence is a FLOOR on a
	// yard's re-read interval, never a target, so equating "fresh" with one cadence
	// would mark almost every reading stale and leave the ranking failing open forever.
	// A constant, not a knob (RULINGS #5 as bounded): the lever is the cadence itself.
	askFreshnessCadences = 3

	// The charting crew: how many probes one dark system may be worked by, and the
	// outstanding counts earning the second and third. Mirrors of the engine's own
	// defaults (parkedsensing.chartcrew), held here so the tune registry publishes a
	// number rather than a zero. A cap of ONE is the single-hull tour.
	defaultChartHullCap      = 3
	defaultSecondChartHullAt = 12
	defaultThirdChartHullAt  = 24

	// screenSweepBatch bounds how many PENDING systems one tick screens. A plain
	// constant, deliberately not a knob: it paces API bursts (an unresolved
	// market costs a remote fetch, and a catalog-unknown system costs a
	// paginated waypoint sweep), not economics. The backlog is not lost — it is
	// worked over more ticks, and every system left over is still PENDING.
	screenSweepBatch = 5

	// budgetWindow is the trailing window the API budget is measured over. It
	// matches the tracker's own retention, so the reading is never diluted by
	// time the tracker cannot answer for.
	budgetWindow = 5 * time.Minute

	// pacerGuardComponent labels the panic guard around the scan pacer goroutine.
	pacerGuardComponent = "parked-sensing-pacer:"
)

// defaultSensingWhitelist is the era-invariant goods whitelist: a market is
// worth observing for what it DEALS IN, never what it is currently worth —
// prices are volatile and would drop a crushed market right before it recovers.
func defaultSensingWhitelist() []string {
	return []string{
		"CLOTHING", "LAB_INSTRUMENTS", "FABRICS", "FOOD", "ADVANCED_CIRCUITRY",
		"MEDICINE", "EQUIPMENT", "URANITE", "MICROPROCESSORS", "SHIP_PLATING",
		"MACHINERY", "ELECTRONICS",
	}
}

// sensingConfig is one tick's effective config, with every default resolved.
type sensingConfig struct {
	Whitelist         map[string]bool
	Tick              time.Duration
	WaitLow, WaitHigh time.Duration
	ProbeCap          int
	// ProbeSpend and SeedDispatch are the TWO SPENDS `expansion_enabled` resolves into,
	// and the reason it is one knob with three states rather than two keys: two knobs
	// that must be kept in sync to stay correct are worse than one, and only the base
	// value is persisted (RULINGS #5, base plus derived tiers).
	//
	// ProbeSpend reaches the buy queue, which pays for a coverage probe. SeedDispatch
	// reaches the expansion pass, which puts a hull on a charting errand. They are
	// separate because the economics are: a coverage probe prices markets the trade
	// planner scores lanes from, while a charting errand burns jump fuel for a reward
	// that only sometimes covers it.
	//
	// FEEDING ONLY ONE OF THEM WAS THE DEFECT that put this pair here: a gate on the
	// expansion pass alone left the drain buying probes with the switch off (sp-com1h).
	// See parkedsensing.ExpandKnobs.SeedsEnabled and parkedsensing.BuyKnobs.SpendEnabled.
	ProbeSpend       bool
	SeedDispatch     bool
	TargetUtilPct    int
	MinScanRateMilli int
	// ExpansionMinBudgetMilli is expansion's own residual floor, never MinScanRateMilli.
	ExpansionMinBudgetMilli int
	ClampR                  int
	InflightCap             int
	CapitalMultiplierKMilli int
	CapexReserveCredits     int64
	QuartermasterCadence    time.Duration
	// SurgeInFlightCap is the standing bound on surge dispatches in flight.
	SurgeInFlightCap int
	// CoverageReserve is the buy queue's coverage-reserve share. See BuyKnobs.CoverageReserve.
	CoverageReserve int
	// The probe-procurement pair. See BuyKnobs.
	WalkAwayMult       int
	JumpPenaltyCredits int64
	// ChartHullCap, SecondChartHullAt and ThirdChartHullAt size a dark system's
	// charting crew. See ExpandKnobs for what each one binds.
	ChartHullCap      int
	SecondChartHullAt int
	ThirdChartHullAt  int
}

// resolveSensingConfig resolves one tick's effective config from the launch
// command overlaid with the live snapshot: a zero/absent knob means the
// documented default, which is exactly the `tune <key> 0` revert.
//
// The two out-of-contract cases are treated differently on purpose:
//
//   - ZERO is the REVERT, and it is silent. `tune <key> 0` means "go back to the
//     documented default" fleet-wide, and an absent key resolves to 0 too — which
//     is the normal state of every default launch. Warning here would fire on
//     every tick of most containers.
//   - NEGATIVE is a MISWRITE, and it warns. The tune registry bounds every key,
//     so a negative can only arrive from a hand-edited config row — and two of
//     them are silently destructive if merely absorbed: a negative
//     min_scan_rate_milli flows straight through to a NEGATIVE sensing rate (the
//     pacer's floor becomes a ceiling), and a clamp below 1 collapses the
//     weighting's optimistic prior so every unmeasured slot degrades to hourly
//     scans. Both faults are invisible in behaviour — the rotation still runs —
//     so the warning is the only trace they would otherwise leave.
func resolveSensingConfig(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand, live liveconfig.Snapshot) sensingConfig {
	goods := cmd.GoodsWhitelist
	if len(goods) == 0 {
		goods = defaultSensingWhitelist()
	}
	whitelist := make(map[string]bool, len(goods))
	for _, good := range goods {
		whitelist[good] = true
	}

	pick := func(key string, launch int) int {
		if v, ok := live.PositiveInt(key); ok {
			return v
		}
		return launch
	}

	c := sensingConfig{
		Whitelist:               whitelist,
		Tick:                    time.Duration(pick("tick_secs", cmd.TickSecs)) * time.Second,
		WaitLow:                 time.Duration(pick("wait_low_ms", cmd.WaitLowMs)) * time.Millisecond,
		WaitHigh:                time.Duration(pick("wait_high_ms", cmd.WaitHighMs)) * time.Millisecond,
		ProbeCap:                pick("probe_cap", cmd.ProbeCap),
		TargetUtilPct:           pick("target_util_pct", cmd.TargetUtilPct),
		MinScanRateMilli:        pick("min_scan_rate_milli", cmd.MinScanRateMilli),
		ExpansionMinBudgetMilli: pick("expansion_min_budget_milli", cmd.ExpansionMinBudgetMilli),
		ClampR:                  pick("value_clamp_r", cmd.ValueClampR),
		InflightCap:             pick("inflight_cap", cmd.InflightCap),
		CapitalMultiplierKMilli: pick("capital_multiplier_k_milli", cmd.CapitalMultiplierKMilli),
		CapexReserveCredits:     int64(pick("capex_reserve_credits", cmd.CapexReserveCredits)),
		QuartermasterCadence:    time.Duration(pick("quartermaster_cadence_secs", cmd.QuartermasterCadence)) * time.Second,
		SurgeInFlightCap:        pick("surge_inflight_cap", cmd.SurgeInFlightCap),
		CoverageReserve:         pick("coverage_reserve", cmd.CoverageReserve),
		WalkAwayMult:            pick("procurement_walkaway_mult", cmd.WalkAwayMult),
		JumpPenaltyCredits:      int64(pick("procurement_jump_penalty_credits", cmd.JumpPenaltyCredits)),
		ChartHullCap:            pick("chart_hull_cap", cmd.ChartHullCap),
		SecondChartHullAt:       pick("chart_hull_2_at", cmd.SecondChartHullAt),
		ThirdChartHullAt:        pick("chart_hull_3_at", cmd.ThirdChartHullAt),
	}

	// 1=both, 2=neither, 3=probes only. Anything else — including the absent-key 0 —
	// is the default, so an unreadable state can only ever widen back to it.
	expansion := pick("expansion_enabled", cmd.ExpansionEnabled)
	c.ProbeSpend = expansion != expansionDisabled
	c.SeedDispatch = c.ProbeSpend && expansion != expansionProbesOnly

	applySensingDefaults(ctx, cmd, &c)
	return c
}

// applySensingDefaults substitutes the documented default for any knob that resolved outside
// its contract, warning on the negatives that would otherwise degrade the rotation silently.
func applySensingDefaults(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand, c *sensingConfig) {
	if c.Tick <= 0 {
		c.Tick = defaultSensingTickSeconds * time.Second
	}
	if c.WaitLow <= 0 {
		c.WaitLow = defaultSensingWaitLowMs * time.Millisecond
	}
	if c.WaitHigh <= 0 {
		c.WaitHigh = defaultSensingWaitHighMs * time.Millisecond
	}
	if c.ProbeCap <= 0 {
		c.ProbeCap = defaultParkedProbeCap
	}
	if c.TargetUtilPct <= 0 {
		c.TargetUtilPct = defaultTargetUtilPct
	}
	if c.MinScanRateMilli <= 0 {
		warnNegativeSensingKnob(ctx, "min_scan_rate_milli", c.MinScanRateMilli, defaultMinScanRateMilli)
		c.MinScanRateMilli = defaultMinScanRateMilli
	}
	if c.ExpansionMinBudgetMilli <= 0 { // reverting on zero ships it armed (RULINGS #22)
		warnNegativeSensingKnob(ctx, "expansion_min_budget_milli", c.ExpansionMinBudgetMilli, defaultExpansionMinBudgetMilli)
		c.ExpansionMinBudgetMilli = defaultExpansionMinBudgetMilli
	}
	if c.ClampR < 1 {
		warnNegativeSensingKnob(ctx, "value_clamp_r", c.ClampR, defaultValueClampR)
		c.ClampR = defaultValueClampR
	}
	if c.InflightCap <= 0 {
		c.InflightCap = defaultInflightCap
	}
	if c.CapitalMultiplierKMilli < 0 {
		warnNegativeSensingKnob(ctx, "capital_multiplier_k_milli", c.CapitalMultiplierKMilli, defaultCapitalMultiplierKMilli)
		c.CapitalMultiplierKMilli = defaultCapitalMultiplierKMilli
	}
	if c.CapitalMultiplierKMilli == 0 && cmd.CapitalMultiplierKMilli == 0 {
		// 0 is a legitimate operator choice (hold back no cargo runway at all),
		// but it is indistinguishable from an absent key, so the documented
		// default wins — matching every other knob's revert semantics.
		//
		// This is exactly why milli-units matter rather than being cosmetic: in
		// whole hours the only sub-1h setting WAS 0, which this branch then
		// reverts to the default — so a fractional runway was unreachable, not
		// merely awkward. 400 (=0.4h) is a distinct, settable value.
		c.CapitalMultiplierKMilli = defaultCapitalMultiplierKMilli
	}
	if c.CapexReserveCredits < 0 {
		warnNegativeSensingKnob(ctx, "capex_reserve_credits", int(c.CapexReserveCredits), defaultCapexReserveCredits)
		c.CapexReserveCredits = defaultCapexReserveCredits
	}
	if c.CapexReserveCredits == 0 && cmd.CapexReserveCredits == 0 {
		c.CapexReserveCredits = defaultCapexReserveCredits
	}
	if c.QuartermasterCadence <= 0 {
		c.QuartermasterCadence = defaultQuartermasterCadenceSecs * time.Second
	}
	if c.SurgeInFlightCap <= 0 {
		// Reverts to the documented default like every other knob, which is also what
		// makes this cap unusable as an off switch: `tune surge_inflight_cap 0` restores
		// 8 rather than stopping the surge. The pass ships ARMED and has no off seam.
		c.SurgeInFlightCap = defaultSurgeInFlightCap
	}
	if c.CoverageReserve < 0 {
		warnNegativeSensingKnob(ctx, "coverage_reserve", c.CoverageReserve, defaultCoverageReserve)
		c.CoverageReserve = defaultCoverageReserve
	}
	// Reverting on zero AND negative ships the pair ON with no config (RULINGS #22).
	if c.WalkAwayMult <= 0 {
		warnNegativeSensingKnob(ctx, "procurement_walkaway_mult", c.WalkAwayMult, defaultWalkAwayMult)
		c.WalkAwayMult = defaultWalkAwayMult
	}
	if c.JumpPenaltyCredits <= 0 {
		warnNegativeSensingKnob(ctx, "procurement_jump_penalty_credits", int(c.JumpPenaltyCredits), defaultJumpPenaltyCredits)
		c.JumpPenaltyCredits = int64(defaultJumpPenaltyCredits)
	}
	if c.ChartHullCap <= 0 {
		c.ChartHullCap = defaultChartHullCap
	}
	if c.SecondChartHullAt <= 0 {
		c.SecondChartHullAt = defaultSecondChartHullAt
	}
	if c.ThirdChartHullAt <= 0 {
		c.ThirdChartHullAt = defaultThirdChartHullAt
	}
}

func warnNegativeSensingKnob(ctx context.Context, key string, v, fallback int) {
	if v >= 0 {
		return
	}
	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
		"Probe sensing knob %s is negative (%d) and has been replaced with its default (%d) — a negative here degrades the scan rotation silently, so it is never honoured",
		key, v, fallback), map[string]interface{}{
		"action": "parked_sensing_knob_rejected",
		"knob":   key,
		"value":  v,
	})
}

// liveSnapshot takes this tick's view of the persisted config. A missing reader
// or a failed read yields a nil snapshot, which resolveSensingConfig reads as
// "no live overrides" and runs entirely on the launch command — the fail-safe
// launch behaviour, never a half-applied config (liveconfig.go).
func (h *RunProbeSensingCoordinatorHandler) liveSnapshot(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand) liveconfig.Snapshot {
	if h.liveConfig == nil {
		return nil
	}
	snapshot, err := h.liveConfig.Snapshot(ctx, cmd.ContainerID, cmd.PlayerID.Value())
	if err != nil {
		return nil
	}
	return snapshot
}

// buyKnobs is the buy queue's economics for this tick, derived from the resolved
// config.
//
// A NAMED FUNCTION RATHER THAN A STRUCT LITERAL AT THE CALL SITE, so the one line
// that matters here is assertable. Wiring the expansion pass's half of
// `expansion_enabled` and not this one is precisely what let 25 probes and 907,545
// credits go out while the switch read off — a correct gate, shipped unreached
// (sp-com1h). See sensing_expand_wiring_test.go.
func buyKnobs(cfg sensingConfig) parkedsensing.BuyKnobs {
	return parkedsensing.BuyKnobs{
		SpendEnabled:    cfg.ProbeSpend,
		ProbeCap:        cfg.ProbeCap,
		CapexReserve:    cfg.CapexReserveCredits,
		KMilli:          cfg.CapitalMultiplierKMilli,
		CoverageReserve: cfg.CoverageReserve,
		// Freshness is DERIVED from the re-read cadence — see askFreshnessCadences.
		WalkAwayMult:       cfg.WalkAwayMult,
		JumpPenaltyCredits: cfg.JumpPenaltyCredits,
		AskFreshness:       cfg.QuartermasterCadence * askFreshnessCadences,
	}
}

// expandKnobs is the expansion pass's half of the same switch, named for the same
// reason: a gate nobody passes an argument to is a gate that ships dormant.
//
// MinBudgetRate is the SENSING residual floor, never the pacer rate: the emergency
// brake can drive the residual below the minimum scan rate while the pacer re-imposes
// it, so gating on the pacer would leave expansion charting through a rate-limit storm.
// It reads expansion_min_budget_milli, deliberately not min_scan_rate_milli.
func expandKnobs(cfg sensingConfig) parkedsensing.ExpandKnobs {
	return parkedsensing.ExpandKnobs{
		SeedsEnabled:      cfg.SeedDispatch,
		MinBudgetRate:     float64(cfg.ExpansionMinBudgetMilli) / 1000.0,
		Whitelist:         cfg.Whitelist,
		ChartHullCap:      cfg.ChartHullCap,
		SecondChartHullAt: cfg.SecondChartHullAt,
		ThirdChartHullAt:  cfg.ThirdChartHullAt,
	}
}
