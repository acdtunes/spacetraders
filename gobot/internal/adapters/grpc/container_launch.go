package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// startContainerRunner wires a ContainerRunner for an already-persisted container,
// registers it under containerID, and runs it in the background — the shared launch
// tail every container-start verb repeated inline. logLabel is the human prefix the
// background failure line carries (ops greps these): it is emitted verbatim as
// "<logLabel> <containerID> failed: <err>", e.g. "Trade-route container" or plain
// "Container" for the single-op ship verbs. containerID is passed explicitly (rather
// than read off the entity) so the registration key and the log line stay byte-for-byte
// what each call site used.
func (s *DaemonServer) startContainerRunner(containerEntity *container.Container, cmd interface{}, containerID, logLabel string) {
	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("%s %s failed: %v\n", logLabel, containerID, err)
		}
	}()
}

// requireIdleHull enforces the idle-gap discipline every hull-taking start verb shares: fly
// a genuinely idle hull, never steal one mid-task. opLabel names the caller in the error.
func (s *DaemonServer) requireIdleHull(ctx context.Context, shipSymbol string, playerID int, opLabel string) error {
	ship, err := s.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		return fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
	}
	if ship == nil {
		return fmt.Errorf("ship %s not found", shipSymbol)
	}
	if !ship.IsIdle() {
		return fmt.Errorf("ship %s is not idle (assigned to %q) - %s only takes idle-gap hulls", shipSymbol, ship.ContainerID(), opLabel)
	}
	return nil
}

// findContainerModelByID returns the container with this ID. The worker start/recovery paths
// reach it without a playerID, so they cannot use the player-scoped Get; this is the shared form
// of the lookup the gas and scouting start paths each inlined. Returns a not-found error if
// absent.
//
// It used to call ListAll(ctx, nil) and scan the result in Go, which loaded the whole containers
// table on every worker start (sp-72gmi). The repository now does one indexed read against the
// primary key, whose leading column is id.
func (s *DaemonServer) findContainerModelByID(ctx context.Context, containerID string) (*persistence.ContainerModel, error) {
	model, err := s.containerRepo.FindByIDAcrossPlayers(ctx, containerID)
	if err != nil {
		return nil, err
	}
	if model == nil {
		return nil, fmt.Errorf("container %s not found", containerID)
	}
	return model, nil
}
