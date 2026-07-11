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
	// their rotation in near-lockstep transiently saturated the rate limiter — the
	// 2026-07-11 00:56-01:02Z burst showed p99 limiter wait plateaued ~4s and Purchase
	// Cargo p99 at 9.5s, while 15m-average utilization read a calm 53%: a phase-locked
	// wave, not a sustained-load problem. Each ship waits hash(ship_symbol) % ceiling
	// before its tour starts — deterministic across restarts (no math/rand) — so the
	// fleet decoheres into a spread instead of stacking on every rotation. The standing
	// scout_post_coordinator waits hash(container_id) % ceiling the same way before its
	// reconcile loop starts. 0/absent => 120s, sized so ~45 scouts spread across two
	// effective reconcile ticks without materially delaying any one hull's first scan.
	TourStartJitterMaxSeconds int `mapstructure:"tour_start_jitter_max_seconds"`
}
