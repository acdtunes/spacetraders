package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// launchDepotWarehouse (depotCoordinatorSink) starts a destination-side depot warehouse on shipSymbol
// parked at warehouseWaypoint, stocking the FIXED far-source whitelist + flat per-good caps
// (contractscaler.FarSourceGoods / DepotTargetUnits) — universe-invariant, resolved once at design time,
// NEVER demand-mined or recomputed (no runtime solver, no re-sensing; Admiral ruling +
// economy-analyst review). This is the SINGLE launch path for BOTH the scaler's grow and the boot
// reload (launchDepotCoordinators), so the pin is RESTART-SAFE (RULINGS #2): a reboot re-pins the SAME
// set, so an armed warehouse's goods survive every restart — a receipt-miner re-solve here would
// OVERWRITE them on the first reboot. A hull already flying its coordinator (recovered on
// reload) has the whitelist re-applied to its running row in place — no double-launch (the IsIdle gate
// governs the LAUNCH only). It reuses persistAndRunWarehouse, so the container persistence / claim /
// recovery path is byte-identical to a captain-launched warehouse.
//
// ROLE-AGNOSTIC: FIRST free+re-dedicate the hull to its OWN "warehouse" fleet via the shared
// positionDepotElementHull (navigateOnAssign=false — the warehouse COORDINATOR parks the hull). This
// unblocks a hull added from a FOREIGN fleet: a "contract"-tagged hull can't be claimed under operation
// "warehouse" and a busy one isn't idle — so re-dedicating to "warehouse" both excludes it from the
// contract grab AND satisfies the coordinator's operation-checked claim.
//
// A warehouse hull is validated for home reachability FIRST, unconditionally, via the SAME
// depotElementHullViable precondition the stocker uses — the live TORWIND-19 stranding
// (IN_ORBIT X1-ZK26, no gate route to home X1-UM5) hit the
// warehouse grow, not just the stocker. A non-viable hull is evicted (stale binding removed,
// un-dedicated, claim released) instead of (re)launched — never seated, never positioned — so the
// scaler ramp re-grows the role on a home-viable hull next tick.
func (s *DaemonServer) launchDepotWarehouse(ctx context.Context, shipSymbol, warehouseWaypoint string, playerID int) error {
	if shipSymbol == "" || warehouseWaypoint == "" {
		return fmt.Errorf("depot warehouse launch requires a ship symbol and warehouse waypoint")
	}
	if !s.depotElementHullViable(ctx, shipSymbol, warehouseWaypoint, playerID) {
		s.evictStrandedDepotElement(ctx, shipSymbol, warehouseWaypoint, depot.RoleWarehouse, playerID)
		return nil // stale binding corrected; the scaler ramp re-grows the role on a home-viable hull next tick
	}
	ship, crewed, err := s.positionDepotElementHull(ctx, depotElementPlacement{
		shipSymbol:     shipSymbol,
		targetWaypoint: warehouseWaypoint,
		fleetTag:       operationWarehouse,
	}, playerID)
	if err != nil {
		return err
	}
	if !crewed {
		return nil // never-poach (sp-udgc): the hull is dedicated to a foreign fleet (e.g. "trade") — element left uncrewed, no coordinator launched
	}
	if ship == nil {
		return fmt.Errorf("depot warehouse hull %s not found", shipSymbol)
	}
	// PIN the fixed far-source whitelist + flat caps — never the demand miner (sp-9le3x / st-wisp-2h6r5).
	supportedGoods := contractscaler.FarSourceGoods
	targetUnits := contractscaler.DepotTargetUnits()
	if !ship.IsIdle() {
		// Already flying its coordinator (recovered on reload): re-apply the fixed whitelist to the
		// running persisted row (the stocker re-reads supported_goods each pass) — no double-launch.
		return s.refreshRunningDepotWarehouseCaps(ctx, shipSymbol, warehouseWaypoint, supportedGoods, playerID)
	}
	_, err = s.persistAndRunWarehouse(ctx, shipSymbol, warehouseWaypoint, supportedGoods, targetUnits, playerID)
	return err
}

// refreshRunningDepotWarehouseCaps re-applies a freshly-recomputed receipt whitelist to an
// ALREADY-RUNNING depot warehouse WITHOUT launching a second coordinator. On boot,
// container recovery re-adopts the warehouse hull (now non-idle) and RESUMES its persisted
// storage_operations row with whatever whitelist it last carried; launchDepotWarehouse's idle
// gate then skips the (re)launch. But a redeployed cap selector must still reach the running
// buffer — and the stocker re-reads each warehouse's supported_goods from the store every pass
// (warehousesAt -> FindRunning), so persisting the fresh whitelist onto that row makes the
// redeploy live on the stocker's next tick, no container restart needed. It matches the running
// warehouse operation by waypoint + crewing hull (the container id carries a random UUID and is
// not reconstructible) and updates ONLY the supported_goods column, so the live status / ship
// registration are untouched. A hull with no running warehouse row yet (recovery still in
// flight) is a benign no-op — the next reload catches it. Fail-open on a nil DB (degraded/test):
// the idle skip simply stands.
func (s *DaemonServer) refreshRunningDepotWarehouseCaps(ctx context.Context, shipSymbol, warehouseWaypoint string, supportedGoods []string, playerID int) error {
	if s.db == nil {
		return nil
	}
	repo := persistence.NewStorageOperationRepository(s.db, s.clock)
	ops, err := repo.FindAllRunningByWaypoint(ctx, playerID, warehouseWaypoint)
	if err != nil {
		return fmt.Errorf("depot warehouse %s at %s: failed to load running warehouse for cap refresh: %w", shipSymbol, warehouseWaypoint, err)
	}
	for _, op := range ops {
		if op.OperationType() != storage.OperationTypeWarehouse {
			continue
		}
		if !hullCrewsOperation(op.StorageShips(), shipSymbol) {
			continue
		}
		if err := repo.UpdateSupportedGoods(ctx, op.ID(), supportedGoods); err != nil {
			return fmt.Errorf("depot warehouse %s at %s: failed to persist recomputed caps: %w", shipSymbol, warehouseWaypoint, err)
		}
		return nil
	}
	return nil // no running warehouse row for this hull yet — recovery in flight; the next reload catches it
}

// depotElementPlacement names one depot element's hull, where it belongs, and the fleet tag
// that claims it. navigateOnAssign is false for warehouse/stocker roles, whose own coordinator
// parks the hull.
type depotElementPlacement struct {
	shipSymbol       string
	targetWaypoint   string
	fleetTag         string
	navigateOnAssign bool
}

// positionDepotElementHull makes a depot element's hull assignment ATOMIC and ROLE-AGNOSTIC —
// the shared spine every role's launch routes through, so a warehouse / stocker /
// source-hub hull is freed + excluded + positioned by the SAME machinery that shipped for the
// delivery hull, instead of being persisted-but-left-docked. Parameterized by the role's
// DedicatedFleet tag (fleetTag) and whether THIS call parks the hull itself (navigateOnAssign). It
// performs, in order:
//
//  1. CLAIM-RELEASE + RE-DEDICATE (free from prior fleet): re-dedicate the hull to fleetTag and
//     sever any prior fleet's LIVE work-claim, reusing the SAME machinery `fleet unassign`
//     uses (AssignFleet + ReleaseContainerClaim). Re-dedicate FIRST so the instant the claim breaks
//     the tag already prevents the old coordinator from re-grabbing it; then break the claim so a
//     hull that was MID-TASK at assign time becomes free. It fires only when the hull is not ALREADY
//     the role's own (see depotHullNeedsFreeing) — so a hull mid-role is never yanked on a reload.
//  2. EXCLUDE from the contract coordinator's grab: emergent from the fleetTag written in step 1
//     (FindIdleLightHaulers excludes any DedicatedFleet != ""; the coordinator's own
//     FindIdleShipsByFleet("contract") returns only "contract"-tagged hulls) — no separate write.
//     A delivery hull uses the DISTINCT depot.DeliveryHullFleet (dispatched only via
//     routeContractViaDepot under that identity); a warehouse/stocker re-dedicates to its OWN
//     coordinator's tag ("warehouse"/"stocker") so the SAME tag both excludes it from the grab AND
//     lets its coordinator's operation-checked ClaimShip take it (never fighting its dedication).
//  3. (RE)NAVIGATE to the waypoint — only when navigateOnAssign is set, for a role with NO standing
//     coordinator to park its hull (delivery hull + source hub). warehouse + stocker pass false:
//     their OWN coordinator parks the hull (run_warehouse navigates to the waypoint; the stocker
//     shuttles), so navigating here would only fight the coordinator's idle-gate and defer its start.
//
// IDEMPOTENT + fail-open, preserving the shipped delivery behavior: a hull already the role's own
// skips the claim-release (never yanked mid-role); a hull still flying is a benign skip; a hull
// already at its waypoint is a no-op. Returns the reloaded ship plus crewed=true so a caller
// (warehouse/stocker launch) can gate its coordinator start on the post-release state.
//
// NEVER-POACH (RULINGS #7 generalized to depot-launch): if the hull is already
// dedicated to a DIFFERENT non-empty fleet than this depot role, it is NOT poached — the element goes
// UNCREWED (crewed=false) and the caller launches no coordinator. An operator's explicit dedication
// (e.g. the Admiral moved a former depot-crew light to "trade") wins over the depot topology's naming,
// so a daemon restart never overrides an existing assignment (the Admiral's invariant: a restart must
// not change ship assignments). An already-dedicated hull is left alone, and
// only an UNDEDICATED hull (the cold-start bootstrap/reconciler provisioning norm) is crewed. A hull
// already on THIS role (DedicatedFleet == fleetTag) is not foreign and crews idempotently.
func (s *DaemonServer) positionDepotElementHull(
	ctx context.Context, el depotElementPlacement, playerID int,
) (ship *navigation.Ship, crewed bool, err error) {
	shipSymbol, targetWaypoint, fleetTag := el.shipSymbol, el.targetWaypoint, el.fleetTag
	pid := shared.MustNewPlayerID(playerID)
	ship, err = s.shipRepo.FindBySymbol(ctx, shipSymbol, pid)
	if err != nil {
		return nil, false, fmt.Errorf("failed to load depot %s hull %s: %w", fleetTag, shipSymbol, err)
	}
	if ship == nil {
		return nil, false, fmt.Errorf("depot %s hull %s not found", fleetTag, shipSymbol)
	}

	// Never-poach: a hull dedicated to a FOREIGN fleet (non-empty and != this role) is left alone —
	// the element goes uncrewed rather than overriding the operator's/existing dedication on restart.
	if fleet := ship.DedicatedFleet(); fleet != "" && fleet != fleetTag {
		fmt.Printf("depot %s element %s left dedicated to %q, not poached for depot (sp-udgc never-poach)\n",
			fleetTag, shipSymbol, fleet)
		return ship, false, nil
	}

	if depotHullNeedsFreeing(ship, fleetTag) {
		if err = s.shipRepo.AssignFleet(ctx, shipSymbol, fleetTag, pid); err != nil {
			return nil, false, fmt.Errorf("failed to re-dedicate depot hull %s to %q: %w", shipSymbol, fleetTag, err)
		}
		brokenFrom, rerr := s.shipRepo.ReleaseContainerClaim(ctx, shipSymbol, pid,
			fmt.Sprintf("re-dedicated as depot %s hull for %s (sp-3l64)", fleetTag, targetWaypoint))
		if rerr != nil {
			return nil, false, fmt.Errorf("failed to release prior work-claim on depot hull %s: %w", shipSymbol, rerr)
		}
		// Reap the container that just lost the hull. Freeing a MID-TASK hull's
		// claim leaves its container running the hull it no longer owns, and a phantom
		// RUNNING row that only a restart's recovery sweep clears.
		s.ReapOrphanedContainer(ctx, brokenFrom, pid,
			fmt.Sprintf("re-dedicated as depot %s hull for %s", fleetTag, targetWaypoint))
		// Reload so the idle / location gates below observe the post-release state.
		ship, err = s.shipRepo.FindBySymbol(ctx, shipSymbol, pid)
		if err != nil {
			return nil, false, fmt.Errorf("failed to reload depot hull %s after re-dedication: %w", shipSymbol, err)
		}
		if ship == nil {
			return nil, false, fmt.Errorf("depot hull %s not found after re-dedication", shipSymbol)
		}
	}

	if !el.navigateOnAssign {
		return ship, true, nil // warehouse/stocker: their own coordinator parks the hull, not this call
	}
	if !ship.IsIdle() {
		return ship, true, nil // still flying (dispatched, or mid-reposition) — benign skip, never yanked
	}
	if loc := ship.CurrentLocation(); loc != nil && loc.Symbol == targetWaypoint {
		return ship, true, nil // already parked at its waypoint — nothing to reposition
	}
	navigate := s.NavigateShip
	if s.depotNavigateOverride != nil {
		navigate = s.depotNavigateOverride
	}
	if _, err = navigate(ctx, shipSymbol, targetWaypoint, playerID); err != nil {
		return ship, true, err
	}
	return ship, true, nil
}

// depotHullNeedsFreeing reports whether a depot element's hull must be claim-released + re-dedicated
// to fleetTag. It fires when the hull is not already the role's own (DedicatedFleet !=
// fleetTag) AND it is safe to break its current occupancy.
//
// The never-poach guard in positionDepotElementHull SHORT-CIRCUITS any hull dedicated to
// a FOREIGN fleet BEFORE this is reached, so by the time this runs the hull is either UNDEDICATED
// (DedicatedFleet == "") or already this role's own. The `|| DedicatedFleet() != ""` clause is thus a
// defensive no-op for the reachable inputs (kept so the predicate stays self-contained). The live
// decision is therefore: re-dedicate an UNDEDICATED IDLE hull (fresh crewing); leave an undedicated
// NON-idle hull alone (the warehouse/stocker RESUMING on reload — a recovered buffer hull that
// StartWarehouse/StartStocker never tagged — which must not be yanked mid-run); and skip a hull
// already on this role (idempotent reload).
func depotHullNeedsFreeing(ship *navigation.Ship, fleetTag string) bool {
	if ship.DedicatedFleet() == fleetTag {
		return false // already the role's own — idempotent skip (never yank a hull mid-role)
	}
	return ship.IsIdle() || ship.DedicatedFleet() != ""
}

// launchDepotDelivery (depotCoordinatorSink) makes depot delivery-hull assignment ATOMIC
// (bead sp-3l64, extending sp-9j9c) so a multi-hub delivery fleet is PRESENT at its hubs for the
// nearest-selection router (SelectDeliveryHull) to route each cluster's contract to its LOCAL hull —
// and STAYS there. It is the free+exclude+park path through the shared positionDepotElementHull:
// re-dedicated to the DISTINCT depot.DeliveryHullFleet (invisible to both pools the contract
// coordinator draws from — dispatched only via routeContractViaDepot), and — having no standing
// coordinator of its own — (re)navigated to its hub on assign and reload (navigateOnAssign=true).
func (s *DaemonServer) launchDepotDelivery(ctx context.Context, shipSymbol, hubWaypoint string, playerID int) error {
	if shipSymbol == "" || hubWaypoint == "" {
		return fmt.Errorf("depot delivery hull launch requires a ship symbol and hub waypoint")
	}
	// crewed is ignored: a poach-refused hull (crewed=false) simply isn't repositioned — it stays on
	// its foreign fleet, which is the never-poach outcome for a hub role too (sp-udgc).
	_, _, err := s.positionDepotElementHull(ctx, depotElementPlacement{
		shipSymbol:       shipSymbol,
		targetWaypoint:   hubWaypoint,
		fleetTag:         depot.DeliveryHullFleet,
		navigateOnAssign: true,
	}, playerID)
	return err
}

// launchDepotSourceHub (depotCoordinatorSink) makes depot source-hub assignment ATOMIC and
// role-agnostic (sp-3l64): like the delivery hull it has no standing coordinator, so its crewing
// hull is freed from any prior fleet, excluded from the contract grab via the DISTINCT
// depotSourceHubFleet tag, and (re)navigated to its market waypoint on assign and reload — instead
// of being persisted-but-left-docked. It holds the buy anchor for the depot's stockers; it is not
// dispatched.
func (s *DaemonServer) launchDepotSourceHub(ctx context.Context, shipSymbol, hubWaypoint string, playerID int) error {
	if shipSymbol == "" || hubWaypoint == "" {
		return fmt.Errorf("depot source-hub launch requires a ship symbol and waypoint")
	}
	// crewed ignored (see launchDepotDelivery): a poach-refused source-hub hull stays on its foreign fleet.
	_, _, err := s.positionDepotElementHull(ctx, depotElementPlacement{
		shipSymbol:       shipSymbol,
		targetWaypoint:   hubWaypoint,
		fleetTag:         depotSourceHubFleet,
		navigateOnAssign: true,
	}, playerID)
	return err
}

// depotSink resolves the depotCoordinatorSink the element-add / reload positioning dispatches each
// launch through: the injected spy in tests (depotSinkOverride), else *DaemonServer itself (the
// real StartWarehouse / StartStocker / navigate path). Mirrors the storageRecovery override seam.
func (s *DaemonServer) depotSink() depotCoordinatorSink {
	if s.depotSinkOverride != nil {
		return s.depotSinkOverride
	}
	return s
}

// depotHomeRouter is the narrow cross-system reachability port the depot stocker hull viability
// precondition consults (sp-fihvy) — the SAME notion of gate-graph routability
// foreignMarketReachable uses (run_stocker_coordinator.go: gateGraphResolver().Routable), never a
// second reachability mechanism invented here. Satisfied by *gategraph.Service, injected
// post-construction via SetGateGraph (main.go builds it after NewDaemonServer runs).
type depotHomeRouter interface {
	Routable(ctx context.Context, fromSystem, toSystem string, playerID int) (bool, error)
}

// launchDepotStocker (depotCoordinatorSink) starts a STANDING, continuous stocker on
// shipSymbol that fills the depot's destination warehouse (warehouseWaypoint) and re-stages
// the moment contracts drain the buffer, surviving restart. It leaves every money/freshness
// knob at the coordinator's own default (targetPerGood 0 → the warehouse's receipt caps drive
// the fill). A hull that is not idle is already flying its coordinator — a benign
// already-launched skip (nil), never an error. It reuses StartStocker (no parallel channel).
//
// RULINGS #14 HARD PRECONDITION, applied to every depot role: the stocker is an
// INTRA-SYSTEM role — it sources the FIXED far-source whitelist from the warehouse's
// HOME system ONLY (homeSystemOnly below), so its hull must be IN, or gate-reachable to, that
// system BEFORE it is ever (re)claimed. This is validated FIRST, unconditionally — before
// positionDepotElementHull's claim and before the idle check — because a stranded hull's
// coordinator (e.g. TORWIND-19, parked unreachable forever) is typically ALREADY recovered/non-idle
// by the time boot replays the depot registry, so a check gated on IsIdle() would never see it. A
// non-viable hull is evicted (stale binding removed, un-dedicated, claim released) instead of
// (re)launched; this is the SAME choke point GrowStocker's fresh grow and the boot/reload replay
// both funnel through, so one guard covers selection, recovery, and positioning at once. A viable
// hull (the overwhelming common case) falls straight through to the unchanged positioning below.
// depotElementHullViable/evictStrandedDepotElement are role-neutral — the warehouse
// launch above applies the identical guard.
//
// ROLE-AGNOSTIC: FIRST free+re-dedicate the hull to its OWN "stocker" fleet via the
// shared positionDepotElementHull (navigateOnAssign=false — the stocker COORDINATOR moves the hull:
// it shuttles buy→home→deposit, so there is no park leg to fire here). Same unblock as the
// warehouse: a hull added from a foreign fleet (or busy) is severed + re-dedicated to "stocker" so
// the coordinator's operation-checked claim can take it, instead of being persisted-but-left-docked.
func (s *DaemonServer) launchDepotStocker(ctx context.Context, shipSymbol, warehouseWaypoint string, playerID int) error {
	if shipSymbol == "" || warehouseWaypoint == "" {
		return fmt.Errorf("depot stocker launch requires a ship symbol and warehouse waypoint")
	}
	if !s.depotElementHullViable(ctx, shipSymbol, warehouseWaypoint, playerID) {
		s.evictStrandedDepotElement(ctx, shipSymbol, warehouseWaypoint, depot.RoleStocker, playerID)
		return nil // stale binding corrected; the scaler ramp re-grows the role on a home-viable hull next tick
	}
	ship, crewed, err := s.positionDepotElementHull(ctx, depotElementPlacement{
		shipSymbol:     shipSymbol,
		targetWaypoint: warehouseWaypoint,
		fleetTag:       operationStocker,
	}, playerID)
	if err != nil {
		return err
	}
	if !crewed {
		return nil // never-poach (sp-udgc): the hull is dedicated to a foreign fleet (e.g. "trade") — element left uncrewed, no coordinator launched
	}
	if ship == nil {
		return fmt.Errorf("depot stocker hull %s not found", shipSymbol)
	}
	if !ship.IsIdle() {
		return nil // already flying its coordinator — benign already-launched skip
	}

	_, err = s.StartStocker(
		ctx, shipSymbol, warehouseWaypoint,
		0,    // budgetPerLeg → coordinator default (capital ceiling + reserve still bind)
		0,    // workingCapitalReserve → 50k default
		-1,   // iterations: CONTINUOUS
		0,    // maxMarketAgeMinutes → 75 default
		0,    // targetPerGood → the warehouse's receipt caps drive the fill
		true, // standing: re-stage on drain, survive restart
		0,    // tickSeconds → 30s default
		0,    // refillHysteresis → default
		true, // homeSystemOnly: the contract depot sources INTRA-system only (RULINGS #14) — buy the fixed far-source goods from the home system's own export waypoints, never cross-gate
		"",   // agentSymbol resolved by the coordinator
		playerID,
	)
	return err
}

// depotElementHullViable reports whether shipSymbol is IN, or gate-reachable to, waypoint's system
// (sp-fihvy; generalized to every depot element role — warehouse, stocker, or any future role — by
// sp-fis8y) — the hard precondition a depot element hull must satisfy, using the exact same
// routability notion foreignMarketReachable consults (gategraph.Service.Routable), never a second
// reachability mechanism.
//
// It fails OPEN — reports viable — whenever the signal itself is unreadable (unresolvable home
// system, unreadable ship/location, no gate graph wired, or a Routable read error): an eviction is a
// consequential, hard-to-reverse action (un-dedicate + claim release + depot-store removal), so an
// unverifiable signal must never trigger one (RULINGS #2 — a transient read hiccup is not grounds for
// churn). This deliberately diverges from foreignMarketReachable's fail-CLOSED-on-error polarity:
// that check only skips ONE candidate market for the current pass (cheap, instantly retried next
// pass); this one gates a destructive side effect, so the safer default is the opposite direction.
// The reachability VERDICT itself (same-system trivially true, else Routable's own answer) is
// identical to foreignMarketReachable's — only the unreadable-signal fallback differs, and only
// because the action it gates differs.
func (s *DaemonServer) depotElementHullViable(ctx context.Context, shipSymbol, waypoint string, playerID int) bool {
	homeSystem := shared.ExtractSystemSymbol(waypoint)
	if homeSystem == "" {
		return true // no resolvable home → nothing to validate against, fail open
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return true
	}
	ship, err := s.shipRepo.FindBySymbol(ctx, shipSymbol, pid)
	if err != nil || ship == nil {
		return true // unreadable hull → fail open (never evict on a read hiccup)
	}
	// RULINGS #7: the command frigate is NEVER a depot hull — non-viable even when home-
	// reachable, so a frigate seated as a depot element is evicted (below) rather than re-claimed on
	// every restart recovery. Fails CLOSED here (unlike the reachability signal) because IsCommandHull
	// is definite + stable (role/symbol never change): the eviction fires once and isReclaimable's
	// matching exclusion keeps the freed frigate from being re-seated, so there is no thrash (RULINGS #2).
	if domainContract.IsCommandHull(ship) {
		return false
	}
	loc := ship.CurrentLocation()
	if loc == nil {
		return true // unreadable location → fail open
	}
	currentSystem := shared.ExtractSystemSymbol(loc.Symbol)
	if currentSystem == "" || currentSystem == homeSystem {
		return true // already home (or unresolvable) — trivially viable
	}
	if s.gateGraph == nil {
		return true // reachability signal unwired → fail open, byte-identical to pre-sp-fihvy
	}
	routable, err := s.gateGraph.Routable(ctx, currentSystem, homeSystem, playerID)
	if err != nil {
		return true // unverifiable route → fail open (never evict on an unreadable graph)
	}
	return routable
}

// evictStrandedDepotElement corrects a stale depot-element binding that failed
// depotElementHullViable (sp-fihvy stocker; generalized to every depot role — warehouse, stocker,
// or any future role — by sp-fis8y; and to the command frigate, which is never a depot hull, by
// sp-gvvph): it removes the depot-store element recording shipSymbol as
// this depot's role member, un-dedicates the hull from its role fleet (releaseDepotHull — the SAME
// single AssignFleet dedication write positionDepotElementHull's re-dedicate uses, run in reverse),
// and releases its work-claim so the hull becomes plain undedicated-idle again — reclaimable by the
// now-hardened picker on ANY future grow, depot or otherwise. It does NOT synchronously re-grow the
// role: the standing scaler coordinator's next tick sees the depot registry's now-lower element
// count and re-fills it through the home-scoped reclaim/buy tiers (contract_scaler_ports.go /
// run_contract_scaler.go), so "re-grow on a home-viable hull" happens one tick later, never here.
//
// Best-effort and fail-open throughout: each step that errors is logged and eviction continues
// rather than aborting — a stranded hull that survives one more restart is no worse than today, but
// blocking boot on the cleanup would be strictly worse.
func (s *DaemonServer) evictStrandedDepotElement(ctx context.Context, shipSymbol, waypoint string, role depot.Role, playerID int) {
	// Honest reason (sp-gvvph): a command-frigate eviction is RULINGS #7 (the flagship is never a depot
	// hull), a DIFFERENT cause than the sp-fihvy/sp-fis8y home-reachability eviction — so name it as such
	// in both the human summary and the persisted claim-release reason. Load the hull to branch; fail-open
	// to the reachability wording if it is momentarily unreadable (the eviction still proceeds — this only
	// names the why, never gates the correction).
	cause := fmt.Sprintf("not home-reachable to %s (sp-fihvy/sp-fis8y home-reachability precondition)", waypoint)
	claimReason := fmt.Sprintf("evicted as an unreachable depot %s hull for %s (sp-fihvy/sp-fis8y home-reachability precondition)", role, waypoint)
	if ship, err := s.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID)); err == nil && ship != nil && domainContract.IsCommandHull(ship) {
		cause = "the command frigate is never a depot hull (sp-gvvph, RULINGS #7)"
		claimReason = fmt.Sprintf("evicted as a depot %s hull: %s", role, cause)
	}
	if depotID, ok := s.depotIDForElement(ctx, shipSymbol, role, playerID); ok {
		if err := s.depotStore(playerID).RemoveElement(ctx, depotID, role, shipSymbol); err != nil {
			fmt.Printf("depot %s eviction: failed to remove stale %s %s binding for %s: %v\n", role, depotID, role, shipSymbol, err)
		}
	} else {
		fmt.Printf("depot %s eviction: no depot found owning %s hull %s — skipping element removal\n", role, role, shipSymbol)
	}
	if err := s.releaseDepotHull(ctx, shipSymbol, playerID); err != nil {
		fmt.Printf("depot %s eviction: failed to un-dedicate stranded hull %s: %v\n", role, shipSymbol, err)
	}
	if brokenFrom, err := s.shipRepo.ReleaseContainerClaim(ctx, shipSymbol, shared.MustNewPlayerID(playerID), claimReason); err != nil {
		fmt.Printf("depot %s eviction: failed to release work-claim on stranded hull %s: %v\n", role, shipSymbol, err)
	} else {
		// Reap the container the eviction orphaned — otherwise it keeps running
		// the evicted hull, and its RUNNING row survives until a restart sweep fails it.
		s.ReapOrphanedContainer(ctx, brokenFrom, shared.MustNewPlayerID(playerID),
			fmt.Sprintf("evicted as an unreachable depot %s hull", role))
	}
	fmt.Printf("depot %s eviction: evicted stranded %s hull %s (%s) — the scaler ramp re-grows on a home-viable hull next tick\n", role, role, shipSymbol, cause)
}

// depotIDForElement finds the id of the depot whose ROLE elements include shipSymbol, by loading
// the player's depot registry and scanning the role-appropriate slice — the same registry the boot
// reload + contract routing consult (no new lookup mechanism). ok=false when no depot claims this
// hull under that role (a defensive case: element removal is then skipped rather than guessed at)
// or the registry fails to load.
func (s *DaemonServer) depotIDForElement(ctx context.Context, shipSymbol string, role depot.Role, playerID int) (string, bool) {
	reg, err := s.depotStore(playerID).LoadRegistry(ctx)
	if err != nil {
		return "", false
	}
	for _, d := range reg.Depots() {
		var elements []depot.Element
		switch role {
		case depot.RoleWarehouse:
			elements = d.Warehouses()
		case depot.RoleStocker:
			elements = d.Stockers()
		case depot.RoleDeliveryHull:
			elements = d.DeliveryHulls()
		case depot.RoleSourceHub:
			elements = d.SourceHubs()
		}
		for _, e := range elements {
			if e.ShipSymbol == shipSymbol {
				return d.ID(), true
			}
		}
	}
	return "", false
}
