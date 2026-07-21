package grpc

import (
	"context"
	"fmt"

	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// This file wires the dedicated contract auto-scaler's launch path + recovery build, mirroring
// container_ops_fleet_autosizer.go: identity-only launch config → buildCommandForType (the single
// builder shared by creation and recovery) → NewContainer with iterations=-1 for the infinite ramp
// loop → Add → runner. UNLIKE the autosizer there is almost nothing to resolve from config.yaml: the
// ONE operator knob (contract_fleet_max_hulls) is read LIVE from the container's own config column each
// tick (Pattern-C, the ContainerConfigReader ceiling), and the sole money guard (the 200000 cushion) is
// a compile-time const — so a config edit needs no launch-config injection here.

// ContractScalerCoordinator starts the standing dedicated contract auto-scaler: a recovery-safe
// container that ramps the exclusive contract fleet to the live-tunable ceiling behind the
// 200000-credit cushion. It is LIVE once launched, but its launch is ARMED (default-off) at the
// bootstrap early-scaling seam — a bare deploy never starts it (byte-identical). Idempotent at the
// caller (the bootstrap launcher skips when one is already RUNNING/PENDING).
func (s *DaemonServer) ContractScalerCoordinator(ctx context.Context, playerID int, agentSymbol string) (string, error) {
	containerID := utils.GenerateContainerID("contract_scaler", fmt.Sprintf("player-%d", playerID))

	// Identity only — the ceiling is Pattern-C (live-read from this container's config column each tick,
	// so a `tune --operation contractscaler` lands with no restart) and the cushion is a const.
	config := map[string]interface{}{
		"container_id": containerID,
		"agent_symbol": agentSymbol,
	}

	cmd, err := s.buildCommandForType("contract_scaler", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create contract scaler command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeContractScaler,
		playerID,
		-1,  // Infinite iterations (ramp loop) — NOT a CoordinatorOwnsIterations type
		nil, // No parent container
		config,
		nil,
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "contract_scaler"); err != nil {
		return "", fmt.Errorf("failed to persist contract scaler container: %w", err)
	}

	s.startContainerRunner(containerEntity, cmd, containerID, "Contract scaler container")

	return containerID, nil
}

// buildContractScalerCommand rebuilds the standing contract-scaler command from a persisted launch
// config so a daemon restart re-adopts it (RULINGS #2). container_id + agent_symbol are IDENTITY
// (set once at creation, survive the rebuild). Disabled/DryRun are reconstructed from optional launch
// keys — absent reads as false, so the scaler is LIVE once launched and a recovery from a config that
// predates the keys stays live (mirrors the autosizer's absent-key-is-enabled discipline). tick_secs 0
// defers to the coordinator's documented default. The ceiling is NOT read here — it is the live
// Pattern-C snapshot the coordinator takes each tick, not a launch-frozen value.
func buildContractScalerCommand(cfg *configReader, playerID int, containerID string) interface{} {
	return &contractScalerCmd.RunContractScalerCommand{
		PlayerID:    playerID,
		ContainerID: cfg.RequiredNonEmptyString("container_id"),
		AgentSymbol: cfg.OptionalString("agent_symbol"),
		Disabled:    cfg.OptionalBool("contract_scaler_disabled"),
		DryRun:      cfg.OptionalBool("contract_scaler_dry_run"),
		TickSeconds: cfg.OptionalInt("contract_scaler_tick_secs", 0),
	}
}
