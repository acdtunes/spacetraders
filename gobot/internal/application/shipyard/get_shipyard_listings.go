package shipyard

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

// GetShipyardListingsQuery is a query to get available ships at a shipyard
type GetShipyardListingsQuery struct {
	SystemSymbol   string
	WaypointSymbol string
	PlayerID       int
}

// GetShipyardListingsResponse contains the shipyard data
type GetShipyardListingsResponse struct {
	Shipyard shipyard.Shipyard
}

// GetShipyardListingsHandler handles the GetShipyardListings query
type GetShipyardListingsHandler struct {
	apiClient ports.APIClient
	playerRepo player.PlayerRepository
	// graphBuilder is unused for now but kept for consistency
}

// NewGetShipyardListingsHandler creates a new GetShipyardListingsHandler
func NewGetShipyardListingsHandler(
	apiClient ports.APIClient,
	playerRepo player.PlayerRepository,
) *GetShipyardListingsHandler {
	return &GetShipyardListingsHandler{
		apiClient:  apiClient,
		playerRepo: playerRepo,
	}
}

// Handle executes the GetShipyardListings query
func (h *GetShipyardListingsHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*GetShipyardListingsQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}
	// Get player to retrieve auth token
	player, err := h.playerRepo.FindByID(ctx, query.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find player: %w", err)
	}

	// Call API to get shipyard data
	shipyardData, err := h.apiClient.GetShipyard(ctx, query.SystemSymbol, query.WaypointSymbol, player.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to get shipyard: %w", err)
	}

	// Convert API data to domain model
	listings := make([]shipyard.ShipListing, len(shipyardData.Ships))
	for i, ship := range shipyardData.Ships {
		listings[i] = shipyard.ShipListing{
			ShipType:      ship.Type,
			Name:          ship.Name,
			Description:   ship.Description,
			PurchasePrice: ship.PurchasePrice,
			Frame:         ship.Frame,
			Reactor:       ship.Reactor,
			Engine:        ship.Engine,
			Modules:       ship.Modules,
			Mounts:        ship.Mounts,
		}
	}

	shipTypes := make([]string, len(shipyardData.ShipTypes))
	for i, st := range shipyardData.ShipTypes {
		shipTypes[i] = st.Type
	}

	shipyardDomain := shipyard.Shipyard{
		Symbol:          shipyardData.Symbol,
		ShipTypes:       shipTypes,
		Listings:        listings,
		Transactions:    shipyardData.Transactions,
		ModificationFee: shipyardData.ModificationFee,
	}

	return &GetShipyardListingsResponse{
		Shipyard: shipyardDomain,
	}, nil
}
