package commands

import (
	"context"
	"testing"

	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// rebalanceStubShipRepo serves a fixed coordinator pool. FindByContainer feeds the
// coordinator-ship lookup; FindAllByPlayer feeds the cached-list optimization. Any other
// method panics via the embedded nil interface (they must not be called here).
type rebalanceStubShipRepo struct {
	navigation.ShipRepository
	fleet []*navigation.Ship
}

func (s *rebalanceStubShipRepo) FindByContainer(_ context.Context, _ string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return s.fleet, nil
}

func (s *rebalanceStubShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return s.fleet, nil
}

// rebalanceStubMarketRepo returns a fixed set of in-system markets.
type rebalanceStubMarketRepo struct {
	markets []string
}

func (r *rebalanceStubMarketRepo) FindAllMarketsInSystem(_ context.Context, _ string, _ int) ([]string, error) {
	return r.markets, nil
}

// rebalanceStubContractRepo serves fixed active contracts. FindByID/Add are unused.
type rebalanceStubContractRepo struct {
	domainContract.ContractRepository
	active []*domainContract.Contract
}

func (r *rebalanceStubContractRepo) FindActiveContracts(_ context.Context, _ int) ([]*domainContract.Contract, error) {
	return r.active, nil
}

// rebalanceStubSourceFinder resolves any good to a fixed market (or none).
type rebalanceStubSourceFinder struct {
	market string // waypoint symbol; "" => not sold anywhere
}

func (r *rebalanceStubSourceFinder) FindCheapestMarketSelling(_ context.Context, _ string, _ string, _ int) (*market.CheapestMarketResult, error) {
	if r.market == "" {
		return nil, nil
	}
	return &market.CheapestMarketResult{WaypointSymbol: r.market}, nil
}

// rebalanceTestShip builds an idle, docked coordinator hull at (x,y).
func rebalanceTestShip(t *testing.T, symbol string, x, y float64) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint("X1-SYS-SHIP", x, y)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(40, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 100, 40, cargo, 30, "FRAME_HAULER", "HAULER", nil, navigation.NavStatusDocked)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

func rebalanceTestContract(t *testing.T, deliveries ...domainContract.Delivery) *domainContract.Contract {
	t.Helper()
	c, err := domainContract.NewContract("CONTRACT-1", shared.MustNewPlayerID(1), "COSMIC", "PROCUREMENT",
		domainContract.Terms{Deliveries: deliveries}, nil)
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	if err := c.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	return c
}

// destinationSentTo returns the navigate destination the handler dispatched for
// shipSymbol, or "" if none.
func destinationSentTo(m *homeFakeMediator, shipSymbol string) string {
	for _, cmd := range m.navigateCalls {
		if cmd.ShipSymbol == shipSymbol {
			return cmd.Destination
		}
	}
	return ""
}

// buildRebalanceHarness wires the handler with a single far-flung idle hull, a NEAR and
// a FAR market (FAR = the predicted source), and the given contract + config. The idle
// hull sits 600 units from NEAR (> the 500 rebalance threshold) so rebalancing always
// triggers; distance alone would send it to NEAR.
func buildRebalanceHarness(
	t *testing.T,
	contract *domainContract.Contract,
	sourceMarket string,
	cfg SourcePrepositionConfig,
) (*RebalanceContractFleetHandler, *homeFakeMediator, *RebalanceContractFleetCommand) {
	t.Helper()
	ship := rebalanceTestShip(t, "HAULER-1", 0, 0)
	near := homeTestWaypoint(t, "X1-SYS-NEAR", 600, 0)
	far := homeTestWaypoint(t, "X1-SYS-FAR", 1000, 0)
	graph := homeTestGraph(near, far)

	med := &homeFakeMediator{}
	shipRepo := &rebalanceStubShipRepo{fleet: []*navigation.Ship{ship}}
	graphProvider := &homeStubGraphProvider{graph: graph}
	marketRepo := &rebalanceStubMarketRepo{markets: []string{"X1-SYS-NEAR", "X1-SYS-FAR"}}
	contractRepo := &rebalanceStubContractRepo{active: []*domainContract.Contract{contract}}
	sourceFinder := &rebalanceStubSourceFinder{market: sourceMarket}

	handler := NewRebalanceContractFleetHandler(
		med, shipRepo, graphProvider, marketRepo, nil,
		contractRepo, sourceFinder, cfg,
	)
	cmd := &RebalanceContractFleetCommand{
		CoordinatorID: "COORD-1",
		PlayerID:      shared.MustNewPlayerID(1),
		SystemSymbol:  "X1-SYS",
	}
	return handler, med, cmd
}

// sp-1ef0 handler pin H1: an idle hull whose coordinator holds a same-good /
// multi-delivery-remaining contract is pre-positioned to the predicted next-source
// market (FAR) even though distance alone would send it to NEAR.
func TestRebalanceHandler_SameGoodMultiDeliveryRemaining_PrePositionsToPredictedSource(t *testing.T) {
	contract := rebalanceTestContract(t, domainContract.Delivery{
		TradeSymbol: "IRON_ORE", UnitsRequired: 100, UnitsFulfilled: 0, DestinationSymbol: "X1-SYS-DEST",
	})
	handler, med, cmd := buildRebalanceHarness(t, contract, "X1-SYS-FAR", SourcePrepositionConfig{})

	if _, err := handler.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if got := destinationSentTo(med, "HAULER-1"); got != "X1-SYS-FAR" {
		t.Errorf("hull navigated to %q, want X1-SYS-FAR (pre-positioned to predicted source)", got)
	}
}

// sp-1ef0 handler pin H2 (the guard): an ambiguous contract (two goods outstanding)
// yields a low-confidence signal, so the hull must NOT be pre-positioned — it falls back
// to the distance pick (NEAR).
func TestRebalanceHandler_AmbiguousContract_DoesNotPrePosition(t *testing.T) {
	contract := rebalanceTestContract(t,
		domainContract.Delivery{TradeSymbol: "IRON_ORE", UnitsRequired: 100, UnitsFulfilled: 0},
		domainContract.Delivery{TradeSymbol: "ALUMINUM_ORE", UnitsRequired: 100, UnitsFulfilled: 0},
	)
	handler, med, cmd := buildRebalanceHarness(t, contract, "X1-SYS-FAR", SourcePrepositionConfig{})

	if _, err := handler.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if got := destinationSentTo(med, "HAULER-1"); got != "X1-SYS-NEAR" {
		t.Errorf("hull navigated to %q, want X1-SYS-NEAR (low confidence must not pre-position)", got)
	}
}

// sp-1ef0 handler pin H3 (config guard, RULINGS #5): with pre-positioning disabled, even
// a near-certain contract does not move the hull off its distance pick (NEAR).
func TestRebalanceHandler_Disabled_DoesNotPrePosition(t *testing.T) {
	contract := rebalanceTestContract(t, domainContract.Delivery{
		TradeSymbol: "IRON_ORE", UnitsRequired: 100, UnitsFulfilled: 0,
	})
	handler, med, cmd := buildRebalanceHarness(t, contract, "X1-SYS-FAR", SourcePrepositionConfig{Disabled: true})

	if _, err := handler.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if got := destinationSentTo(med, "HAULER-1"); got != "X1-SYS-NEAR" {
		t.Errorf("hull navigated to %q, want X1-SYS-NEAR (feature disabled)", got)
	}
}
