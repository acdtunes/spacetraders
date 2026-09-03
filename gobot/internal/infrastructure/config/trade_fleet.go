package config

import "time"

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
	// ReplanLimit->2, WorkingCapitalReserve->the non-tunable floor, sp-05glh flat).
	// Iterations is NOT configurable: every relaunched tour is continuous (-1) by
	// construction — a finite tour would exit and park the hull, the sink this
	// coordinator retires.
	MaxHops               int   `mapstructure:"max_hops"`
	MaxSpend              int64 `mapstructure:"max_spend"`
	MinMargin             int   `mapstructure:"min_margin"`
	ReplanLimit           int   `mapstructure:"replan_limit"`
	WorkingCapitalReserve int64 `mapstructure:"working_capital_reserve"`

	// ACapTranches is the FLEET-WIDE sink cap in trade_volume tranches; 0 → defaultTourACapTranches, which says why it moves in step with the solver's own cap.
	ACapTranches int `mapstructure:"acap_tranches"`

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
	// inaccessible, where both discovery paths return empty so the hull
	// can never self-reposition) a hull must accrue before the tour coordinator pages the watch
	// with a distinct WARN + the fleet_hull_stranded_total counter (the StrandedHull alert),
	// instead of the hull silently relaunch-looping until a human notices. 0/absent → the tour
	// coordinator's own default (3); the default lives in the consumer, not this config layer.
	// Threaded through the tour container config so a captain retunes it by editing config.yaml
	// + restarting the daemon (RULINGS #5, no code redeploy).
	StrandedConsecutiveThreshold int `mapstructure:"stranded_consecutive_threshold"`

	// RepositionJumpBound is the jump bound a tour reposition resolves its cross-system
	// leg over the PERSISTED stored adjacency (RepositionPath) with, routing PAST an unreadable
	// frontier gate rather than fail-closing on it via the strict fetch-through Path. A tour
	// reposition is a MOVEMENT of the hull to a fresh trading ground — not a commitment of money —
	// so it shares the scout reposition's stored-adjacency relaxation (sp-8k9m): a heavy whose
	// ORIGIN gate sits in the unreadable-backoff set can still
	// reposition. 0/absent → the tour coordinator's own default (12, matching the scout frontier
	// depth); the default lives in the consumer, not this config layer. Threaded through the tour
	// container config so a captain retunes it by editing config.yaml + restarting the daemon
	// (RULINGS #5, no code redeploy). The buy-side (arb pre-buy, trade-route lane commits, cargo
	// delivery) keeps the strict Path — money-commitment vs hull-movement is the guard line.
	RepositionJumpBound int `mapstructure:"reposition_jump_bound"`

	// MaxTourSystems is the per-tour DISTINCT-system cap (start system + gate
	// neighbors) — the fleet-wide tour-length lever that reverses the tour-length clamp
	// SAFELY. Like RepositionJumpBound/StrandedConsecutiveThreshold it is a daemon-global
	// tour tuning: StartTourRun stamps it from here into every tour container's launch
	// config, buildTourCoordinatorCommand reads it back, and it rides TourConstraints to
	// the Python solver. 0/absent → the solver's own MAX_TOUR_SYSTEMS default (2), so an
	// unset knob is byte-identical to today; the default lives in the consumer (the solver),
	// not this config layer. A positive value sweeps tour length by editing config.yaml +
	// restarting the daemon (RULINGS #5, no code redeploy).
	MaxTourSystems int `mapstructure:"max_tour_systems"`

	// TourPlanConcurrency caps concurrent tour planners per player; 0 → SetPlanConcurrency's default.
	TourPlanConcurrency int `mapstructure:"tour_plan_concurrency"`

	// ClosedTours arms closed-circuit (return-to-anchor) tour mode (sp-im74 built the
	// solver + the cmd.ClosedTours -> TourConstraints.Closed thread; this is the config
	// knob im74 deferred). Like MaxTourSystems it is a daemon-global tour tuning:
	// StartTourRun stamps it from here into every tour container's launch config,
	// buildTourCoordinatorCommand reads it back into cmd.ClosedTours, and it rides
	// TourConstraints.Closed to the Python solver's closed-circuit path. A bare bool, so
	// absent/false => cmd.ClosedTours=false => OPEN tours, byte-identical to today (the
	// default-off governance gate — nothing arms without an explicit captain config
	// change). true makes each planned tour end at the anchor (the ship's FLOATING
	// current system when no explicit anchor is set — im74 handles the floating anchor).
	// Arm by editing config.yaml + restarting the daemon (RULINGS #5, no code redeploy).
	ClosedTours bool `mapstructure:"closed_tours"`

	// CargoBlocklist names goods the tour planner must NEVER trade as cargo (sp-o4wa, epic
	// sp-g9td): the economy-analyst's sub-70-cr/u noise goods whose per-leg dock+dwell tempo
	// cost exceeds the cargo value (recommended value: [FUEL, ALUMINUM, PLASTICS]). UNLIKE the
	// launch-config tour knobs above (MaxTourSystems/ClosedTours, stamped per-container), this
	// is a daemon-global list injected straight into the tour coordinator handler via
	// SetCargoBlocklist at boot — the SAME global-config → setter mechanism the contract
	// pre_positioning.blocklist uses. The handler filters it out of the market snapshot before
	// the solver sees it, so a listed good is unselectable as buy source OR sell sink. Absent/
	// empty ⇒ no filtering ⇒ byte-identical to today (the default-off governance gate: nothing
	// arms without an explicit captain config change). This is FUEL-as-tradeable-CARGO only —
	// ship refueling is a separate path that never reads the tour snapshot. Arm by editing
	// config.yaml + restarting the daemon (RULINGS #5, no code redeploy).
	CargoBlocklist []string `mapstructure:"cargo_blocklist"`

	// ConstructionCargoBlocklist, unlike CargoBlocklist, applies only while construction is active.
	ConstructionCargoBlocklist []string `mapstructure:"construction_cargo_blocklist"`

	// --- Placement/relocation scoring loop (sp-z7ng, epic sp-fguo Layer-B) ---
	// Daemon-global tour tunings, same for every tour container (the mirror of
	// reposition_jump_bound/max_tour_systems above): StartTourRun stamps them into every tour
	// launch config, buildTourCoordinatorCommand reads them back onto the command. Zero =
	// unset ⇒ defer to the consumer's default (the default lives in the coordinator, not here),
	// so an absent knob is byte-identical to today. Shipped ARMED (RULINGS #22): PlacementDisabled
	// defaults FALSE — a kill switch, not an arming gate.
	PlacementDisabled          bool `mapstructure:"placement_disabled"`
	PlacementBetaWindowMinutes int  `mapstructure:"placement_beta_window_minutes"`
	PlacementParkFloorPct      int  `mapstructure:"placement_park_floor_pct"`
	PlacementShortlistTopN     int  `mapstructure:"placement_shortlist_top_n"`
	PlacementHorizonMinutes    int  `mapstructure:"placement_horizon_minutes"`

	// --- Reposition reach (sp-uf64) ---
	// Daemon-global tour tunings, same for every tour container (the mirror of
	// closed_tours/placement_* above): StartTourRun stamps them into every tour launch config,
	// buildTourCoordinatorCommand reads them back onto the command. RepositionReachEnabled defaults
	// FALSE, so the legacy 1-hop-first reposition runs unchanged (the governance gate — nothing arms
	// without an explicit captain config change). The two int knobs default to 0 ⇒ the coordinator's
	// own reposition_reach_{hop_decay_pct,max_hulls_per_system} default (85, 5), so an absent knob is
	// byte-identical to today; the default lives in the consumer, not here. Arm/retune by editing
	// config.yaml + restarting the daemon (RULINGS #5, no code redeploy).
	RepositionReachEnabled           bool `mapstructure:"reposition_reach_enabled"`
	RepositionReachHopDecayPct       int  `mapstructure:"reposition_reach_hop_decay_pct"`
	RepositionReachMaxHullsPerSystem int  `mapstructure:"reposition_reach_max_hulls_per_system"`

	// Own-trade recency de-ranking: stamped and read back as the reach knobs above, but shipped
	// ARMED (RULINGS #22) — ints at 0 ⇒ the coordinator's fitted defaults, kill switch FALSE ⇒
	// live with no config present. Retune via config.yaml + restart.
	OwnTradePenaltyPct      int  `mapstructure:"reposition_own_trade_penalty_pct"`
	OwnTradeColdMinutes     int  `mapstructure:"reposition_own_trade_cold_minutes"`
	OwnTradePenaltyDisabled bool `mapstructure:"reposition_own_trade_penalty_disabled"`

	// --- Rate-floor early-reposition (epic sp-fguo, Part 2) ---
	// Daemon-global tour tunings, same for every tour container (the mirror of reposition_reach_*
	// above): StartTourRun stamps them into every tour launch config, buildTourCoordinatorCommand
	// reads them back onto the command. RepositionRateFloorEnabled defaults FALSE, so the trigger is
	// dormant and the productive-tour path is byte-identical to today (the governance gate — nothing
	// arms without an explicit captain config change). The three int knobs default to 0 ⇒ the
	// coordinator's own reposition_rate_floor_{pct,improvement_pct,dwell_minutes} default (40, 200,
	// 15), so an absent knob is byte-identical to today; the default lives in the consumer, not here.
	// Arm/retune by editing config.yaml + restarting the daemon (RULINGS #5, no code redeploy).
	RepositionRateFloorEnabled        bool `mapstructure:"reposition_rate_floor_enabled"`
	RepositionRateFloorPct            int  `mapstructure:"reposition_rate_floor_pct"`
	RepositionRateFloorImprovementPct int  `mapstructure:"reposition_rate_floor_improvement_pct"`
	RepositionRateFloorDwellMinutes   int  `mapstructure:"reposition_rate_floor_dwell_minutes"`

	// --- Wider candidate set (sp-jsng, epic sp-fguo build-decomp #5; the #1 fleet-$/hr lever, sp-7q5t) ---
	// Daemon-global tour tunings, same for every tour container (the mirror of closed_tours /
	// max_tour_systems above): StartTourRun stamps them into every tour launch config,
	// buildTourCoordinatorCommand reads them back onto the command. sp-jsng BUILT the widened
	// candidate producer (widenedTourSystems + the profitable-edge shortlist) but DEFERRED this
	// config knob, so the widening was unreachable in production. Both knobs are DOUBLE default-safe:
	// (1) CandidateHopDepth 0/absent ⇒ resolveCandidateHopDepth floors to 1 ⇒ widenedTourSystems
	// returns the exact live 1-hop set, byte-identical to today; AND (2) effectiveCandidateHopDepth
	// independently holds the depth at 1 unless MaxTourSystems > 2 (the solver clamp is lifted), so a
	// lone candidate_hop_depth edit can never feed a non-gate-adjacent set to the flat-pricing solver.
	// The default lives in the consumer (the coordinator's resolveCandidate* helpers: 1 / 6), not this
	// config layer. Arm by setting candidate_hop_depth: 2 (with max_tour_systems already > 2) + restart
	// (RULINGS #5, no code redeploy).
	CandidateHopDepth      int `mapstructure:"candidate_hop_depth"`
	CandidateShortlistTopN int `mapstructure:"candidate_shortlist_top_n"`

	// Tour-graph neighbours from the persisted gate adjacency; absent → today's live read.
	TourNeighborsDurableFirst bool `mapstructure:"tour_neighbors_durable_first"`

	// --- Recovery-externality pricing ---
	// Daemon-global tour tuning (the mirror of candidate_hop_depth above): StartTourRun stamps
	// it into every tour launch config, buildTourCoordinatorCommand reads it back, and the
	// solver charges each planned sell tranche for the recovery burden it imposes on the rest
	// of the fleet — so at equal spread a hull prefers the sink the fleet is not still
	// recovering. A FLOAT, deliberately: the coefficient is fitted, not a step. 0/absent is the
	// UNARMED default (no charge, byte-identical to today) and is also the documented revert —
	// set 0 + restart the daemon, the candidate_hop_depth numeric-knob pattern, no flag.
	ExternalityWeight float64 `mapstructure:"externality_weight"`

	// --- Liveness watchdog (sp-m3122) ---
	// WatchdogStallSeconds is how long a RUNNING tour may make ZERO real progress
	// (plan/navigate/arrive/buy/sell) before the coordinator declares it HUNG, kills the
	// container, and relaunches a fresh tour — the automated form of the manual
	// `container stop <hung-tour>`. The watchdog has NO on/off gate:
	// it ships ARMED (a daemon restart routinely strands hulls this way, so it must always
	// heal). This is the one tunable, an operational value (RULINGS #5), not an arm-seam: a
	// captain who ever needs to soften it raises the threshold. 0/absent → the coordinator's
	// own default (720s / 12 min), which lives in the consumer, not this config layer. A hull
	// IN_TRANSIT is never killed regardless (a flying hull is progressing), so this bounds only
	// PARKED-and-silent time.
	WatchdogStallSeconds int `mapstructure:"watchdog_stall_seconds"`

	// --- Sink freshness clause on the firm-sink buy gate (sp-tgll8 item 2) ---
	// SinkFreshnessMaxMinutes is the maximum age a downstream sink's cached market_data may
	// carry at BUY EXECUTION before the tour refuses the buy — the "FRESH" clause of the
	// Admiral principle "never buy cargo we cannot GUARANTEED-RESERVED-REACHABLE-FRESH-DEEP
	// sell". sp-pcxju gates FIRM+DEEP off the reservation; this re-reads the sink's LIVE
	// market and fail-closes on stale data. Daemon-global: injected into the tour coordinator
	// handler via SetSinkFreshness at boot (the same cfg.TradeFleet → handler-setter path
	// CargoBlocklist uses), re-read on every restart. It ships ARMED (no on/off flag,
	// RULINGS #5 operational value): 0/absent → the documented default below,
	// byte-identical for any genuinely fresh sink. A captain softens it by RAISING the
	// threshold (like the watchdog), never disabling.
	//
	// It is the BOOT floor only, and a floor is not the cap: the effective cap is
	// max(floor, the live scan rotation's own anti-starvation bound) (sp-k4z5b). The LIVE
	// lever is the tour operation's SINGLE market-data freshness knob — `spacetraders tune
	// --operation tour market_data_max_age_minutes <N>` — which lands next tick
	// with no daemon bounce, and is what an operator should reach for mid-incident. Reach
	// for this key only when the daemon must come up on an already-widened floor.
	SinkFreshnessMaxMinutes int `mapstructure:"sink_freshness_max_minutes"`

	// --- Inventory-pressure governor (sp-tgll8 item 1) ---
	// FullHullPausePct is the percentage of trade-fleet hulls sitting FULL (cargo at capacity,
	// unable to offload) above which the coordinator PAUSES relaunching EMPTY idle hulls into
	// NEW buying tours this tick — governing buy-rate by sell-rate so the fleet stops buying
	// what it cannot sell. LADEN idle hulls ALWAYS relaunch so they can drain (never block a
	// sell — RULINGS #4 no-wedge), so a saturated fleet un-throttles itself as it sells down.
	// It ships ARMED (no on/off flag): 0/absent → the coordinator's 65% default (a healthy
	// fleet is nowhere near 65% FULL, so byte-identical below saturation). A captain softens it
	// by RAISING the threshold toward 100 (never reached, so effectively off), never disabling.
	FullHullPausePct int `mapstructure:"full_hull_pause_pct"`

	// --- MVT trade loop (spec docs/superpowers/specs/2026-09-02-mvt-trade-loop-design.md) ---
	// MVT trade loop knobs (spec §5). Zero means "use the code default".
	YieldWindowSells          int `mapstructure:"yield_window_sells"`
	YieldMinSells             int `mapstructure:"yield_min_sells"`
	ClaimReachHops            int `mapstructure:"claim_reach_hops"`
	ClaimReachMaxHops         int `mapstructure:"claim_reach_max_hops"`
	RankerMinSpreadPerUnit    int `mapstructure:"ranker_min_spread_per_unit"`
	SpecialistFractionPct     int `mapstructure:"specialist_fraction_pct"`
	FatLaneMultiplePct        int `mapstructure:"fat_lane_multiple_pct"`
	SpecialistCadenceMinutes  int `mapstructure:"specialist_cadence_minutes"`
	YieldRateSpanFloorMinutes int `mapstructure:"yield_rate_span_floor_minutes"`
	MVTRescueJumpsPerEpisode  int `mapstructure:"mvt_rescue_jumps_per_episode"`
	MVTJumpFeeMaxSharePct     int `mapstructure:"mvt_jump_fee_max_share_pct"`
	MVTRecentlyLeftMinutes    int `mapstructure:"mvt_recently_left_minutes"`
}

// defaultSinkFreshnessMaxMinutes is the sink-freshness clause's boot floor, applied when
// the [trade_fleet] knob is unset so the clause ships ARMED.
//
// TWELVE HOURS, because the floor is what the guard runs on whenever the rotation bound
// cannot be derived — the window a restart opens before the map has been counted, and any
// stretch where the census behind that count is unreadable. In that window the floor IS the
// cap, so it has to be a number the fleet's real market-refresh interval fits inside.
//
// The previous value was sized against a map of a few hundred markets and did not survive
// the map growing. Refusing on it is not the conservative choice it looks like: the firm-sink
// gate fails closed, so a floor below the rotation turns rows that are merely waiting their
// turn into tours that never run, and the fleet stops trading rather than trades carefully.
// This is the value an operator already had to reach for by hand, mid-incident, to get
// throughput back.
//
// It is a FLOOR and not a ceiling. Raising it never disarms anything, and the money guard's
// arming stays a separate boot decision: only a non-positive [trade_fleet] value leaves the
// clause inert, and this default is never non-positive.
const defaultSinkFreshnessMaxMinutes = 720

// ResolvedSinkFreshnessMaxAge returns the configured sink-freshness ceiling as a Duration, or
// the documented default when unset (non-positive) — so the daemon always injects a POSITIVE
// age and the clause ships ARMED (sp-tgll8 item 2). Centralizes default resolution the way the
// [trade_impact] Resolved* helpers do.
func (c TradeFleetConfig) ResolvedSinkFreshnessMaxAge() time.Duration {
	minutes := c.SinkFreshnessMaxMinutes
	if minutes <= 0 {
		minutes = defaultSinkFreshnessMaxMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// EnabledOrDefault reports whether the coordinator is enabled, treating an unset (nil)
// value as true — the default-ON behavior the bead intends (sp-1278).
func (c TradeFleetConfig) EnabledOrDefault() bool {
	return c.Enabled == nil || *c.Enabled
}
