package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
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

	// defaultExpansionEnabled is the expansion engine's master switch, encoded
	// 1=on / 2=off. It is NOT a bool-as-0/1 because `tune <key> 0` means "revert
	// to the default" fleet-wide — a 0/1 encoding would make "off" unexpressible.
	defaultExpansionEnabled = 1
	// expansionDisabled is the sentinel that switches expansion off.
	expansionDisabled = 2

	// defaultTargetUtilPct is the share of the rate-limiter ceiling the whole
	// fleet aims to occupy, leaving the remainder as burst headroom.
	defaultTargetUtilPct = 92
	// defaultMinScanRateMilli is the floor the pacer is clamped up to, in
	// thousandths of a request per second (100 = 0.1 req/s). It is what
	// guarantees planner data never goes fully dark under pressure.
	defaultMinScanRateMilli = 100
	// defaultValueClampR is the ceiling on how much more attention the hottest
	// market may earn than the baseline.
	defaultValueClampR = 4
	// defaultInflightCap bounds concurrent scans, and with it how hard a slow
	// API can push back on the pacer.
	defaultInflightCap = 3
	// defaultCapitalMultiplierKMilli is how many MILLI-hours of the trading
	// fleet's cargo runway the probe buy floor holds back on top of the
	// immutable reserve. 400 = 0.4h. Same convention as defaultMinScanRateMilli.
	//
	// 2h priced expansion out of its own era: a fleet trading hard enough to be
	// worth expanding is exactly the one whose cargo runway lifts the floor past
	// its treasury, so the drain held every probe at a floor that grew with the
	// success that was meant to fund it.
	defaultCapitalMultiplierKMilli = 400
	// defaultCapexReserveCredits is the credits held back for ship capex the
	// operation has already committed to elsewhere.
	defaultCapexReserveCredits = 100_000
	// defaultQuartermasterCadenceSecs is a yard slot's re-read interval. It is a
	// FLOOR on the scan interval, never a target: hull prices move on their own
	// schedule, so the budget may slow a yard down but never speed it past this.
	defaultQuartermasterCadenceSecs = 3600

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
	// ExpansionSpend is whether this coordinator may spend on hulls at all. It
	// feeds BOTH engines that can: the expansion pass, which asks other engines to
	// buy (a charting seed from the buy queue), and the buy queue itself, which is
	// what actually pays for a coverage probe.
	//
	// FEEDING ONLY THE FIRST WAS THE DEFECT. Half of it was the NAME: a switch
	// called `Expansion` reads as the engine being off while what the operator
	// wants off is the spending, and switching the whole engine
	// off costs the fleet its free frontier discovery. The other half was that
	// "spending" then reached one spender: the drain bought six probes a cycle with
	// the switch off, 907,545 credits' worth (sp-com1h). Both knobs now read this
	// one field. See parkedsensing.ExpandKnobs.SpendEnabled and
	// parkedsensing.BuyKnobs.SpendEnabled.
	ExpansionSpend          bool
	TargetUtilPct           int
	MinScanRateMilli        int
	ClampR                  int
	InflightCap             int
	CapitalMultiplierKMilli int
	CapexReserveCredits     int64
	QuartermasterCadence    time.Duration
	// SurgeInFlightCap is the standing bound on surge dispatches in flight.
	SurgeInFlightCap int
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
		ClampR:                  pick("value_clamp_r", cmd.ValueClampR),
		InflightCap:             pick("inflight_cap", cmd.InflightCap),
		CapitalMultiplierKMilli: pick("capital_multiplier_k_milli", cmd.CapitalMultiplierKMilli),
		CapexReserveCredits:     int64(pick("capex_reserve_credits", cmd.CapexReserveCredits)),
		QuartermasterCadence:    time.Duration(pick("quartermaster_cadence_secs", cmd.QuartermasterCadence)) * time.Second,
		SurgeInFlightCap:        pick("surge_inflight_cap", cmd.SurgeInFlightCap),
	}

	// 1=on, 2=off. Anything else — including the absent-key 0 — is the default.
	c.ExpansionSpend = pick("expansion_enabled", cmd.ExpansionEnabled) != expansionDisabled

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
// that matters here is assertable. SpendEnabled carries the SAME
// `expansion_enabled` switch the expansion pass reads, and both engines need it
// because they spend through different doors: expansion stops ASKING other engines
// to buy, and this queue stops BUYING. Wiring only the first is precisely what let
// 25 probes and 907,545 credits go out while the switch read off — a correct gate,
// shipped unreached (sp-com1h). See sensing_expand_wiring_test.go.
func buyKnobs(cfg sensingConfig) parkedsensing.BuyKnobs {
	return parkedsensing.BuyKnobs{
		SpendEnabled: cfg.ExpansionSpend,
		ProbeCap:     cfg.ProbeCap,
		CapexReserve: cfg.CapexReserveCredits,
		KMilli:       cfg.CapitalMultiplierKMilli,
	}
}
