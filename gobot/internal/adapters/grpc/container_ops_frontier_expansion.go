package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// FrontierExpansionCoordinator creates and starts the standing frontier expansion
// coordinator for a player (sp-8w89), mirroring ScoutPostCoordinator. One coordinator
// per player measures coverage demand, declares frontier sweep-once posts, and buys
// probes under the money guards — while the scout-post reconciler does all movement and
// manning. The container id is keyed by player so a restart re-adopts the same one; the
// persisted config is the recovery source (RULINGS #2), read back through the SAME
// buildCommandForType the creation path uses, so launch and recovery can never drift.
//
// Every knob is parametrized (RULINGS #5); a 0/false value uses the coordinator's own
// documented default. dryRun logs decisions without buying or declaring (pin #7).
func (s *DaemonServer) FrontierExpansionCoordinator(
	ctx context.Context,
	playerID int,
	tickIntervalSecs int,
	dryRun bool,
	maxProbeFleet int,
	maxSpendPerCycle int,
	purchaseCooldownSecs int,
	expansionMaxHops int,
) (string, error) {
	containerID := utils.GenerateContainerID("frontier_expansion_coordinator", fmt.Sprintf("player-%d", playerID))

	config := map[string]interface{}{
		"container_id":           containerID,
		"tick_interval_secs":     tickIntervalSecs,
		"dry_run":                dryRun,
		"max_probe_fleet":        maxProbeFleet,
		"max_spend_per_cycle":    maxSpendPerCycle,
		"purchase_cooldown_secs": purchaseCooldownSecs,
		"expansion_max_hops":     expansionMaxHops,
	}

	// sp-ve3q: re-adopt the last persisted live-tuned config for this player's frontier
	// coordinator so a relaunch of a stopped one keeps its tunes (matching the daemon-restart
	// recovery path) instead of silently reverting to config-file defaults. Warns loudly for
	// any safety-critical knob (the max_probe_price overpay ceiling) that comes up disabled.
	config, warnings, err := s.frontierStartConfig(ctx, playerID, config)
	if err != nil {
		return "", fmt.Errorf("failed to resolve frontier start config: %w", err)
	}
	for _, warning := range warnings {
		fmt.Printf("⚠️  WARNING [frontier start player %d]: %s\n", playerID, warning)
	}

	cmd, err := s.buildCommandForType("frontier_expansion_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeFrontierExpansion,
		playerID,
		-1,  // Infinite iterations (reconcile loop)
		nil, // No parent container
		config,
		nil, // Use default RealClock for production
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "frontier_expansion_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist container: %w", err)
	}

	s.startContainerRunner(containerEntity, cmd, containerID, "Container")

	return containerID, nil
}

// frontierStartConfig loads the last persisted live-config for the player's frontier
// coordinator and overlays the fresh start config on top of it, so relaunching a
// previously-stopped coordinator via `frontier start` RE-ADOPTS its live-tuned knobs
// (sp-ve3q) instead of silently reverting to config-file defaults — the same persisted
// config column the daemon-restart recovery path already rebuilds from. A player with no
// prior frontier coordinator gets the base config verbatim (byte-identical fresh start).
// It also returns operator warnings for any safety-critical knob that a (re)start resolved
// to a permissive value (today: the sp-3u5d max_probe_price overpay ceiling resolving to
// 0 = disabled).
func (s *DaemonServer) frontierStartConfig(ctx context.Context, playerID int, base map[string]interface{}) (map[string]interface{}, []string, error) {
	prior, err := s.containerRepo.FindMostRecentByType(ctx, string(container.ContainerTypeFrontierExpansion), playerID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load prior frontier config for re-apply: %w", err)
	}
	priorConfig, err := parseContainerConfig(prior)
	if err != nil {
		return nil, nil, err
	}
	merged := mergeFrontierStartConfig(priorConfig, base)
	return merged, frontierStartSafetyWarnings(merged), nil
}

// parseContainerConfig decodes a prior container's persisted config column into a map, or
// nil when there is no prior container (a fresh start). A present-but-unparseable config
// is a hard error rather than a silent fall-back to defaults: dropping a real coordinator's
// tunes is the very failure sp-ve3q fixes, so we surface it instead of masking it.
func parseContainerConfig(model *persistence.ContainerModel) (map[string]interface{}, error) {
	if model == nil || model.Config == "" {
		return nil, nil
	}
	config := map[string]interface{}{}
	if err := json.Unmarshal([]byte(model.Config), &config); err != nil {
		return nil, fmt.Errorf("failed to parse prior frontier container %s config: %w", model.ID, err)
	}
	return config, nil
}

// frontierStartOverrideKeys are the numeric CLI flags a `frontier start` carries. On a
// relaunch they override the carried-forward config ONLY when explicitly set (>0), so a
// no-flag relaunch preserves every persisted tune while an explicit `--max-probe-fleet N`
// on the relaunch still wins. (0 = "use the default" in the CLI contract, so it is NOT an
// override.)
var frontierStartOverrideKeys = []string{
	"tick_interval_secs",
	"max_probe_fleet",
	"max_spend_per_cycle",
	"purchase_cooldown_secs",
	"expansion_max_hops",
}

// mergeFrontierStartConfig overlays a fresh start's config on top of the last persisted
// config so a relaunch RE-ADOPTS the live-tuned knobs (sp-ve3q). With no prior config it
// returns base UNCHANGED — a fresh coordinator comes up on config-file defaults exactly as
// before. Otherwise the prior config is the base (carrying every tune the daemon-restart
// path would rebuild), with two classes taken from the new start: container_id + dry_run
// are always the new start's (a new id; the mode chosen for THIS start), and the numeric
// CLI flags override only when explicitly set (>0).
func mergeFrontierStartConfig(prior, base map[string]interface{}) map[string]interface{} {
	if len(prior) == 0 {
		return base
	}
	merged := make(map[string]interface{}, len(prior)+len(base))
	for key, value := range prior {
		merged[key] = value
	}
	merged["container_id"] = base["container_id"]
	merged["dry_run"] = base["dry_run"]
	for _, key := range frontierStartOverrideKeys {
		if value, ok := intValue(base[key]); ok && value > 0 {
			merged[key] = value
		}
	}
	return merged
}

// frontierStartSafetyWarnings flags safety-critical knobs a (re)start resolved to a
// PERMISSIVE value (sp-ve3q backstop). Today the only such knob is the sp-3u5d per-unit
// probe price ceiling max_probe_price: a resolved 0 means "disabled — buy at any price",
// the exact overpay exposure (210-235k deep-frontier spirals) the ceiling was built to
// prevent. This does NOT change the knob's 0=disabled contract; it only makes a start that
// comes up UNARMED loud instead of silent, so the operator knows to re-arm the guard.
func frontierStartSafetyWarnings(config map[string]interface{}) []string {
	if value, ok := intValue(config["max_probe_price"]); ok && value > 0 {
		return nil
	}
	return []string{
		"max_probe_price is 0 (DISABLED — the frontier will buy probes at ANY price); the sp-3u5d overpay " +
			"ceiling is UNARMED. Re-arm it: spacetraders tune --operation frontier max_probe_price <credits>",
	}
}
