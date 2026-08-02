package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// noteIdleHullsCoverShortfall is a DISPATCH signal, never a WAIT one: the wait
// gate stays keyed on worker-held cargo. Observation only; a count failure is ignored.
func (h *RunFleetCoordinatorHandler) noteIdleHullsCoverShortfall(ctx context.Context, contractID, requiredCargo string, unitsNeeded, inFlightCargo, playerID int) {
	logger := common.LoggerFromContext(ctx)
	idleReclaimedCargo, err := h.idleReclaimedContractCargoHeld(ctx, requiredCargo, playerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to count idle reclaimed contract cargo: %v", err), nil)
		return
	}
	shortfall := unitsNeeded - inFlightCargo
	if shortfall <= 0 || idleReclaimedCargo < shortfall {
		return
	}
	logger.Log("INFO", fmt.Sprintf(
		"%d units of %s already aboard idle hull(s) cover the %d-unit shortfall - the cargo-priority selection below completes that load with its holder before sourcing a duplicate onto a second hull (sp-1pf0r double-load defense)",
		idleReclaimedCargo, requiredCargo, shortfall), map[string]interface{}{
		"action":          "idle_cargo_dispatch",
		"contract_id":     contractID,
		"trade_symbol":    requiredCargo,
		"idle_reclaimed":  idleReclaimedCargo,
		"units_shortfall": shortfall,
	})
}

func logHolderSelection(logger common.ContainerLogger, contractID, holder, requiredCargo string, deliverHeldOnly bool) {
	if deliverHeldOnly {
		logger.Log("INFO", fmt.Sprintf(
			"Idle hull %s already holds %s for contract %s - dispatching it to DELIVER what it holds where it stands, then sourcing the remainder with the hull nearest the source (sp-zve2q single-hull, weighed by sp-5jce2)",
			holder, requiredCargo, contractID), map[string]interface{}{
			"action":            "select_cargo_holder",
			"contract_id":       contractID,
			"ship_symbol":       holder,
			"trade_symbol":      requiredCargo,
			"deliver_held_only": true,
		})
		return
	}
	logger.Log("INFO", fmt.Sprintf(
		"Idle hull %s already holds %s for contract %s - selecting it to complete its load instead of sourcing a duplicate onto the closest empty hull (sp-zve2q deterministic single-hull)",
		holder, requiredCargo, contractID), map[string]interface{}{
		"action":       "select_cargo_holder",
		"contract_id":  contractID,
		"ship_symbol":  holder,
		"trade_symbol": requiredCargo,
	})
}

// outstandingDelivery returns the contract's first delivery still short of its
// required units; stillOwed is false once every delivery is fulfilled.
func outstandingDelivery(contract *domainContract.Contract) (delivery domainContract.Delivery, stillOwed bool) {
	for _, d := range contract.Terms().Deliveries {
		if d.UnitsRequired > d.UnitsFulfilled {
			return d, true
		}
	}
	return delivery, false
}

// calculateInFlightCargo calculates the total cargo of a specific trade symbol
// that is currently held by ships working on active contract workflows, plus
// cargo still aboard ships whose contract worker was interrupted (marked
// FAILED) but hasn't been reclaimed to idle yet. Without the second source, a
// partially-laden hull orphaned by a dead worker read as 0 in-flight the
// moment its worker died, letting the coordinator purchase units redundant
// with what that hull is still physically holding.
//
// Ordering rules out double-counting: readoptInterruptedDeliveries runs once,
// before the main loop starts, and moves any successfully
// re-adopted ship onto a fresh RUNNING container before this function is ever
// called from inside the loop. That ship is therefore picked up exactly once,
// by the RUNNING-workers pass below, and no longer matches
// FindInterruptedWorkerShipsWithCargo's query (it queries by container ID,
// and the ship has moved off the dead one — the dead container's own row can
// still be sitting in the FAILED list, but nothing on it matches anymore). A
// ship that is NOT re-adopted (readoption only re-adopts one hull per
// startup) stays attached to its FAILED container - a transient state the
// loop's unconditional ReclaimShipsFromInterruptedWorkers pass forces closed
// on its very next iteration - so counting it here can delay, but never
// permanently stall, the coordinator, unlike counting arbitrary idle-ship
// cargo would.
//
// This is used during restart recovery to prevent duplicate cargo purchases.
func (h *RunFleetCoordinatorHandler) calculateInFlightCargo(
	ctx context.Context,
	tradeSymbol string,
	playerID int,
) (int, error) {
	logger := common.LoggerFromContext(ctx)

	// Find all active CONTRACT_WORKFLOW containers
	activeWorkers, err := h.workerLifecycleManager.FindExistingWorkers(ctx, playerID)
	if err != nil {
		return 0, fmt.Errorf("failed to find existing workers: %w", err)
	}

	totalInFlight := 0

	// For each active worker, find its assigned ships and check their cargo
	for _, worker := range activeWorkers {
		ships, err := h.shipRepo.FindByContainer(ctx, worker.ID, shared.MustNewPlayerID(playerID))
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to get ships for container %s: %v", worker.ID, err), nil)
			continue
		}

		for _, ship := range ships {
			// Count cargo of the required trade symbol
			for _, item := range ship.Cargo().Inventory {
				if item.Symbol == tradeSymbol {
					totalInFlight += item.Units
					logger.Log("INFO", fmt.Sprintf("Found %d units of %s in ship %s cargo (worker %s)",
						item.Units, tradeSymbol, ship.ShipSymbol(), worker.ID), nil)
				}
			}
		}
	}

	// Also count cargo still aboard ships whose contract worker was
	// interrupted (marked FAILED) but hasn't been reclaimed to idle yet.
	// Reuses FindInterruptedWorkerShipsWithCargo rather than a new query, so
	// this always agrees with readoptInterruptedDeliveries
	// about which ships are "interrupted with cargo to salvage." A failure
	// here is logged and swallowed, matching this function's existing
	// "better to risk duplication than block indefinitely" contract with its
	// caller — the RUNNING-workers total above is still valid on its own.
	interruptedShips, err := h.workerLifecycleManager.FindInterruptedWorkerShipsWithCargo(ctx, playerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to find interrupted-worker ships for in-flight cargo count: %v", err), nil)
	} else {
		for _, ship := range interruptedShips {
			for _, item := range ship.Cargo().Inventory {
				if item.Symbol == tradeSymbol {
					totalInFlight += item.Units
					logger.Log("INFO", fmt.Sprintf("Found %d units of %s in interrupted ship %s cargo (worker dead, not yet reclaimed)",
						item.Units, tradeSymbol, ship.ShipSymbol()), nil)
				}
			}
		}
	}

	if totalInFlight > 0 {
		logger.Log("INFO", fmt.Sprintf("Total in-flight cargo: %d units of %s", totalInFlight, tradeSymbol), nil)
	}

	return totalInFlight, nil
}

// idleReclaimedContractCargoHeld sums the contract good already aboard IDLE
// (unassigned) hulls — most importantly one just reclaimed from a crashed
// contract worker that still physically holds its contract load.
//
// calculateInFlightCargo deliberately counts only RUNNING/interrupted-worker
// cargo for its WAIT gate: counting idle cargo there would STALL the coordinator,
// because an idle hull's load is dispatchable NOW (the wait gate short-circuits
// before selection, so a counted-but-not-dispatched idle load would loop
// forever). This companion instead surfaces that idle load as a DISPATCH signal —
// the coordinator completes it with the holding hull (cargo-priority selection
// picks the holder) rather than sourcing a duplicate onto a second hull, which is
// the sp-1pf0r double-load defense. Read-only; a load-failure is returned so the
// caller can log and proceed (better to risk a duplicate than to block).
func (h *RunFleetCoordinatorHandler) idleReclaimedContractCargoHeld(ctx context.Context, tradeSymbol string, playerID int) (int, error) {
	ships, err := h.shipRepo.FindAllByPlayer(ctx, shared.MustNewPlayerID(playerID))
	if err != nil {
		return 0, fmt.Errorf("failed to load ships for idle-cargo count: %w", err)
	}
	total := 0
	for _, ship := range ships {
		if ship.IsAssigned() {
			continue // on a worker/other container — waited on / counted elsewhere
		}
		total += ship.Cargo().GetItemUnits(tradeSymbol)
	}
	return total, nil
}

// idleContractCargoHolder names the IDLE (unassigned) hull best positioned to
// COMPLETE the active contract's delivery from cargo it ALREADY holds: among idle
// hulls carrying ONLY the contract good (a PURE holder — safe to claim without the
// NO-CARGO-DUMP guard tripping), the one holding the MOST units, ties broken by the
// lexicographically-smallest symbol so a restart deterministically re-picks the
// SAME hull, never a second cargo-laden one. Returns "" when no pure holder exists.
//
// This is the SELECTION companion to idleReclaimedContractCargoHeld's dispatch-
// signal COUNT: that surfaces the intent to complete the load with its holder; this
// NAMES the holder so the coordinator actually SELECTS it, short-circuiting the
// distance-based pick. It is needed because the candidate-discovery filters
// (FindIdleLightHaulers / FindIdleShipsByFleet) drop an idle holder that is IN
// TRANSIT, or UNDEDICATED while a dedicated contract fleet is active (EXCLUSIVE
// MODE) — so such a holder is DETECTED yet never reaches SelectClosestShip's own
// cargo-priority, and the closest empty hull sources a duplicate (the observed
// TORWIND-15-holds-43 / TORWIND-8-double-sources gap). This scans the full fleet,
// like its count sibling, so it sees the holder regardless of those filters.
// Read-only; a load failure is returned so the caller logs and falls back to
// distance selection (better a possible duplicate than a blocked contract).
func (h *RunFleetCoordinatorHandler) idleContractCargoHolder(ctx context.Context, requiredCargo string, playerID int) (string, error) {
	ships, err := h.shipRepo.FindAllByPlayer(ctx, shared.MustNewPlayerID(playerID))
	if err != nil {
		return "", fmt.Errorf("failed to load ships for idle-holder selection: %w", err)
	}
	bestSymbol := ""
	bestUnits := 0
	for _, ship := range ships {
		if ship.IsAssigned() {
			continue // on a worker/other container — completing on its own worker, not idle-dispatchable here
		}
		units := ship.Cargo().GetItemUnits(requiredCargo)
		if units <= 0 {
			continue // holds none of the contract good
		}
		if ship.Cargo().HasItemsOtherThan(requiredCargo) {
			continue // impure holder — leave to the NO-CARGO-DUMP park/liquidate path, never force-claim
		}
		symbol := ship.ShipSymbol()
		if units > bestUnits || (units == bestUnits && symbol < bestSymbol) {
			bestSymbol = symbol
			bestUnits = units
		}
	}
	return bestSymbol, nil
}

// holderRun is the (holder, claimable pool, geometry, good) tuple the placement
// measurement and the deliver-held decision both read.
type holderRun struct {
	Holder         string
	Candidates     []string
	SourceWaypoint string
	Destination    string
	Good           string
	UnitsNeeded    int
	PlayerID       int
}

// weighHolderPlacement measures the ONE comparison the holder-vs-source decision
// needs: how far the idle holder sits from the SOURCE market versus the nearest
// spawnable candidate, plus whether the holder is standing on the delivery (so
// its held units can be registered without travel). dist(source,destination) is
// identical for every candidate and cancels out, so this is a scalar sweep of
// already-loaded ship positions — never a routing solve.
//
// Only hulls in the SOURCE's system are compared: Waypoint.DistanceTo is a plain
// Euclidean coordinate distance and is meaningless across systems, and a hull
// outside the contract's home system could reach neither the source nor the
// delivery anyway (RULINGS #14). Candidates are taken from the pass's already
// dedication-filtered, cargo-filtered, governor-filtered spawnable pool, so this
// never reaches into another fleet's hulls. Errors (and a nil graph provider)
// return a placement with no alternative named, which the decision reads as
// "keep the holder" — fail-closed onto sp-zve2q's behaviour.
func (h *RunFleetCoordinatorHandler) weighHolderPlacement(ctx context.Context, run holderRun) (domainContract.HolderPlacement, error) {
	placement := domainContract.HolderPlacement{Holder: run.Holder, UnitsNeeded: run.UnitsNeeded}
	if run.Holder == "" || run.SourceWaypoint == "" || h.graphProvider == nil {
		return placement, nil
	}

	ships, err := h.shipRepo.FindAllByPlayer(ctx, shared.MustNewPlayerID(run.PlayerID))
	if err != nil {
		return placement, fmt.Errorf("failed to load ships for holder placement: %w", err)
	}

	sourceSystem := shared.ExtractSystemSymbol(run.SourceWaypoint)
	graphResult, err := h.graphProvider.GetGraph(ctx, sourceSystem, false, run.PlayerID)
	if err != nil {
		return placement, fmt.Errorf("failed to load system graph for holder placement: %w", err)
	}
	source, ok := graphResult.Graph.Waypoints[run.SourceWaypoint]
	if !ok {
		return placement, fmt.Errorf("source waypoint %s not found in graph", run.SourceWaypoint)
	}

	candidateSet := make(map[string]bool, len(run.Candidates))
	for _, symbol := range run.Candidates {
		candidateSet[symbol] = true
	}

	nearestDist := 0.0
	for _, ship := range ships {
		location := ship.CurrentLocation()
		if location == nil || shared.ExtractSystemSymbol(location.Symbol) != sourceSystem {
			continue
		}

		if ship.ShipSymbol() == run.Holder {
			placement.HeldUnits = ship.Cargo().GetItemUnits(run.Good)
			placement.HolderSourceDist = location.DistanceTo(source)
			placement.HolderAtDestination = location.Symbol == run.Destination
			continue
		}

		if !candidateSet[ship.ShipSymbol()] {
			continue // not claimable for this contract on this pass
		}
		if ship.NavStatus() == navigation.NavStatusInTransit {
			continue // its position is already stale — never rank a moving hull
		}

		if distance := location.DistanceTo(source); placement.NearestHull == "" || distance < nearestDist {
			placement.NearestHull = ship.ShipSymbol()
			nearestDist = distance
		}
	}
	placement.NearestSourceDist = nearestDist

	return placement, nil
}

// decideDeliverHeldFirst weighs the sp-zve2q holder short-circuit against source
// proximity and reports whether the holder should be dispatched in DELIVER-HELD
// mode — registering the load it is standing on at zero travel and stopping, so
// the next pass hands the sourcing run to the hull near the source instead of
// flying this one to the source and back.
//
// attempted bounds it to ONE zero-travel delivery per (contract, hull) for this
// coordinator's lifetime. If the hull comes back still holding its load — an API
// refusal, or a daemon restart that rebuilt the worker as an ordinary run — the
// next pass runs the FULL leg rather than re-dispatching the same no-op forever.
func (h *RunFleetCoordinatorHandler) decideDeliverHeldFirst(ctx context.Context, contractID string, run holderRun, attempted map[string]bool) bool {
	logger := common.LoggerFromContext(ctx)

	placement, err := h.weighHolderPlacement(ctx, run)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf(
			"Failed to measure holder placement for %s (keeping the single-hull short-circuit unchanged): %v", run.Holder, err), nil)
		return false
	}

	decision := domainContract.WeighHolderAgainstSource(placement)
	if !decision.DeliverHeldFirst {
		return false
	}

	key := contractID + "|" + run.Holder
	if attempted[key] {
		logger.Log("INFO", fmt.Sprintf(
			"Hull %s already had its zero-travel deliver-held run this coordinator lifetime and still holds %s - running the FULL source+deliver leg instead of re-dispatching it (sp-5jce2 one-shot)",
			run.Holder, run.Good), map[string]interface{}{
			"action":      "deliver_held_already_attempted",
			"contract_id": contractID,
			"ship_symbol": run.Holder,
		})
		return false
	}
	attempted[key] = true

	logger.Log("INFO", fmt.Sprintf(
		"Holder %s sits %.1f units from source %s holding only %d of %d needed, while %s sits %.1f units away - dispatching %s to register its load where it stands (zero travel), then sourcing the remainder with the near hull instead of a %.1f-unit round trip (sp-5jce2): %s",
		run.Holder, placement.HolderSourceDist, run.SourceWaypoint, placement.HeldUnits, placement.UnitsNeeded,
		placement.NearestHull, placement.NearestSourceDist, run.Holder,
		2*placement.HolderSourceDist, decision.Reason), map[string]interface{}{
		"action":              "deliver_held_split",
		"contract_id":         contractID,
		"ship_symbol":         run.Holder,
		"trade_symbol":        run.Good,
		"held_units":          placement.HeldUnits,
		"units_needed":        placement.UnitsNeeded,
		"holder_source_dist":  placement.HolderSourceDist,
		"nearest_hull":        placement.NearestHull,
		"nearest_source_dist": placement.NearestSourceDist,
		"source":              run.SourceWaypoint,
	})
	return true
}
