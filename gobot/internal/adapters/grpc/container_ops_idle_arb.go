package grpc

import (
	"context"
	"fmt"

	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// LaunchIdleArb implements appContract.IdleArbLauncher (sp-1z2h): it starts a
// hub-local one-shot guarded arb leg on a contract-fleet hull, dispatched by
// the contract coordinator's idle-gap dispatcher.
//
// It deliberately differs from the captain-directed StartArbRun in exactly one
// way, and inherits everything else (the arb_run command factory, the
// recovery-safe container row, the ContainerRunner's release-on-death, and
// every in-run guard):
//
//   - SYNCHRONOUS CLAIM: the hull is claimed through the atomic operation-checked
//     ClaimShip with the DISPATCHER'S fleet identity (spec.Operation, "contract")
//     — the same row-locked l7h2 check every contract worker claim goes through —
//     BEFORE this returns, rather than leaving the claim to the runner's async
//     start the way StartArbRun does. A hull that is busy, captain-reserved, or
//     dedicated to a different fleet is rejected here; losing a race against the
//     coordinator's own contract claim surfaces as that same rejection. Claiming
//     before return makes the dispatcher's reserve accounting exact: return nil and
//     the hull is claimed in the DB, return an error and the launch left nothing
//     behind.
//
// ORDERING (sp-1hp9): the container row MUST be persisted BEFORE the hull claim.
// ships.container_id carries a foreign key to containers.id (fk_ships_container),
// so a ClaimShip that writes ships.container_id = containerID before the containers
// row exists is an FK violation (Postgres 23503) — which is exactly what made every
// idle-arb dispatch dead on arrival. StartArbRun is FK-safe for free because its
// only claim is the runner's, which runs after Add; this claim-before-return path
// has to order Add → ClaimShip explicitly.
//
// The claim identity is persisted in the launch config ("operation"), so the
// runner's createShipAssignments re-claims idempotently on start AND on restart
// recovery — the container stays recovery-safe with the correct fleet identity
// (RULINGS #2).
func (s *DaemonServer) LaunchIdleArb(ctx context.Context, spec appContract.IdleArbSpec) (string, error) {
	if spec.ShipSymbol == "" || spec.Good == "" || spec.BuyAt == "" || spec.SellAt == "" {
		return "", fmt.Errorf("idle-arb launch requires ship, good, buy-at and sell-at")
	}
	if spec.BuyAt == spec.SellAt {
		return "", fmt.Errorf("idle-arb buy-at and sell-at must differ (both %s)", spec.BuyAt)
	}
	if spec.Operation == "" {
		return "", fmt.Errorf("idle-arb launch requires the dispatcher's fleet identity (operation)")
	}

	playerID := shared.MustNewPlayerID(spec.PlayerID)
	containerID := utils.GenerateContainerID("idle-arb", spec.ShipSymbol)

	config := map[string]interface{}{
		"ship_symbol":             spec.ShipSymbol,
		"good":                    spec.Good,
		"buy_at":                  spec.BuyAt,
		"sell_at":                 spec.SellAt,
		"container_id":            containerID,
		"max_units":               0, // hold-capped by the run itself
		"max_spend":               spec.MaxSpend,
		"min_margin":              spec.MinMargin,
		"working_capital_reserve": 0, // 0 → the run's non-tunable default floor
		"operation":               spec.Operation,
		// sp-lbbm: arm the arb run's per-tranche sell floor with the dispatcher's
		// live 80%-of-quote knob (0 → the run's own default). Persisted so a restart
		// rebuild resumes with the same floor (RULINGS #2).
		"sell_floor_fraction": spec.SellFloorFraction,
	}

	// Same factory recovery uses ("arb_run"), so launch and restart rebuild can
	// never drift. Built before anything is persisted or claimed — a build failure
	// leaves no side effects.
	cmd, err := s.buildCommandForType("arb_run", config, spec.PlayerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create idle-arb command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeTrading,
		spec.PlayerID,
		1,   // one-shot leg
		nil, // top-level, recovered independently
		config,
		nil, // default RealClock
	)

	// Persist the container row FIRST: it is the FK parent for the hull claim's
	// ships.container_id (fk_ships_container). Claiming before this row exists is the
	// sp-1hp9 FK 23503 that made every idle-arb dispatch dead on arrival.
	if err := s.containerRepo.Add(ctx, containerEntity, "arb_run"); err != nil {
		return "", fmt.Errorf("failed to persist idle-arb container: %w", err)
	}

	// Now the atomic, operation-checked claim (l7h2): busy / captain-reserved /
	// foreign-dedicated hulls are rejected inside the row-locked transaction. The
	// container row above satisfies the FK, and this synchronous claim keeps the
	// dispatcher's reserve accounting exact (claimed-on-return, or not launched).
	if err := s.shipRepo.ClaimShip(ctx, spec.ShipSymbol, containerID, playerID, spec.Operation); err != nil {
		// The row is now an orphan — persisted, but no hull was claimed and no runner
		// owns it. Terminalize it FAILED the way the runner's sp-cr86 claim-failure
		// path does, so it is not a zombie stuck at PENDING with no one to advance it.
		s.terminalizeIdleArbClaimFailure(ctx, containerEntity, err)
		return "", fmt.Errorf("idle-arb claim of %s refused: %w", spec.ShipSymbol, err)
	}

	// The runner's own claim is idempotent for this containerID; it owns release on
	// every terminal path from here (completion, crash, cancel).
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Idle-arb container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// terminalizeIdleArbClaimFailure marks a just-persisted idle-arb container row FAILED
// when the synchronous hull claim is refused AFTER the row exists (the hull was taken
// between the dispatcher's read and this claim). Without it the row is a zombie:
// persisted PENDING with no runner ever created to advance or release it, and the
// watchkeeper would spam heartbeat_lost for it — the same failure sp-cr86 fixed inside
// the runner, here at the pre-runner claim boundary. No ship state is released: the
// claim failed, so nothing was ever assigned to this container.
func (s *DaemonServer) terminalizeIdleArbClaimFailure(ctx context.Context, c *container.Container, cause error) {
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
		fmt.Printf("Idle-arb launch cleanup: could not terminalize orphan container %s (%v)\n", c.ID(), err)
	}
}
