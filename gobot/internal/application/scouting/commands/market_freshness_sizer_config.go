package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// SizerTunableDefaults maps every LIVE-tunable freshness-sizer knob to its
// documented default — the value that applies when neither the live container config
// nor the launch command carries a positive one. The daemon's tune bounds registry
// reads THIS map, so the defaults-of-record stay in this file next to the consts they
// mirror (including today's Admiral retunes: cooldown 1m, max spend 500k). The map's
// KEY SET is also the contract for which keys resolveSizerConfig live-overlays.
func SizerTunableDefaults() map[string]int {
	return map[string]int{
		"max_spend_per_cycle":         defaultSizerMaxSpend,
		"purchase_cooldown_secs":      int(defaultSizerCooldown / time.Second),
		"spend_window_secs":           int(defaultSizerSpendWindow / time.Second),
		"max_probe_fleet":             defaultSizerMaxProbeFleet,
		"max_probes_per_system":       defaultMaxProbesPerSystem,
		"sla_seconds":                 defaultSLASeconds,
		"sla_seconds_weak":            defaultSLAWeakSeconds,        // Per-activity SLA (WEAK, 360m)
		"sla_seconds_restricted":      defaultSLARestrictedSeconds,  // Per-activity SLA (RESTRICTED + unknown/null, 135m)
		"sla_seconds_growing":         defaultSLAGrowingSeconds,     // Per-activity SLA (GROWING, 45m)
		"sla_seconds_strong":          defaultSLAStrongSeconds,      // Per-activity SLA (STRONG, 22m)
		"target_percentile":           defaultTargetPercentile,      // sp-r57g percentile-age target
		"value_weighted":              valueWeightedModeOn,          // sp-r57g value-weighting mode (2=on default, 1=off)
		"demand_ewma_half_life_secs":  defaultDemandHalfLifeSeconds, // Realized-demand EWMA half-life
		"worst_cycle_seconds":         defaultWorstCycleSeconds,
		"cycle_dampening_percent":     defaultCycleDampeningPercent,
		"breach_response_percent":     defaultBreachResponsePercent,
		"release_slack_percent":       defaultReleaseSlackPercent,
		"release_stable_window_secs":  defaultReleaseStableWindowSecs,
		"reserved_frontier_floor":     defaultReservedFrontierFloor,
		"hold_unscanned_market_posts": defaultHoldUnscannedMarketPosts, // sp-u8jc/sp-gucu bootstrap flag (0=off)
		// Sensing scope: how long a traded system stays in the footprint, how many out-of-footprint
		// systems stay under watch, and the relaxed target those watch posts carry.
		"scan_footprint_retention_secs": defaultScanFootprintRetentionSecs,
		"scan_discovery_allowance":      defaultScanDiscoveryAllowance,
		"scan_discovery_sla_seconds":    defaultScanDiscoverySLASeconds,
	}
}

// sizerConfig is the launch command with every default resolved.
type sizerConfig struct {
	DefaultSLA time.Duration
	Overrides  map[string]time.Duration
	// ActivitySLA is the per-activity freshness SLA: each market ACTIVITY state is sized
	// against its own SLA, keyed by the canonical shared.ActivityLevel. Resolved once in
	// resolveSizerConfig from the sla_seconds_{weak,restricted,growing,strong} knobs; read via
	// slaForActivity, which maps an unknown/absent activity to the RESTRICTED entry.
	ActivitySLA              map[shared.ActivityLevel]time.Duration
	SeedCycle                time.Duration
	MinCycleSamples          int
	WorstCycle               time.Duration
	CycleDampeningPercent    int
	MaxProbesPerSystem       int
	BreachResponsePercent    int
	TargetPercentile         int           // sp-r57g percentile-age target (default 90; 100 = max-age behavior)
	ValueWeighted            bool          // sp-r57g: weight the percentile by per-market value (default ON)
	DemandHalfLife           time.Duration // EWMA half-life of the realized-sink-demand weight
	ReleaseSlackPercent      int
	ReleaseStableWindow      time.Duration
	ReservedFrontierFloor    int
	HoldUnscannedMarketPosts bool // sp-u8jc/sp-gucu: hold-not-retire charted-but-unscanned posts
	// FootprintRetention is how long a system stays in the trading footprint after its last
	// realized trade; DiscoveryAllowance how many out-of-footprint systems keep a standing watch;
	// DiscoverySLA the relaxed freshness target those watch posts carry.
	FootprintRetention time.Duration
	DiscoveryAllowance int
	DiscoverySLA       time.Duration
	Buy                probebuy.Config
}

// resolveSizerConfig resolves one tick's effective config. live is the tick-start
// snapshot of the container's persisted config column (nil when unwired/unreadable).
// For the TUNABLE knobs (SizerTunableDefaults) a non-nil snapshot is AUTHORITATIVE:
// a positive value is the live value (the launch verb wrote its values into the same
// column, so untuned knobs still read their launch values here), and an absent/zeroed
// key means the documented default — the `tune <key> 0` revert. Only when there is NO
// snapshot does the launch command fill those knobs (fail-safe launch behavior). The
// non-tunable knobs always resolve from the launch command, unchanged.
func resolveSizerConfig(cmd *RunMarketFreshnessSizerCoordinatorCommand, live liveconfig.Snapshot) sizerConfig {
	c := sizerConfig{
		DefaultSLA:            time.Duration(cmd.SLASeconds) * time.Second,
		SeedCycle:             time.Duration(cmd.SeedCycleSeconds) * time.Second,
		MinCycleSamples:       cmd.MinCycleSamples,
		WorstCycle:            time.Duration(cmd.WorstCycleSeconds) * time.Second,
		CycleDampeningPercent: cmd.CycleDampeningPercent,
		MaxProbesPerSystem:    cmd.MaxProbesPerSystem,
		BreachResponsePercent: cmd.BreachResponsePercent,
		TargetPercentile:      cmd.TargetPercentile,
		ValueWeighted:         valueWeightedFromMode(cmd.ValueWeightedMode),
		ReleaseSlackPercent:   cmd.ReleaseSlackPercent,
		ReleaseStableWindow:   time.Duration(cmd.ReleaseStableWindowSecs) * time.Second,
		ReservedFrontierFloor: cmd.ReservedFrontierFloor,
		// sp-u8jc/sp-gucu int-mode flag: >0 ⇒ hold-not-retire charted-but-unscanned posts.
		HoldUnscannedMarketPosts: cmd.HoldUnscannedMarketPosts > 0,
		Buy: probebuy.Config{
			MaxProbeFleet:    cmd.MaxProbeFleet,
			MaxSpendPerCycle: cmd.MaxSpendPerCycle,
			PurchaseCooldown: time.Duration(cmd.PurchaseCooldownSecs) * time.Second,
			SpendWindow:      time.Duration(cmd.SpendWindowSecs) * time.Second,
		},
	}
	overlayLiveSizerKnobs(&c, live)
	applySizerDefaults(&c)

	c.Overrides = make(map[string]time.Duration, len(cmd.SystemSLAOverrides))
	for system, secs := range cmd.SystemSLAOverrides {
		if secs > 0 {
			c.Overrides[system] = time.Duration(secs) * time.Second
		}
	}
	c.ActivitySLA = resolveActivitySLA(live)
	c.DemandHalfLife = resolveDemandHalfLife(live)
	c.FootprintRetention = liveSecondsOrDefault(live, "scan_footprint_retention_secs", defaultScanFootprintRetentionSecs)
	c.DiscoverySLA = liveSecondsOrDefault(live, "scan_discovery_sla_seconds", defaultScanDiscoverySLASeconds)
	c.DiscoveryAllowance = defaultScanDiscoveryAllowance
	if live != nil {
		if allowance := live.PositiveIntOrZero("scan_discovery_allowance"); allowance > 0 {
			c.DiscoveryAllowance = allowance
		}
	}
	return c
}

func overlayLiveSizerKnobs(c *sizerConfig, live liveconfig.Snapshot) {
	if live == nil {
		return
	}
	c.DefaultSLA = time.Duration(live.PositiveIntOrZero("sla_seconds")) * time.Second
	c.WorstCycle = time.Duration(live.PositiveIntOrZero("worst_cycle_seconds")) * time.Second
	c.CycleDampeningPercent = live.PositiveIntOrZero("cycle_dampening_percent")
	c.MaxProbesPerSystem = live.PositiveIntOrZero("max_probes_per_system")
	c.BreachResponsePercent = live.PositiveIntOrZero("breach_response_percent")
	c.TargetPercentile = live.PositiveIntOrZero("target_percentile")
	// value_weighted is live-authoritative both ways (2=on, 1=off, absent/0=default) — a live
	// snapshot can re-enable weighting the launch disabled, or disable it live if it misbehaves.
	c.ValueWeighted = valueWeightedFromMode(live.PositiveIntOrZero("value_weighted"))
	c.ReleaseSlackPercent = live.PositiveIntOrZero("release_slack_percent")
	c.ReleaseStableWindow = time.Duration(live.PositiveIntOrZero("release_stable_window_secs")) * time.Second
	// sp-iopd reserved frontier floor: live-authoritative. Absent/zeroed ⇒ 0 (floor OFF), which is
	// the documented default — no <=0 fallback, since 0 IS the default here.
	c.ReservedFrontierFloor = live.PositiveIntOrZero("reserved_frontier_floor")
	// sp-u8jc/sp-gucu hold-unscanned flag: live-authoritative int-mode bool. Absent/zeroed ⇒ OFF (the
	// documented default, retire-as-gone) — no fallback, since 0 IS the default here.
	c.HoldUnscannedMarketPosts = live.PositiveIntOrZero("hold_unscanned_market_posts") > 0
	c.Buy.MaxProbeFleet = live.PositiveIntOrZero("max_probe_fleet")
	c.Buy.MaxSpendPerCycle = live.PositiveIntOrZero("max_spend_per_cycle")
	c.Buy.PurchaseCooldown = time.Duration(live.PositiveIntOrZero("purchase_cooldown_secs")) * time.Second
	c.Buy.SpendWindow = time.Duration(live.PositiveIntOrZero("spend_window_secs")) * time.Second
}

// applySizerDefaults substitutes the documented default for any knob that resolved
// non-positive, which is exactly the `tune <key> 0` revert.
func applySizerDefaults(c *sizerConfig) {
	if c.DefaultSLA <= 0 {
		c.DefaultSLA = defaultSLASeconds * time.Second
	}
	if c.SeedCycle <= 0 {
		c.SeedCycle = defaultSeedCycleSeconds * time.Second
	}
	if c.MinCycleSamples <= 0 {
		c.MinCycleSamples = defaultMinCycleSamples
	}
	if c.WorstCycle <= 0 {
		c.WorstCycle = defaultWorstCycleSeconds * time.Second
	}
	if c.CycleDampeningPercent <= 0 {
		c.CycleDampeningPercent = defaultCycleDampeningPercent
	}
	if c.MaxProbesPerSystem <= 0 {
		c.MaxProbesPerSystem = defaultMaxProbesPerSystem
	}
	if c.BreachResponsePercent <= 0 {
		c.BreachResponsePercent = defaultBreachResponsePercent
	}
	if c.TargetPercentile <= 0 {
		c.TargetPercentile = defaultTargetPercentile
	}
	// ValueWeighted needs no <=0 fallback — valueWeightedFromMode already maps the unset mode (0)
	// to defaultValueWeighted, so both the launch and live branches carry a resolved bool by here.
	if c.ReleaseSlackPercent <= 0 {
		c.ReleaseSlackPercent = defaultReleaseSlackPercent
	}
	if c.ReleaseStableWindow <= 0 {
		c.ReleaseStableWindow = defaultReleaseStableWindowSecs * time.Second
	}
	if c.Buy.MaxProbeFleet <= 0 {
		c.Buy.MaxProbeFleet = defaultSizerMaxProbeFleet
	}
	if c.Buy.MaxSpendPerCycle <= 0 {
		c.Buy.MaxSpendPerCycle = defaultSizerMaxSpend
	}
	if c.Buy.PurchaseCooldown <= 0 {
		c.Buy.PurchaseCooldown = defaultSizerCooldown
	}
	if c.Buy.SpendWindow <= 0 {
		c.Buy.SpendWindow = defaultSizerSpendWindow
	}
}

// liveSecondsOrDefault resolves a tunable-only seconds knob: live-authoritative when a positive
// value is present, the documented default otherwise (absent key, `tune <key> 0`, or no snapshot).
func liveSecondsOrDefault(live liveconfig.Snapshot, key string, defaultSeconds int) time.Duration {
	secs := 0
	if live != nil {
		secs = live.PositiveIntOrZero(key)
	}
	if secs <= 0 {
		secs = defaultSeconds
	}
	return time.Duration(secs) * time.Second
}

// resolveDemandHalfLife resolves the realized-demand EWMA half-life from the tick's live
// snapshot: live-authoritative when a positive value is present, the documented default otherwise
// (an absent/zeroed key, or no snapshot). It is tunable-only — no launch-command field, mirroring
// the per-activity SLA knobs — so a nil snapshot yields the armed default directly.
func resolveDemandHalfLife(live liveconfig.Snapshot) time.Duration {
	secs := 0
	if live != nil {
		secs = live.PositiveIntOrZero("demand_ewma_half_life_secs")
	}
	if secs <= 0 {
		secs = defaultDemandHalfLifeSeconds
	}
	return time.Duration(secs) * time.Second
}

func (c sizerConfig) slaFor(system string) time.Duration {
	if sla, ok := c.Overrides[system]; ok {
		return sla
	}
	return c.DefaultSLA
}

// resolveActivitySLA resolves the per-activity freshness SLA map from the tick's live
// snapshot. Each knob is live-authoritative when a positive value is present and falls back to its
// documented default otherwise (an absent/zeroed key, or no snapshot) — the same tune-registry
// semantics the other tunables use, where `tune <key> 0` reverts to the default. The launch verb
// does not carry these knobs (they arm at the defaults and tune live), so there is no launch-command
// field to fold in — a nil snapshot yields the armed defaults directly.
func resolveActivitySLA(live liveconfig.Snapshot) map[shared.ActivityLevel]time.Duration {
	weak, restricted, growing, strong := 0, 0, 0, 0
	if live != nil {
		weak = live.PositiveIntOrZero("sla_seconds_weak")
		restricted = live.PositiveIntOrZero("sla_seconds_restricted")
		growing = live.PositiveIntOrZero("sla_seconds_growing")
		strong = live.PositiveIntOrZero("sla_seconds_strong")
	}
	return map[shared.ActivityLevel]time.Duration{
		shared.ActivityLevelWeak:       activitySLAOrDefault(weak, defaultSLAWeakSeconds),
		shared.ActivityLevelRestricted: activitySLAOrDefault(restricted, defaultSLARestrictedSeconds),
		shared.ActivityLevelGrowing:    activitySLAOrDefault(growing, defaultSLAGrowingSeconds),
		shared.ActivityLevelStrong:     activitySLAOrDefault(strong, defaultSLAStrongSeconds),
	}
}

// activitySLAOrDefault turns a knob's resolved seconds into a duration, substituting the documented
// default for a non-positive (unset / `tune 0`-reverted) value.
func activitySLAOrDefault(secs, defaultSecs int) time.Duration {
	if secs <= 0 {
		secs = defaultSecs
	}
	return time.Duration(secs) * time.Second
}

// slaForActivity returns the freshness SLA for a canonical activity level. An activity
// absent from the map — an unknown/null state, or the "" zero value — resolves to the RESTRICTED
// entry, the documented unknown default.
func (c sizerConfig) slaForActivity(level shared.ActivityLevel) time.Duration {
	if sla, ok := c.ActivitySLA[level]; ok {
		return sla
	}
	return c.ActivitySLA[shared.ActivityLevelRestricted]
}

// valueWeightedFromMode maps the int-mode value_weighted knob to a bool: valueWeightedModeOff (1)
// → off, valueWeightedModeOn (2) → on, and anything else (0/unset, or an out-of-range value) →
// the documented default (ON). The int encoding exists because the tune registry stores ints and
// treats 0 as "revert to default", so a plain 0/1 bool could never express an OFF that survives a
// revert; 1=off / 2=on keeps the toggle live-tunable in BOTH directions.
func valueWeightedFromMode(mode int) bool {
	switch mode {
	case valueWeightedModeOff:
		return false
	case valueWeightedModeOn:
		return true
	default:
		return defaultValueWeighted
	}
}

// liveConfigSnapshot takes the tick's live-config snapshot (sp-vwek). A nil reader
// (not wired — tests, minimal boots) or a read error yields nil, which
// resolveSizerConfig treats as "run this tick entirely on the launch command" — the
// fail-safe launch behavior, never a half-applied config. The read is logged, not
// fatal: a transient DB gap must not kill the reconcile loop.
func (h *RunMarketFreshnessSizerCoordinatorHandler) liveConfigSnapshot(ctx context.Context, cmd *RunMarketFreshnessSizerCoordinatorCommand) liveconfig.Snapshot {
	if h.liveConfig == nil {
		return nil
	}
	snap, err := h.liveConfig.Snapshot(ctx, cmd.ContainerID, cmd.PlayerID.Value())
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Live config unreadable — this tick runs on launch values: %v", err), nil)
		return nil
	}
	return snap
}
