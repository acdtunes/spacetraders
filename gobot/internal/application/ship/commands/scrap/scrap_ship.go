// Package scrap sells a hull back to a shipyard for credits and retires it from
// the fleet. It ships inert: nothing invokes it automatically.
package scrap

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// scrapOperation is the claim op (RULING #7). It matches no dedicated-fleet tag,
// so ClaimShip refuses a hull pinned to a fleet or flown by a coordinator.
const scrapOperation = "scrap"

// releaseContextTimeout bounds the cancellation-proof claim-release DB work.
const releaseContextTimeout = 10 * time.Second

// ContainerRepository is the container-persistence port the handler needs to
// satisfy the ships table's (container_id, player_id) foreign key while claiming.
type ContainerRepository interface {
	Add(ctx context.Context, containerEntity *domainContainer.Container, commandType string) error
	Remove(ctx context.Context, containerID string, playerID int) error
}

// ScrapShipCommand sells one hull for credits. Irreversible.
type ScrapShipCommand struct {
	ShipSymbol  string
	PlayerID    *int
	AgentSymbol string
}

// ScrapShipResponse is the outcome of a completed scrap.
type ScrapShipResponse struct {
	Success        bool
	ShipSymbol     string
	WaypointSymbol string
	TotalPrice     int
	Message        string
}

// ScrapShipHandler serves ScrapShipCommand.
type ScrapShipHandler struct {
	shipRepo       navigation.ShipRepository
	playerRepo     player.PlayerRepository
	apiClient      ports.APIClient
	containerRepo  ContainerRepository
	mediator       common.Mediator
	clock          shared.Clock
	playerResolver *common.PlayerResolver
}

// NewScrapShipHandler creates a ScrapShipHandler. A nil clock selects the real one.
func NewScrapShipHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient ports.APIClient,
	containerRepo ContainerRepository,
	mediator common.Mediator,
	clock shared.Clock,
) *ScrapShipHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &ScrapShipHandler{
		shipRepo:       shipRepo,
		playerRepo:     playerRepo,
		apiClient:      apiClient,
		containerRepo:  containerRepo,
		mediator:       mediator,
		clock:          clock,
		playerResolver: common.NewPlayerResolver(playerRepo),
	}
}

// Handle runs the scrap: claim the hull, refuse a laden or unreadable hold, dock,
// sell, bank the proceeds, then delete the row the game no longer backs.
func (h *ScrapShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*ScrapShipCommand)
	if !ok {
		return nil, fmt.Errorf("ScrapShipHandler: unsupported request type %T", request)
	}
	if cmd.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, cmd.PlayerID, cmd.AgentSymbol)
	if err != nil {
		return nil, err
	}
	owner, err := h.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get player: %w", err)
	}

	// FindBySymbol falls back to a sync when the row is missing, so the row exists
	// before ClaimShip locks it.
	if _, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID); err != nil {
		return nil, fmt.Errorf("failed to get ship %s: %w", cmd.ShipSymbol, err)
	}

	containerID, containerEntity := h.newScrapContainer(cmd.ShipSymbol, playerID)
	if err := h.containerRepo.Add(ctx, containerEntity, "scrap_ship"); err != nil {
		return nil, fmt.Errorf("failed to create scrap container record: %w", err)
	}
	defer h.removeContainer(containerID, playerID.Value())

	if err := h.shipRepo.ClaimShip(ctx, cmd.ShipSymbol, containerID, playerID, scrapOperation); err != nil {
		return nil, fmt.Errorf("cannot scrap %s: %w", cmd.ShipSymbol, err)
	}
	// Releasing a RETIRED hull would resurrect its row: the release re-loads the
	// ship, and a missing row is re-synced from the API before being written back.
	retired := false
	defer func() {
		if !retired {
			h.releaseClaim(cmd.ShipSymbol, playerID)
		}
	}()

	// The guard reads the hold only AFTER the claim, so nothing can load the hull
	// between the check and the sale.
	ship, err := h.emptyHullOrRefusal(ctx, cmd.ShipSymbol, playerID)
	if err != nil {
		return nil, err
	}

	if err := h.shipRepo.Dock(ctx, ship, playerID); err != nil {
		return nil, fmt.Errorf("failed to dock %s to scrap it: %w", cmd.ShipSymbol, err)
	}

	result, err := h.apiClient.ScrapShip(ctx, cmd.ShipSymbol, owner.Token)
	if err != nil {
		if ports.IsNotAtShipyard(err) {
			return nil, fmt.Errorf("cannot scrap %s: it is not docked at a waypoint with the SHIPYARD trait", cmd.ShipSymbol)
		}
		return nil, fmt.Errorf("failed to scrap %s: %w", cmd.ShipSymbol, err)
	}

	// The credits are already banked server-side, so record BEFORE the delete: a
	// persistence failure must not be the reason the income goes unrecorded.
	h.recordProceeds(ctx, cmd.ShipSymbol, owner.AgentSymbol, playerID, result)
	message := h.retireHull(ctx, cmd.ShipSymbol, playerID, result)
	retired = true

	return &ScrapShipResponse{
		Success:        true,
		ShipSymbol:     cmd.ShipSymbol,
		WaypointSymbol: result.WaypointSymbol,
		TotalPrice:     result.TotalPrice,
		Message:        message,
	}, nil
}

// emptyHullOrRefusal is the money guard (RULINGS #4, fails CLOSED). A scrap
// DESTROYS the hold and pays nothing for it, so anything short of a live,
// readable, empty hold is a refusal.
func (h *ScrapShipHandler) emptyHullOrRefusal(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	ship, err := h.shipRepo.SyncShipFromAPI(ctx, symbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("refusing to scrap %s: its live cargo state could not be read, and a scrap destroys cargo without paying for it (fail-closed): %w", symbol, err)
	}
	if ship == nil {
		return nil, fmt.Errorf("refusing to scrap %s: the live read produced no hull to read a hold from (fail-closed)", symbol)
	}
	if units := ship.CargoUnits(); units > 0 {
		return nil, fmt.Errorf("refusing to scrap %s: it still holds %d unit(s) of cargo, which a scrap destroys without paying for — drain the hold first", symbol, units)
	}
	return ship, nil
}

// retireHull deletes the row of a hull the game has destroyed. The sale cannot be
// undone, so a failed delete is reported and left to the next fleet sync to prune.
func (h *ScrapShipHandler) retireHull(ctx context.Context, symbol string, playerID shared.PlayerID, result *ports.ScrapResult) string {
	success := fmt.Sprintf("Scrapped %s at %s for %d credits", symbol, result.WaypointSymbol, result.TotalPrice)

	if err := h.shipRepo.DeleteShip(context.Background(), symbol, playerID); err != nil {
		common.LoggerFromContext(ctx).Log("ERROR", fmt.Sprintf("Scrapped %s but failed to remove its ships row: %v", symbol, err), map[string]interface{}{
			"ship":      symbol,
			"player_id": playerID.Value(),
		})
		return success + " (WARNING: its fleet row could not be removed and will linger until the next fleet sync)"
	}
	return success
}

// recordProceeds writes the sale to the financial ledger. AgentCredits re-anchors
// the running chain to API truth; a ledger failure is logged, never returned.
func (h *ScrapShipHandler) recordProceeds(ctx context.Context, symbol, agentSymbol string, playerID shared.PlayerID, result *ports.ScrapResult) {
	logger := common.LoggerFromContext(ctx)

	// ledger.Validate rejects amount == 0, so a free scrap is not a transaction.
	if result.TotalPrice == 0 {
		return
	}
	if h.mediator == nil {
		logger.Log("ERROR", "Cannot record scrap proceeds: no mediator wired", map[string]interface{}{
			"ship":     symbol,
			"proceeds": result.TotalPrice,
		})
		return
	}
	if agentSymbol == "" {
		agentSymbol = "UNKNOWN"
	}

	// Zero baseline: a nil AgentCredits reconstructs from the running chain.
	const balanceBefore = 0

	recordCmd := &ledgerCommands.RecordTransactionCommand{
		PlayerID:             playerID.Value(),
		TransactionType:      string(ledger.TransactionTypeScrapShip),
		Amount:               result.TotalPrice,
		BalanceBefore:        balanceBefore,
		BalanceAfter:         balanceBefore + result.TotalPrice,
		AuthoritativeBalance: result.AgentCredits,
		Description:          fmt.Sprintf("Scrapped %s at %s", symbol, result.WaypointSymbol),
		Metadata: map[string]interface{}{
			"agent":       agentSymbol,
			"ship_symbol": symbol,
			"waypoint":    result.WaypointSymbol,
		},
	}

	if opCtx := shared.OperationContextFromContext(ctx); opCtx != nil && opCtx.IsValid() {
		recordCmd.RelatedEntityType = "container"
		recordCmd.RelatedEntityID = opCtx.ContainerID
		recordCmd.OperationType = opCtx.NormalizedOperationType()
	} else {
		recordCmd.OperationType = "manual"
	}

	// context.Background(): the income is already real, so the record must survive
	// a cancelled caller context.
	if _, err := h.mediator.Send(context.Background(), recordCmd); err != nil {
		logger.Log("ERROR", "Failed to record scrap proceeds in ledger", map[string]interface{}{
			"error":     err.Error(),
			"ship":      symbol,
			"proceeds":  result.TotalPrice,
			"player_id": playerID.Value(),
		})
	}
}

// newScrapContainer builds the FK-parent container row; the nonce keeps a
// concurrent scrap of the same hull off this primary key.
func (h *ScrapShipHandler) newScrapContainer(symbol string, playerID shared.PlayerID) (string, *domainContainer.Container) {
	containerID := fmt.Sprintf("ship-scrap-%s-%d", symbol, h.clock.Now().UnixNano())
	return containerID, domainContainer.NewContainer(
		containerID,
		domainContainer.ContainerTypeScrap,
		playerID.Value(),
		1,
		nil,
		map[string]interface{}{"ship_symbol": symbol},
		h.clock,
	)
}

// releaseClaim returns a hull the scrap did NOT consume to idle. On the completed
// path the row is already gone and the retry finds nothing to release.
func (h *ScrapShipHandler) releaseClaim(symbol string, playerID shared.PlayerID) {
	ctx, cancel := context.WithTimeout(context.Background(), releaseContextTimeout)
	defer cancel()

	_, _, _ = h.shipRepo.SaveWithRetry(ctx, symbol, playerID,
		func(sh *navigation.Ship) (bool, error) {
			if !sh.IsAssigned() {
				return false, nil
			}
			sh.ForceRelease("scrap_done", h.clock)
			return true, nil
		})
}

// removeContainer deletes the lightweight scrap container row.
func (h *ScrapShipHandler) removeContainer(containerID string, playerID int) {
	ctx, cancel := context.WithTimeout(context.Background(), releaseContextTimeout)
	defer cancel()
	_ = h.containerRepo.Remove(ctx, containerID, playerID)
}
