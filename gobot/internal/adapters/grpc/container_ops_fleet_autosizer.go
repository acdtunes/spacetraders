package grpc

import (
	"context"
	"fmt"

	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// This file wires the fleet capacity autosizer's launch path + live-config resolution + recovery
// build (sp-1txd). The launch trigger mirrors SitingCoordinator: identity-only launch config →
// buildCommandForType (the single builder shared by creation and recovery) → NewContainer with
// iterations=-1 for the infinite reconcile loop → Add → runner → registerContainer → go Start. All
// tuning ([fleet_autosizer]) resolves LIVE from config.yaml inside buildCommandForType, so a config
// edit + restart retunes even a recovered coordinator.

// FleetAutosizerCoordinator starts the standing fleet capacity autosizer (sp-1txd): a recovery-safe
// container that sizes the hull pool to demand each slow tick and auto-buys hulls (lights to factory
// demand, heavies to trade demand) behind the fail-closed money-guard stack. LIVE BY DEFAULT once
// launched.
func (s *DaemonServer) FleetAutosizerCoordinator(ctx context.Context, playerID int, agentSymbol string) (string, error) {
	containerID := utils.GenerateContainerID("fleet_autosizer", fmt.Sprintf("player-%d", playerID))

	// Identity only — the [fleet_autosizer] knobs are injected by resolveFleetAutosizerConfig inside
	// buildCommandForType, the single injection point shared by creation and recovery.
	config := map[string]interface{}{
		"container_id": containerID,
		"agent_symbol": agentSymbol,
	}

	cmd, err := s.buildCommandForType("fleet_autosizer", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create fleet autosizer command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeFleetAutosizer,
		playerID,
		-1,  // Infinite iterations (reconcile loop) — NOT a CoordinatorOwnsIterations type
		nil, // No parent container
		config,
		nil,
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "fleet_autosizer"); err != nil {
		return "", fmt.Errorf("failed to persist fleet autosizer container: %w", err)
	}

	s.startContainerRunner(containerEntity, cmd, containerID, "Fleet autosizer container")

	return containerID, nil
}

// fleetAutosizerConfigKeys enumerates every launch-config key the [fleet_autosizer] knobs
// occupy. resolveFleetAutosizerConfig clears these before re-injecting the live values, so a
// stale persisted copy from a prior boot can never shadow the current config.yaml (the sp-ts82
// live-config discipline). Keep in lockstep with injectFleetAutosizerConfig and
// buildFleetAutosizerCommand's reads. container_id and agent_symbol are IDENTITY (set once at
// creation) and are deliberately NOT in this list — they must survive a rebuild.
var fleetAutosizerConfigKeys = []string{
	"autosizer_tick_secs",
	"autosizer_purchase_cap_per_tick",
	// NOTE: the live-tunable knob is the BARE key "heavy_cap", NOT this prefixed launch key.
	// resolveFleetAutosizerConfig CLEARS and re-injects every autosizer_* key from config.yaml on
	// each rebuild, so a tune written to a prefixed key would be wiped on the next daemon bounce.
	// The bare key is untouched by that cycle and therefore survives (the bootstrap precedent).
	"autosizer_heavy_cap",
	"autosizer_purchase_margin_over_floor",
	"autosizer_light_rotation_slots",
	"autosizer_heavy_unserved_lanes_min",
	"autosizer_heavy_treasury_pct_per_purchase",
	"autosizer_api_utilization_ceiling_pct",
	"autosizer_max_price_lights",
	"autosizer_max_price_heavies",
	"autosizer_max_premium_over_cheapest_pct",
	"autosizer_prefer_demand_proximal_yard",
	"autosizer_ship_type_lights",
	"autosizer_ship_type_heavies",
	"autosizer_zero_effect_alarm_ticks",
	"autosizer_explorer_hulls_enabled",
	"autosizer_fleet_ceiling_explorer",
	"autosizer_explorer_treasury_pct_per_purchase",
	"autosizer_max_price_explorer",
	"autosizer_ship_type_explorer",
}

// resolveFleetAutosizerConfig makes config.yaml the single LIVE source of truth for the
// autosizer's knobs (sp-1txd, mirroring resolveSitingConfig). It clears any autosizer_* keys
// already in the launch config (stale copies persisted at a prior boot) and re-injects the
// daemon's boot-loaded values, so the rebuilt command reflects the CURRENT config.yaml on every
// build — creation and restart recovery alike. The clear is what lets dropping a knob from
// config.yaml fall back to the coordinator's own default rather than being shadowed by the
// now-absent live value.
func (s *DaemonServer) resolveFleetAutosizerConfig(config map[string]interface{}) {
	for _, key := range fleetAutosizerConfigKeys {
		delete(config, key)
	}
	s.injectFleetAutosizerConfig(config)
}

// injectFleetAutosizerConfig writes the [fleet_autosizer] knobs from config.yaml
// (s.fleetAutosizerConfig) into a coordinator container's launch config. Only keys the captain
// actually set (non-zero / non-nil) are written, so an unset knob defers to the coordinator's own
// documented default (RULINGS #5 — the daemon never hardcodes the operational values).
func (s *DaemonServer) injectFleetAutosizerConfig(config map[string]interface{}) {
	fa := s.fleetAutosizerConfig
	if fa.TickIntervalSecs != 0 {
		config["autosizer_tick_secs"] = fa.TickIntervalSecs
	}
	if fa.PurchaseCapPerTick != 0 {
		config["autosizer_purchase_cap_per_tick"] = fa.PurchaseCapPerTick
	}
	// Injected whenever CONFIGURED — including an explicit 0, which is a legitimate operator
	// HOLD ("own no heavies"), not an unset knob. This is why the config field is a *int and why
	// this check is `!= nil` rather than the `!= 0` used by the knobs above.
	if fa.HeavyCap != nil {
		config["autosizer_heavy_cap"] = *fa.HeavyCap
	}
	if fa.PurchaseMarginOverFloor != 0 {
		config["autosizer_purchase_margin_over_floor"] = int(fa.PurchaseMarginOverFloor)
	}
	if fa.LightRotationSlots != 0 {
		config["autosizer_light_rotation_slots"] = fa.LightRotationSlots
	}
	if fa.HeavyUnservedLanesMin != 0 {
		config["autosizer_heavy_unserved_lanes_min"] = fa.HeavyUnservedLanesMin
	}
	if fa.HeavyTreasuryPctPerPurchase != 0 {
		config["autosizer_heavy_treasury_pct_per_purchase"] = fa.HeavyTreasuryPctPerPurchase
	}
	if fa.APIUtilizationCeilingPct != 0 {
		config["autosizer_api_utilization_ceiling_pct"] = fa.APIUtilizationCeilingPct
	}
	if fa.MaxPriceLights != 0 {
		config["autosizer_max_price_lights"] = int(fa.MaxPriceLights)
	}
	if fa.MaxPriceHeavies != 0 {
		config["autosizer_max_price_heavies"] = int(fa.MaxPriceHeavies)
	}
	if fa.MaxPremiumOverCheapestPct != 0 {
		config["autosizer_max_premium_over_cheapest_pct"] = fa.MaxPremiumOverCheapestPct
	}
	// Default-TRUE bool: write it ONLY when the captain set it (non-nil), so an absent key defers
	// to the coordinator's true default.
	if fa.PreferDemandProximalYard != nil {
		config["autosizer_prefer_demand_proximal_yard"] = *fa.PreferDemandProximalYard
	}
	if fa.ShipTypeLights != "" {
		config["autosizer_ship_type_lights"] = fa.ShipTypeLights
	}
	if fa.ShipTypeHeavies != "" {
		config["autosizer_ship_type_heavies"] = fa.ShipTypeHeavies
	}
	if fa.ZeroEffectAlarmTicks != 0 {
		config["autosizer_zero_effect_alarm_ticks"] = fa.ZeroEffectAlarmTicks
	}
	// Explorer class. The opt-in arming bool is written ONLY when true (an absent key reads
	// as DISARMED, so nothing boot-arms it — mirrors warehouse_hulls_enabled).
	if fa.ExplorerHullsEnabled {
		config["autosizer_explorer_hulls_enabled"] = true
	}
	if fa.FleetCeilingExplorer != 0 {
		config["autosizer_fleet_ceiling_explorer"] = fa.FleetCeilingExplorer
	}
	if fa.ExplorerTreasuryPctPerPurchase != 0 {
		config["autosizer_explorer_treasury_pct_per_purchase"] = fa.ExplorerTreasuryPctPerPurchase
	}
	if fa.MaxPriceExplorer != 0 {
		config["autosizer_max_price_explorer"] = int(fa.MaxPriceExplorer)
	}
	if fa.ShipTypeExplorer != "" {
		config["autosizer_ship_type_explorer"] = fa.ShipTypeExplorer
	}
	// sp-y2ptq: the autosizer's contract-delivery class was removed (dedicated scaler owns it) — no
	// autosizer_contract_delivery_* keys are injected.
}

// buildFleetAutosizerCommand rebuilds the standing autosizer command from a persisted
// launch config so a daemon restart re-adopts it. The [fleet_autosizer] knobs are resolved LIVE
// from config.yaml just before this runs (resolveFleetAutosizerConfig in buildCommandForType), so
// the persisted autosizer_* keys are transient — the reads below see the current config.yaml.
func buildFleetAutosizerCommand(cfg *configReader, playerID int, containerID string) interface{} {
	cmd := &fleetCmd.RunFleetAutosizerCoordinatorCommand{
		PlayerID:    playerID,
		ContainerID: containerID,
		AgentSymbol: cfg.OptionalString("agent_symbol"),

		TickIntervalSecs:   cfg.OptionalInt("autosizer_tick_secs", 0),
		PurchaseCapPerTick: cfg.OptionalInt("autosizer_purchase_cap_per_tick", 0),

		HeavyCap: presentIntPtr(cfg, "autosizer_heavy_cap"),

		PurchaseMarginOverFloor: int64(cfg.OptionalInt("autosizer_purchase_margin_over_floor", 0)),

		LightRotationSlots: cfg.OptionalFloat("autosizer_light_rotation_slots", 0),

		HeavyUnservedLanesMin:       cfg.OptionalInt("autosizer_heavy_unserved_lanes_min", 0),
		HeavyTreasuryPctPerPurchase: cfg.OptionalInt("autosizer_heavy_treasury_pct_per_purchase", 0),

		APIUtilizationCeilingPct: cfg.OptionalInt("autosizer_api_utilization_ceiling_pct", 0),

		MaxPriceLights:            int64(cfg.OptionalInt("autosizer_max_price_lights", 0)),
		MaxPriceHeavies:           int64(cfg.OptionalInt("autosizer_max_price_heavies", 0)),
		MaxPremiumOverCheapestPct: cfg.OptionalInt("autosizer_max_premium_over_cheapest_pct", 0),

		ShipTypeLights:  cfg.OptionalString("autosizer_ship_type_lights"),
		ShipTypeHeavies: cfg.OptionalString("autosizer_ship_type_heavies"),

		ZeroEffectAlarmTicks: cfg.OptionalInt("autosizer_zero_effect_alarm_ticks", 0),

		ExplorerHullsEnabled:           cfg.OptionalBool("autosizer_explorer_hulls_enabled"),
		FleetCeilingExplorer:           cfg.OptionalInt("autosizer_fleet_ceiling_explorer", 0),
		ExplorerTreasuryPctPerPurchase: cfg.OptionalInt("autosizer_explorer_treasury_pct_per_purchase", 0),
		MaxPriceExplorer:               int64(cfg.OptionalInt("autosizer_max_price_explorer", 0)),
		ShipTypeExplorer:               cfg.OptionalString("autosizer_ship_type_explorer"),
		// sp-y2ptq: autosizer contract-delivery class removed — no autosizer_contract_delivery_* reads.
	}
	// Default-TRUE bool: present-vs-absent is what carries the default, so read it as a *bool.
	if v, ok := cfg.PresentBool("autosizer_prefer_demand_proximal_yard"); ok {
		cmd.PreferDemandProximalYard = &v
	}
	return cmd
}

// presentIntPtr reads an optional int knob as a POINTER so an explicit 0 survives the round trip
// through container config. OptionalInt would flatten absent and 0 to the same value, which for
// heavy_cap would turn an operator's deliberate "own no heavies" hold into "unset, use the
// default 5" — silently buying hulls the operator asked us not to buy.
func presentIntPtr(cfg *configReader, key string) *int {
	if v, ok := cfg.PresentInt(key); ok {
		return &v
	}
	return nil
}
