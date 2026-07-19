package config

// ScoutingConfig holds the scouting subsystem's knobs (sp-x8i5). The daemon injects
// these into scout_tour and scout_post_coordinator launch configs on every build —
// creation AND restart recovery, via resolveScoutingConfig — so a captain retunes the
// fleet's phase behavior by editing config.yaml and restarting, no code redeploy
// (sp-ts82 live-config pattern, RULINGS #5).
//
// A zero value means "unset" and defers to the handler's own documented default for
// that knob, so the daemon injects only the keys the captain actually set.
type ScoutingConfig struct {
	// TourStartJitterMaxSeconds bounds the per-ship deterministic phase jitter a scout
	// tour waits before its first navigation/scan (sp-x8i5). ~45 scouts restarting
	// their rotation in near-lockstep transiently saturates the rate limiter in a
	// phase-locked wave, not a sustained-load problem. Each ship waits hash(ship_symbol) % ceiling
	// before its tour starts — deterministic across restarts (no math/rand) — so the
	// fleet decoheres into a spread instead of stacking on every rotation. The standing
	// scout_post_coordinator waits hash(container_id) % ceiling the same way before its
	// reconcile loop starts. 0/absent => 120s, sized so ~45 scouts spread across two
	// effective reconcile ticks without materially delaying any one hull's first scan.
	TourStartJitterMaxSeconds int `mapstructure:"tour_start_jitter_max_seconds"`

	// MaxRepositionJumps bounds the EXPENDABLE-probe reposition reach the standing
	// scout_post_coordinator resolves over the PERSISTED stored adjacency (sp-8k9m): the
	// nearest-satellite selection AND the dispatched relay both route PAST unreadable
	// frontier gates up to this many jumps, reaching the posts that sit beyond the strict
	// heavy-hull cap (gategraph.MaxJumpPath=5). Measured worst-case charted depth from the
	// probe supply to the darkest posts was 6-12 jumps, so 0/absent => 12. The strict cap is deliberately NOT raised — only the probe class,
	// whose arrival re-reads the gate it crossed, is allowed this reach.
	MaxRepositionJumps int `mapstructure:"max_reposition_jumps"`

	// RepositionFailureCooldownSecs is how long a scout post whose reposition relay FAILED
	// waits before the coordinator retries repositioning to it (sp-o34q). On a failure the
	// coordinator frees the probe and tries the NEXT candidate post this tick instead of
	// respawning the same corpse, so one genuinely-unroutable post can no longer crash-loop
	// the relay dispatcher and flood the event queue. 0/absent => 1800s (30 min): long enough that a broken
	// post is retried on the order of the frontier's own change cadence, not every 30s tick.
	RepositionFailureCooldownSecs int `mapstructure:"reposition_failure_cooldown_secs"`

	// RespawnAttemptCap bounds how many CONSECUTIVE times the standing scout_post_coordinator
	// respawns a post's dead tour before it PARKS the post for a backoff window instead of
	// respawning it yet again (sp-py4n). The reconciler respawns any dead tour every tick, so a
	// tour crashing on a PERSISTENT non-cross-system reason would respawn-loop at tick cadence
	// forever; this caps that loop. A tour that finally runs healthy resets the count, so the cap
	// is on consecutive failures, not lifetime, and the count is persisted per post so it survives
	// a daemon restart (a crash-loop that reset on every restart would never cap). 0/absent => 10:
	// ~5 min of 30s-tick respawns before parking — long enough to ride out a transient blip, short
	// enough to stop a genuinely-broken post from flooding the fleet.
	RespawnAttemptCap int `mapstructure:"respawn_attempt_cap"`

	// RespawnCapDisabled turns OFF the sp-py4n respawn-loop cap entirely, restoring the
	// pre-py4n behavior where a dead tour is respawned every tick without limit. false/absent =>
	// LIVE: the cap is on by default. RULINGS #5 disable escape so a captain can lift the cap
	// without a redeploy if it ever mis-parks a post that should keep retrying; not expected to be
	// set in normal operation.
	RespawnCapDisabled bool `mapstructure:"respawn_cap_disabled"`

	// HeavyShipTypes is the set of ship types that count as HEAVY freight for
	// shipyard discovery (sp-42ow): the scout tour's piggybacked shipyard scan
	// emits a one-time-per-era milestone event when a yard selling one of these
	// is first discovered, and the fleet autosizer's nearest-reachable-heavy-yard
	// signal keys on the same classification. Empty/absent defers to the domain
	// default {SHIP_HEAVY_FREIGHTER, SHIP_BULK_FREIGHTER} (RULINGS #5).
	HeavyShipTypes []string `mapstructure:"heavy_ship_types"`

	// CoverageSpreadDisabled turns OFF the sp-6ovd coverage-first manning order in the
	// standing scout_post_coordinator, reverting to the legacy depth-first order (all of a
	// post's slots before the next post's). false/absent => LIVE: the reconciler interleaves
	// unmanned slots by tier so a scarce idle-probe pool spreads one-per-uncovered-system
	// before piling a multi-hull post's extra slots — the durable fix for the reconciler
	// herding the whole probe group onto one target per cycle. RULINGS #5 disable escape: a captain can pin
	// depth-first without a redeploy; not expected to be set in normal operation.
	CoverageSpreadDisabled bool `mapstructure:"coverage_spread_disabled"`

	// GateReconcileEnabled arms the sp-bcsu RETROACTIVE gate-reconcile sweep in the standing
	// scout_post_coordinator: a bounded pass that dispatches leftover idle probes to chart
	// market-known-but-gate-uncharted frontier systems (Part 1 charts the gate on arrival),
	// automating the manual UF64 procedure that clears the market-swept-but-gate-empty backlog
	// the strict pathfinder strands hulls on. false/absent => OFF (deploy-inert): the sweep
	// moves probes and spends API budget, so it is opt-in — armed once the backlog needs
	// clearing. RULINGS #5 opt-in escape, mirroring the disable-escape bools.
	GateReconcileEnabled bool `mapstructure:"gate_reconcile_enabled"`

	// GateReconcileMaxDispatch HARD-CAPS how many gate-reconcile relays the sweep dispatches
	// per tick (sp-bcsu) — the rate-budget guard so it can never burst the limiter or starve
	// trade hulls of it. 0/absent => defaultGateReconcileMaxDispatch (2), mirroring
	// MaxRepositionJumps' 0 => default idiom.
	GateReconcileMaxDispatch int `mapstructure:"gate_reconcile_max_dispatch"`

	// GateReconcileMarketlessDisabled is the sp-ywh1 disable-escape: it reverts the widened
	// gate-reconcile sweep to the market-only sp-bcsu backlog, dropping the traffic-markered
	// MARKETLESS transit gates from the target set. false/absent => LIVE (the widened scope is
	// ON whenever gate_reconcile_enabled arms the sweep): the sweep also charts uncharted transit
	// systems a stale backoff marker proves traffic jumps THROUGH — the residual GetJumpGate-400
	// source the market-scoped enumeration structurally cannot reach. Set true to pin market-only
	// without a redeploy. RULINGS #5 disable escape, mirroring coverage_spread_disabled /
	// respawn_cap_disabled (bool ⇒ liveconfig-only; the tune registry is int-typed).
	GateReconcileMarketlessDisabled bool `mapstructure:"gate_reconcile_marketless_disabled"`
}
