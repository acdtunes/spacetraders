package config

// FleetAutosizerConfig holds the fleet capacity autosizer's knobs (sp-1txd). It nests under
// the top-level [fleet_autosizer] section and is injected into the fleet_autosizer container's
// launch config on every build — creation AND restart recovery, via resolveFleetAutosizerConfig
// — so a captain retunes the sizing/buying behaviour by editing config.yaml and restarting,
// with NO code redeploy (the sp-ts82 live-config pattern, RULINGS #2/#5).
//
// Every knob follows the codebase idiom: a zero value means "unset" and defers to the
// coordinator's documented protective default (resolved once in the handler's
// resolveFleetAutosizerConfig). The two default-TRUE bools are *bool so nil (unset) can be told
// apart from an explicit false. The Analyst/Admiral own these numbers — they are all config,
// never call-site constants (RULINGS #5). Every purchase decision logs which knob would have
// blocked at what value, so the captain retunes from evidence (the iv65 park-line idiom).
type FleetAutosizerConfig struct {
	// --- master + per-class escapes (LIVE BY DEFAULT; Admiral: no dark-shipping) ---

	// AutosizerDisabled stands the WHOLE autosizer down. Absent/false = ACTIVE, so an
	// absent-config boots live (pinned by test). Set true only in an emergency.
	AutosizerDisabled bool `mapstructure:"autosizer_disabled"`
	// DryRun evaluates every buy decision and logs what it WOULD purchase (with full
	// arithmetic) but spends nothing. NOT dark-shipping — it WARNs loudly every tick
	// (no-silent-dry-run rule) and the zero-effect alarm still fires.
	DryRun bool `mapstructure:"dry_run"`
	// LightsDisabled / HeaviesDisabled freeze one class while the other keeps running (the
	// captain can pause heavy buys during an absorption dip without stopping worker buys).
	LightsDisabled  bool `mapstructure:"lights_disabled"`
	HeaviesDisabled bool `mapstructure:"heavies_disabled"`
	// WarehouseHullsEnabled opts INTO the warehouse-hull class (default OFF until the
	// dispatch step is armed — sp-1txd M7). Unlike lights/heavies it is opt-in, not live.
	WarehouseHullsEnabled bool `mapstructure:"warehouse_hulls_enabled"`

	// --- cadence + purchase pacing ---

	// TickIntervalSecs is the slow autosizer cadence (sizing is strategic). 0/absent → 900s.
	TickIntervalSecs int `mapstructure:"tick_interval_secs"`
	// PurchaseCapPerTick bounds hulls bought per tick, across ALL classes (protects the
	// treasury from a runaway multi-buy on one tick). 0/absent → 1.
	PurchaseCapPerTick int `mapstructure:"purchase_cap_per_tick"`

	// --- fleet ceilings (the HARD API-request-budget bound: each hull adds request load) ---

	// FleetCeilingTotal caps the absolute fleet size the autosizer will grow to. 0/absent →
	// defaultFleetCeilingTotal. FleetCeiling{Lights,Heavies,Warehouse} cap each class; 0/absent
	// → that class's documented default.
	FleetCeilingTotal     int `mapstructure:"fleet_ceiling_total"`
	FleetCeilingLights    int `mapstructure:"fleet_ceiling_lights"`
	FleetCeilingHeavies   int `mapstructure:"fleet_ceiling_heavies"`
	FleetCeilingWarehouse int `mapstructure:"fleet_ceiling_warehouse"`

	// --- treasury guard (reuses common.EffectiveReserveFloor) ---

	// PurchaseMarginOverFloor is absolute credits of headroom required ABOVE the reserve floor
	// stack after a buy (liveTreasury − floor ≥ price + this). 0/absent → 200000.
	PurchaseMarginOverFloor int64 `mapstructure:"purchase_margin_over_floor"`
	// Reserve is the absolute working-capital reserve fed to EffectiveReserveFloor. 0/absent →
	// the common.ImmutableReserveFloor is still enforced; the resolver clamps.
	Reserve int64 `mapstructure:"reserve"`
	// ReserveTreasuryPct is the proportional reserve floor (percent of live treasury) fed to
	// EffectiveReserveFloor. 0/absent → common.DefaultReserveTreasuryPct (40).
	ReserveTreasuryPct int `mapstructure:"reserve_treasury_pct"`

	// --- light (factory-worker) demand ---

	// LightRotationSlots is the C3 rotation divisor inverted: K chains need K × this workers.
	// 0/absent → 3.5.
	LightRotationSlots float64 `mapstructure:"light_rotation_slots"`

	// --- heavy (trade) demand ---

	// HeavyMarginalRateFloor is the fraction of fleet-average realized tour $/hr the marginal
	// (next) heavy must be expected to clear before it is bought. 0/absent → 0.7.
	HeavyMarginalRateFloor float64 `mapstructure:"heavy_marginal_rate_floor"`
	// HeavyUnservedLanesMin is how many CONSECUTIVE ticks the profitable-lanes-beyond-hulls
	// shortfall must persist before a heavy is bought (anti-thrash on a transient spike).
	// 0/absent → 3.
	HeavyUnservedLanesMin int `mapstructure:"heavy_unserved_lanes_min"`
	// HeavyTreasuryPctPerPurchase is the analyst's 25%-treasury affordability rule for a heavy
	// buy (a single heavy must cost ≤ this percent of live treasury). 0/absent → 25.
	HeavyTreasuryPctPerPurchase int `mapstructure:"heavy_treasury_pct_per_purchase"`
	// DecliningRateUnservedFloor (sp-zbe6) is the near-zero unserved-lane count at/below which a
	// DECLINING aggregate realized tour-rate is treated as genuine absorption saturation and STOPS
	// a heavy buy. Above it a declining aggregate is a hull-CONCENTRATION artifact (the fleet
	// compressed a few fat lanes while profitable lanes sit unflown) and the next heavy flies a fresh
	// lane, so the buy proceeds. Trade-scoped (keys off the heavy class's unserved-lane Shortfall);
	// other classes keep the unconditional declining stop-buy. 0/absent → 2. The resolver forbids 0,
	// so the stop-buy can never be silently disabled.
	DecliningRateUnservedFloor int `mapstructure:"declining_rate_unserved_floor"`

	// --- API-utilization ceiling (dynamic rate protection; fleet ceilings are the hard bound) ---

	// APIUtilizationCeilingPct blocks buys above this sustained request-utilization percent.
	// 0/absent → 85. This guard fails OPEN (a buy proceeds) with a WARN when utilization is
	// unreadable — it is a dynamic protection, and the fleet ceilings are the hard budget bound.
	APIUtilizationCeilingPct int `mapstructure:"api_utilization_ceiling_pct"`

	// --- era-clock payback guard (hulls evaporate at reset — a buy must pay back in-era) ---

	// PaybackSafetyFactor: buy only if price ≤ expected_marginal_rate × hours_to_era_end × this.
	// 0/absent → 0.5.
	PaybackSafetyFactor float64 `mapstructure:"payback_safety_factor"`
	// PurchaseCutoffAtEraMinusHours is the hard last-buy cutoff before era end (no buys inside
	// this window whatever the payback math says). 0/absent → 3.0 (T-3h).
	PurchaseCutoffAtEraMinusHours float64 `mapstructure:"purchase_cutoff_at_era_minus_hours"`

	// --- per-class price ceilings + demand-proximal yard preference ---

	// MaxPriceLights / MaxPriceHeavies cap the absolute price paid per class (0/absent → no
	// absolute cap; the premium-over-cheapest ceiling still applies).
	MaxPriceLights  int64 `mapstructure:"max_price_lights"`
	MaxPriceHeavies int64 `mapstructure:"max_price_heavies"`
	// MaxPremiumOverCheapestPct caps how far above the cheapest known yard's ask a purchase may
	// pay (value, not just affordability — yard asks vary). 0/absent → 50.
	MaxPremiumOverCheapestPct int `mapstructure:"max_premium_over_cheapest_pct"`
	// PreferDemandProximalYard spawns hulls where the demand is (transit is the real cost),
	// subject to the premium ceiling. Default TRUE — *bool so unset (nil) is told from explicit
	// false. nil/absent → true.
	PreferDemandProximalYard *bool `mapstructure:"prefer_demand_proximal_yard"`

	// --- ship types purchased per class (RULINGS #5: even the asset is a knob) ---

	// ShipTypeLights / ShipTypeHeavies are the shipyard ship-type symbols bought for each class.
	// 0/absent → the documented defaults (SHIP_LIGHT_HAULER / SHIP_HEAVY_FREIGHTER).
	ShipTypeLights  string `mapstructure:"ship_type_lights"`
	ShipTypeHeavies string `mapstructure:"ship_type_heavies"`

	// --- zero-effect alarm (no-silent-dry-run corollary) ---

	// ZeroEffectAlarmTicks: when demand persists this many consecutive ticks with NO purchase
	// attempted (blocked every tick, or silently dry-running), emit ONE edge-triggered WARN
	// naming the persistent blocker (the f5pr silent-dry-run lesson). 0/absent → 4.
	ZeroEffectAlarmTicks int `mapstructure:"zero_effect_alarm_ticks"`

	// --- warehouse hull class + capacity ladder (sp-1txd M7/M8) ---

	// WarehouseMinChainRealizedPerHour gates warehouse placement on a chain's realized $/hr
	// (rh2z chain_pnl): only warehouse chains that PAY. 0/absent → 0 (no floor until tuned).
	WarehouseMinChainRealizedPerHour float64 `mapstructure:"warehouse_min_chain_realized_per_hour"`
	// WarehouseMinChainTickPersistence: a chain must persist in the durable set this many ticks
	// before a warehouse follows it (hysteresis — warehouses follow durable chains, not every
	// tick's reshuffle). 0/absent → 2.
	WarehouseMinChainTickPersistence int `mapstructure:"warehouse_min_chain_tick_persistence"`
	// MaxWarehouseHulls caps the warehouse-hull class. 0/absent → defaultFleetCeilingWarehouse.
	MaxWarehouseHulls int `mapstructure:"max_warehouse_hulls"`
	// StockerHullsPerWarehouseGroup is the 0/1 stocker demand per home-warehouse group under the
	// capital ceiling. 0/absent → 0 (off until tuned).
	StockerHullsPerWarehouseGroup int `mapstructure:"stocker_hulls_per_warehouse_group"`
	// WarehouseCapacityTargetHours is the buffer depth (deposit rate × draw latency) the capacity
	// ladder sizes slots to. 0/absent → 2.
	WarehouseCapacityTargetHours float64 `mapstructure:"warehouse_capacity_target_hours"`
	// MaxModuleSpendPerHull caps per-hull module spend on the capacity ladder's rung 1 (protects
	// against a 196k HOLD_III install; market-priced per yard). 0/absent → 0 (no module installs
	// until tuned — rung 1 disabled).
	MaxModuleSpendPerHull int64 `mapstructure:"max_module_spend_per_hull"`
	// WarehouseFrameClassCeiling caps the frame class the capacity ladder may buy ("light" or
	// "heavy"); heavy requires explicit enable. 0/absent → "light".
	WarehouseFrameClassCeiling string `mapstructure:"warehouse_frame_class_ceiling"`
}
