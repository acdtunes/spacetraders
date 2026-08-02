package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// depotLaunchIntent is one coordinator to (idempotently) start for one crewed depot
// element. Every position is data carried from the topology — nothing here is hardcoded
// (sp-u9xa parametrization principle).
type depotLaunchIntent struct {
	depotID string
	role    depot.Role // RoleWarehouse | RoleStocker | RoleDeliveryHull | RoleSourceHub
	// shipSymbol is the crewing hull to fly (a warehouse hull, a stocker hull, a delivery hull, or a source-hub hull).
	shipSymbol string
	// targetWaypoint is where the element is anchored: a warehouse parks at its OWN waypoint; a
	// stocker deposits into the depot's destination warehouse ANCHOR (warehouses[0]); a delivery
	// hull parks at its OWN hub waypoint to wait for the contract coordinator's dispatch;
	// a source hub parks its crewing hull at its OWN market waypoint (sp-3l64).
	targetWaypoint string
}

// planDepotLaunches reads a registry and returns the coordinators to start: one warehouse
// per crewed warehouse element (parked at its own waypoint) and one stocker per crewed stocker
// element (all pointed at the depot's destination warehouse anchor as their deposit target).
// It is PURE — no I/O — so the launch DECISION is unit-tested without any container
// infrastructure. A declared-but-uncrewed slot (empty ShipSymbol — sized before a hull is
// pinned) yields no launch: there is no hull to fly yet. A nil/empty registry yields nothing
// (destination warehousing OFF — the regression-safe default).
func planDepotLaunches(reg *depot.Registry) []depotLaunchIntent {
	if reg == nil {
		return nil
	}
	var intents []depotLaunchIntent
	for _, c := range reg.Depots() {
		warehouses := c.Warehouses()
		if len(warehouses) == 0 {
			continue // NewContractDepot guarantees >=1, but never trust a mutated registry
		}
		anchor := warehouses[0].Waypoint // the routing anchor + shared deposit target
		for _, w := range warehouses {
			if w.ShipSymbol == "" {
				continue // declared-but-uncrewed slot: no hull to fly yet
			}
			intents = append(intents, depotLaunchIntent{
				depotID:        c.ID(),
				role:           depot.RoleWarehouse,
				shipSymbol:     w.ShipSymbol,
				targetWaypoint: w.Waypoint, // a warehouse parks at its own waypoint
			})
		}
		for _, st := range c.Stockers() {
			if st.ShipSymbol == "" {
				continue
			}
			intents = append(intents, depotLaunchIntent{
				depotID:        c.ID(),
				role:           depot.RoleStocker,
				shipSymbol:     st.ShipSymbol,
				targetWaypoint: anchor, // every depot stocker deposits into the anchor
			})
		}
		// sp-9j9c #2: place each crewed delivery hull at its OWN hub waypoint — the hulls are
		// positioned across hubs so the nearest-selection router (#1) can route each cluster's
		// contract to its local hull. A declared-but-uncrewed slot yields no launch (no hull to fly
		// yet), matching the warehouse/stocker discipline.
		for _, dh := range c.DeliveryHulls() {
			if dh.ShipSymbol == "" {
				continue
			}
			intents = append(intents, depotLaunchIntent{
				depotID:        c.ID(),
				role:           depot.RoleDeliveryHull,
				shipSymbol:     dh.ShipSymbol,
				targetWaypoint: dh.Waypoint, // a delivery hull parks at its own hub waypoint
			})
		}
		// ROLE-AGNOSTIC POSITIONING: position each crewed source-hub hull at its OWN
		// market waypoint. A source hub has no standing coordinator (it feeds the stockers as a
		// buy anchor), so — like a delivery hull — its assignment is a one-shot free+exclude+park:
		// the crewing hull is freed from any prior fleet, excluded from the contract grab, and
		// navigated to the hub, instead of drifting off-config. An uncrewed slot yields no launch.
		for _, sh := range c.SourceHubs() {
			if sh.ShipSymbol == "" {
				continue // declared-but-uncrewed slot: no hull to fly yet
			}
			intents = append(intents, depotLaunchIntent{
				depotID:        c.ID(),
				role:           depot.RoleSourceHub,
				shipSymbol:     sh.ShipSymbol,
				targetWaypoint: sh.Waypoint, // a source hub parks its hull at its own market waypoint
			})
		}
	}
	return intents
}

// depotCoordinatorSink is the driven-port boundary to the container-launch infrastructure:
// the two primitives the depot orchestrator dispatches to. *DaemonServer satisfies it by
// delegating to its existing StartWarehouse/StartStocker path (no parallel channel). Kept
// narrow + injectable so the orchestration is unit-tested against a spy without spawning
// container goroutines or requiring idle hulls in a DB.
type depotCoordinatorSink interface {
	launchDepotWarehouse(ctx context.Context, shipSymbol, warehouseWaypoint string, playerID int) error
	launchDepotStocker(ctx context.Context, shipSymbol, warehouseWaypoint string, playerID int) error
	// launchDepotDelivery POSITIONS a delivery hull at its hub waypoint. Unlike the
	// standing warehouse/stocker coordinators it is a one-shot reposition — the hull waits idle
	// at its hub for the contract coordinator to dispatch it on demand.
	launchDepotDelivery(ctx context.Context, shipSymbol, hubWaypoint string, playerID int) error
	// launchDepotSourceHub POSITIONS a source-hub hull at its market waypoint (sp-3l64). Like the
	// delivery hull it is a one-shot free+exclude+park (a source hub has no standing coordinator);
	// unlike it, the parked hull is not dispatched — it holds the buy anchor for the stockers.
	launchDepotSourceHub(ctx context.Context, shipSymbol, hubWaypoint string, playerID int) error
	// homeContractWorkerReserve is the reserve-floor census the delivery-hull launch consults so it
	// never pins the LAST undedicated home general haulers the contract coordinator needs to source
	// an UNBUFFERED-good contract: the current pool size, the configured floor to
	// keep, and which ships are in the pool right now. *DaemonServer computes it from the ship repo
	// + the live min_home_contract_workers knob; a spy returns a canned budget. A zero value reserves
	// nothing (regression-safe: the pre-sp-mzdk pin-everything behavior).
	homeContractWorkerReserve(ctx context.Context, reg *depot.Registry, playerID int) deliveryPinBudget
	// dedicateContractReserve fleet-ASSIGNS a reserved (or reclaimed) home general hauler to the
	// exclusive "contract" fleet (bead sp-7zoq) — the write that makes the reserve poach-proof. It
	// routes through the SAME single AssignShipFleet dedication path the CLI `fleet assign` and the
	// coordinator's own reconcile use, so the exclusion (FindIdleLightHaulers / SENSE / ClaimShip's
	// atomic guard) takes effect for free. *DaemonServer sends the automated AssignShipFleetCommand; a
	// spy records the symbol. Idempotent: re-dedicating a hull already tagged "contract" writes nothing.
	dedicateContractReserve(ctx context.Context, shipSymbol string, playerID int) error
}

// depotSourceHubFleet is the DedicatedFleet tag a depot source-hub hull carries (sp-3l64). Like
// depot.DeliveryHullFleet it is DISTINCT from the contract coordinator's "contract" fleet, so a
// parked source-hub hull is invisible to BOTH pools the coordinator draws from and can never be
// re-grabbed off its market anchor. A source hub has no coordinator of its own, so — unlike
// warehouse/stocker, which re-dedicate to their coordinator's own tag — it uses this depot-owned tag.
const depotSourceHubFleet = "depot-source-hub"

// launchDepotCoordinators starts every coordinator a loaded registry declares, dispatching
// each planned intent to the sink. It is FAIL-OPEN and safely re-runnable: a per-element launch
// failure (most commonly a hull that is already flying its coordinator — a benign
// already-launched skip the sink returns as nil) is logged and stepped over so one bad element
// never blocks the rest, and a reboot re-runs it harmlessly (the sink's idle-gap discipline
// refuses a double-launch). It is the same shape as ensureBootStandingCoordinators.
func launchDepotCoordinators(ctx context.Context, reg *depot.Registry, playerID int, sink depotCoordinatorSink) {
	intents := planDepotLaunches(reg)
	// Reserve floor (sp-mzdk + sp-7zoq): before pinning, consult the census of home general haulers
	// and RESERVE min_home_contract_workers of them so an unbuffered-good contract always has a
	// sourcing worker. sp-7zoq: a reserved delivery hull is not left undedicated (poachable) but
	// DEDICATED to the exclusive "contract" fleet — held back from its hub pin and fleet-assigned so
	// no other coordinator can grab it, while the contract coordinator still sources with it via its
	// own FindIdleShipsByFleet("contract") lookup.
	budget := sink.homeContractWorkerReserve(ctx, reg, playerID)
	reserved := reserveHomeContractWorkers(intents, budget)
	// Reclaim (sp-7zoq, the deferred sp-mzdk temp-un-pin): when too few undedicated hulls remained to
	// reach the floor, ALSO re-dedicate already-pinned delivery hulls to "contract" — computed UP FRONT
	// so the pin loop below skips them (never re-pins a hull it is about to reclaim, no churn). Capped
	// at the exact shortfall, so the reserve lands at the floor and no more.
	reclaim := reclaimPinnedForFloor(budget, len(reserved))
	toDedicate := map[string]bool{}
	for shipSymbol := range reserved {
		toDedicate[shipSymbol] = true
	}
	for _, shipSymbol := range reclaim {
		toDedicate[shipSymbol] = true
	}
	handled := map[string]bool{}
	for _, intent := range intents {
		if intent.role == depot.RoleDeliveryHull && toDedicate[intent.shipSymbol] {
			if err := sink.dedicateContractReserve(ctx, intent.shipSymbol, playerID); err != nil {
				fmt.Printf("Reserve floor (sp-7zoq): failed to dedicate home general hauler %s to the contract fleet (left in prior state): %v\n",
					intent.shipSymbol, err)
				continue // never pin a hull the floor meant to reserve — leave it as-is on failure
			}
			handled[intent.shipSymbol] = true
			fmt.Printf("Reserve floor (sp-7zoq): dedicated home general hauler %s to the exclusive contract fleet (poach-proof reserve) instead of pinning it to hub %s\n",
				intent.shipSymbol, intent.targetWaypoint)
			continue
		}
		dispatchDepotLaunch(ctx, sink, intent, playerID)
	}
	// A reclaim target that is no longer a declared delivery intent (declared-removed but still pinned)
	// never passes through the loop above, so dedicate it directly — still a re-dedication TO contract,
	// never an un-dedication to the poachable pool.
	for _, shipSymbol := range reclaim {
		if handled[shipSymbol] {
			continue
		}
		if err := sink.dedicateContractReserve(ctx, shipSymbol, playerID); err != nil {
			fmt.Printf("Reserve floor (sp-7zoq): failed to reclaim hull %s to the contract fleet: %v\n", shipSymbol, err)
			continue
		}
		fmt.Printf("Reserve floor (sp-7zoq): reclaimed hull %s to the exclusive contract fleet to restore the sourcing floor\n", shipSymbol)
	}
}

// depotsWithLiveDemand partitions depots by whether their DOMAIN still has LIVE contract demand
// (bead sp-udgc — the depot-launch re-strander (ii), sibling to sp-2jrz's capacity_reconciler
// re-strander (i)). A depot's domain is the destination SYSTEM of its anchor (first) warehouse —
// the SAME system the depot's receipt-demand solve is scoped to (depotWarehouseTargetUnits) — so a
// depot is LIVE iff liveSystems contains that system. A DECOMMISSIONED/stale depot (its contracts
// fulfilled or expired, so no active contract delivers to its system any longer) lands in skipped:
// it must NOT be re-materialized into stocker/warehouse containers or have its crewing hulls
// re-dedicated off trade on restart. Input order is preserved, so a live-only subset launches
// byte-identically to the pre-guard launchDepotCoordinators. A depot with no warehouse (never a
// valid depot — NewContractDepot forbids it) has no destination geometry and so no domain to be
// live for: it is treated as skipped.
func depotsWithLiveDemand(depots []*depot.ContractDepot, liveSystems map[string]bool) (live, skipped []*depot.ContractDepot) {
	for _, d := range depots {
		warehouses := d.Warehouses()
		if len(warehouses) == 0 {
			skipped = append(skipped, d)
			continue
		}
		if liveSystems[shared.ExtractSystemSymbol(warehouses[0].Waypoint)] {
			live = append(live, d)
		} else {
			skipped = append(skipped, d)
		}
	}
	return live, skipped
}

// launchLiveDepotCoordinators is the RESTART-SAFE launch entrypoint (bead sp-udgc): it launches only
// the coordinators of depots whose domain still has LIVE contract demand, WITHHOLDING any
// decommissioned/stale depot so a daemon restart never re-spawns its buffer containers or
// re-dedicates its crewing hulls off trade (the confirmed re-strander (ii)). It is a thin demand
// FILTER in front of launchDepotCoordinators — a live depot launches byte-identically; only a
// no-live-demand depot is held back. liveSystems is the set of destination systems the player's live
// (accepted, not-fulfilled) contracts deliver to. Returns the ids of the skipped depots so the boot
// log can name what it withheld. A nil registry launches nothing.
func launchLiveDepotCoordinators(ctx context.Context, reg *depot.Registry, playerID int, sink depotCoordinatorSink, liveSystems map[string]bool) []string {
	if reg == nil {
		return nil
	}
	live, skipped := depotsWithLiveDemand(reg.Depots(), liveSystems)
	launchDepotCoordinators(ctx, depot.NewRegistry(live), playerID, sink)
	ids := make([]string, 0, len(skipped))
	for _, d := range skipped {
		ids = append(ids, d.ID())
	}
	return ids
}

// dispatchDepotLaunch routes ONE planned intent to the sink's per-role launch (sp-3l64). Extracted
// so BOTH the whole-registry boot/reload path (launchDepotCoordinators) and the granular
// element-add path (positionAddedDepotElement) dispatch through ONE role→launch mapping — a new
// role is wired in exactly one place. Fail-open: a per-element launch failure (most commonly a hull
// already flying its coordinator — the benign already-launched skip the sink returns as nil) is
// logged and stepped over so one bad element never blocks the rest.
func dispatchDepotLaunch(ctx context.Context, sink depotCoordinatorSink, intent depotLaunchIntent, playerID int) {
	var err error
	switch intent.role {
	case depot.RoleWarehouse:
		err = sink.launchDepotWarehouse(ctx, intent.shipSymbol, intent.targetWaypoint, playerID)
	case depot.RoleStocker:
		err = sink.launchDepotStocker(ctx, intent.shipSymbol, intent.targetWaypoint, playerID)
	case depot.RoleDeliveryHull:
		err = sink.launchDepotDelivery(ctx, intent.shipSymbol, intent.targetWaypoint, playerID)
	case depot.RoleSourceHub:
		err = sink.launchDepotSourceHub(ctx, intent.shipSymbol, intent.targetWaypoint, playerID)
	default:
		return
	}
	if err != nil {
		fmt.Printf("Warning: depot %q %s launch for ship %s skipped: %v\n",
			intent.depotID, intent.role, intent.shipSymbol, err)
	}
}

// hullCrewsOperation reports whether shipSymbol is one of a warehouse operation's storage hulls
// — the join that pairs a reload intent's hull to its running storage_operations row.
func hullCrewsOperation(storageShips []string, shipSymbol string) bool {
	for _, s := range storageShips {
		if s == shipSymbol {
			return true
		}
	}
	return false
}
