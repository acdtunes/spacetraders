// Package assignment holds captain-facing commands that directly control a
// ship's assignment ownership, distinct from the coordinator-facing claim
// path in the container packages.
package assignment

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ReserveShipCommand reserves a ship for the captain's direct, manual use,
// hiding it from every coordinator's assignment discovery. A
// captain reservation is persisted as an active ShipAssignment row (owner=
// captain), so it survives daemon restarts and is excluded from the
// stale-claim reconciliation pass that runs on ship refresh (see
// RefreshShipHandler.reconcileStaleClaim and Ship.IsReservedByCaptain).
type ReserveShipCommand struct {
	ShipSymbol  string // Required: ship symbol to reserve
	Reason      string // Optional: free-text reason, shown in `ship list`
	PlayerID    *int   // Resolve by numeric player ID (takes precedence)
	AgentSymbol string // Resolve by agent symbol if PlayerID is nil

	// Force PREEMPTS a coordinator's live claim: `ship reserve --force`.
	// When true, a hull actively claimed by a coordinator container is atomically
	// revoked and transferred to the captain (operator authority wins) instead of
	// being rejected. The zero value (false) rejects a claimed hull as normal.
	// --force is the ONLY ownership bypass — explicit and operator-initiated.
	Force bool
}

// ReserveShipResponse confirms the reservation and carries a soft warning
// when the reserved hull was idle-critical.
type ReserveShipResponse struct {
	ShipSymbol string
	Reason     string
	// Warning is non-empty if reserving this ship left zero other idle ships
	// sharing its role (e.g. "the last idle hauler"). Advisory only — the
	// reservation has already succeeded by the time this is computed.
	Warning string

	// Preempted is true when --force revoked a coordinator's live claim (as
	// opposed to reserving an already-idle hull). PreemptedFrom names the
	// container the claim was revoked from, so the CLI can tell the operator
	// exactly what was taken back. Both are zero for a non-force
	// reserve or a force reserve of an idle hull.
	Preempted     bool
	PreemptedFrom string
}

// ReserveShipHandler handles the ReserveShip command.
type ReserveShipHandler struct {
	shipRepo       navigation.ShipRepository
	playerResolver *common.PlayerResolver
}

// NewReserveShipHandler creates a new ReserveShipHandler.
func NewReserveShipHandler(shipRepo navigation.ShipRepository, playerRepo player.PlayerRepository) *ReserveShipHandler {
	return &ReserveShipHandler{
		shipRepo:       shipRepo,
		playerResolver: common.NewPlayerResolver(playerRepo),
	}
}

// Handle executes the ReserveShip command.
func (h *ReserveShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*ReserveShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *ReserveShipCommand, got %T", request)
	}

	if cmd.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, cmd.PlayerID, cmd.AgentSymbol)
	if err != nil {
		return nil, err
	}

	var preemptedFrom string
	if cmd.Force {
		// --force: atomically revoke any coordinator claim and transfer the hull
		// to the captain. preemptedFrom is the container the claim was
		// revoked from, or "" when the hull was already idle.
		preemptedFrom, err = h.shipRepo.PreemptForCaptain(ctx, cmd.ShipSymbol, cmd.Reason, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to preempt ship for captain: %w", err)
		}
	} else if err := h.shipRepo.ReserveForCaptain(ctx, cmd.ShipSymbol, cmd.Reason, playerID); err != nil {
		// Non-force: a live coordinator claim is rejected.
		return nil, fmt.Errorf("failed to reserve ship: %w", err)
	}

	return &ReserveShipResponse{
		ShipSymbol:    cmd.ShipSymbol,
		Reason:        cmd.Reason,
		Warning:       h.idleCriticalWarning(ctx, cmd.ShipSymbol, playerID),
		Preempted:     preemptedFrom != "",
		PreemptedFrom: preemptedFrom,
	}, nil
}

// idleCriticalWarning reports whether reserving shipSymbol left the fleet
// with zero other idle ships sharing its role. Best-effort: any lookup
// failure yields no warning rather than failing the command, since the
// reservation has already succeeded and this is advisory only.
func (h *ReserveShipHandler) idleCriticalWarning(ctx context.Context, shipSymbol string, playerID shared.PlayerID) string {
	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil || ship == nil {
		return ""
	}
	role := ship.Role()

	idleShips, err := h.shipRepo.FindIdleByPlayer(ctx, playerID)
	if err != nil {
		return ""
	}

	for _, other := range idleShips {
		if other.Role() == role {
			return "" // at least one other idle ship still shares this role
		}
	}

	return fmt.Sprintf("warning: %s was the last idle ship with role %s — no idle %s remain", shipSymbol, role, role)
}
