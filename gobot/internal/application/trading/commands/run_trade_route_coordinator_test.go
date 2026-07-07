package commands

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	trSystem = "X1-TR"
	trSource = "X1-TR-EXPORT" // exporter: we BUY here at its ask (2000)
	trDest   = "X1-TR-IMPORT" // importer: we SELL here at its (decaying) bid
	trGood   = "WIDGET"

	trSourceAsk    = 2000 // per-unit acquisition cost (basis) → bid-floor = 3000
	trSellRevenue  = 3500 // per-unit revenue the fake grants → +1500/unit each visit
	trStartDestBid = 4000 // importer bid before any fills
	trBidDecay     = 400  // bid drop per completed sell (importer filling)
)

// trFixture shares one sellCount between the market repo (which decays the
// importer bid as sells accumulate) and the mediator (which increments it on each
// sell). This models the importer filling: the dest bid walks down until it falls
// through the basis+1000 floor, ending the circuit.
type trFixture struct {
	mu        sync.Mutex
	sellCount int
}

func (f *trFixture) currentDestBid() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return trStartDestBid - trBidDecay*f.sellCount
}

func (f *trFixture) recordSell() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sellCount++
}

// trFakeMediator records buys/sells (for tranche-cap and economics assertions)
// and no-ops navigation/docking, mirroring the factory coordinator's fake.
type trFakeMediator struct {
	mu        sync.Mutex
	fixture   *trFixture
	purchases []*shipCargo.PurchaseCargoCommand
	sells     []*shipCargo.SellCargoCommand
}

func (m *trFakeMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipCargo.PurchaseCargoCommand:
		m.mu.Lock()
		m.purchases = append(m.purchases, cmd)
		m.mu.Unlock()
		return &shipCargo.PurchaseCargoResponse{
			TotalCost:        cmd.Units * trSourceAsk,
			UnitsAdded:       cmd.Units,
			TransactionCount: 1,
		}, nil
	case *shipCargo.SellCargoCommand:
		m.mu.Lock()
		m.sells = append(m.sells, cmd)
		m.mu.Unlock()
		m.fixture.recordSell() // importer fills → dest bid decays for the next visit
		return &shipCargo.SellCargoResponse{
			TotalRevenue:     cmd.Units * trSellRevenue,
			UnitsSold:        cmd.Units,
			TransactionCount: 1,
		}, nil
	default:
		return nil, nil // navigate, dock, etc. succeed silently
	}
}

func (m *trFakeMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *trFakeMediator) RegisterMiddleware(middleware common.Middleware) {}

// trFakeMarketRepo serves a two-market system: an exporter selling WIDGET at a
// fixed ask, and an importer whose bid decays with sellCount.
type trFakeMarketRepo struct {
	market.MarketRepository
	fixture *trFixture
}

func (r *trFakeMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return []string{trSource, trDest}, nil
}

func (r *trFakeMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	supply := "MODERATE"
	activity := "STRONG"
	switch waypointSymbol {
	case trSource:
		// Exporter: ask (SellPrice) 2000 is what we pay; bid (PurchasePrice) is low.
		good, err := market.NewTradeGood(trGood, &supply, &activity, 1900, trSourceAsk, 60, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	case trDest:
		// Importer: bid (PurchasePrice) is what we receive and decays with fills.
		bid := r.fixture.currentDestBid()
		good, err := market.NewTradeGood(trGood, &supply, &activity, bid, 4100, 30, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	}
	return nil, nil
}

type trFakeClock struct{}

func (c *trFakeClock) Now() time.Time        { return time.Now() }
func (c *trFakeClock) Sleep(d time.Duration) {}

// trFakeContainerRepo models the containers table for the FK check below: Add
// inserts a row (so a later ship save that points at it passes the constraint),
// Remove deletes it. It records the order of Adds and Removes so a test can prove
// the container is inserted before the claim and dropped on release.
type trFakeContainerRepo struct {
	mu       sync.Mutex
	existing map[string]bool
	added    []string
	removed  []string
}

func newTrFakeContainerRepo() *trFakeContainerRepo {
	return &trFakeContainerRepo{existing: map[string]bool{}}
}

func (r *trFakeContainerRepo) Add(ctx context.Context, c *container.Container, commandType string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.existing[c.ID()] = true
	r.added = append(r.added, c.ID())
	return nil
}

func (r *trFakeContainerRepo) Remove(ctx context.Context, containerID string, playerID int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.existing, containerID)
	r.removed = append(r.removed, containerID)
	return nil
}

func (r *trFakeContainerRepo) has(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.existing[id]
}

// trFakeShipRepo returns one hull and records the container each Save observed, so
// a test can prove the ship was claimed (container set) then released (""). Its
// Save enforces the ships.container_id -> containers.id FK the way real Postgres
// does: a save that points at a container row absent from the shared container
// repo is rejected with the exact SQLSTATE 23503 the live run hit — the in-memory
// fakes used to skip this, which is why the bug survived to first exercise (L42).
type trFakeShipRepo struct {
	navigation.ShipRepository
	mu              sync.Mutex
	ship            *navigation.Ship
	savedContainers []string
	containers      *trFakeContainerRepo
}

func (r *trFakeShipRepo) FindBySymbol(ctx context.Context, symbol string, playerID shared.PlayerID) (*navigation.Ship, error) {
	return r.ship, nil
}

func (r *trFakeShipRepo) Save(ctx context.Context, ship *navigation.Ship) error {
	r.mu.Lock()
	r.savedContainers = append(r.savedContainers, ship.ContainerID())
	r.mu.Unlock()
	if cid := ship.ContainerID(); cid != "" && r.containers != nil && !r.containers.has(cid) {
		return fmt.Errorf("insert or update on table \"ships\" violates foreign key constraint \"fk_ships_container\" (SQLSTATE 23503)")
	}
	return nil
}

func newTradeHauler(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("fuel: %v", err)
	}
	waypoint, err := shared.NewWaypoint(trSource, 0, 0)
	if err != nil {
		t.Fatalf("waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("ship: %v", err)
	}
	return ship
}

type trHarness struct {
	handler       *RunTradeRouteCoordinatorHandler
	mediator      *trFakeMediator
	shipRepo      *trFakeShipRepo
	containerRepo *trFakeContainerRepo
	ship          *navigation.Ship
}

func newTradeHarness(t *testing.T, ship *navigation.Ship) *trHarness {
	t.Helper()
	fixture := &trFixture{}
	mediator := &trFakeMediator{fixture: fixture}
	marketRepo := &trFakeMarketRepo{fixture: fixture}
	containerRepo := newTrFakeContainerRepo()
	shipRepo := &trFakeShipRepo{ship: ship, containers: containerRepo}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, marketRepo, containerRepo, &trFakeClock{})
	return &trHarness{handler: handler, mediator: mediator, shipRepo: shipRepo, containerRepo: containerRepo, ship: ship}
}

// The circuit must fly the top lane in ≤18u tranches, loop while the importer bid
// clears basis+1000, stop the moment it drops below the floor, net positive, and
// leave the hull released.
func TestTradeRouteCoordinator_RunsDisciplinedCircuitUntilMarginDies(t *testing.T) {
	ship := newTradeHauler(t, "TRADER-2")
	h := newTradeHarness(t, ship)

	resp, err := h.handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: trSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("coordinator returned error: %v", err)
	}
	coord, ok := resp.(*RunTradeRouteCoordinatorResponse)
	if !ok || coord == nil {
		t.Fatalf("unexpected response %T", resp)
	}
	if !coord.Completed {
		t.Fatalf("expected a completed run, got %+v", coord)
	}

	// Lane identity: buy at the exporter, sell at the importer.
	if coord.Good != trGood || coord.SourceWaypoint != trSource || coord.DestWaypoint != trDest {
		t.Fatalf("wrong lane: good=%q source=%q dest=%q", coord.Good, coord.SourceWaypoint, coord.DestWaypoint)
	}

	// Bid decays 4000→3600→3200 (alive, ≥3000), then 2800 (<3000, dead): 3 visits.
	if coord.Visits != 3 {
		t.Fatalf("expected 3 visits before the bid-floor kills the margin, got %d", coord.Visits)
	}
	if coord.UnitsTraded != 54 {
		t.Fatalf("expected 54 units traded (3 visits x 18u), got %d", coord.UnitsTraded)
	}

	// Tranche caps: no visit may buy or sell more than 18 units.
	for i, p := range h.mediator.purchases {
		if p.Units > 18 {
			t.Fatalf("purchase %d bought %d units, exceeding the 18u tranche cap", i, p.Units)
		}
	}
	for i, s := range h.mediator.sells {
		if s.Units > 18 {
			t.Fatalf("sell %d sold %d units, exceeding the 18u tranche cap", i, s.Units)
		}
	}
	if len(h.mediator.purchases) != 3 || len(h.mediator.sells) != 3 {
		t.Fatalf("expected 3 buys and 3 sells, got %d buys / %d sells", len(h.mediator.purchases), len(h.mediator.sells))
	}

	// Net positive: revenue (3x18x3500=189000) − cost (3x18x2000=108000) = 81000.
	if coord.NetProfit <= 0 {
		t.Fatalf("expected a net-positive circuit, got net %d (cost %d, revenue %d)", coord.NetProfit, coord.TotalCost, coord.TotalRevenue)
	}
	if coord.NetProfit != 81000 {
		t.Fatalf("expected net 81000, got %d", coord.NetProfit)
	}

	// The hull must be claimed (a container set) then released (idle) at the end.
	if !ship.IsIdle() {
		t.Fatalf("expected the ship released to idle after the run, still assigned to %q", ship.ContainerID())
	}
	if len(h.shipRepo.savedContainers) < 2 {
		t.Fatalf("expected at least a claim save and a release save, got %v", h.shipRepo.savedContainers)
	}
	if h.shipRepo.savedContainers[0] == "" {
		t.Fatalf("first save must record the claimed container id, got empty")
	}
	if last := h.shipRepo.savedContainers[len(h.shipRepo.savedContainers)-1]; last != "" {
		t.Fatalf("last save must record the release (empty container), got %q", last)
	}
}

// Regression for sp-r3cl. On real Postgres the composite FK fk_ships_container
// (ships.(container_id, player_id) -> containers.(id, player_id)) rejects a claim
// that points at a container row that was never inserted. This coordinator is
// CLI-driven, so — unlike the daemon coordinators, whose row is created by the
// container runner before ship assignment — nothing else creates its container. It
// must insert its own container row BEFORE saving the ship claim, and drop that row
// on release so restart recovery cannot adopt a leftover. The FK-enforcing fake
// ship repo reproduces the production 23503 the unit fakes previously hid.
func TestTradeRouteCoordinator_PersistsContainerBeforeClaim_FKSafe(t *testing.T) {
	ship := newTradeHauler(t, "TORWIND-1")
	h := newTradeHarness(t, ship)

	_, err := h.handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: trSystem,
		PlayerID:     1,
	})
	// With the ordering bug (claim saved before its container row exists) the
	// FK-enforcing ship repo rejects the claim exactly as Postgres does, and the
	// run errors out. The fix must insert the container first so the claim persists.
	if err != nil {
		t.Fatalf("trade-route claim violated the ships->containers FK (container not persisted before claim): %v", err)
	}

	// The claimed container must have been inserted (not merely skipped). The FK
	// check above already guarantees it existed at the moment of the claim save;
	// this pins down that the very row the ship was assigned to is the one created.
	if len(h.shipRepo.savedContainers) == 0 {
		t.Fatal("ship was never saved")
	}
	claimed := h.shipRepo.savedContainers[0]
	if claimed == "" {
		t.Fatal("first ship save recorded no container id — the ship was not claimed")
	}
	if len(h.containerRepo.added) == 0 || h.containerRepo.added[0] != claimed {
		t.Fatalf("container %q was not inserted before the ship claim (added=%v)", claimed, h.containerRepo.added)
	}

	// Recovery-safe release: the trade-route container row must be removed on
	// release. It is only ever PENDING (never RUNNING), so era-scoped restart
	// recovery (sp-njpu) — which resurrects only RUNNING/INTERRUPTED containers —
	// skips it even if the process dies before this Remove; removing it as well
	// keeps orphan rows from accumulating and closes the adoption (hkfb) window.
	if !containsStr(h.containerRepo.removed, claimed) {
		t.Fatalf("trade-route container %q was not removed on release (removed=%v)", claimed, h.containerRepo.removed)
	}
	if h.containerRepo.has(claimed) {
		t.Fatalf("trade-route container %q still present after release — restart recovery could adopt it", claimed)
	}
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// A ship already assigned to another coordinator must be refused, not stolen, and
// no trades may fire.
func TestTradeRouteCoordinator_RefusesNonIdleShip(t *testing.T) {
	ship := newTradeHauler(t, "TRADER-3")
	if err := ship.AssignToContainer("goods_factory-OTHER", shared.NewRealClock()); err != nil {
		t.Fatalf("pre-assign: %v", err)
	}
	h := newTradeHarness(t, ship)

	_, err := h.handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: trSystem,
		PlayerID:     1,
	})
	if err == nil {
		t.Fatal("expected an error claiming a non-idle ship")
	}
	if len(h.mediator.purchases) != 0 || len(h.mediator.sells) != 0 {
		t.Fatalf("a refused claim must not trade: %d buys / %d sells", len(h.mediator.purchases), len(h.mediator.sells))
	}
	if ship.ContainerID() != "goods_factory-OTHER" {
		t.Fatalf("trade-route clobbered another coordinator's assignment: now %q", ship.ContainerID())
	}
}

// trEmptyMarketRepo has no markets, so no lane can be ranked.
type trEmptyMarketRepo struct {
	market.MarketRepository
}

func (r *trEmptyMarketRepo) FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error) {
	return nil, nil
}

// With no profitable lane in cache the coordinator must complete cleanly, trade
// nothing, and still release the hull it claimed.
func TestTradeRouteCoordinator_NoLane_ReleasesShipWithoutTrading(t *testing.T) {
	ship := newTradeHauler(t, "TRADER-4")
	fixture := &trFixture{}
	mediator := &trFakeMediator{fixture: fixture}
	containerRepo := newTrFakeContainerRepo()
	shipRepo := &trFakeShipRepo{ship: ship, containers: containerRepo}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, &trEmptyMarketRepo{}, containerRepo, &trFakeClock{})

	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   ship.ShipSymbol(),
		SystemSymbol: trSystem,
		PlayerID:     1,
	})
	if err != nil {
		t.Fatalf("expected clean completion with no lane, got error: %v", err)
	}
	coord := resp.(*RunTradeRouteCoordinatorResponse)
	if !coord.Completed || coord.Visits != 0 {
		t.Fatalf("expected completed run with 0 visits, got %+v", coord)
	}
	if len(mediator.purchases) != 0 || len(mediator.sells) != 0 {
		t.Fatal("no lane must mean no trades")
	}
	if !ship.IsIdle() {
		t.Fatalf("ship must be released even when no lane is found, still on %q", ship.ContainerID())
	}
}
