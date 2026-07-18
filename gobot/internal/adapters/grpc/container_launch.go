package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
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

// findContainerModelByID linear-scans every container for one with the given ID. The
// worker start/recovery paths reach it without a playerID, so they cannot use the
// indexed Get; this is the shared form of the ListAll-then-scan the gas and scouting
// start paths each inlined. Returns a not-found error if absent.
func (s *DaemonServer) findContainerModelByID(ctx context.Context, containerID string) (*persistence.ContainerModel, error) {
	allContainers, err := s.containerRepo.ListAll(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}
	for _, c := range allContainers {
		if c.ID == containerID {
			return c, nil
		}
	}
	return nil, fmt.Errorf("container %s not found", containerID)
}
