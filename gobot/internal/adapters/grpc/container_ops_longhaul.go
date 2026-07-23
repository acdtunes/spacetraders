package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// longHaulOperation is the fleet-claim identity every long-haul worker claims its hull under
// (design §1/§2). It matches the DedicatedFleet tag the operator sets with
// `fleet add --operation long-haul --ship X`, so the operation-checked ClaimShip (l7h2)
// accepts a long-haul-tagged hull and rejects any other.
const longHaulOperation = "long-haul"

// LaunchLongHaul implements tradingCmd.LongHaulLauncher (sp-mepj): it starts one recovery-safe
// per-hull long-haul worker for an idle long-haul-tagged hull. It MIRRORS LaunchIdleArb on the
// two gotchas the idle-arb path (sp-1hp9/sp-cr86) proved load-bearing:
//
//   - ORDERING (sp-1hp9): the container row is persisted (Add) BEFORE the hull claim.
//     ships.container_id carries an FK to containers.id (fk_ships_container), so a ClaimShip
//     that writes ships.container_id before the containers row exists is a 23503 FK violation
//     — dead on arrival. Build → Add → ClaimShip is the FK-safe order.
//   - RECOVERY IDENTITY (RULINGS #2): the operation="long-haul" claim identity is persisted in
//     the launch config, so the runner's createShipAssignments re-claims idempotently on start
//     AND on restart recovery, keeping the worker recovery-safe under the long-haul fleet.
//
// The coordinator hands only decision inputs (LongHaulLaunchSpec); the daemon owns the claim,
// the single-writer container row, and release-on-death, so the coordinator claims nothing
// itself (RULINGS #3/#7). A claim refused AFTER the row is persisted terminalizes the orphan
// row FAILED (the sp-cr86 pre-runner boundary) rather than leaving a zombie PENDING.
func (s *DaemonServer) LaunchLongHaul(ctx context.Context, spec tradingCmd.LongHaulLaunchSpec) (string, error) {
	if spec.ShipSymbol == "" {
		return "", fmt.Errorf("long-haul launch requires a ship symbol")
	}

	playerID := shared.MustNewPlayerID(spec.PlayerID)
	containerID := utils.GenerateContainerID("longhaul_arb", spec.ShipSymbol)

	config := map[string]interface{}{
		"ship_symbol":  spec.ShipSymbol,
		"agent_symbol": spec.AgentSymbol,
		"container_id": containerID,
		"iterations":   spec.Iterations,
		// int keeps the on-disk form identical across the native and JSON config paths; int is
		// 64-bit on the daemon's target, so a credit cap never overflows (the trade-fleet idiom).
		"per_haul_cap":       int(spec.PerHaulCap),
		"total_exposure_cap": int(spec.TotalExposureCap),
		// The claim identity: createShipAssignments re-claims under this on start AND on restart
		// recovery, so the worker is recovery-safe under the long-haul fleet (RULINGS #2).
		"operation": longHaulOperation,
	}

	// Built through the SAME factory recovery uses ("longhaul_arb"), so launch and restart
	// rebuild can never drift. Built before anything is persisted or claimed — a build failure
	// leaves no side effects.
	cmd, err := s.buildCommandForType("longhaul_arb", config, spec.PlayerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create long-haul worker command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeLongHaulArb,
		spec.PlayerID,
		1,   // coordinator owns iterations; the runner invokes Handle() once, the worker loops internally
		nil, // top-level, recovered independently
		config,
		nil, // default RealClock
	)

	// Persist the container row FIRST: it is the FK parent for the hull claim's
	// ships.container_id (fk_ships_container). Claiming before this row exists is the sp-1hp9
	// FK 23503 that made every idle-arb dispatch dead on arrival.
	if err := s.containerRepo.Add(ctx, containerEntity, "longhaul_arb"); err != nil {
		return "", fmt.Errorf("failed to persist long-haul worker container: %w", err)
	}

	// Now the atomic, operation-checked claim (l7h2): busy / captain-reserved / foreign-dedicated
	// hulls are rejected inside the row-locked transaction. The container row above satisfies the
	// FK, and this synchronous claim keeps the coordinator's exposure accounting exact
	// (claimed-on-return, or not launched).
	if err := s.shipRepo.ClaimShip(ctx, spec.ShipSymbol, containerID, playerID, longHaulOperation); err != nil {
		// The row is now an orphan — persisted, but no hull was claimed and no runner owns it.
		// Terminalize it FAILED so it is not a zombie stuck at PENDING with no one to advance it.
		s.terminalizeLongHaulClaimFailure(ctx, containerEntity, err)
		return "", fmt.Errorf("long-haul claim of %s refused: %w", spec.ShipSymbol, err)
	}

	// The runner's own claim is idempotent for this containerID; it owns release on every
	// terminal path from here (completion, crash, cancel).
	s.startContainerRunner(containerEntity, cmd, containerID, "Long-haul worker container")

	return containerID, nil
}

// terminalizeLongHaulClaimFailure marks a just-persisted long-haul worker row FAILED when the
// synchronous hull claim is refused AFTER the row exists (the hull was taken between the
// coordinator's snapshot and this claim). Without it the row is a zombie: persisted PENDING
// with no runner to advance or release it (the sp-cr86 failure mode, here at the pre-runner
// claim boundary). No ship state is released: the claim failed, so nothing was assigned.
func (s *DaemonServer) terminalizeLongHaulClaimFailure(ctx context.Context, c *container.Container, cause error) {
	now := s.clock.Now()
	exitCode := 1
	if err := s.containerRepo.UpdateStatus(
		ctx,
		c.ID(),
		c.PlayerID(),
		container.ContainerStatusFailed,
		&now,
		&exitCode,
		fmt.Sprintf("claim_failed: %s", cause.Error()),
	); err != nil {
		fmt.Printf("Long-haul launch cleanup: could not terminalize orphan container %s (%v)\n", c.ID(), err)
	}
}

// LongHaulArbCoordinator starts the standing long-haul arb fleet coordinator (sp-mepj): a
// recovery-safe container that, each tick, launches a per-hull long-haul worker on every idle
// long-haul-tagged hull and runs the SHARED sp-m3122 liveness watchdog. It mirrors
// TradeFleetCoordinator's shape exactly (identity-only config → buildCommandForType so creation
// and recovery share one builder → NewContainer with iterations=-1 for the infinite reconcile
// loop → Add → runner). It claims nothing itself; each worker claims its own hull under
// operation="long-haul" through LaunchLongHaul.
//
// It is idempotent: a second arm while one is already running returns the live coordinator's id
// rather than spawning a rival that would double-dispatch the fleet. The engine is naturally
// INERT until the operator tags a hull long-haul (an empty long-haul fleet → an empty idle
// bucket → zero launches), so arming here changes nothing until the first `fleet add`.
func (s *DaemonServer) LongHaulArbCoordinator(ctx context.Context, playerID int, agentSymbol string) (string, error) {
	if existingID, err := firstContainerIDOfType(ctx, s.containerRepo, playerID, container.ContainerTypeLongHaulArbCoordinator); err != nil {
		return "", fmt.Errorf("failed to check for a running long-haul coordinator: %w", err)
	} else if existingID != "" {
		return existingID, nil // idempotent: already armed — return the live coordinator's id
	}

	containerID := utils.GenerateContainerID("longhaul_arb_coordinator", fmt.Sprintf("player-%d", playerID))

	// Identity only — the money-envelope caps + cadence default to the command's own
	// Admiral-authorized aggressive values (~1M/haul, ~2M exposure) when unset.
	config := map[string]interface{}{
		"container_id": containerID,
		"agent_symbol": agentSymbol,
	}

	cmd, err := s.buildCommandForType("longhaul_arb_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create long-haul arb coordinator command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeLongHaulArbCoordinator,
		playerID,
		-1,  // Infinite iterations (reconcile loop) — NOT a CoordinatorOwnsIterations type
		nil, // No parent container
		config,
		nil,
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "longhaul_arb_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist long-haul arb coordinator container: %w", err)
	}

	s.startContainerRunner(containerEntity, cmd, containerID, "Long-haul arb coordinator container")

	return containerID, nil
}

// longHaulTreasuryAdapter reads live treasury for the long-haul worker's money-envelope cushion
// fence (design §3), mirroring the autosizer/idle-arb treasury readers: the player token from
// context → GetAgent → credits. A read failure surfaces as an error so the worker's readEnvelope
// fail-closes (trades nothing that episode) rather than sizing a buy on a treasury it could not
// read (RULINGS #4).
type longHaulTreasuryAdapter struct{ api agentReader }

// NewLongHaulTreasuryReader wires the live-treasury reader the long-haul worker's money
// envelope reads through (design §3). Exported so main.go's composition root can build it over
// the daemon's API client without naming the unexported adapter type.
func NewLongHaulTreasuryReader(api agentReader) *longHaulTreasuryAdapter {
	return &longHaulTreasuryAdapter{api: api}
}

func (r *longHaulTreasuryAdapter) LiveTreasury(ctx context.Context) (int64, error) {
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("long-haul treasury read: no player token in context: %w", err)
	}
	agent, err := r.api.GetAgent(ctx, token)
	if err != nil {
		return 0, fmt.Errorf("long-haul treasury read: %w", err)
	}
	if agent == nil {
		return 0, fmt.Errorf("long-haul treasury read: nil agent")
	}
	return int64(agent.Credits), nil
}
