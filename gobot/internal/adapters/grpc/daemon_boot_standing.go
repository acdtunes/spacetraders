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
//
// The STANDING stocker (sp-k1ka) is deliberately NOT a member here: unlike the player-scoped
// construction drain (which discovers idle haulers per-tick and needs no launch parameters), a
// stocker is pinned to a SPECIFIC dedicated hull + home warehouse, so there is nothing to
// unconditionally boot-launch without captain-supplied config. Its "survives restart" comes
// instead from the persisted `standing` launch config + RecoverRunningContainers, which re-adopts
// the RUNNING stocker row and rebuilds it STANDING via buildCommandForType (RULINGS #2). The
// captain launches it once (`workflow stocker --standing`); it then self-sustains and re-adopts
// across restarts with no manual relaunch.
var bootStandingCoordinatorTypes = []container.ContainerType{
	container.ContainerTypeConstructionCoordinator,
	// sp-orgp: the market-freshness auto-sizer is a genuinely STANDING coordinator (it must
	// hold every market under an SLA continuously), so it boot-launches unconditionally like
	// the construction drain. Its launch is idempotent (skips if already RUNNING/PENDING) and
	// every action it takes is guarded — money guards on buys, and a fail-safe that never
	// mass-retires on an empty census — so an armed auto-start is safe.
	container.ContainerTypeMarketFreshnessSizer,
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
		case container.ContainerTypeMarketFreshnessSizer:
			// Idempotent: skip if a sizer is already RUNNING/PENDING (a warm restart re-adopts
			// it from its persisted config via RecoverRunningContainers instead). All-default
			// knobs (RULINGS #5); the coordinator fills in its documented defaults.
			running, err := containerTypeRunning(ctx, s.containerRepo, playerID, container.ContainerTypeMarketFreshnessSizer)
			if err != nil {
				fmt.Printf("Warning: failed to check market-freshness sizer state: %v\n", err)
			} else if !running {
				if _, lerr := s.MarketFreshnessSizerCoordinator(ctx, playerID, 0, false, 0, 0, 0, 0, 0); lerr != nil {
					fmt.Printf("Warning: failed to launch boot-standing market-freshness sizer: %v\n", lerr)
				}
			}
		}
	}

	// sp-hoc6: ALONGSIDE the construction drain, keep the gate's source EXPORT-factories fed with their
	// import inputs (standing InputsOnly goods_factory feeders) so the drain's buying of the gate output
	// stays under the buy-ceiling (sp-layd) instead of depleting export supply. Config-driven feeder set
	// (RULINGS #5), idempotent, and restart-resilient the same way the drain is — the feeders are
	// goods_factory coordinators re-adopted by RecoverRunningContainers, and this pass skips any already
	// running (RULINGS #2). Runs here, after recovery, so a warm restart re-adopts rather than duplicates.
	s.ensureGateSourceFeeders(ctx, playerID)
}
