package grpc

import (
	"context"
	"fmt"

	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// This file wires the fleet-growth coordinator's launch path + launch-key resolution + recovery
// build. The trigger mirrors the autosizer's: identity-only launch config → buildCommandForType
// (the single builder shared by creation and recovery) → NewContainer with iterations=-1 for the
// infinite reconcile loop → Add → runner → registerContainer → go Start.

// FleetGrowthCoordinator starts the standing fleet-growth coordinator: a recovery-safe container
// that owns the fleet's heavy buying and derives the heavy/probe wave every tick. LIVE BY DEFAULT
// once launched — the coordinator ships armed and growth_enabled is the operator's control, not an
// arming step.
//
// ONE HEAVY BUYER PER PLAYER, AND THE GUARD LIVES HERE — not at either caller. Two callers reach
// this method (the `workflow fleet-growth` RPC and the bootstrap hand-off), so a check at one of
// them still lets the other start a rival: two coordinators bidding against each other over one
// treasury, and — because the heavy cap resolves off the first DECLARED owner in RUNNING-then-id
// order — one of them withholding toward a ceiling the other never consults. A third caller added
// later inherits the guard rather than having to remember it.
//
// Idempotent rather than loud, following LongHaulArbCoordinator: the bootstrap hand-off re-enters
// on every EXPANSION tick, so refusing would turn an ordinary re-entry into a hand-off blocker.
// A repository read that fails returns the error and launches NOTHING — an unknown fleet state
// must never authorise a second spender.
func (s *DaemonServer) FleetGrowthCoordinator(ctx context.Context, playerID int, agentSymbol string) (string, error) {
	if existingID, err := firstContainerIDOfType(ctx, s.containerRepo, playerID, container.ContainerTypeFleetGrowth); err != nil {
		return "", fmt.Errorf("failed to check for a running fleet-growth coordinator: %w", err)
	} else if existingID != "" {
		return existingID, nil // idempotent: already launched — return the live coordinator's id
	}

	containerID := utils.GenerateContainerID("fleet_growth", fmt.Sprintf("player-%d", playerID))

	// Identity only — every growth_* knob is resolved inside buildCommandForType.
	config := map[string]interface{}{
		"container_id": containerID,
		"agent_symbol": agentSymbol,
	}

	cmd, err := s.buildCommandForType("fleet_growth", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create fleet growth command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeFleetGrowth,
		playerID,
		-1,  // Infinite iterations (reconcile loop) — NOT a CoordinatorOwnsIterations type
		nil, // No parent container
		config,
		nil,
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "fleet_growth"); err != nil {
		return "", fmt.Errorf("failed to persist fleet growth container: %w", err)
	}

	s.startContainerRunner(containerEntity, cmd, containerID, "Fleet growth container")

	return containerID, nil
}

// fleetGrowthConfigKeys enumerates every launch-config key the growth knobs occupy.
// resolveFleetGrowthConfig clears these on each rebuild, so a stale persisted copy from a prior
// boot can never shadow the coordinator's current documented default. container_id and
// agent_symbol are IDENTITY (set once at creation) and are deliberately NOT in this list — they
// must survive a rebuild.
var fleetGrowthConfigKeys = []string{
	"growth_tick_secs",
	// NOTE: the live-tunable knobs are the BARE keys "heavy_cap", "growth_enabled" and
	// "growth_runway_milli_hours", NOT these prefixed launch keys. resolveFleetGrowthConfig CLEARS
	// every growth_* key on each rebuild, so a tune written to a prefixed key would be wiped on the
	// next daemon bounce. The bare keys are untouched by that cycle and therefore survive.
	//
	// The suffix matters: the heavy-buyer cap read resolves a launch cap by its "_heavy_cap"
	// suffix, so this key must keep that ending.
	"growth_heavy_cap",
	"growth_unserved_lanes_min",
	"growth_runway_milli_hours",
	"growth_purchase_margin_over_floor",
	"growth_treasury_pct_per_purchase",
	"growth_api_utilization_ceiling_pct",
	"growth_max_price_heavies",
	"growth_max_premium_over_cheapest_pct",
	"growth_prefer_demand_proximal_yard",
	"growth_ship_type_heavies",
	"growth_zero_effect_alarm_ticks",
}

// resolveFleetGrowthConfig clears every growth_* launch key before the command is rebuilt, so each
// build starts from the coordinator's own documented defaults.
//
// THERE IS NO config.yaml SECTION TO RE-INJECT FROM, and that is the design rather than an
// omission: the growth coordinator's operator surface is the three LIVE knobs, which are bare keys
// and survive this clear. A launch key written by hand is transient by construction, which is what
// stops a stale copy of a knob from outliving the value an operator actually tuned.
func (s *DaemonServer) resolveFleetGrowthConfig(config map[string]interface{}) {
	for _, key := range fleetGrowthConfigKeys {
		delete(config, key)
	}
}

// buildFleetGrowthCommand rebuilds the standing growth command from a persisted launch config so a
// daemon restart re-adopts it.
func buildFleetGrowthCommand(cfg *configReader, playerID int, containerID string) interface{} {
	cmd := &fleetCmd.RunFleetGrowthCoordinatorCommand{
		PlayerID:    playerID,
		ContainerID: containerID,
		AgentSymbol: cfg.OptionalString("agent_symbol"),

		TickIntervalSecs: cfg.OptionalInt("growth_tick_secs", 0),

		// A POINTER so a present 0 stays distinguishable from an absent key: 0 means "own no
		// heavies", and flattening it to "unset, use the default" would buy hulls nobody asked for.
		// The reachable operator hold is the master switch — this key is cleared on every build —
		// so the distinction guards the reader rather than a live path.
		HeavyCap: presentIntPtr(cfg, "growth_heavy_cap"),

		UnservedLanesMin: cfg.OptionalInt("growth_unserved_lanes_min", 0),
		RunwayMilliHours: cfg.OptionalInt("growth_runway_milli_hours", 0),

		PurchaseMarginOverFloor: int64(cfg.OptionalInt("growth_purchase_margin_over_floor", 0)),
		TreasuryPctPerPurchase:  cfg.OptionalInt("growth_treasury_pct_per_purchase", 0),

		APIUtilizationCeilingPct: cfg.OptionalInt("growth_api_utilization_ceiling_pct", 0),

		MaxPriceHeavies:           int64(cfg.OptionalInt("growth_max_price_heavies", 0)),
		MaxPremiumOverCheapestPct: cfg.OptionalInt("growth_max_premium_over_cheapest_pct", 0),

		ShipTypeHeavies: cfg.OptionalString("growth_ship_type_heavies"),

		ZeroEffectAlarmTicks: cfg.OptionalInt("growth_zero_effect_alarm_ticks", 0),
	}
	// Default-TRUE bool: present-vs-absent is what carries the default, so read it as a *bool.
	if v, ok := cfg.PresentBool("growth_prefer_demand_proximal_yard"); ok {
		cmd.PreferDemandProximalYard = &v
	}
	return cmd
}
