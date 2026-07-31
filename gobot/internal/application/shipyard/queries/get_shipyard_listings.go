package queries

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
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

// yardPriceReader is the one verb this query needs from the fleet's shipyard
// scanner: a METERED live shipyard read. *ship.ShipyardScanner satisfies it.
//
// Narrowed to one method rather than taking the scanner concretely so this
// package keeps no dependency on the application/ship package's other surface,
// following the same idiom as parkedsensing's shipyardScanAPI.
type yardPriceReader interface {
	ReadShipyard(ctx context.Context, playerID uint, waypointSymbol string, class marketscan.Class) (*domainPorts.ShipyardData, error)
	NoteTarget(waypoint string)
}

// GetShipyardListingsHandler handles the GetShipyardListings query.
//
// EVERY CONSUMER OF THIS QUERY IS A PRE-COMMIT PRICE READ. The fleet autosizer's
// price guard, the bootstrap capital gate, the probe-purchase treasury floor, the
// frontier probe buy, the batch purchase path and purchase_ship's own
// pre-buy verification all reach a shipyard exclusively through here, and every
// one of them is documented fail-closed — an unreadable price must stop a spend,
// never be replaced by a remembered one. That is why the read is classed Earning
// (metered, never denied) rather than routed through the deniable scan path: a
// cached hull price is not something a money guard can be satisfied with
// (RULINGS #4).
type GetShipyardListingsHandler struct {
	yards      yardPriceReader
	playerRepo player.PlayerRepository
}

// NewGetShipyardListingsHandler creates a new GetShipyardListingsHandler.
//
// It takes the scanner rather than the API client on purpose: there is no longer a
// way to reach GET /shipyard from this query without drawing on the fleet's one
// shipyard-read allowance (sp-mb0er).
func NewGetShipyardListingsHandler(
	yards yardPriceReader,
	playerRepo player.PlayerRepository,
) *GetShipyardListingsHandler {
	return &GetShipyardListingsHandler{
		yards:      yards,
		playerRepo: playerRepo,
	}
}

// Handle executes the GetShipyardListings query
func (h *GetShipyardListingsHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*GetShipyardListingsQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	if h.yards == nil {
		return nil, fmt.Errorf("shipyard listings unavailable: no scanner wired")
	}

	// The fleet is buying at this counter, not merely shopping — the strongest
	// demand signal the budget has. Recorded before the read so a yard we price
	// stays warm in the rotation between guard reads rather than decaying back to
	// the baseline the moment the buy loop looks away.
	h.yards.NoteTarget(query.WaypointSymbol)

	shipyardData, err := h.yards.ReadShipyard(ctx, uint(query.PlayerID.Value()), query.WaypointSymbol, marketscan.Earning)
	if err != nil && shipyardData == nil {
		return nil, fmt.Errorf("failed to get shipyard: %w", err)
	}
	if shipyardData == nil {
		// Unreachable for an Earning read, which the budget never declines. Kept as
		// a hard stop rather than a nil deref so that if the class ever changed the
		// failure would be a refusal to price, not a panic or a silent zero.
		return nil, fmt.Errorf("shipyard listings for %s were not read live", query.WaypointSymbol)
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
