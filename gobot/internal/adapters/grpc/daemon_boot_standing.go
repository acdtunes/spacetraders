package grpc

// sp-382j: Admiral-selected launch model (a) — the construction-supply drain is now a STANDING
// coordinator launched unconditionally at every daemon boot, mirroring how the other standing
// coordinators (GoodsFactoryCoordinator/StartGoodsFactory, SitingCoordinator, ...) already
// auto-start. Before this, launch was bootstrap-EnsureRunning-only: with no active bootstrapper
// the ConstructionCoordinator never ran even once, so RecoverRunningContainers (which only
// re-adopts containers already PERSISTED as RUNNING) found nothing to recover, leaving a live
// gate-construction pipeline unsupplied forever. bootStandingCoordinatorTypes declares the
// boot-launch membership as data, mirroring executorContainerTypes in bootstrap_ports_gate.go.

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// bootStandingCoordinatorTypes are the container types launched unconditionally at every daemon
// boot (Start()), regardless of whether a bootstrapper has ever run. Each launch reuses the
// idempotent EnsureRunning path (skips if already RUNNING/PENDING), so a restart never
// double-launches — it simply re-adopts the container already running from the prior boot.
var bootStandingCoordinatorTypes = []container.ContainerType{
	container.ContainerTypeConstructionCoordinator,
}

// ensureBootStandingCoordinators launches every boot-standing coordinator type not already
// running, for the given player. Safe to call every boot: each type's launch is idempotent, so a
// restart adopts the existing container instead of double-launching. A launch failure is logged
// and non-fatal — one type's failure must never block another's launch attempt, and must never
// fail daemon startup.
func (s *DaemonServer) ensureBootStandingCoordinators(ctx context.Context, playerID int) {
	for _, ct := range bootStandingCoordinatorTypes {
		switch ct {
		case container.ContainerTypeConstructionCoordinator:
			mc := &bootstrapManufacturingController{server: s}
			if err := mc.EnsureRunning(ctx, playerID); err != nil {
				fmt.Printf("Warning: failed to launch boot-standing construction coordinator: %v\n", err)
			}
		}
	}
}
