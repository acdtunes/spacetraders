package assignment

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// AssignShipFleetCommand dedicates a ship to a named fleet — the SINGLE write
// path for the DedicatedFleet tag (sp-l7h2). Fleet == "" clears the
// dedication, returning the ship to the general pool (the CLI's `fleet
// unassign` sends exactly that). Dedication is permanent ownership, distinct
// from a container claim ("who holds it right now"): assigning a busy ship
// succeeds and takes effect when its current claim is released — it never
// evicts the holder. Enforcement is two-layered: the FindIdleLightHaulers
// exclude filter (discovery pre-check) plus the atomic dedication guard
// inside ClaimShip's row-locked transaction (the correctness guarantee).
type AssignShipFleetCommand struct {
	ShipSymbol  string // Required: ship symbol to (un)dedicate
	Fleet       string // Fleet name; "" clears the dedication
	PlayerID    *int   // Resolve by numeric player ID (takes precedence)
	AgentSymbol string // Resolve by agent symbol if PlayerID is nil

	// Assigner names the code path performing this dedication write, for the
	// assigner-named audit line every write now emits (sp-r6f1 finding #3):
	// e.g. "contract-coordinator-reconcile:<containerID>", "cli". Empty logs as
	// "unknown". So the next mispin names its culprit in one grep.
	Assigner string

	// Manual distinguishes a human/operator-initiated write (true, the CLI's
	// `fleet assign`) from an automated coordinator write (false, the default —
	// the contract coordinator's --dedicated-ships reconcile). It selects the
	// eligibility-failure behavior for a cargo-required fleet (sp-r6f1): an
	// AUTOMATED attempt to pin a 0-cargo hull into a hauling fleet is BLOCKED
	// (this is the reconcile mispinner that re-pinned TORWIND-24/25), while a
	// MANUAL one is WARNED-and-allowed — the captain may deliberately pin
	// anything. The zero value (false) fails closed: an assigner that forgets
	// to set it gets the strict auto behavior.
	Manual bool

	// BreakWorkClaim additionally severs the hull's LIVE coordinator work-claim
	// after clearing the dedication (sp-w3yd) — the operator's `fleet unassign`
	// sets this so the coordinator actually STOPS routing the hull, closing the
	// "unassign says success but the coordinator keeps routing it" gap. Scoped to
	// the operator path on purpose: the zero value (false) fails safe so the
	// automated dedication reconcile — which shares this handler — never strands a
	// running worker by breaking its claim. Only meaningful on the unassign
	// (Fleet=="") path; a captain reservation is left untouched by the break.
	BreakWorkClaim bool
}

// AssignShipFleetResponse confirms the dedication write.
type AssignShipFleetResponse struct {
	ShipSymbol string
	Fleet      string // The fleet now persisted; "" means undedicated
}

// FleetCargoRequirement maps a fleet name to the minimum cargo capacity a hull
// must have to be eligible for it (sp-r6f1). A hauling fleet requires > 0 so a
// 0-cargo satellite/probe can never be auto-pinned into it; a fleet absent from
// the map imposes no requirement — scout/tour fleets legitimately fly 0-cargo
// hulls, so their assignments are never gated. Cargo capacity (not a hardcoded
// frame-name list) is what expresses "can this hull haul": a SATELLITE frame has
// 0 capacity, so the capacity floor already excludes it (RULINGS #5).
type FleetCargoRequirement map[string]int

// MinCargoCapacity returns the cargo-capacity floor for fleet, or 0 (no
// requirement) for the empty (unassign) fleet or a fleet not in the map.
func (r FleetCargoRequirement) MinCargoCapacity(fleet string) int {
	if fleet == "" {
		return 0
	}
	return r[fleet]
}

// dedicatedFleetContract is the one pool-managed hauling fleet today, matching
// dedicatedFleetContract in run_fleet_coordinator.go and
// captain.defaultStandingCoordinatorFleets. Its members must be able to haul.
const dedicatedFleetContract = "contract"

// dedicatedFleetStocker is the durable continuous-stocking hauler fleet (sp-m92a),
// matching operationStocker in container_ops_stocker.go — the fleet name the
// captain pins with `fleet assign --fleet stocker` AND the operation the stocker
// container claims under. A stocker hull IS a cargo hauler, so it joins the
// cargo-floor set alongside contract: a 0-cargo hull can never be pinned to it.
const dedicatedFleetStocker = "stocker"

// DefaultFleetCargoRequirement is the standing eligibility rule wired in
// production (sp-r6f1): a hauling fleet's members must carry cargo (floor 1).
// Parametrized here rather than hardcoded in the handler so the rule has one
// obvious home and can grow other hauling fleets without touching the gate. Both
// the contract pool and the sp-m92a stocker dedication are cargo-required.
var DefaultFleetCargoRequirement = FleetCargoRequirement{
	dedicatedFleetContract: 1,
	dedicatedFleetStocker:  1,
}

// AssignShipFleetHandler handles the AssignShipFleet command.
type AssignShipFleetHandler struct {
	shipRepo       navigation.ShipRepository
	playerResolver *common.PlayerResolver
	fleetCargoReq  FleetCargoRequirement
}

// NewAssignShipFleetHandler creates a new AssignShipFleetHandler with the
// production eligibility rule (DefaultFleetCargoRequirement).
func NewAssignShipFleetHandler(shipRepo navigation.ShipRepository, playerRepo player.PlayerRepository) *AssignShipFleetHandler {
	return &AssignShipFleetHandler{
		shipRepo:       shipRepo,
		playerResolver: common.NewPlayerResolver(playerRepo),
		fleetCargoReq:  DefaultFleetCargoRequirement,
	}
}

// Handle executes the AssignShipFleet command.
//
// Every dedication write now flows through one eligibility gate + one
// assigner-named audit line (sp-r6f1):
//
//   - The hull is loaded so the gate and the audit line can see its frame and
//     cargo capacity.
//   - A change into a cargo-required fleet (the "contract" hauling fleet) by a
//     0-cargo hull is BLOCKED on the automated path (the reconcile mispinner)
//     and WARNED-but-allowed on the manual path (operator authority). Fleets
//     with no cargo floor (scouts/tours) and the unassign path are never gated.
//   - An idempotent no-op (the hull already carries the target tag) is neither
//     gated nor audit-logged: the reconcile re-applies its whole config list
//     every restart, and re-touching an already-correct pin changes nothing.
//   - Every ACTUAL write emits one INFO audit line naming ship, fleet, assigner,
//     frame and cargo — the line the daemon.log lacked at incident time.
func (h *AssignShipFleetHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*AssignShipFleetCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *AssignShipFleetCommand, got %T", request)
	}

	if cmd.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, cmd.PlayerID, cmd.AgentSymbol)
	if err != nil {
		return nil, err
	}

	logger := common.LoggerFromContext(ctx)
	assigner := cmd.Assigner
	if assigner == "" {
		assigner = "unknown"
	}

	// Load the hull to read its frame + cargo for the gate and the audit line.
	// Fail closed: if the hull cannot be read, no dedication is written (the old
	// path would also have failed inside AssignFleet).
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to assign ship fleet: %w", err)
	}
	if ship == nil {
		return nil, fmt.Errorf("failed to assign ship fleet: ship %s not found for player %d", cmd.ShipSymbol, playerID.Value())
	}
	frame := ship.FrameSymbol()
	cargoCapacity := ship.CargoCapacity()
	changed := ship.DedicatedFleet() != cmd.Fleet

	// Eligibility gate (sp-r6f1) — only a REAL change into a cargo-required
	// fleet by an under-capacity hull. A no-op re-touch of an already-mispinned
	// hull is intentionally skipped so the reconcile does not error-spam every
	// restart while a legacy mispin persists.
	if changed {
		if minCargo := h.fleetCargoReq.MinCargoCapacity(cmd.Fleet); minCargo > 0 && cargoCapacity < minCargo {
			if !cmd.Manual {
				// AUTOMATED path — BLOCK. This is the reconcile mispinner: refuse
				// the write, name the assigner + hull class so the culprit is one
				// grep away.
				logger.Log("ERROR", fmt.Sprintf(
					"BLOCKED auto-assign of %s to %q fleet: cargo capacity %d below floor %d (frame %s) — a hull that cannot haul is never auto-pinned to a hauling fleet [assigner=%s]",
					cmd.ShipSymbol, cmd.Fleet, cargoCapacity, minCargo, frame, assigner),
					map[string]interface{}{
						"action":         "block_ineligible_fleet_assign",
						"ship_symbol":    cmd.ShipSymbol,
						"fleet":          cmd.Fleet,
						"assigner":       assigner,
						"frame":          frame,
						"cargo_capacity": cargoCapacity,
						"min_cargo":      minCargo,
					})
				return nil, fmt.Errorf(
					"ship %s ineligible for %q fleet: cargo capacity %d below floor %d (frame %s) — auto-assign blocked (sp-r6f1)",
					cmd.ShipSymbol, cmd.Fleet, cargoCapacity, minCargo, frame)
			}
			// MANUAL path — WARN, do not block. The captain may deliberately pin
			// anything; the selection side already refuses to dispatch a 0-cargo
			// hull (sp-lybx), so this is dead weight, not a crash — say so loudly.
			logger.Log("WARNING", fmt.Sprintf(
				"Manual assign of %s to %q fleet: 0-cargo hull (frame %s, cargo %d) cannot haul — the fleet coordinator will exclude it from selection (sp-lybx). Proceeding on operator authority [assigner=%s]",
				cmd.ShipSymbol, cmd.Fleet, frame, cargoCapacity, assigner),
				map[string]interface{}{
					"action":         "warn_manual_ineligible_fleet_assign",
					"ship_symbol":    cmd.ShipSymbol,
					"fleet":          cmd.Fleet,
					"assigner":       assigner,
					"frame":          frame,
					"cargo_capacity": cargoCapacity,
					"min_cargo":      minCargo,
				})
		}
	}

	if err := h.shipRepo.AssignFleet(ctx, cmd.ShipSymbol, cmd.Fleet, playerID); err != nil {
		return nil, fmt.Errorf("failed to assign ship fleet: %w", err)
	}

	// sp-w3yd: `fleet unassign` (BreakWorkClaim) additionally severs the live
	// coordinator work-claim so the coordinator stops routing the hull — clearing
	// the dedication alone only governs the NEXT acquisition, not the current
	// claim. Scoped to the operator path (the reconcile leaves BreakWorkClaim
	// false), and a captain reservation is left untouched by the break (that is
	// `ship release`'s job). Best-effort audit: a broken claim logs one line.
	if cmd.BreakWorkClaim {
		broke, err := h.shipRepo.ReleaseContainerClaim(ctx, cmd.ShipSymbol, playerID, "fleet unassign (sp-w3yd)")
		if err != nil {
			return nil, fmt.Errorf("failed to break live work-claim on unassign: %w", err)
		}
		if broke {
			logger.Log("INFO", fmt.Sprintf(
				"Broke live coordinator work-claim on %s during unassign — coordinator will stop routing it [assigner=%s]",
				cmd.ShipSymbol, assigner),
				map[string]interface{}{
					"action":      "break_work_claim_on_unassign",
					"ship_symbol": cmd.ShipSymbol,
					"assigner":    assigner,
				})
		}
	}

	// Assigner-named audit line (sp-r6f1 finding #3): EVERY actual dedication
	// write logs exactly one line naming ship, fleet, assigner, frame and cargo,
	// closing the "no fleet-assign log line found" gap the incident hit. A no-op
	// re-touch writes nothing, so it emits no line (keeps the reconcile quiet).
	if changed {
		logger.Log("INFO", fmt.Sprintf(
			"Fleet dedication written: %s -> %q (frame %s, cargo %d) [assigner=%s]",
			cmd.ShipSymbol, cmd.Fleet, frame, cargoCapacity, assigner),
			map[string]interface{}{
				"action":         "fleet_dedication_write",
				"ship_symbol":    cmd.ShipSymbol,
				"fleet":          cmd.Fleet,
				"assigner":       assigner,
				"frame":          frame,
				"cargo_capacity": cargoCapacity,
			})
	}

	return &AssignShipFleetResponse{
		ShipSymbol: cmd.ShipSymbol,
		Fleet:      cmd.Fleet,
	}, nil
}
