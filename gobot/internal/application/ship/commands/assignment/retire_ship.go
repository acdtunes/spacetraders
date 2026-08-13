package assignment

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RetireShipCommand withdraws a hull from service: the tour it is flying finishes and
// sells normally, and once its hold is empty no coordinator plans it another. It is
// deliberately NOT `fleet unassign`, which breaks the live claim, stops the container and
// leaves a mid-tour hull parked wherever it stood, still holding its load — and then makes
// it claimable by every other coordinator.
type RetireShipCommand struct {
	ShipSymbol  string // Required: ship symbol to retire
	Cancel      bool   // Clear the mark, returning the hull to normal service
	PlayerID    *int   // Resolve by numeric player ID (takes precedence)
	AgentSymbol string // Resolve by agent symbol if PlayerID is nil
}

// RetireShipResponse reports the mark and the hull's drain progress, so the operator
// learns at once whether it is already scrap-ready or still has a load to sell.
type RetireShipResponse struct {
	ShipSymbol string
	Retiring   bool
	CargoUnits int
	Drained    bool
}

// RetireShipHandler handles the RetireShip command.
type RetireShipHandler struct {
	shipRepo       navigation.ShipRepository
	playerResolver *common.PlayerResolver
}

// NewRetireShipHandler creates a new RetireShipHandler.
func NewRetireShipHandler(shipRepo navigation.ShipRepository, playerRepo player.PlayerRepository) *RetireShipHandler {
	return &RetireShipHandler{
		shipRepo:       shipRepo,
		playerResolver: common.NewPlayerResolver(playerRepo),
	}
}

// Handle executes the RetireShip command.
func (h *RetireShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RetireShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *RetireShipCommand, got %T", request)
	}

	if cmd.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, cmd.PlayerID, cmd.AgentSymbol)
	if err != nil {
		return nil, err
	}

	// Fail closed: a hull that cannot be read gets no mark, so an operator is never told a
	// retirement took when the daemon could not see the hull to begin with.
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to retire ship: %w", err)
	}
	if ship == nil {
		return nil, fmt.Errorf("failed to retire ship: ship %s not found for player %d", cmd.ShipSymbol, playerID.Value())
	}

	retiring := !cmd.Cancel
	if err := h.shipRepo.SetShipRetiring(ctx, cmd.ShipSymbol, retiring, playerID); err != nil {
		return nil, fmt.Errorf("failed to retire ship: %w", err)
	}

	h.applyMark(ship, retiring)
	h.logDecision(ctx, ship, retiring)

	return &RetireShipResponse{
		ShipSymbol: cmd.ShipSymbol,
		Retiring:   retiring,
		CargoUnits: ship.CargoUnits(),
		Drained:    ship.RetirementDrained(),
	}, nil
}

// applyMark mirrors the persisted write onto the loaded hull so the drain verdict comes
// from the one domain predicate the coordinator's gate reads, never a second copy of it.
func (h *RetireShipHandler) applyMark(ship *navigation.Ship, retiring bool) {
	if !retiring {
		ship.CancelRetirement()
		return
	}
	ship.MarkRetiring(shared.NewRealClock())
}

func (h *RetireShipHandler) logDecision(ctx context.Context, ship *navigation.Ship, retiring bool) {
	logger := common.LoggerFromContext(ctx)
	if !retiring {
		logger.Log("INFO", fmt.Sprintf("Retirement cancelled for %s — it returns to normal service", ship.ShipSymbol()),
			map[string]interface{}{"action": "ship_retirement_cancelled", "ship_symbol": ship.ShipSymbol()})
		return
	}
	logger.Log("INFO", fmt.Sprintf(
		"%s marked retiring holding %d unit(s) — its current tour finishes and sells, then it is planned no more",
		ship.ShipSymbol(), ship.CargoUnits()),
		map[string]interface{}{
			"action":      "ship_retirement_marked",
			"ship_symbol": ship.ShipSymbol(),
			"cargo_units": ship.CargoUnits(),
			"drained":     ship.RetirementDrained(),
		})
}
