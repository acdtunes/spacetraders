package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/contract/depotstore"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// This file is the depot LIFECYCLE surface (bead sp-38xc): `depot start <name> <spec>`
// and `depot stop <name>`. start is the live-activation the plain declarative apply is
// missing — it PERSISTS one depot's topology AND launches its coordinators in one shot,
// with no daemon restart; stop is its inverse, tearing down that depot's running
// coordinators. Both reuse the existing seams (depotstore for durability,
// launchDepotCoordinators for the idle-idempotent launch, ListContainers/StopContainer for
// teardown) so the lifecycle adds orchestration only, never a parallel channel.

// depotContainerRef is one live container tagged with the ship crewing it — the join key
// depot teardown selects a depot's coordinators by.
type depotContainerRef struct {
	containerID string
	shipSymbol  string
}

// depotStopSink is the driven-port boundary for depot teardown: enumerate the player's
// live coordinator containers (id + crewing ship) and stop one by id. *DaemonServer
// satisfies it over ListContainers + StopContainer, so the SELECTION logic is unit-tested
// against a spy without a live container registry.
type depotStopSink interface {
	listDepotContainers(playerID int) []depotContainerRef
	stopContainer(containerID string) error
	// releaseDepotHull clears a reaped depot hull's role-fleet dedication (AssignFleet "") so the hull
	// returns to the general/trade pool after `depot stop` reaps its buffer container (sp-udgc). Without
	// it a `depot stop` leaves the hull stopped-but-still-"stocker"/"warehouse"-dedicated = off trade
	// (the claimed-hull half of the decommission strand). *DaemonServer writes the empty fleet tag; a spy
	// records the symbol.
	releaseDepotHull(ctx context.Context, shipSymbol string, playerID int) error
}

// startDepot persists ONE depot (upsert, non-destructive to the rest of the set) then
// launches ITS coordinators only — the live activation `depot start <name> <spec>` drives
// with no restart. The launch reuses the idle-idempotent path (a hull already flying its
// coordinator is a benign skip), so re-running start never double-launches. It builds a
// single-depot registry so the launch is scoped to exactly the named depot, never a
// whole-topology relaunch. Returns the number of coordinators launched.
func startDepot(ctx context.Context, store *depotstore.Store, sink depotCoordinatorSink, playerID int, c *depot.ContractDepot) (int, error) {
	if err := store.AddDepot(ctx, c); err != nil {
		return 0, err
	}
	reg := depot.NewRegistry([]*depot.ContractDepot{c})
	launchDepotCoordinators(ctx, reg, playerID, sink)
	return len(planDepotLaunches(reg)), nil
}

// stopDepot loads the live registry, then stops the named depot's warehouse + stocker
// coordinator containers (joined by crewing ship). It is FAIL-OPEN: a per-container stop
// error is logged and stepped over so one bad container never blocks the rest. Returns the
// number of containers stopped.
func stopDepot(ctx context.Context, store *depotstore.Store, sink depotStopSink, playerID int, depotID string) (int, error) {
	reg, err := store.LoadRegistry(ctx)
	if err != nil {
		return 0, err
	}
	refs := sink.listDepotContainers(playerID)
	// The crewing ship of each live container, so a reaped container's hull can be released.
	shipByContainer := make(map[string]string, len(refs))
	for _, r := range refs {
		shipByContainer[r.containerID] = r.shipSymbol
	}
	stopped := 0
	for _, id := range planDepotStops(reg, depotID, refs) {
		if err := sink.stopContainer(id); err != nil {
			fmt.Printf("Warning: depot %q stop of container %s skipped: %v\n", depotID, id, err)
			continue
		}
		stopped++
		// sp-udgc: RELEASE the reaped hull's role-fleet dedication (AssignFleet "") so an explicit
		// `depot stop` fully returns the warehouse/stocker hull to the general/trade pool. Without this
		// the stopped-but-still-"stocker"/"warehouse"-dedicated hull stays off trade — the claimed-hull
		// half of the decommission strand (the boot guard prevents re-strand on restart; this frees what
		// a running buffer container held). SCOPED to hulls whose container we SUCCESSFULLY reaped just
		// now: a graceful StopContainer released the hull's work-claim, so clearing the dedication returns
		// it to the pool; a hull whose container did NOT stop keeps its dedication (never leave a live
		// container on a poachable hull). Best-effort: a release failure is logged and stepped over so one
		// bad hull never blocks the rest of the teardown.
		ship := shipByContainer[id]
		if ship == "" {
			continue
		}
		if err := sink.releaseDepotHull(ctx, ship, playerID); err != nil {
			fmt.Printf("Warning: depot %q release of reaped hull %s skipped (left dedicated; operator can `fleet unassign`): %v\n", depotID, ship, err)
		}
	}
	return stopped, nil
}

// planDepotStops returns the container ids whose crewing ship is a warehouse or stocker
// element of the named depot — exactly the coordinators start launches, so stop is the
// precise inverse. Containers on ships the depot does not own are left running.
func planDepotStops(reg *depot.Registry, depotID string, refs []depotContainerRef) []string {
	ships := depotCoordinatorShips(reg, depotID)
	var ids []string
	for _, r := range refs {
		if ships[r.shipSymbol] {
			ids = append(ids, r.containerID)
		}
	}
	return ids
}

// depotCoordinatorShips is the set of ship symbols crewing the named depot's STANDING
// coordinators (warehouses + stockers) — the long-running containers stop tears down. Delivery
// hulls are POSITIONED (a one-shot NavigateShip reposition, sp-9j9c) rather than run as a standing
// coordinator, and source hubs are config-only, so neither has a long-running container to stop —
// they are excluded here, keeping stop the inverse of the STANDING-coordinator launches.
func depotCoordinatorShips(reg *depot.Registry, depotID string) map[string]bool {
	ships := map[string]bool{}
	if reg == nil {
		return ships
	}
	for _, c := range reg.Depots() {
		if c.ID() != depotID {
			continue
		}
		for _, w := range c.Warehouses() {
			if w.ShipSymbol != "" {
				ships[w.ShipSymbol] = true
			}
		}
		for _, st := range c.Stockers() {
			if st.ShipSymbol != "" {
				ships[st.ShipSymbol] = true
			}
		}
	}
	return ships
}

// StartDepot persists one depot's topology from a boundary spec and launches its
// coordinators in one shot — the daemon-side of `depot start <name> <spec>`. Returns the
// number of coordinators launched.
func (s *DaemonServer) StartDepot(ctx context.Context, playerID int, spec DepotSpec) (int, error) {
	c, err := spec.toDomain()
	if err != nil {
		return 0, err
	}
	return startDepot(ctx, s.depotStore(playerID), s, playerID, c)
}

// StopDepot stops the named depot's running warehouse + stocker coordinators — the
// daemon-side of `depot stop <name>`. Returns the number of containers stopped.
func (s *DaemonServer) StopDepot(ctx context.Context, playerID int, depotID string) (int, error) {
	return stopDepot(ctx, s.depotStore(playerID), s, playerID, depotID)
}

// listDepotContainers (depotStopSink) enumerates the player's RUNNING/PENDING containers
// tagged with the ship crewing each — the join depot teardown selects coordinators by. The
// ship symbol is the launch-frozen "ship_symbol" metadata every warehouse/stocker
// coordinator carries.
func (s *DaemonServer) listDepotContainers(playerID int) []depotContainerRef {
	live := string(container.ContainerStatusRunning) + "," + string(container.ContainerStatusPending)
	conts := s.ListContainers(&playerID, &live)
	refs := make([]depotContainerRef, 0, len(conts))
	for _, cont := range conts {
		shipSymbol, _ := cont.GetMetadataValue("ship_symbol")
		ship, _ := shipSymbol.(string)
		refs = append(refs, depotContainerRef{containerID: cont.ID(), shipSymbol: ship})
	}
	return refs
}

// stopContainer (depotStopSink) delegates to the daemon's existing container-stop path
// (which also stops child containers).
func (s *DaemonServer) stopContainer(containerID string) error {
	return s.StopContainer(containerID)
}

// releaseDepotHull (depotStopSink) clears a reaped depot hull's role-fleet dedication so it returns
// to the general/trade pool after `depot stop` reaps its buffer container (sp-udgc). It writes the
// empty fleet tag through the SAME single AssignFleet dedication column positionDepotElementHull
// re-dedicates through (the `fleet unassign` idiom), so every idle-grab exclusion built on that tag
// stops applying and trade re-adopts the hull. A graceful StopContainer already released the hull's
// work-claim on reap, so clearing the dedication is what actually returns it to the pool. Idempotent:
// an already-undedicated hull is a no-op write.
func (s *DaemonServer) releaseDepotHull(ctx context.Context, shipSymbol string, playerID int) error {
	if s.shipRepo == nil {
		return fmt.Errorf("release depot hull %s: no ship repository wired", shipSymbol)
	}
	return s.shipRepo.AssignFleet(ctx, shipSymbol, "", shared.MustNewPlayerID(playerID))
}
