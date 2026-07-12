package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// These tests pin the reconcile-tick latency cut for the synchronous buy path.
// A hauler buy used to fire three redundant reads INSIDE PurchaseShip that the
// caller (the batch budget pass / bootstrap PriceCheck+Observe) had already
// done: a second GetShipyard (validateAndGetShipPrice), a second GetAgent
// (the credit pre-check), and a post-purchase GET /my/ships resync even though
// the PurchaseShip response already carries the full ship. Threading the
// already-read price down (KnownPurchasePrice) skips the first two; persisting
// the response ship directly drops the third. The CLI single-buy / auto-discover
// path (no known price) must keep reading, so the fix never silently skips
// validation on a path the caller does not front-run.

// --- doubles -----------------------------------------------------------------

// purchaseFakeShipRepo embeds the domain interface so any unused method
// nil-panics. FindBySymbol returns a purchasing hull already docked AT the yard
// (so navigate + dock are no-ops), Save records the persisted ship, and
// SyncShipFromAPI FAILS the test if the dropped post-purchase resync ever fires.
type purchaseFakeShipRepo struct {
	navigation.ShipRepository

	purchasingShip *navigation.Ship
	saved          []*navigation.Ship
	syncCalled     int
	t              *testing.T
}

func (r *purchaseFakeShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	return r.purchasingShip, nil
}

func (r *purchaseFakeShipRepo) Save(_ context.Context, ship *navigation.Ship) error {
	r.saved = append(r.saved, ship)
	return nil
}

func (r *purchaseFakeShipRepo) SyncShipFromAPI(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	r.syncCalled++
	r.t.Errorf("post-purchase SyncShipFromAPI(%s) must not be called: the purchase response ship is persisted directly", symbol)
	return nil, nil
}

type purchaseFakePlayerRepo struct {
	player.PlayerRepository
	p *player.Player
}

func (r *purchaseFakePlayerRepo) FindByID(_ context.Context, _ shared.PlayerID) (*player.Player, error) {
	return r.p, nil
}
func (r *purchaseFakePlayerRepo) Add(_ context.Context, _ *player.Player) error { return nil }

type purchaseFakeWaypointProvider struct {
	system.IWaypointProvider
	wp *shared.Waypoint
}

func (p *purchaseFakeWaypointProvider) GetWaypoint(_ context.Context, _, _ string, _ int) (*shared.Waypoint, error) {
	return p.wp, nil
}

// purchaseFakeAPIClient counts the two reads the dedup eliminates. GetShipyard
// is reached only via the mediator's listings query (never called directly by
// the handler), so the GetShipyard dedup is asserted on the mediator; GetAgent
// is called straight from guardSufficientCredits, so its counter is the
// authoritative credit-read count. PurchaseShip returns the canned response.
type purchaseFakeAPIClient struct {
	domainPorts.APIClient

	getAgentCalls  int
	purchaseResult *domainPorts.ShipPurchaseResult
}

func (c *purchaseFakeAPIClient) PurchaseShip(_ context.Context, _, _, _ string) (*domainPorts.ShipPurchaseResult, error) {
	return c.purchaseResult, nil
}
func (c *purchaseFakeAPIClient) GetAgent(_ context.Context, _ string) (*player.AgentData, error) {
	c.getAgentCalls++
	return &player.AgentData{Credits: 999999999}, nil
}

// purchaseFakeMediator flags a shipyard-listings query (the GetShipyard read the
// dedup must eliminate) and otherwise no-ops (the RecordTransactionCommand the
// ledger fire-and-forgets).
type purchaseFakeMediator struct {
	common.Mediator
	sawShipyardQuery bool
}

func (m *purchaseFakeMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	if _, ok := request.(*queries.GetShipyardListingsQuery); ok {
		m.sawShipyardQuery = true
	}
	return nil, nil
}

// --- fixtures ----------------------------------------------------------------

func newPurchaseTestShip(t *testing.T, locationSymbol string, navStatus navigation.NavStatus) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint(locationSymbol, 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(400, 400)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		"BUYER-1", shared.MustNewPlayerID(1), loc, fuel, 400, 40, cargo, 30,
		"FRAME_FRIGATE", "COMMAND", nil, navStatus,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// newPurchaseResponseShipData models the full ship the PurchaseShip API returns
// in-band: crucially it carries a non-empty Role, the exact field the dropped
// resync existed to heal — proving the response is a sufficient source of truth.
func newPurchaseResponseShipData(symbol string) *navigation.ShipData {
	return &navigation.ShipData{
		Symbol:         symbol,
		NavStatus:      string(navigation.NavStatusDocked),
		FuelCurrent:    400,
		FuelCapacity:   400,
		CargoCapacity:  40,
		EngineSpeed:    30,
		FrameSymbol:    "FRAME_FRIGATE",
		Role:           "COMMAND",
		ModuleSlots:    3,
		MountingPoints: 3,
		Cargo:          &navigation.CargoData{Capacity: 40, Units: 0, Inventory: nil},
	}
}

// --- tests -------------------------------------------------------------------

// TestPurchaseShip_KnownPrice_SkipsDuplicateReadsAndPersistsFromResponse drives
// the whole buy through the driving port (Handle) with a caller-supplied
// KnownPurchasePrice — the INCOME hauler / batch case. It must NOT re-read the
// shipyard (mediator sees no listings query), NOT re-read the agent (GetAgent
// count 0), NOT fire the post-purchase resync (SyncShipFromAPI count 0), and
// must persist the ship built from the authoritative response (role and all).
func TestPurchaseShip_KnownPrice_SkipsDuplicateReadsAndPersistsFromResponse(t *testing.T) {
	const yard = "X1-TEST-A1"
	const knownPrice = 150000
	const postCredits = 350000

	shipRepo := &purchaseFakeShipRepo{
		purchasingShip: newPurchaseTestShip(t, yard, navigation.NavStatusDocked),
		t:              t,
	}
	apiClient := &purchaseFakeAPIClient{purchaseResult: &domainPorts.ShipPurchaseResult{
		Agent: &player.AgentData{Credits: postCredits},
		Ship:  newPurchaseResponseShipData("NEW-SHIP-1"),
		Transaction: &domainPorts.ShipPurchaseTransaction{
			ShipSymbol: "NEW-SHIP-1", ShipType: "SHIP_LIGHT_HAULER", Price: knownPrice,
			Timestamp: "2026-07-12T00:00:00Z",
		},
	}}
	med := &purchaseFakeMediator{}
	yardWp, _ := shared.NewWaypoint(yard, 0, 0)
	handler := &PurchaseShipHandler{
		shipRepo:         shipRepo,
		playerRepo:       &purchaseFakePlayerRepo{p: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT-1", "token")},
		waypointProvider: &purchaseFakeWaypointProvider{wp: yardWp},
		apiClient:        apiClient,
		mediator:         med,
	}

	ctx := common.WithPlayerToken(context.Background(), "token")
	resp, err := handler.Handle(ctx, &PurchaseShipCommand{
		PurchasingShipSymbol: "BUYER-1",
		ShipType:             "SHIP_LIGHT_HAULER",
		PlayerID:             shared.MustNewPlayerID(1),
		ShipyardWaypoint:     yard,
		KnownPurchasePrice:   knownPrice,
	})
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	// The three eliminated reads.
	if med.sawShipyardQuery {
		t.Error("PurchaseShip re-read the shipyard (GetShipyardListingsQuery) despite a caller-supplied KnownPurchasePrice — the duplicate GetShipyard was not eliminated")
	}
	if apiClient.getAgentCalls != 0 {
		t.Errorf("PurchaseShip called GetAgent %d times despite a known price — the duplicate credit read was not eliminated", apiClient.getAgentCalls)
	}
	if shipRepo.syncCalled != 0 {
		t.Errorf("post-purchase SyncShipFromAPI fired %d times — the response ship must be persisted directly", shipRepo.syncCalled)
	}

	// The ship is persisted from the response, with its role intact.
	if len(shipRepo.saved) != 1 {
		t.Fatalf("expected the purchased ship persisted exactly once (from the response), got %d saves", len(shipRepo.saved))
	}
	if got := shipRepo.saved[0].ShipSymbol(); got != "NEW-SHIP-1" {
		t.Errorf("persisted ship = %q, want the response ship NEW-SHIP-1", got)
	}
	if got := shipRepo.saved[0].Role(); got != "COMMAND" {
		t.Errorf("persisted ship role = %q, want COMMAND from the response (the empty-role the resync guarded is avoided)", got)
	}

	// The observable purchase result is correct.
	pr, ok := resp.(*PurchaseShipResponse)
	if !ok {
		t.Fatalf("unexpected response type %T", resp)
	}
	if pr.PurchasePrice != knownPrice {
		t.Errorf("PurchasePrice = %d, want %d (authoritative transaction price)", pr.PurchasePrice, knownPrice)
	}
	if pr.AgentCredits != postCredits {
		t.Errorf("AgentCredits = %d, want %d (authoritative post-purchase balance)", pr.AgentCredits, postCredits)
	}
	if pr.Ship == nil || pr.Ship.ShipSymbol() != "NEW-SHIP-1" {
		t.Errorf("returned ship = %v, want NEW-SHIP-1", pr.Ship)
	}
}

// --- CLI / auto-discover fallback (no known price) doubles -------------------

type purchaseUnknownMediator struct {
	common.Mediator
	sends int
	price int
}

func (m *purchaseUnknownMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	if _, ok := request.(*queries.GetShipyardListingsQuery); !ok {
		return nil, nil
	}
	m.sends++
	return &queries.GetShipyardListingsResponse{
		Shipyard: shipyard.Shipyard{
			Symbol:    "X1-TEST-A1",
			ShipTypes: []string{"SHIP_LIGHT_HAULER"},
			Listings:  []shipyard.ShipListing{{ShipType: "SHIP_LIGHT_HAULER", PurchasePrice: m.price}},
		},
	}, nil
}

type purchaseCreditsAPIClient struct {
	domainPorts.APIClient
	getAgentCalls int
	credits       int
}

func (c *purchaseCreditsAPIClient) GetAgent(_ context.Context, _ string) (*player.AgentData, error) {
	c.getAgentCalls++
	return &player.AgentData{Credits: c.credits}, nil
}

// TestPurchaseShip_UnknownPrice_FallsBackToLiveReads proves the CLI single-buy /
// auto-discover path (KnownPurchasePrice == 0) is unchanged: resolveShipPrice
// still reads the live shipyard listing and guardSufficientCredits still reads
// the live agent credits (and still rejects an unaffordable buy). This is the
// regression guard that the dedup did not weaken validation on a path the caller
// does not front-run.
func TestPurchaseShip_UnknownPrice_FallsBackToLiveReads(t *testing.T) {
	const yard = "X1-TEST-A1"
	med := &purchaseUnknownMediator{price: 120000}
	handler := &PurchaseShipHandler{mediator: med}
	cmd := &PurchaseShipCommand{
		ShipType:         "SHIP_LIGHT_HAULER",
		PlayerID:         shared.MustNewPlayerID(1),
		ShipyardWaypoint: yard,
		// KnownPurchasePrice deliberately 0 — the CLI / auto-discover default.
	}

	price, systemSymbol, err := handler.resolveShipPrice(context.Background(), cmd, yard)
	if err != nil {
		t.Fatalf("resolveShipPrice: %v", err)
	}
	if med.sends != 1 {
		t.Errorf("expected the unknown-price path to read the shipyard exactly once, got %d listings queries", med.sends)
	}
	if price != 120000 {
		t.Errorf("price = %d, want 120000 from the live listing", price)
	}
	if systemSymbol != "X1-TEST" {
		t.Errorf("systemSymbol = %q, want X1-TEST", systemSymbol)
	}

	// Sufficient credits: reads the agent and passes.
	okClient := &purchaseCreditsAPIClient{credits: 200000}
	handler.apiClient = okClient
	if err := handler.guardSufficientCredits(context.Background(), cmd, "token", price); err != nil {
		t.Fatalf("guardSufficientCredits with sufficient credits: %v", err)
	}
	if okClient.getAgentCalls != 1 {
		t.Errorf("expected the unknown-price guard to read the agent exactly once, got %d", okClient.getAgentCalls)
	}

	// Insufficient credits: still rejects (validation not weakened).
	brokeClient := &purchaseCreditsAPIClient{credits: 100}
	handler.apiClient = brokeClient
	if err := handler.guardSufficientCredits(context.Background(), cmd, "token", price); err == nil {
		t.Error("expected an insufficient-credits error on the unknown-price path, got nil")
	}
}
