package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	shipAssignment "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/assignment"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// dedicatedFleetContract is the Ship.DedicatedFleet() value this coordinator
// reconciles its --dedicated-ships list into.
const dedicatedFleetContract = "contract"

// ErrCommandFrigateNotLastResort is returned by spawnContractWorker when it
// refuses to draft an UNDEDICATED command frigate for a contract haul because a
// regular hauler is available (RULINGS #7: the command frigate hauls only as a
// last resort). A sentinel so callers/tests can distinguish this deliberate
// policy refusal from a transient spawn failure via errors.Is.
var ErrCommandFrigateNotLastResort = errors.New("command frigate not drafted for contract haul: regular haulers available (last-resort only)")

// hasRegularHaulerCandidate reports whether any candidate is a non-command hull
// (a regular hauler). The main loop uses it to compute the command frigate's
// last-resort verdict for the claim-side guard: a regular hauler among the
// discovered candidates means the frigate is NOT the last resort.
func hasRegularHaulerCandidate(candidates []*navigation.Ship) bool {
	for _, ship := range candidates {
		if !domainContract.IsCommandHull(ship) {
			return true
		}
	}
	return false
}

// DedicatedFleetSeedMarker durably records that this coordinator has applied its
// --dedicated-ships launch seed ONCE, so a later daemon-restart rebuild reads the
// marker back and does NOT replay the stale launch seed over live fleet state —
// a hull deliberately `fleet remove`d stays removed across the restart
// (RULINGS #2). Reporting/gating only — no ship state is written here.
type DedicatedFleetSeedMarker interface {
	// MarkDedicatedShipsSeeded records that containerID's --dedicated-ships seed
	// has been applied, so a later restart rebuild reads DedicatedShipsSeeded=true.
	// A returned error is advisory: the seed has already been applied, so the caller
	// logs and continues (a persistence failure degrades restart-resilience of the
	// removal, it never fails the coordinator).
	MarkDedicatedShipsSeeded(ctx context.Context, containerID string, playerID int) error
}

// dedicationSeed applies the --dedicated-ships launch seed EXACTLY
// once per coordinator lifetime. On genuine first boot (seeded=false) it
// reconciles the seed into the dedication tag and then persists a durable "seeded"
// marker into the coordinator's own container config; on every subsequent daemon
// restart (seeded=true, reloaded from that marker) it does NOTHING, leaving the
// live dedicated_fleet tag authoritative. Replaying the seed on every boot would
// re-stamp a hull removed via `fleet remove` (tag cleared) back onto the fleet,
// resurrecting a deliberate removal.
//
// An empty seed still touches nothing (mediator lookup included). A nil marker
// leaves the seed un-persisted and warns: the seed still applies, but a restart
// would re-seed (fail-open; production always wires it).
type dedicationSeed struct {
	logger         common.ContainerLogger
	med            common.Mediator
	playerID       shared.PlayerID
	dedicatedShips []string
	fleetName      string
	assigner       string
}

func (s dedicationSeed) seedIfFirstBoot(ctx context.Context, marker DedicatedFleetSeedMarker, containerID string, seeded bool) {
	if len(s.dedicatedShips) == 0 {
		return
	}
	// Already seeded on a previous boot — the live dedicated_fleet tag is now
	// authoritative. Do NOT replay the stale launch snapshot, or a hull removed
	// via `fleet remove` (tag cleared) that is still listed in the seed would be
	// re-stamped "contract", resurrecting a deliberate removal.
	if seeded {
		return
	}

	s.reconcile(ctx)

	// Persist the first-boot marker so a later restart reloads seeded=true and skips
	// the replay above. Fail-open: the seed has already been applied, so a marker
	// failure is a WARNING, never a coordinator abort (RULINGS #1 never-skip).
	if marker == nil {
		s.logger.Log("WARNING", fmt.Sprintf(
			"dedicated fleet seed applied for %d ship(s) but no seed marker is wired - a daemon restart may replay the launch seed over live fleet state (sp-86vb)",
			len(s.dedicatedShips)), nil)
		return
	}
	if err := marker.MarkDedicatedShipsSeeded(ctx, containerID, s.playerID.Value()); err != nil {
		s.logger.Log("WARNING", fmt.Sprintf(
			"dedicated fleet seed applied but failed to persist the seeded marker (a restart may replay the launch seed): %v", err), nil)
	}
}

// reconcile marks every operator-configured --dedicated-ships
// entry into fleetName so the DedicatedFleet claim-filter in
// FindIdleLightHaulers actually takes effect. Routed through
// AssignShipFleetCommand, the single write path for the dedication tag, rather
// than mutating ships directly, so reconciliation and `fleet assign` can never
// drift apart. Additive-only: a symbol removed from a later --dedicated-ships
// list on restart is NOT un-dedicated by this pass — only configured symbols
// are marked. Idempotent: the repository write skips the DB write when the tag
// is already fleetName. Per-ship failures are logged at WARNING and skipped
// rather than aborting the whole pass, since one bad symbol must not block
// reconciling the rest.
func (s dedicationSeed) reconcile(ctx context.Context) {
	for _, symbol := range s.dedicatedShips {
		pid := s.playerID.Value()
		// Automated path (Manual: false): the assign handler BLOCKS a 0-cargo hull
		// from being pinned into a hauling fleet. A blocked symbol surfaces as the
		// WARNING below and is skipped, like any other per-ship failure — the
		// rest of the list still reconciles.
		_, err := s.med.Send(ctx, &shipAssignment.AssignShipFleetCommand{
			ShipSymbol: symbol,
			Fleet:      s.fleetName,
			PlayerID:   &pid,
			Assigner:   s.assigner,
			Manual:     false,
		})
		if err != nil {
			s.logger.Log("WARNING", fmt.Sprintf("dedicated fleet reconciliation: failed to assign ship %s: %v", symbol, err), nil)
			continue
		}
		s.logger.Log("INFO", fmt.Sprintf("Ship %s reconciled into dedicated %s fleet", symbol, s.fleetName), nil)
	}
}

// isDedicatedShip reports whether shipSymbol is present in the given
// dedicated-membership list. Used at the "previous ship" hook to decide whether
// an idle ship homes to a standby station instead of being balanced to a market.
// The list is the LIVE dedicated-fleet membership, not the immutable
// --dedicated-ships launch snapshot — see resolveDedicatedMembersForHoming.
func isDedicatedShip(shipSymbol string, dedicatedShips []string) bool {
	for _, symbol := range dedicatedShips {
		if symbol == shipSymbol {
			return true
		}
	}
	return false
}

// resolveDedicatedMembersForHoming returns the LIVE dedicated-fleet membership
// the between-legs homing gate keys off, so the gate and the standby-occupancy
// peer list track actual membership, matching the live authority
// FindIdleShipsByFleet / FleetHasMembers already give the selection side.
//
// On a membership read error it falls back to launchList, so a transient repo
// failure just forgoes the live view for that one repositioning.
func resolveDedicatedMembersForHoming(
	ctx context.Context,
	logger common.ContainerLogger,
	shipRepo navigation.ShipRepository,
	playerID shared.PlayerID,
	fleet string,
	launchList []string,
) []string {
	members, err := appContract.FindFleetMemberSymbols(ctx, playerID, shipRepo, fleet)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf(
			"failed to read live %s-fleet membership for homing (falling back to launch --dedicated-ships list): %v", fleet, err), nil)
		return launchList
	}
	return members
}

// scopeCandidatesToContractHome narrows the worker-candidate pool to hulls idle in the
// contract's HOME system — the delivery-destination system, the SAME authoritative scope
// PlanSourcing/market_finder derive contract sourcing from (RULINGS #14: the worker is
// zero-jump, so a hull outside the delivery system can neither source nor deliver).
//
// Scope applies to the GENERAL grab pool only: in EXCLUSIVE MODE (a dedicated contract
// fleet active) the pool passes through unscoped, since a dedicated fleet already draws
// ONLY from its own members. Reserved hulls from the fleet reserve floor are UNDEDICATED +
// home, so they ride this general path and stay eligible. An un-derivable destination
// yields an empty home system, so FilterToHomeSystem degrades to fleet-wide (fail-open)
// and never blocks the contract.
func (h *RunFleetCoordinatorHandler) scopeCandidatesToContractHome(
	ctx context.Context,
	playerID shared.PlayerID,
	candidates []string,
	deliveryDestination string,
	dedicatedFleetActive bool,
) ([]string, error) {
	if dedicatedFleetActive {
		return candidates, nil
	}
	homeSystem := shared.ExtractSystemSymbol(deliveryDestination)
	return appContract.FilterToHomeSystem(ctx, playerID, h.shipRepo, candidates, homeSystem)
}
