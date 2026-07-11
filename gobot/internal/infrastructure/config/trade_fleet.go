package config

// TradeFleetConfig holds the trade-fleet coordinator's knobs (sp-1278). The daemon
// injects these into the coordinator container's launch config on every build —
// creation AND restart recovery, via resolveTradeFleetConfig — so a captain retunes
// the standing relaunch loop (the on/off switch, the per-hull cooldown, the
// concurrency cap, and the per-tour spend/reserve caps) by editing config.yaml and
// restarting the daemon, with NO code redeploy (sp-ts82 live-config pattern, RULINGS
// #2/#5).
//
// A zero value means "unset" and defers to the coordinator's documented default for
// that knob, so the daemon injects only the keys the captain actually set — it never
// hardcodes an operational value. Enabled is the one exception (a *bool) so an unset
// config defaults ON while an explicit `enabled: false` is a real off-switch.
type TradeFleetConfig struct {
	// Enabled turns the coordinator ON (default true, sp-1278 intent). A *bool so an
	// unset config (nil) is distinct from an explicit `enabled: false`: the captain
	// parks the entire relaunch loop without unpinning any hull. SetDefaults resolves
	// nil to true.
	Enabled *bool `mapstructure:"enabled"`

	// CooldownSeconds is the per-hull wait after an honest tour exit before relaunch
	// (0 => the coordinator default, 180s). It lets the local ground breathe through
	// the rich->tapped->rich cycle so the next tour re-plans against a recovered market.
	CooldownSeconds int `mapstructure:"cooldown_seconds"`

	// MaxConcurrentTours caps simultaneously-running trade tours (0 => unlimited,
	// bounded naturally by fleet size — every idle trade hull is relaunched). Set a
	// positive cap to bound concurrent capital deployment / API load.
	MaxConcurrentTours int `mapstructure:"max_concurrent_tours"`

	// TickSeconds is the reconcile cadence (0 => the coordinator default, 30s).
	TickSeconds int `mapstructure:"tick_seconds"`

	// Per-tour launch caps, passed verbatim to each StartTourRun. 0 => the tour's own
	// documented default for that knob (MaxHops->6, MaxSpend->25% of live treasury,
	// ReplanLimit->2, WorkingCapitalReserve->the non-tunable floor). Iterations is NOT
	// configurable: every relaunched tour is continuous (-1) by construction — a finite
	// tour would exit and park the hull, the sink this coordinator retires.
	MaxHops               int   `mapstructure:"max_hops"`
	MaxSpend              int64 `mapstructure:"max_spend"`
	MinMargin             int   `mapstructure:"min_margin"`
	ReplanLimit           int   `mapstructure:"replan_limit"`
	WorkingCapitalReserve int64 `mapstructure:"working_capital_reserve"`

	// WorkingCapitalReserveTreasuryPct is the sp-yqx4 counter-cyclical floor as a percent of
	// LIVE treasury: each tour buy is floored at max(50k, min(working_capital_reserve, pct% ×
	// treasury)) so a reserve above the treasury can no longer deadlock the fleet (6/9 heavies
	// idled at sub-1M). 0/absent → the tour's 40% default (common.DefaultReserveTreasuryPct,
	// pending the trade-analyst's ruling). Config, not a constant (RULINGS #5), so the ruled
	// number lands on a restart with no redeploy.
	WorkingCapitalReserveTreasuryPct int `mapstructure:"working_capital_reserve_treasury_pct"`

	// RelaunchBackoffMaxMinutes caps the per-hull ADAPTIVE relaunch backoff (sp-1pli): when a
	// hull's continuous tour exits unproductive (fast-fail — no plausible trade leg flown), the
	// coordinator doubles THAT hull's relaunch cooldown from CooldownSeconds, up to this
	// ceiling, instead of retrying the full discovery+solver pass at the base cooldown forever.
	// Any PRODUCTIVE exit resets the hull straight back to the base cooldown. In minutes (not
	// seconds, unlike its siblings above) because 30 default minutes is the natural unit for a
	// human-tuned ceiling. 0/absent → 30 min. Escalation state is in-memory only (not persisted
	// like the base cooldown) — a coordinator restart resets every hull to base, a deliberate,
	// self-healing trade-off (see the handler's hullBackoff doc).
	RelaunchBackoffMaxMinutes int `mapstructure:"relaunch_backoff_max_minutes"`

	// MassParkExemptDisabled turns OFF the sp-nkci restart-mass-park exemption. When a daemon
	// blip/restart force-parks the whole trade fleet in one window, the sp-1pli adaptive backoff
	// would misread that synchronized park as fleet-wide thin depth and ramp every hull's cooldown
	// to ~12min in lockstep (~fleet-wide idle for 15-40min after every restart). The exemption
	// treats a synchronized mass-park as non-signal so it does NOT feed the backoff. Live by
	// default (a bare bool: absent/false = exemption ON) — set true only to restore the old ramp.
	MassParkExemptDisabled bool `mapstructure:"masspark_exempt_disabled"`

	// MassParkWindowSeconds is how close together (sp-nkci) idle hull releases must be to count as
	// one synchronized mass-park. 0/absent → the coordinator default (120s), which comfortably
	// spans a restart's force-release sweep. Widen it if a slow restart parks the fleet over a
	// longer window. Config, not a constant (RULINGS #5) — retuned by editing config.yaml +
	// restarting the daemon, no redeploy.
	MassParkWindowSeconds int `mapstructure:"masspark_window_seconds"`

	// MassParkMinHulls is how many idle hulls must have released within MassParkWindowSeconds of
	// each other before the park is treated as a restart mass-park (sp-nkci) rather than organic
	// thin-depth. 0/absent → the coordinator default (4): well above any organic 1-2-hull
	// coincidence, well below the ~10-heavy fleet a blip parks at once. Config (RULINGS #5).
	MassParkMinHulls int `mapstructure:"masspark_min_hulls"`

	// StrandedConsecutiveThreshold is the sp-686e stranded-hull detector threshold: how many
	// CONSECUTIVE origin-level empty reposition discoveries (no durable adjacency + gate
	// inaccessible — the TORWIND-2C shape, where both discovery paths return empty so the hull
	// can never self-reposition) a hull must accrue before the tour coordinator pages the watch
	// with a distinct WARN + the fleet_hull_stranded_total counter (the StrandedHull alert),
	// instead of the hull silently relaunch-looping until a human notices. 0/absent → the tour
	// coordinator's own default (3); the default lives in the consumer, not this config layer.
	// Threaded through the tour container config so a captain retunes it by editing config.yaml
	// + restarting the daemon (RULINGS #5, no code redeploy).
	StrandedConsecutiveThreshold int `mapstructure:"stranded_consecutive_threshold"`

	// RepositionJumpBound is the sp-kl16 jump bound a tour reposition resolves its cross-system
	// leg over the PERSISTED stored adjacency (RepositionPath) with, routing PAST an unreadable
	// frontier gate rather than fail-closing on it via the strict fetch-through Path. A tour
	// reposition is a MOVEMENT of the hull to a fresh trading ground — not a commitment of money —
	// so it shares the scout reposition's stored-adjacency relaxation (sp-8k9m): a heavy whose
	// ORIGIN gate sits in the sp-ikx1 unreadable-backoff set (the C1-blocking TORWIND-37/2C ->
	// GQ92 incident, unroutable within the strict MaxJumpPath=5 even for a 2-hop route) can still
	// reposition. 0/absent → the tour coordinator's own default (12, matching the scout frontier
	// depth); the default lives in the consumer, not this config layer. Threaded through the tour
	// container config so a captain retunes it by editing config.yaml + restarting the daemon
	// (RULINGS #5, no code redeploy). The buy-side (arb pre-buy, trade-route lane commits, cargo
	// delivery) keeps the strict Path — money-commitment vs hull-movement is the guard line.
	RepositionJumpBound int `mapstructure:"reposition_jump_bound"`
}

// EnabledOrDefault reports whether the coordinator is enabled, treating an unset (nil)
// value as true — the default-ON behavior the bead intends (sp-1278).
func (c TradeFleetConfig) EnabledOrDefault() bool {
	return c.Enabled == nil || *c.Enabled
}
