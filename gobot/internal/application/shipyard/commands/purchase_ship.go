package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainShipyard "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// PurchaseShipCommand is a command to purchase a ship from a shipyard
//
// The purchasing ship will:
// 1. Auto-discover nearest shipyard that sells the desired ship type (if not specified)
// 2. Navigate to the shipyard waypoint if not already there
// 3. Dock if in orbit
// 4. Purchase the specified ship type
// 5. Return the new ship entity
type PurchaseShipCommand struct {
	PurchasingShipSymbol string
	ShipType             string
	PlayerID             shared.PlayerID
	ShipyardWaypoint     string // Optional - will auto-discover if empty
	// OperationType is the ledger operation_type this purchase is booked under.
	// Empty means OperationTypeFleetExpansion, which is what every caller that
	// genuinely IS growing the fleet leaves it as.
	//
	// It exists because two different engines buy probes and one ledger label
	// covered both: the frontier expansion engine, and the parked-sensing
	// coordinator buying coverage for markets it already watches. With both
	// stamped "fleet expansion", an operator who switched expansion off and then
	// checked the ledger saw "fleet expansion" still spending — and could only
	// conclude the switch was broken. It was not; the label was (sp-com1h). One
	// engine per operation_type is what makes that question answerable in SQL.
	OperationType string
}

// shipyardPurchaseContainer labels the ferry's spend. A purchase is not a container: nothing
// here is long-lived and there is no running context to name, so a per-purchase identifier
// would put an invocation where every other writer puts an operating context — splitting one
// question ("what do shipyard purchases cost to fly?") across a row per purchase, and meaning
// something different from the same column elsewhere. Correlating a single ferry back to its
// purchase stays possible from the hull and the timestamp already on the row.
const shipyardPurchaseContainer = "shipyard-purchase"

// OperationTypeFleetExpansion is the ledger operation_type for a purchase that
// grows the fleet: the frontier expansion engine's probes, the autosizer's
// haulers and explorers, bootstrap's contract hulls, an operator's manual buy.
//
// It is the DEFAULT rather than one option among several, because it is what
// every ship purchase meant before any engine needed to be told apart. A caller
// that wants its spend attributable on its own sets PurchaseShipCommand.
// OperationType; everyone else is fleet expansion and stays so.
const OperationTypeFleetExpansion = "fleet expansion"

// PurchaseShipResponse contains the newly purchased ship
type PurchaseShipResponse struct {
	Ship          *navigation.Ship
	PurchasePrice int
	AgentCredits  int
	// ShipType is the authoritative type actually purchased, echoed by the
	// shipyard transaction. Batch orchestration verifies this against the
	// requested type as a money-integrity floor so a yard can never silently
	// substitute a different in-stock ship for the one asked for (sp-e7je).
	ShipType        string
	TransactionTime string
}

// shipyardCandidate represents a potential shipyard with its distance from current location
type shipyardCandidate struct {
	waypoint string
	distance float64
}

// yardCatalogReader is what this command needs from the fleet's shipyard scanner
// to shop for a hull without bypassing the shipyard-read budget: a metered live
// read, the persisted catalogue behind it, and the demand signal that tells the
// budget which yards are worth keeping priced. *ship.ShipyardScanner satisfies it.
type yardCatalogReader interface {
	ReadShipyard(ctx context.Context, playerID uint, waypointSymbol string, class marketscan.Class) (*domainPorts.ShipyardData, error)
	OffersFor(ctx context.Context, playerID int, shipType string) ([]domainShipyard.ShipTypeAvailability, error)
	NoteDemand(shipType string)
}

// PurchaseShipHandler handles the PurchaseShip command
type PurchaseShipHandler struct {
	shipRepo         navigation.ShipRepository
	playerRepo       player.PlayerRepository
	waypointRepo     system.WaypointRepository
	waypointProvider system.IWaypointProvider
	apiClient        domainPorts.APIClient
	mediator         common.Mediator
	yards            yardCatalogReader
}

// NewPurchaseShipHandler creates a new PurchaseShipHandler. yards is the metered
// shipyard reader the type search shops through; without it the handler can still
// buy at an explicitly named yard but cannot discover one, which is the correct
// failure — the alternative was an unmetered live read per yard in the system.
func NewPurchaseShipHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	waypointRepo system.WaypointRepository,
	waypointProvider system.IWaypointProvider,
	apiClient domainPorts.APIClient,
	mediator common.Mediator,
	yards yardCatalogReader,
) *PurchaseShipHandler {
	return &PurchaseShipHandler{
		shipRepo:         shipRepo,
		playerRepo:       playerRepo,
		waypointRepo:     waypointRepo,
		waypointProvider: waypointProvider,
		apiClient:        apiClient,
		mediator:         mediator,
		yards:            yards,
	}
}

// Handle executes the PurchaseShip command
func (h *PurchaseShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*PurchaseShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return nil, err
	}

	purchasingShip, err := h.loadPurchasingShip(ctx, cmd)
	if err != nil {
		return nil, err
	}

	shipyardWaypoint, err := h.resolveShipyardWaypoint(ctx, cmd, purchasingShip, token)
	if err != nil {
		return nil, err
	}

	purchasingShip, err = h.prepareShipForPurchase(ctx, cmd, shipyardWaypoint, purchasingShip)
	if err != nil {
		return nil, err
	}

	purchasePrice, systemSymbol, err := h.validateAndGetShipPrice(ctx, cmd, shipyardWaypoint)
	if err != nil {
		return nil, err
	}

	balanceBefore, err := h.ensureSufficientCredits(ctx, token, purchasePrice)
	if err != nil {
		return nil, err
	}

	purchaseResult, err := h.apiClient.PurchaseShip(ctx, cmd.ShipType, shipyardWaypoint, token)
	if err != nil {
		return nil, fmt.Errorf("failed to purchase ship: %w", err)
	}

	// Update player credits in database and return updated credits after successful purchase
	updatedPlayer, err := h.updatePlayerCredits(ctx, cmd.PlayerID, purchaseResult.Agent.Credits)
	if err != nil {
		return nil, fmt.Errorf("failed to update player credits: %w", err)
	}

	// Sync the updated player back to API client for subsequent operations
	// This ensures GetAgent() returns updated credits in batch operations
	h.syncPlayerToAPIClient(updatedPlayer)

	newShip, err := h.convertShipDataToEntity(ctx, purchaseResult.Ship, cmd.PlayerID, shipyardWaypoint, systemSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to convert ship data: %w", err)
	}

	// Create idle ship assignment for newly purchased ship
	if err := h.createIdleAssignment(ctx, newShip); err != nil {
		return nil, fmt.Errorf("failed to create ship assignment: %w", err)
	}

	// Auto-heal the cache the moment the ship exists: force GET /my/ships so a
	// freshly purchased ship never lingers with an empty Role (invisible to
	// role-based coordinators) or phantom cargo/nav state (cluster lesson L50).
	h.refreshPurchasedShip(ctx, newShip.ShipSymbol(), cmd.PlayerID)

	// Record transaction synchronously to ensure it's saved
	h.recordShipPurchaseTransaction(ctx, cmd, shipyardWaypoint, purchaseResult, balanceBefore)

	return &PurchaseShipResponse{
		Ship:            newShip,
		PurchasePrice:   purchaseResult.Transaction.Price,
		AgentCredits:    purchaseResult.Agent.Credits,
		ShipType:        purchaseResult.Transaction.ShipType,
		TransactionTime: purchaseResult.Transaction.Timestamp,
	}, nil
}

// loadPurchasingShip fetches the ship that will make the purchase
// Returns: purchasing ship entity, error
func (h *PurchaseShipHandler) loadPurchasingShip(
	ctx context.Context,
	cmd *PurchaseShipCommand,
) (*navigation.Ship, error) {
	purchasingShip, err := h.shipRepo.FindBySymbol(ctx, cmd.PurchasingShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("purchasing ship not found: %w", err)
	}
	return purchasingShip, nil
}

// resolveShipyardWaypoint determines the target shipyard (provided or auto-discovered)
// Returns: shipyard waypoint symbol, error
func (h *PurchaseShipHandler) resolveShipyardWaypoint(
	ctx context.Context,
	cmd *PurchaseShipCommand,
	purchasingShip *navigation.Ship,
	token string,
) (string, error) {
	if cmd.ShipyardWaypoint != "" {
		return cmd.ShipyardWaypoint, nil
	}

	discoveredWaypoint, err := h.discoverNearestShipyard(ctx, purchasingShip, cmd.ShipType, token)
	if err != nil {
		return "", fmt.Errorf("failed to discover shipyard: %w", err)
	}
	return discoveredWaypoint, nil
}

// prepareShipForPurchase ensures ship is at shipyard and docked
// Combines navigation and docking steps
// Returns: prepared ship (reloaded after movements), error
func (h *PurchaseShipHandler) prepareShipForPurchase(
	ctx context.Context,
	cmd *PurchaseShipCommand,
	shipyardWaypoint string,
	purchasingShip *navigation.Ship,
) (*navigation.Ship, error) {
	var err error
	purchasingShip, err = h.navigateToShipyard(ctx, cmd, shipyardWaypoint, purchasingShip)
	if err != nil {
		return nil, err
	}

	purchasingShip, err = h.dockShipIfNeeded(ctx, cmd, purchasingShip)
	if err != nil {
		return nil, err
	}

	return purchasingShip, nil
}

// navigateToShipyard moves ship to shipyard waypoint if not already there
// Returns: reloaded ship after navigation, error
func (h *PurchaseShipHandler) navigateToShipyard(
	ctx context.Context,
	cmd *PurchaseShipCommand,
	shipyardWaypoint string,
	purchasingShip *navigation.Ship,
) (*navigation.Ship, error) {
	if purchasingShip.CurrentLocation().Symbol == shipyardWaypoint {
		return purchasingShip, nil
	}

	// Stamp the operation before the hull moves. The flight burns fuel, and the refuel that
	// pays for it reads its attribution off this context: unstamped, a continuous cost of
	// buying ships books under the unpropagated else-branch, which is where spend goes to
	// become permanently unassignable rather than a category anyone chose.
	//
	// The label is the one the caller already declared for the purchase itself, so the flight
	// and the hull it fetches land under the same name instead of the ledger splitting one
	// decision across two operations. A synthetic container id keeps the pair complete —
	// the readers ignore a context missing either half.
	ctx = shared.WithOperationContext(ctx, shared.NewOperationContext(
		shipyardPurchaseContainer,
		purchaseOperationType(cmd.OperationType),
	))

	navCmd := &shipNav.NavigateRouteCommand{
		ShipSymbol:  cmd.PurchasingShipSymbol,
		Destination: shipyardWaypoint,
		PlayerID:    cmd.PlayerID,
	}
	_, err := h.mediator.Send(ctx, navCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate to shipyard: %w", err)
	}

	purchasingShip, err = h.shipRepo.FindBySymbol(ctx, cmd.PurchasingShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship after navigation: %w", err)
	}

	return purchasingShip, nil
}

// dockShipIfNeeded docks the ship if currently in orbit
// Returns: reloaded ship after docking, error
func (h *PurchaseShipHandler) dockShipIfNeeded(
	ctx context.Context,
	cmd *PurchaseShipCommand,
	purchasingShip *navigation.Ship,
) (*navigation.Ship, error) {
	if purchasingShip.NavStatus() != navigation.NavStatusInOrbit {
		return purchasingShip, nil
	}

	dockCmd := &shipTypes.DockShipCommand{
		Ship:     purchasingShip,
		PlayerID: cmd.PlayerID,
	}
	_, err := h.mediator.Send(ctx, dockCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to dock ship: %w", err)
	}

	purchasingShip, err = h.shipRepo.FindBySymbol(ctx, cmd.PurchasingShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship after docking: %w", err)
	}

	return purchasingShip, nil
}

// validateAndGetShipPrice gets shipyard listings and validates ship availability
// Returns: purchase price for ship type, system symbol, error
func (h *PurchaseShipHandler) validateAndGetShipPrice(
	ctx context.Context,
	cmd *PurchaseShipCommand,
	shipyardWaypoint string,
) (int, string, error) {
	systemSymbol := shared.ExtractSystemSymbol(shipyardWaypoint)

	query := &queries.GetShipyardListingsQuery{
		SystemSymbol:   systemSymbol,
		WaypointSymbol: shipyardWaypoint,
		PlayerID:       cmd.PlayerID,
		// Pre-buy verification: this price is checked immediately before the
		// purchase commits, so it must be live and undeniable (RULINGS #4).
		Class: marketscan.Earning,
	}
	shipyardResp, err := h.mediator.Send(ctx, query)
	if err != nil {
		return 0, "", fmt.Errorf("failed to get shipyard listings: %w", err)
	}

	shipyardListings, ok := shipyardResp.(*queries.GetShipyardListingsResponse)
	if !ok {
		return 0, "", fmt.Errorf("invalid response type from GetShipyardListings")
	}

	listing, found := shipyardListings.Shipyard.FindListingByType(cmd.ShipType)
	if !found {
		return 0, "", fmt.Errorf("ship type %s not available at shipyard %s", cmd.ShipType, shipyardWaypoint)
	}

	return listing.PurchasePrice, systemSymbol, nil
}

// ensureSufficientCredits validates player has enough credits for purchase
// Returns: agent credits after validation, error
func (h *PurchaseShipHandler) ensureSufficientCredits(
	ctx context.Context,
	token string,
	purchasePrice int,
) (int, error) {
	agentData, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		return 0, fmt.Errorf("failed to get agent data: %w", err)
	}

	if agentData.Credits < purchasePrice {
		return 0, fmt.Errorf("insufficient credits: have %d, need %d", agentData.Credits, purchasePrice)
	}

	return agentData.Credits, nil
}

// updatePlayerCredits updates player's credits in the database
func (h *PurchaseShipHandler) updatePlayerCredits(
	ctx context.Context,
	playerID shared.PlayerID,
	newCredits int,
) (*player.Player, error) {
	// Load player from database
	p, err := h.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find player: %w", err)
	}

	// Update credits
	p.Credits = newCredits

	// Persist to database
	if err := h.playerRepo.Add(ctx, p); err != nil {
		return nil, fmt.Errorf("failed to persist player: %w", err)
	}

	return p, nil
}

// syncPlayerToAPIClient syncs player data to API client (for test mocks)
// This ensures mock GetAgent() calls return updated player data
func (h *PurchaseShipHandler) syncPlayerToAPIClient(p *player.Player) {
	// Type assert to check if we're using MockAPIClient (test environment)
	type playerUpdater interface {
		UpdatePlayer(*player.Player)
	}

	if mockClient, ok := h.apiClient.(playerUpdater); ok {
		mockClient.UpdatePlayer(p)
	}
}

// recordShipPurchaseTransaction records the ship purchase transaction in the ledger
func (h *PurchaseShipHandler) recordShipPurchaseTransaction(
	ctx context.Context,
	cmd *PurchaseShipCommand,
	shipyardWaypoint string,
	purchaseResult *domainPorts.ShipPurchaseResult,
	balanceBefore int,
) {
	logger := logging.LoggerFromContext(ctx)

	// balanceBefore comes from the pre-purchase GetAgent; balanceAfter is the
	// agent's credits as reported in-band by the purchase response, which is
	// the authoritative post-transaction balance the ledger anchors on.
	authoritativeBalance := &purchaseResult.Agent.Credits
	balanceAfter := purchaseResult.Agent.Credits

	// Fetch player to get agent symbol
	playerData, err := h.playerRepo.FindByID(ctx, cmd.PlayerID)
	agentSymbol := "UNKNOWN"
	if err == nil && playerData != nil {
		agentSymbol = playerData.AgentSymbol
	}

	// The hull this purchase produced, taken from the created ship in the purchase
	// response — the ONLY place the real symbol appears. The shipyard transaction's
	// own `shipSymbol` field is the API's deprecated alias for the ship TYPE
	// ("SHIP_HEAVY_FREIGHTER"), so reading it here is what left every historical
	// PURCHASE_SHIP row unjoinable to the hull it bought. Empty only if
	// the response carried no ship, in which case the row stays unattributed
	// rather than re-stamping a type into a field named ship_symbol.
	hullSymbol := ""
	if purchaseResult.Ship != nil {
		hullSymbol = purchaseResult.Ship.Symbol
	}

	// Build metadata
	metadata := map[string]interface{}{
		"agent":          agentSymbol,
		"ship_type":      cmd.ShipType,
		"ship_symbol":    hullSymbol,
		"waypoint":       shipyardWaypoint,
		"transaction_id": hullSymbol, // Use ship symbol as transaction reference
	}

	// The server's own purchase instant, so hull age is read from the receipt
	// instead of inferred from the hull's first trade (which understates it by the
	// purchase-to-first-tour gap). Omitted entirely when unparseable so a consumer
	// casting this key can never trip over a malformed value.
	if purchasedAt, ok := normalisePurchaseTimestamp(purchaseResult.Transaction.Timestamp); ok {
		metadata["purchased_at"] = purchasedAt
	}

	// Create record transaction command
	recordCmd := &ledgerCommands.RecordTransactionCommand{
		PlayerID:             cmd.PlayerID.Value(),
		TransactionType:      "PURCHASE_SHIP",
		Amount:               -purchaseResult.Transaction.Price, // Negative for expense
		BalanceBefore:        balanceBefore,
		BalanceAfter:         balanceAfter,
		AuthoritativeBalance: authoritativeBalance,
		Description:          fmt.Sprintf("Purchased %s ship at %s", cmd.ShipType, shipyardWaypoint),
		Metadata:             metadata,
		// The buying engine names itself, or the row is fleet expansion. See
		// PurchaseShipCommand.OperationType.
		OperationType: purchaseOperationType(cmd.OperationType),
	}

	// First-class entity link so capex joins to the hull on an indexed column
	// rather than a JSON key. Set as a pair or not at all — a "ship" link with no
	// id is worse than an absent one.
	if hullSymbol != "" {
		recordCmd.RelatedEntityType = "ship"
		recordCmd.RelatedEntityID = hullSymbol
	}

	// Record transaction via mediator (use passed context, not Background)
	_, err = h.mediator.Send(ctx, recordCmd)
	if err != nil {
		// Log error but don't fail the operation
		logger.Log("ERROR", "Failed to record ship purchase transaction in ledger", map[string]interface{}{
			"error":     err.Error(),
			"ship_type": cmd.ShipType,
			"price":     purchaseResult.Transaction.Price,
			"player_id": cmd.PlayerID.Value(),
		})
	} else {
		logger.Log("DEBUG", "Ship purchase transaction recorded in ledger", map[string]interface{}{
			"ship_type": cmd.ShipType,
			"price":     purchaseResult.Transaction.Price,
		})
	}
}

// normalisePurchaseTimestamp re-formats the shipyard transaction's timestamp as
// canonical RFC3339 UTC, reporting false when the API sent nothing usable.
// Normalising here (rather than passing the raw string through) is what lets a
// consumer cast metadata->>'purchased_at' unguarded: the key is either absent or
// a valid timestamp, never an empty or malformed string.
func normalisePurchaseTimestamp(raw string) (string, bool) {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return "", false
	}
	return parsed.UTC().Format(time.RFC3339), true
}

// purchaseOperationType resolves the ledger operation_type for a purchase,
// falling back to fleet expansion when the caller named none.
//
// Whitespace-only is treated as unset rather than written through: operation_type
// is a grouping key, and a row filed under " " would form its own silent bucket
// that no operator's query would ever name.
func purchaseOperationType(requested string) string {
	if trimmed := strings.TrimSpace(requested); trimmed != "" {
		return trimmed
	}
	return OperationTypeFleetExpansion
}

// discoverNearestShipyard discovers the nearest shipyard that sells the desired ship type
func (h *PurchaseShipHandler) discoverNearestShipyard(
	ctx context.Context,
	purchasingShip *navigation.Ship,
	shipType string,
	token string,
) (string, error) {
	systemSymbol := purchasingShip.CurrentLocation().SystemSymbol

	shipyardWaypoints, err := h.getShipyardWaypoints(ctx, systemSymbol)
	if err != nil {
		return "", err
	}

	candidates, err := h.filterShipyardsBySupportedType(
		ctx, shipyardWaypoints, shipType, purchasingShip.PlayerID(), purchasingShip.CurrentLocation(),
	)
	if err != nil {
		return "", err
	}

	if len(candidates) == 0 {
		return "", fmt.Errorf("no shipyards in system %s sell %s", systemSymbol, shipType)
	}

	return h.findNearestShipyard(candidates), nil
}

// getShipyardWaypoints fetches all waypoints in system with SHIPYARD trait
// Returns: waypoint array, error
func (h *PurchaseShipHandler) getShipyardWaypoints(
	ctx context.Context,
	systemSymbol string,
) ([]*shared.Waypoint, error) {
	shipyardWaypoints, err := h.waypointRepo.ListBySystemWithTrait(ctx, systemSymbol, "SHIPYARD")
	if err != nil {
		return nil, fmt.Errorf("failed to find shipyards: %w", err)
	}

	if len(shipyardWaypoints) == 0 {
		return nil, fmt.Errorf("no shipyards found in system %s", systemSymbol)
	}

	return shipyardWaypoints, nil
}

// filterShipyardsBySupportedType finds shipyards that sell the desired ship type.
//
// IT IS STORE-FIRST, and that is the sp-mb0er change. This used to issue one live
// GET /shipyard PER SHIPYARD IN THE SYSTEM on every discovery, uncached and
// unmetered — a burst that scaled with how many yards a system happened to have,
// and one of the four paths that put shipyard reads at 44.7% of the server
// ceiling. One store query now answers the whole system, and only yards the store
// has never heard of cost a request — a request the budget may decline, in which
// case the yard drops out of this tick's candidates and is picked up by the
// rotation rather than being bought at blind.
//
// Returns: array of shipyard candidates with distances
func (h *PurchaseShipHandler) filterShipyardsBySupportedType(
	ctx context.Context,
	waypoints []*shared.Waypoint,
	shipType string,
	playerID shared.PlayerID,
	currentLocation *shared.Waypoint,
) ([]shipyardCandidate, error) {
	// Searching for a hull type IS the demand signal: every yard known to sell it
	// rises in the budget's rotation, so the counters we are about to shop at are
	// the ones kept priced.
	if h.yards != nil {
		h.yards.NoteDemand(shipType)
	}

	stored := h.storedYardsSelling(ctx, playerID, shipType)

	var validShipyards []shipyardCandidate
	for _, waypoint := range waypoints {
		sells, ok := stored[waypoint.Symbol]
		if !ok {
			// The store has never seen this yard sell anything of the sort. Ask for a
			// live look; a declined or failed read means "not a candidate this tick",
			// never "does not sell it".
			var err error
			sells, err = h.doesShipyardSellType(ctx, playerID, waypoint, shipType)
			if err != nil {
				continue
			}
		}
		if !sells {
			continue
		}
		validShipyards = append(validShipyards, shipyardCandidate{
			waypoint: waypoint.Symbol,
			distance: currentLocation.DistanceTo(waypoint),
		})
	}

	return validShipyards, nil
}

// storedYardsSelling is the persisted answer to "which yards sell this type",
// keyed by waypoint. A yard ABSENT from the map is unknown rather than negative —
// the store records only what a scan has seen — so absence sends the caller to a
// metered live read rather than silently excluding the yard.
func (h *PurchaseShipHandler) storedYardsSelling(ctx context.Context, playerID shared.PlayerID, shipType string) map[string]bool {
	if h.yards == nil {
		return nil
	}
	rows, err := h.yards.OffersFor(ctx, playerID.Value(), shipType)
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.WaypointSymbol != "" {
			out[row.WaypointSymbol] = true
		}
	}
	return out
}

// doesShipyardSellType checks if a specific shipyard sells the ship type, through
// the fleet's metered shipyard reader.
//
// It asks the CATALOGUE question, not the price question, so it is Discretionary:
// a declined read costs the fleet a candidate this tick, never a bad spend. The
// pre-buy price verification that actually guards the purchase is a separate,
// Earning-class read (validateAndGetShipPrice, via GetShipyardListings) and is
// never declined.
//
// Returns: true if shipyard supports type, false otherwise, error
func (h *PurchaseShipHandler) doesShipyardSellType(
	ctx context.Context,
	playerID shared.PlayerID,
	waypoint *shared.Waypoint,
	shipType string,
) (bool, error) {
	if h.yards == nil {
		return false, fmt.Errorf("shipyard catalogue unavailable: no scanner wired")
	}
	shipyardData, err := h.yards.ReadShipyard(ctx, uint(playerID.Value()), waypoint.Symbol, marketscan.Discretionary)
	if err != nil {
		return false, err
	}
	if shipyardData == nil {
		// Served from store, and the store had nothing for this yard — otherwise the
		// caller would not have asked. Not a candidate this tick.
		return false, nil
	}

	for _, shipTypeInfo := range shipyardData.ShipTypes {
		if shipTypeInfo.Type == shipType {
			return true, nil
		}
	}

	return false, nil
}

// findNearestShipyard selects the closest shipyard from candidates
// Returns: waypoint symbol of nearest shipyard
func (h *PurchaseShipHandler) findNearestShipyard(
	candidates []shipyardCandidate,
) string {
	nearest := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.distance < nearest.distance {
			nearest = candidate
		}
	}
	return nearest.waypoint
}

// convertShipDataToEntity converts API ship data to domain entity
func (h *PurchaseShipHandler) convertShipDataToEntity(
	ctx context.Context,
	shipData *navigation.ShipData,
	playerID shared.PlayerID,
	waypointSymbol string,
	systemSymbol string,
) (*navigation.Ship, error) {
	waypoint, err := h.getWaypointDetails(ctx, waypointSymbol, systemSymbol, playerID)
	if err != nil {
		return nil, err
	}

	cargoItems, err := h.convertInventoryItems(shipData.Cargo.Inventory)
	if err != nil {
		return nil, err
	}

	cargo, fuel, navStatus, err := h.createShipValueObjects(shipData, cargoItems)
	if err != nil {
		return nil, err
	}

	// Convert modules
	var modules []*navigation.ShipModule
	for _, mod := range shipData.Modules {
		requirements := navigation.NewShipRequirements(mod.Requirements.Power, mod.Requirements.Crew, mod.Requirements.Slots)
		module := navigation.NewShipModule(mod.Symbol, mod.Capacity, mod.Range, requirements)
		modules = append(modules, module)
	}

	ship, err := navigation.NewShip(
		shipData.Symbol,
		playerID,
		waypoint,
		fuel,
		shipData.FuelCapacity,
		shipData.CargoCapacity,
		cargo,
		shipData.EngineSpeed,
		shipData.FrameSymbol,
		shipData.Role,
		modules,
		navStatus,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create ship: %w", err)
	}

	// Enrich with power/slot/crew data (sp-el60) from the fresh API payload so
	// a newly purchased hull is immediately outfit-feasibility computable
	// rather than waiting on the next full sync.
	mounts := make([]*navigation.ShipMount, len(shipData.Mounts))
	for i, mnt := range shipData.Mounts {
		mountRequirements := navigation.NewShipRequirements(mnt.Requirements.Power, mnt.Requirements.Crew, mnt.Requirements.Slots)
		mounts[i] = navigation.NewShipMount(mnt.Symbol, mnt.Name, mnt.Strength, mnt.Deposits, mountRequirements)
	}
	ship.SetMounts(mounts)
	ship.SetSlots(shipData.ModuleSlots, shipData.MountingPoints)
	reactorRequirements := navigation.NewShipRequirements(
		shipData.ReactorRequirements.Power,
		shipData.ReactorRequirements.Crew,
		shipData.ReactorRequirements.Slots,
	)
	ship.SetReactor(shipData.ReactorSymbol, shipData.ReactorName, shipData.ReactorPowerOutput, reactorRequirements)
	ship.SetCrew(shipData.CrewCurrent, shipData.CrewRequired, shipData.CrewCapacity)

	return ship, nil
}

// getWaypointDetails fetches waypoint data for ship's current location
// Returns: waypoint entity, error
func (h *PurchaseShipHandler) getWaypointDetails(
	ctx context.Context,
	waypointSymbol string,
	systemSymbol string,
	playerID shared.PlayerID,
) (*shared.Waypoint, error) {
	waypoint, err := h.waypointProvider.GetWaypoint(ctx, waypointSymbol, systemSymbol, playerID.Value())
	if err != nil {
		return nil, fmt.Errorf("failed to get waypoint %s: %w", waypointSymbol, err)
	}
	return waypoint, nil
}

// convertInventoryItems converts API cargo data to domain cargo items
// Returns: cargo item array, error
func (h *PurchaseShipHandler) convertInventoryItems(
	inventoryData []shared.CargoItem,
) ([]*shared.CargoItem, error) {
	var inventory []*shared.CargoItem
	for _, item := range inventoryData {
		cargoItem, err := shared.NewCargoItem(item.Symbol, item.Name, item.Description, item.Units)
		if err != nil {
			return nil, fmt.Errorf("failed to create cargo item: %w", err)
		}
		inventory = append(inventory, cargoItem)
	}
	return inventory, nil
}

// createShipValueObjects creates domain value objects from API data
// Returns: cargo, fuel, navStatus value objects, error
func (h *PurchaseShipHandler) createShipValueObjects(
	shipData *navigation.ShipData,
	cargoItems []*shared.CargoItem,
) (*shared.Cargo, *shared.Fuel, navigation.NavStatus, error) {
	cargo, err := shared.NewCargo(shipData.Cargo.Capacity, shipData.Cargo.Units, cargoItems)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to create cargo: %w", err)
	}

	// Authoritative API snapshot: clamp a transient current>capacity over-report
	// to capacity rather than reject, so a freshly-purchased hull isn't dropped
	// on ingest.
	fuel, err := shared.ReconstructFuel(shipData.FuelCurrent, shipData.FuelCapacity)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to create fuel: %w", err)
	}

	navStatus := navigation.NavStatus(shipData.NavStatus)

	return cargo, fuel, navStatus, nil
}

// refreshPurchasedShip forces an authoritative GET /my/ships for a freshly
// purchased ship, reconciling the daemon cache against the server the moment
// the ship exists. A brand-new ship can cache with an empty Role (invisible to
// role-based coordinators) or phantom cargo, and the stale entry never self-
// heals on its own. Best-effort by design: the ship is already bought and
// persisted, so a refresh failure must not fail the purchase — the next pool
// sync reconciles it. Logs the auto-refresh at INFO with the trigger.
func (h *PurchaseShipHandler) refreshPurchasedShip(
	ctx context.Context,
	shipSymbol string,
	playerID shared.PlayerID,
) {
	logger := logging.LoggerFromContext(ctx)

	if _, err := h.shipRepo.SyncShipFromAPI(ctx, shipSymbol, playerID); err != nil {
		logger.Log("WARN", "Post-purchase ship refresh failed (cache self-heals on next pool sync)", map[string]interface{}{
			"ship_symbol": shipSymbol,
			"trigger":     "post_purchase",
			"error":       err.Error(),
		})
		return
	}

	logger.Log("INFO", "Auto-refreshed ship state after purchase", map[string]interface{}{
		"ship_symbol": shipSymbol,
		"trigger":     "post_purchase",
	})
}

// createIdleAssignment creates an idle ship assignment for a newly purchased ship
// Uses the Ship aggregate's assignment management via ShipRepository
func (h *PurchaseShipHandler) createIdleAssignment(
	ctx context.Context,
	ship *navigation.Ship,
) error {
	// Persist the new ship's idle assignment under CAS-retry: the closure
	// sets the idle assignment on the FRESH row so a concurrent writer's cargo/nav
	// update on the just-synced hull survives instead of being last-write-wins
	// clobbered. New ships start without an assignment.
	if _, _, err := h.shipRepo.SaveWithRetry(ctx, ship.ShipSymbol(), ship.PlayerID(),
		func(sh *navigation.Ship) (bool, error) {
			sh.SetAssignment(navigation.NewIdleAssignment())
			return true, nil
		}); err != nil {
		return fmt.Errorf("failed to persist ship assignment: %w", err)
	}

	return nil
}
