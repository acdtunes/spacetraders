package cargo

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- market trade-volume (4604) clamp -----------------------------------------
//
// THE INCIDENT: tour containers died selling 330 FOOD into a market whose live
// per-transaction limit had dropped to 300 while the cached trade_volume still read 330.
// The API refuses the WHOLE tranche and states the true limit in data.tradeVolume; the sale
// aborted at zero transactions, crashing the container with a full hold.
//
// These drive the real CargoTransactionHandler tranche loop against a miniature market that
// enforces a per-transaction limit and reports it in-band exactly as the live API does.

const (
	volumeGood     = "FOOD"
	volumeShip     = "TORWIND-2E3"
	volumeWaypoint = testBuyWaypoint
)

// marketVolume4604 builds the API's real trade-volume rejection, wrapped the way
// production wraps it: the adapter returns a typed *domainPorts.APIError from request(),
// and the client wraps that with "failed to <verb> cargo: %w".
func marketVolume4604(verb, waypoint, good string, requested, limit int) error {
	body := fmt.Sprintf(
		`{"error":{"code":4604,"message":"Market transaction failed. Trade good %s has a limit of %d units per transaction.",`+
			`"data":{"waypointSymbol":"%s","tradeSymbol":"%s","units":%d,"tradeVolume":%d}}}`,
		good, limit, waypoint, good, requested, limit)
	return fmt.Errorf("failed to %s cargo: %w", verb, &domainPorts.APIError{StatusCode: 400, Body: body})
}

// volumeFakeAPI models the market side of the incident: it takes at most liveLimit units
// per transaction and rejects anything larger with the true limit in-band. rejectWith
// overrides the rejection outright (used to inject contradictory or malformed payloads);
// rejectEverything makes even a correctly clamped retry fail.
type volumeFakeAPI struct {
	domainPorts.APIClient
	liveLimit        int
	pricePerUnit     int
	rejectWith       error
	rejectEverything bool
	sells            []int
	buys             []int
}

func (c *volumeFakeAPI) SellCargo(_ context.Context, _, goodSymbol string, units int, _ string) (*domainPorts.SellResult, error) {
	c.sells = append(c.sells, units)
	if c.rejectWith != nil {
		return nil, c.rejectWith
	}
	if c.rejectEverything || units > c.liveLimit {
		return nil, marketVolume4604("sell", volumeWaypoint, goodSymbol, units, c.liveLimit)
	}
	return &domainPorts.SellResult{TotalRevenue: units * c.pricePerUnit, UnitsSold: units}, nil
}

func (c *volumeFakeAPI) PurchaseCargo(_ context.Context, _, goodSymbol string, units int, _ string) (*domainPorts.PurchaseResult, error) {
	c.buys = append(c.buys, units)
	if c.rejectWith != nil {
		return nil, c.rejectWith
	}
	if units > c.liveLimit {
		return nil, marketVolume4604("purchase", volumeWaypoint, goodSymbol, units, c.liveLimit)
	}
	return &domainPorts.PurchaseResult{TotalCost: units * c.pricePerUnit, UnitsAdded: units}, nil
}

// volumeMarketRepo serves the STALE cached trade_volume the handler chunks by — the belief
// the live market is about to disprove. bid is the cached sell price the armed floor reads.
type volumeMarketRepo struct {
	scoutingQuery.MarketRepository
	cachedLimit int
	bid         int
}

func (r *volumeMarketRepo) GetMarketData(_ context.Context, _ string, _ int) (*market.Market, error) {
	if r.cachedLimit == 0 {
		return nil, nil
	}
	supply, activity := "MODERATE", "STRONG"
	bid := r.bid
	if bid == 0 {
		bid = 100
	}
	g, err := market.NewTradeGood(volumeGood, &supply, &activity, bid*2, bid, r.cachedLimit, market.TradeTypeImport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(volumeWaypoint, []market.TradeGood{*g}, time.Now())
}

// runVolumeSell drives the real SellCargoHandler over the fixture. minBid arms the
// per-tranche sell floor (0 leaves it disarmed, the plain sell).
func runVolumeSell(t *testing.T, api *volumeFakeAPI, repo *volumeMarketRepo, hold, sellUnits, minBid int) (*SellCargoResponse, error) {
	t.Helper()
	ship := newLadenHull(t, volumeGood, hold, 400)
	h := NewSellCargoHandler(&buyFakeShipRepo{ship: ship},
		&buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(1), "TORWIND", "tok")},
		api, repo, &buyRecordingMediator{}, nil)

	resp, err := h.Handle(auth.WithPlayerToken(context.Background(), "tok"), &SellCargoCommand{
		ShipSymbol: volumeShip, GoodSymbol: volumeGood, Units: sellUnits,
		PlayerID: shared.MustNewPlayerID(1), MinBidPerUnit: minBid,
	})
	if err != nil {
		return nil, err
	}
	return resp.(*SellCargoResponse), nil
}

// (a) THE REPRO: the cache says 330 units fit in one transaction, the market takes 300.
// Unguarded the whole sale aborted at zero transactions; now the rejection's own limit
// governs — clamped to 300, retried once, and the 30-unit remainder follows.
// Mutation target: deleting the retry, or the 4604 branch in executeTransactions.
func TestSellCargo_MarketVolume4604_ChunksToTheStatedLimitInsteadOfFailing(t *testing.T) {
	api := &volumeFakeAPI{liveLimit: 300, pricePerUnit: 2263}

	resp, err := runVolumeSell(t, api, &volumeMarketRepo{cachedLimit: 330}, 330, 330, 0)

	require.NoError(t, err, "a 4604 whose payload states the market's true limit must NOT fail the sale")
	if resp.UnitsSold != 330 {
		t.Fatalf("the whole 330-unit hold must sell across chunks, got %d", resp.UnitsSold)
	}
	if resp.TotalRevenue != 330*2263 {
		t.Fatalf("revenue must be booked for every unit sold (330 x 2263), got %d", resp.TotalRevenue)
	}
	if len(api.sells) != 3 || api.sells[0] != 330 || api.sells[1] != 300 || api.sells[2] != 30 {
		t.Fatalf("expected the rejected 330, the clamped 300, then the 30-unit remainder, got %v", api.sells)
	}
}

// (b) NO-STRAND: the corrected limit governs every REMAINING tranche, not only the one
// rejected, so the hold drains to zero — adopting it for the retry alone leaves 238 units
// aboard to re-arm the identical rejection next leg.
// Mutation target: dropping `transactionLimit = volume.limit` in executeTransactions.
func TestSellCargo_MarketVolume4604_LeavesNoUnitsStrandedAfterTheClamp(t *testing.T) {
	api := &volumeFakeAPI{liveLimit: 112, pricePerUnit: 100}

	resp, err := runVolumeSell(t, api, &volumeMarketRepo{cachedLimit: 350}, 350, 350, 0)

	require.NoError(t, err)
	if resp.UnitsSold != 350 {
		t.Fatalf("no-strand violated: %d of 350 units never left the hull", 350-resp.UnitsSold)
	}
	want := []int{350, 112, 112, 112, 14}
	if len(api.sells) != len(want) {
		t.Fatalf("expected the rejected tranche then 112/112/112/14, got %v", api.sells)
	}
	for i := range want {
		if api.sells[i] != want[i] {
			t.Fatalf("tranche %d = %d units, want %d (all of %v)", i, api.sells[i], want[i], api.sells)
		}
	}
	if resp.TransactionCount != 4 {
		t.Fatalf("the four chunks that actually moved units must be counted, got %d", resp.TransactionCount)
	}
}

// (c) THE API BOUND: a sale that already fits the cached limit is byte-identical to the
// pre-fix path — one full-size transaction, no rejection, no retry. The clamp costs nothing
// on the common path because it rides a rejection already paid for.
// Mutation target: any pre-emptive live re-read of the limit shows up here as extra calls.
func TestSellCargo_WithinTradeVolume_IsUnchangedAndCostsNoExtraCall(t *testing.T) {
	api := &volumeFakeAPI{liveLimit: 300, pricePerUnit: 2263}

	resp, err := runVolumeSell(t, api, &volumeMarketRepo{cachedLimit: 300}, 300, 300, 0)

	require.NoError(t, err)
	if len(api.sells) != 1 || api.sells[0] != 300 {
		t.Fatalf("a sale within the market's limit must be exactly one full-size transaction, got %v", api.sells)
	}
	if resp.UnitsSold != 300 || resp.TransactionCount != 1 {
		t.Fatalf("got %d units in %d transactions, want 300 in 1", resp.UnitsSold, resp.TransactionCount)
	}
}

// A chunked sale is ONE dispatch carrying ONE floor verdict: the floor is re-verified per
// tranche as before, and a crushed bid aborts the remainder once — never once per chunk.
// That is what keeps the tour's one-refusal-per-good budget immune to the chunk count.
// Mutation target: hoisting the chunk loop above the floor check.
func TestSellCargo_MarketVolume4604_ChunkedSaleCarriesOneFloorVerdict(t *testing.T) {
	api := &volumeFakeAPI{liveLimit: 100, pricePerUnit: 100}
	// The cached bid (100) sits under the armed floor (150), so the floor trips at the
	// FIRST tranche: nothing sells, and the whole 330-unit hold is held aboard.
	resp, err := runVolumeSell(t, api, &volumeMarketRepo{cachedLimit: 330, bid: 100}, 330, 330, 150)

	require.NoError(t, err)
	if !resp.FloorAborted || resp.UnitsSold != 0 {
		t.Fatalf("the armed floor must hold the hold aboard: aborted=%t, sold=%d", resp.FloorAborted, resp.UnitsSold)
	}
	if len(api.sells) != 0 {
		t.Fatalf("a floor that trips before the first tranche must dispatch nothing, got %v", api.sells)
	}
}

// The floor still guards every chunk the clamp creates: with the bid above it the whole
// hold sells, each chunk dispatched under the same armed command.
func TestSellCargo_MarketVolume4604_EveryChunkStaysUnderTheArmedFloor(t *testing.T) {
	api := &volumeFakeAPI{liveLimit: 300, pricePerUnit: 100}

	resp, err := runVolumeSell(t, api, &volumeMarketRepo{cachedLimit: 330, bid: 400}, 330, 330, 150)

	require.NoError(t, err)
	if resp.FloorAborted {
		t.Fatalf("a bid of 400 is above the 150 floor and must not trip it")
	}
	if resp.UnitsSold != 330 {
		t.Fatalf("the whole hold must sell across chunks under the armed floor, got %d", resp.UnitsSold)
	}
}

// FAIL CLOSED: the clamp is min(planned, limit) and must NEVER become a path to asking for
// more than planned. A limit at or above what we asked for is self-contradictory, so it is
// distrusted entirely and the original rejection stands.
func TestSellCargo_MarketVolume4604_NeverClampsUpFromTheStatedLimit(t *testing.T) {
	api := &volumeFakeAPI{
		liveLimit: 100, pricePerUnit: 100,
		rejectWith: marketVolume4604("sell", volumeWaypoint, volumeGood, 200, 500),
	}

	_, err := runVolumeSell(t, api, &volumeMarketRepo{cachedLimit: 200}, 200, 200, 0)

	require.Error(t, err, "a self-contradictory limit must be distrusted, leaving the rejection standing")
	if len(api.sells) != 1 {
		t.Fatalf("no retry may be attempted from a limit at or above the planned size, got %v", api.sells)
	}
	for _, units := range api.sells {
		if units > 200 {
			t.Fatalf("the handler must NEVER ask to move more than it planned, got %v", api.sells)
		}
	}
}

// Only if the CLAMPED retry also fails does the transaction fail, and the retry is
// attempted exactly once — never a second time. Mutation target: a retry loop instead of
// a single attempt shows more than two calls here.
func TestSellCargo_MarketVolume4604_ClampedRetryAlsoFails_TransactionFails(t *testing.T) {
	api := &volumeFakeAPI{liveLimit: 300, pricePerUnit: 100, rejectEverything: true}

	_, err := runVolumeSell(t, api, &volumeMarketRepo{cachedLimit: 330}, 330, 330, 0)

	require.Error(t, err, "a clamped retry that also fails must fail the transaction")
	if len(api.sells) != 2 {
		t.Fatalf("the clamped retry must be attempted exactly ONCE, got %v", api.sells)
	}
}

// FAIL CLOSED: the limit is authoritative only for the good the payload names. A rejection
// describing a DIFFERENT good tells us nothing about this one, so no retry is attempted.
func TestSellCargo_MarketVolume4604_PayloadNamesDifferentGood_NoRetry(t *testing.T) {
	api := &volumeFakeAPI{
		liveLimit: 300, pricePerUnit: 100,
		rejectWith: marketVolume4604("sell", volumeWaypoint, "FERTILIZER", 330, 300),
	}

	_, err := runVolumeSell(t, api, &volumeMarketRepo{cachedLimit: 330}, 330, 330, 0)

	require.Error(t, err)
	if len(api.sells) != 1 {
		t.Fatalf("a limit stated for a different good must not drive a retry of this one, got %v", api.sells)
	}
}

// The clamp reads the MARKET's depth, not the hull's, so it is side-agnostic: a purchase
// tranche the market caps is clamped and chunked exactly like a sell. The 4219 hold
// reconcile beside it stays sell-only because a hold count says nothing about a buy.
func TestPurchaseCargo_MarketVolume4604_ChunksToTheStatedLimit(t *testing.T) {
	api := &volumeFakeAPI{liveLimit: 60, pricePerUnit: 10}
	ship := newLadenHull(t, volumeGood, 0, 400)
	h := NewPurchaseCargoHandler(&buyFakeShipRepo{ship: ship},
		&buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(1), "TORWIND", "tok")},
		api, &volumeMarketRepo{cachedLimit: 100}, &buyRecordingMediator{}, nil)

	resp, err := h.Handle(auth.WithPlayerToken(context.Background(), "tok"), &PurchaseCargoCommand{
		ShipSymbol: volumeShip, GoodSymbol: volumeGood, Units: 100, PlayerID: shared.MustNewPlayerID(1),
	})

	require.NoError(t, err, "a capped purchase tranche must chunk, not kill the leg")
	if got := resp.(*PurchaseCargoResponse).UnitsAdded; got != 100 {
		t.Fatalf("the whole 100-unit buy must complete across chunks, got %d", got)
	}
	if len(api.buys) != 3 || api.buys[0] != 100 || api.buys[1] != 60 || api.buys[2] != 40 {
		t.Fatalf("expected the rejected 100, the clamped 60, then the 40-unit remainder, got %v", api.buys)
	}
}
