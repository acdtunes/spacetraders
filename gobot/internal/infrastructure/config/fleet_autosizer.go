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
	// --- cadence + purchase pacing ---

	// TickIntervalSecs is the slow autosizer cadence (sizing is strategic). 0/absent → 900s.
	TickIntervalSecs int `mapstructure:"tick_interval_secs"`
	// PurchaseCapPerTick bounds hulls bought per tick, across ALL classes (protects the
	// treasury from a runaway multi-buy on one tick). 0/absent → 1.
	PurchaseCapPerTick int `mapstructure:"purchase_cap_per_tick"`

	// sp-r7eiu: FleetCeiling{Lights,Heavies} were removed with the class_ceiling guard they fed.
	// A `fleet_ceiling_lights`/`fleet_ceiling_heavies` key left behind in an existing config.yaml is
	// simply IGNORED on read — viper's non-strict unmarshal (config.go's plain v.Unmarshal, no
	// ErrorUnused) drops keys with no matching field, so a stale config.yaml still boots.

	// --- treasury guard ---

	// PurchaseMarginOverFloor is absolute credits of headroom required ABOVE the reserve floor
	// (the flat common.ImmutableReserveFloor, sp-05glh) after a buy (liveTreasury − floor ≥
	// price + this). 0/absent → 200000.
	PurchaseMarginOverFloor int64 `mapstructure:"purchase_margin_over_floor"`

	// --- light (factory-worker) demand ---

	// LightRotationSlots is the C3 rotation divisor inverted: K chains need K × this workers.
	// 0/absent → 3.5.
	LightRotationSlots float64 `mapstructure:"light_rotation_slots"`

	// --- heavy (trade) demand ---

	// HeavyUnservedLanesMin is how many CONSECUTIVE ticks the profitable-lanes-beyond-hulls
	// shortfall must persist before a heavy is bought (anti-thrash on a transient spike).
	// 0/absent → 3.
	HeavyUnservedLanesMin int `mapstructure:"heavy_unserved_lanes_min"`
	// HeavyTreasuryPctPerPurchase is the analyst's 25%-treasury affordability rule for a heavy
	// buy (a single heavy must cost ≤ this percent of live treasury). 0/absent → 25.
	HeavyTreasuryPctPerPurchase int `mapstructure:"heavy_treasury_pct_per_purchase"`
	// HeavyCap is the maximum HEAVY HULLS the fleet may own — capital exposure in large
	// hulls, counted fleet-wide regardless of dedicated_fleet tag. Since sp-r7eiu removed the
	// class_ceiling guard and its per-class pool ceilings, this is the ONLY count-based bound
	// on any hull class.
	//
	// *int, not int, so an explicit 0 can be told from unset: heavy_cap: 0 in config.yaml is
	// a legitimate operator HOLD ("own no heavies"), not an unset knob deferring to the
	// default. nil/absent → defaultHeavyCap (5). NOTE: the `tune` path cannot express the
	// hold — `tune <key> 0` DELETES the key fleet-wide (revert-to-default semantics), so a
	// tuned 0 reads as absent. Holding at zero is a config.yaml + restart operation.
	HeavyCap *int `mapstructure:"heavy_cap"`

	// --- API-utilization ceiling (dynamic rate protection; fleet ceilings are the hard bound) ---

	// APIUtilizationCeilingPct blocks buys above this sustained request-utilization percent.
	// 0/absent → 85. This guard fails OPEN (a buy proceeds) with a WARN when utilization is
	// unreadable — it is a dynamic protection, and the fleet ceilings are the hard budget bound.
	APIUtilizationCeilingPct int `mapstructure:"api_utilization_ceiling_pct"`

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

	// --- zero-effect alarm (a buyer that never buys must say so) ---

	// ZeroEffectAlarmTicks: when demand persists this many consecutive ticks with NO purchase
	// attempted (a guard blocking every tick, or an unwired purchaser), emit ONE edge-triggered
	// WARN naming the persistent blocker (the f5pr lesson). 0/absent → 4.
	ZeroEffectAlarmTicks int `mapstructure:"zero_effect_alarm_ticks"`

	// sp-y2ptq: the autosizer's demand-driven contract-delivery hull class
	// was REMOVED with the capacity reconciler that fed it — the dedicated contract scaler now owns
	// contract-fleet capacity. The shared BuyAndDedicate primitive + the HullClassContractDelivery→
	// "contract" dedication mapping the scaler reuses are kept; only the autosizer's own auto-scaling
	// of the class (these config knobs + the demand-provider consumption) is gone.
}
