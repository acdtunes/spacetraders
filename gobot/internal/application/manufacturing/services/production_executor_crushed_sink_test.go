package services

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/tactics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// bp6f #3: the trade crisis (over-buying + emergency liquidation) crushed home
// sinks - e.g. D40 ADV_CIRC's bid fell 7000->2191 - so factories kept
// harvesting fabricated output and reselling it into a sink priced BELOW
// their own input/harvest cost: loss-making production on every cycle. This
// guard compares the downstream resale bid (FindImportMarket) against the
// factory's own harvest cost (the TradeGood's SellPrice - what we pay to buy
// FROM the factory, the same idiom collection_opportunity_finder.go already
// uses: profit = buyer.purchasePrice - factory.sellPrice) right when
// production is confirmed, and PARKS (skips the harvest, returns (0,0,nil) -
// the same graceful-skip idiom already used by the inputsOnly bypass and
// purchaseInputWithEmptyTrancheGuard's exhaustion case) instead of harvesting
// at a proven loss.

// crushedSinkMarketRepo independently controls the factory's own harvest
// cost (GetMarketData, same shape as dockRaceMarketRepo) and the downstream
// sink's bid (FindBestMarketBuying), so the park-on-loss, harvest-when-
// profitable, and fail-open-on-no-sink paths can each be exercised precisely.
// The shared dockRaceMarketRepo cannot do this: it has no sink price knob at
// all (FindBestMarketBuying is unimplemented there), so this scenario needs
// its own fake rather than reusing newDockRaceExecutor.
type crushedSinkMarketRepo struct {
	market.MarketRepository
	harvestSellPrice    int   // factory's own ask - what we pay to harvest (GetMarketData)
	sinkBidPrice        int   // downstream sink's bid - what we'd receive reselling (FindBestMarketBuying)
	findBestMarketErr   error // when set, FindImportMarket fails -> guard must fail OPEN and harvest anyway
	findBestMarketCalls int
}

func (r *crushedSinkMarketRepo) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	if waypointSymbol != dockRaceMarketWP {
		return nil, nil
	}
	supply := "HIGH"
	activity := "STRONG"
	good, err := market.NewTradeGood(dockRaceGood, &supply, &activity, r.harvestSellPrice-2, r.harvestSellPrice, 10, market.TradeTypeExport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
}

func (r *crushedSinkMarketRepo) FindBestMarketBuying(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.BestMarketBuyingResult, error) {
	r.findBestMarketCalls++
	if r.findBestMarketErr != nil {
		return nil, r.findBestMarketErr
	}
	return &market.BestMarketBuyingResult{
		WaypointSymbol: dockRaceMarketWP,
		TradeSymbol:    goodSymbol,
		PurchasePrice:  r.sinkBidPrice,
		Supply:         "HIGH",
	}, nil
}

// newCrushedSinkExecutor mirrors newDockRaceExecutor exactly, substituting a
// crushedSinkMarketRepo (configurable harvest cost / sink bid) for the fixed
// dockRaceMarketRepo, since this scenario needs independent control over both
// prices that the shared harness does not expose.
func newCrushedSinkExecutor(t *testing.T, harvestSellPrice, sinkBidPrice int, findBestMarketErr error) (*ProductionExecutor, *dockRaceShipRepo, *dockRaceMediator) {
	t.Helper()

	repo := &dockRaceShipRepo{
		location:      dockRaceOrigin,
		navStatus:     navigation.NavStatusDocked,
		cargoCapacity: 40,
	}
	mediator := &dockRaceMediator{
		repo:        repo,
		dockHandler: tactics.NewDockShipHandler(repo),
	}
	marketRepo := &crushedSinkMarketRepo{
		harvestSellPrice:  harvestSellPrice,
		sinkBidPrice:      sinkBidPrice,
		findBestMarketErr: findBestMarketErr,
	}
	marketLocator := NewMarketLocator(marketRepo, nil, nil, nil)

	executor := NewProductionExecutorWithConfig(
		mediator,
		repo,
		marketRepo,
		marketLocator,
		&dockRaceClock{},
		[]time.Duration{time.Millisecond},
		nil, // apiClient: crushed-sink guards the harvest path, not the input buy; floor stays disabled here
	)
	return executor, repo, mediator
}

// A sink bid below the factory's own harvest cost means every unit harvested
// sells at a loss. The guard must PARK: skip the harvest entirely (0,0,nil),
// issue zero purchases, and log why.
func TestPollForProduction_CrushedSink_ParksInsteadOfProducingAtALoss(t *testing.T) {
	// Harvest costs 10/unit; the only resale sink bids just 3/unit - a proven loss.
	executor, _, mediator := newCrushedSinkExecutor(t, 10, 3, nil)
	logger := &dwellCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	quantity, cost, err := executor.PollForProduction(
		ctx,
		dockRaceGood,
		dockRaceMarketWP,
		dockRaceShip,
		shared.MustNewPlayerID(1),
		nil,
		false, // NOT inputs-only - the guard must still catch this on the harvest path
		"X1-DR",
	)
	if err != nil {
		t.Fatalf("a crushed sink must be parked gracefully, not surfaced as an error: got %v", err)
	}
	if quantity != 0 || cost != 0 {
		t.Fatalf("a parked (crushed-sink) poll must yield (0 units, 0 cost), got (%d, %d)", quantity, cost)
	}
	if mediator.purchaseAttempts() != 0 {
		t.Fatalf("a crushed sink must issue ZERO purchases, got %d", mediator.purchaseAttempts())
	}

	warnEntries := logger.entriesWithLevel("WARNING")
	if len(warnEntries) == 0 {
		t.Fatal("expected a WARNING log explaining the park, got none")
	}
	found := false
	for _, e := range warnEntries {
		if strings.Contains(e.message, dockRaceGood) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the park WARNING to name the good %s, got: %+v", dockRaceGood, warnEntries)
	}
}

// A sink bid above the factory's own harvest cost is profitable: the
// pre-existing harvest behavior must be unchanged (no false-positive parks).
func TestPollForProduction_ProfitableSink_StillHarvests(t *testing.T) {
	// Harvest costs 10/unit; the sink bids 100/unit - clearly profitable.
	executor, _, mediator := newCrushedSinkExecutor(t, 10, 100, nil)

	quantity, cost, err := executor.PollForProduction(
		context.Background(),
		dockRaceGood,
		dockRaceMarketWP,
		dockRaceShip,
		shared.MustNewPlayerID(1),
		nil,
		false,
		"X1-DR",
	)
	if err != nil {
		t.Fatalf("a profitable sink must harvest normally, got error: %v", err)
	}
	if quantity <= 0 {
		t.Fatalf("expected a successful harvest, got %d units (cost %d)", quantity, cost)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly 1 purchase for a profitable harvest, got %d", mediator.purchaseAttempts())
	}
}

// A good with no resale sink at all (FindImportMarket errors - e.g. a
// construction-only intermediate that nothing downstream buys) must NOT be
// blocked from harvesting: the guard fails OPEN on any lookup error so goods
// legitimately lacking a sink are unaffected.
func TestPollForProduction_NoImportMarket_FailsOpenAndHarvests(t *testing.T) {
	executor, _, mediator := newCrushedSinkExecutor(t, 10, 0, fmt.Errorf("no market found importing %s", dockRaceGood))

	quantity, _, err := executor.PollForProduction(
		context.Background(),
		dockRaceGood,
		dockRaceMarketWP,
		dockRaceShip,
		shared.MustNewPlayerID(1),
		nil,
		false,
		"X1-DR",
	)
	if err != nil {
		t.Fatalf("a missing import market must fail OPEN (harvest anyway), got error: %v", err)
	}
	if quantity <= 0 {
		t.Fatalf("expected the harvest to proceed despite the missing sink lookup, got %d units", quantity)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected exactly 1 purchase (fail-open harvest), got %d", mediator.purchaseAttempts())
	}
}
