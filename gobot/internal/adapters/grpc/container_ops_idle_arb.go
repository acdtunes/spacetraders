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
// It deliberately differs from the captain-directed StartArbRun in exactly two
// ways, and inherits everything else (the arb_run command factory, the
// recovery-safe container row, the ContainerRunner's release-on-death, and
// every in-run guard):
//
//   - CLAIM-FIRST: the hull is claimed through the atomic operation-checked
//     ClaimShip BEFORE any container row exists, with the DISPATCHER'S fleet
//     identity (spec.Operation, "contract") — the same row-locked l7h2 check
//     every contract worker claim goes through. A hull that is busy,
//     captain-reserved, or dedicated to a different fleet is rejected right
//     here with zero side effects; losing a race against the coordinator's own
//     contract claim surfaces as that same rejection. Claim-first also makes
//     the dispatcher's reserve accounting exact: when this returns, the hull
//     is claimed in the DB or the launch didn't happen.
//
//   - The claim identity is persisted in the launch config ("operation"), so
//     the runner's createShipAssignments re-claims idempotently on start AND
//     on restart recovery — the container remains recovery-safe with the
//     correct fleet identity (RULINGS #2).
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

	// Atomic, operation-checked claim (l7h2): busy / captain-reserved /
	// foreign-dedicated hulls are rejected inside the row-locked transaction.
	// This subsumes StartArbRun's advisory IsIdle read with the stronger
	// guarantee the reserve invariant needs.
	if err := s.shipRepo.ClaimShip(ctx, spec.ShipSymbol, containerID, playerID, spec.Operation); err != nil {
		return "", fmt.Errorf("idle-arb claim of %s refused: %w", spec.ShipSymbol, err)
	}

	// From here on, a failed launch must release the claim it just took —
	// there is no container row yet, so no runner owns release-on-death.
	releaseClaim := func(reason string) {
		ship, err := s.shipRepo.FindBySymbol(ctx, spec.ShipSymbol, playerID)
		if err != nil || ship == nil {
			fmt.Printf("Idle-arb launch cleanup: could not load %s to release claim (%v)\n", spec.ShipSymbol, err)
			return
		}
		ship.ForceRelease(reason, s.clock)
		if err := s.shipRepo.Save(ctx, ship); err != nil {
			fmt.Printf("Idle-arb launch cleanup: could not release %s (%v)\n", spec.ShipSymbol, err)
		}
	}

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
	}

	// Same factory recovery uses ("arb_run"), so launch and restart rebuild
	// can never drift.
	cmd, err := s.buildCommandForType("arb_run", config, spec.PlayerID, containerID)
	if err != nil {
		releaseClaim("idle_arb_launch_failed")
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

	if err := s.containerRepo.Add(ctx, containerEntity, "arb_run"); err != nil {
		releaseClaim("idle_arb_launch_failed")
		return "", fmt.Errorf("failed to persist idle-arb container: %w", err)
	}

	// The runner's own claim is idempotent for this containerID; it owns
	// release on every terminal path from here (completion, crash, cancel).
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Idle-arb container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}
