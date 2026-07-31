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

	// Class is why this read is being made, and it is the ONE thing that can
	// exempt it from the shipyard-read budget (sp-lr27k).
	//
	// THE ZERO VALUE IS Discretionary, which is the fail-safe direction and the
	// reason this is a field rather than a constructor argument. An unstamped
	// query — a call site added tomorrow, or one nobody remembered to classify —
	// becomes DENIABLE: trait-filtered, floored by the rescan window, and paceable
	// by the allowance. The failure mode of forgetting is "this read is paced",
	// never "this read is unmetered", exactly as marketscan.Class documents at
	// its own declaration.
	//
	// Stamp Earning ONLY when a fail-closed money guard consumes the result before
	// a spend commits. That read is metered but never denied (RULINGS #4): a
	// cached hull price is not something a money guard can be satisfied with, and
	// serving one from store would convert a live guard into a stale one.
	Class marketscan.Class
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
// NOT EVERY CONSUMER OF THIS QUERY IS A PRE-COMMIT PRICE READ, and this handler
// used to assert that they all were — hardcoding the read at Earning on that
// basis (sp-lr27k). Earning is "metered but never denied", so a read classed
// there skips the cached SHIPYARD trait filter, skips the rescan-window floor
// and cannot be declined by the allowance. Every discovery read that reached
// here therefore bypassed the budget entirely, which is how shipyard reads came
// to run at 3.2x their configured allowance while the market budget held.
//
// The class now comes from the CALLER, because only the caller knows whether a
// money guard is about to consume the answer. The genuine pre-commit paths —
// the fleet autosizer's price guard, the bootstrap capital gate, purchase_ship's
// own pre-buy verification, the batch purchase path, the parked-sensing probe
// queue's working-capital floor and the expansion relay's post-arrival dock
// re-check — stamp Earning explicitly and are UNCHANGED: still metered, still
// never denied (RULINGS #4). Everything else takes the Discretionary zero value
// and is paced like any other scan.
//
// A DECLINED READ IS A REFUSAL TO PRICE, NOT A STALE PRICE. When the budget
// serves a deniable read from store the scanner returns (nil, nil) and the nil
// check below turns it into an error, so a caller either gets a LIVE price or
// gets nothing. No path was given a remembered price to spend against, which is
// what makes this change strictly stricter than what it replaces.
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
	//
	// GATED ON Earning, and that gate is what breaks a compounding loop rather
	// than merely tidying a signal. Unconditionally, every discovery read declared
	// "the fleet is buying HERE", which raised that yard's demand weight, which
	// shortened its interval, which kept it hot in the rotation — sensing
	// inflating the very signal the budget uses to allocate attention. Only a read
	// a money guard is about to consume is real buy intent.
	if query.Class == marketscan.Earning {
		h.yards.NoteTarget(query.WaypointSymbol)
	}

	shipyardData, err := h.yards.ReadShipyard(ctx, uint(query.PlayerID.Value()), query.WaypointSymbol, query.Class)
	if err != nil && shipyardData == nil {
		return nil, fmt.Errorf("failed to get shipyard: %w", err)
	}
	if shipyardData == nil {
		// The budget served this yard from store, or the trait cache says the
		// waypoint holds no shipyard. Unreachable for an Earning read, which is
		// never declined. For a deniable one this is the load-bearing stop: the
		// caller is handed an ERROR rather than a remembered price, so a declined
		// read can only ever prevent a spend, never authorise one on stale data.
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
