package config

// ScoutingConfig holds the scouting subsystem's knobs. The daemon injects
// these into scout_tour launch configs on every build —
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
	// fleet decoheres into a spread instead of stacking on every rotation. 0/absent => 120s,
	// sized so ~45 scouts spread without materially delaying any one hull's first scan.
	TourStartJitterMaxSeconds int `mapstructure:"tour_start_jitter_max_seconds"`

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
