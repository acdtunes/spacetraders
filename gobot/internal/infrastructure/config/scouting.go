package config

// ScoutingConfig holds the scouting subsystem's knobs. The daemon injects
// these into scout_tour and scout_post_coordinator launch configs on every build —
// creation AND restart recovery, via resolveScoutingConfig — so a captain retunes the
// fleet's phase behavior by editing config.yaml and restarting, no code redeploy
// (sp-ts82 live-config pattern, RULINGS #5).
//
// A zero value means "unset" and defers to the handler's own documented default for
// that knob, so the daemon injects only the keys the captain actually set.
type ScoutingConfig struct {
	// TourStartJitterMaxSeconds bounds the per-ship deterministic phase jitter a scout
	// tour waits before its first navigation/scan. ~45 scouts restarting
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
	// waits before the coordinator retries repositioning to it. On a failure the
	// coordinator frees the probe and tries the NEXT candidate post this tick instead of
	// respawning the same corpse, so one genuinely-unroutable post can no longer crash-loop
	// the relay dispatcher and flood the event queue. 0/absent => 1800s (30 min): long enough that a broken
	// post is retried on the order of the frontier's own change cadence, not every 30s tick.
	RepositionFailureCooldownSecs int `mapstructure:"reposition_failure_cooldown_secs"`

	// RespawnAttemptCap bounds how many CONSECUTIVE times the standing scout_post_coordinator
	// respawns a post's dead tour before it PARKS the post for a backoff window instead of
	// respawning it yet again. The reconciler respawns any dead tour every tick, so a
	// tour crashing on a PERSISTENT non-cross-system reason would respawn-loop at tick cadence
	// forever; this caps that loop. A tour that finally runs healthy resets the count, so the cap
	// is on consecutive failures, not lifetime, and the count is persisted per post so it survives
	// a daemon restart (a crash-loop that reset on every restart would never cap). 0/absent => 10:
	// ~5 min of 30s-tick respawns before parking — long enough to ride out a transient blip, short
	// enough to stop a genuinely-broken post from flooding the fleet.
	RespawnAttemptCap int `mapstructure:"respawn_attempt_cap"`

	// HeavyShipTypes is the set of ship types that count as HEAVY freight for
	// shipyard discovery: the scout tour's piggybacked shipyard scan
	// emits a one-time-per-era milestone event when a yard selling one of these
	// is first discovered, and the fleet autosizer's nearest-reachable-heavy-yard
	// signal keys on the same classification. Empty/absent defers to the domain
	// default {SHIP_HEAVY_FREIGHTER, SHIP_BULK_FREIGHTER} (RULINGS #5).
	HeavyShipTypes []string `mapstructure:"heavy_ship_types"`

	// ShipyardRescanTTLSeconds is the recency window between live shipyard reads at one
	// waypoint. The shipyard scan piggybacks every scout market visit, so overlapping
	// scout routes re-read the same yards far more often than their contents change; this
	// window collapses that clustering. A yard with no scan yet is ALWAYS read immediately
	// — the window bounds re-reads, never discovery. Sized against the price consumers, not
	// the discovery ones: the stored purchase_price feeds the reachable-yard ranking and the
	// autosizer's heavy-price signal, so this is also the staleness bound on a money-guard
	// input and does not belong in the hours range. 0/absent => 15 minutes (RULINGS #5).
	ShipyardRescanTTLSeconds int `mapstructure:"shipyard_rescan_ttl_seconds"`
}
