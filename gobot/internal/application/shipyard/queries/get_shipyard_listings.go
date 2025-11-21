package queries

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// GetShipyardListingsQuery is a query to get available ships at a shipyard
type GetShipyardListingsQuery struct {
	SystemSymbol   string
	WaypointSymbol string
	PlayerID       shared.PlayerID
}

// GetShipyardListingsResponse contains the shipyard data
type GetShipyardListingsResponse struct {
	Shipyard shipyard.Shipyard
}

// GetShipyardListingsHandler handles the GetShipyardListings query
type GetShipyardListingsHandler struct {
	apiClient  domainPorts.APIClient
	playerRepo player.PlayerRepository
	// graphBuilder is unused for now but kept for consistency
}

// NewGetShipyardListingsHandler creates a new GetShipyardListingsHandler
func NewGetShipyardListingsHandler(
	apiClient domainPorts.APIClient,
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

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}

	shipyardData, err := h.apiClient.GetShipyard(ctx, query.SystemSymbol, query.WaypointSymbol, token)
	if err != nil {
		return nil, fmt.Errorf("failed to get shipyard: %w", err)
	}

	shipListings := h.convertShipListings(shipyardData.Ships)
	shipTypes := h.extractShipTypeStrings(shipyardData.ShipTypes)

	shipyardDomain, err := h.buildShipyardDomain(shipyardData, shipListings, shipTypes)
	if err != nil {
		return nil, err
	}

	return &GetShipyardListingsResponse{
		Shipyard: shipyardDomain,
	}, nil
}

// convertShipListings converts API ship listings to domain model
// Returns: array of domain ShipListing objects
func (h *GetShipyardListingsHandler) convertShipListings(
	apiShips []domainPorts.ShipListingData,
) []shipyard.ShipListing {
	listings := make([]shipyard.ShipListing, len(apiShips))
	for i, ship := range apiShips {
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
	return listings
}

// extractShipTypeStrings extracts ship type names from API structures
// Returns: array of ship type strings
func (h *GetShipyardListingsHandler) extractShipTypeStrings(
	apiShipTypes []domainPorts.ShipTypeInfo,
) []string {
	shipTypes := make([]string, len(apiShipTypes))
	for i, st := range apiShipTypes {
		shipTypes[i] = st.Type
	}
	return shipTypes
}

// buildShipyardDomain constructs the domain Shipyard entity from API data
// Returns: shipyard domain object, error
func (h *GetShipyardListingsHandler) buildShipyardDomain(
	shipyardData *domainPorts.ShipyardData,
	shipListings []shipyard.ShipListing,
	shipTypes []string,
) (shipyard.Shipyard, error) {
	return shipyard.Shipyard{
		Symbol:          shipyardData.Symbol,
		ShipTypes:       shipTypes,
		Listings:        shipListings,
		Transactions:    shipyardData.Transactions,
		ModificationFee: shipyardData.ModificationFee,
	}, nil
}
