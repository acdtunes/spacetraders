package cargo

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- sp-wbcil cargo-shortfall (4219) reconcile --------------------------------
//
// THE INCIDENT: tour-run-TORWIND-D7 died because the daemon believed the hull held
// 71 MACHINERY while the server held 11. The sell was rejected with 4219 — a payload
// that STATES the true count in data.cargoUnits — and the whole sale aborted at zero
// transactions, failing the leg, killing the tour, and releasing a hull still laden
// with ~242k of cargo that was then dumped below the profit floor.
//
// These tests drive the real CargoTransactionHandler tranche loop. The fake API is a
// miniature server: it holds serverUnits and rejects any request above that with the
// genuine 4219 envelope, so a fix that clamped UPWARD (or failed to clamp at all)
// would be rejected by the fixture exactly as the live API rejects it.

const shortfallGood = "MACHINERY"
const shortfallShip = "TORWIND-D7"

// cargoShortfall4219 builds the API's real cargo-shortfall rejection, wrapped the way
// production wraps it: the adapter returns a typed *domainPorts.APIError from
// request(), and SellCargo wraps that with "failed to sell cargo: %w".
func cargoShortfall4219(ship, good string, requested, onHand int) error {
	body := fmt.Sprintf(
		`{"error":{"message":"Ship %s cargo does not contain %d unit(s) of %s. Ship has %d unit(s).","code":4219,`+
			`"data":{"shipSymbol":"%s","tradeSymbol":"%s","cargoUnits":%d,"unitsToRemove":%d}}}`,
		ship, requested, good, onHand, ship, good, onHand, requested)
	return fmt.Errorf("failed to sell cargo: %w", &domainPorts.APIError{StatusCode: 400, Body: body})
}

// shortfallFakeAPI models the server side of the incident: it really holds serverUnits
// of the good, so any sell above that is rejected with the true count in-band and any
// sell at or below it succeeds. rejectWith overrides the rejection outright (used to
// inject contradictory or malformed payloads); rejectEverything makes even a correctly
// clamped retry fail, to pin the (b) leg-fails path.
type shortfallFakeAPI struct {
	domainPorts.APIClient
	serverUnits      int
	pricePerUnit     int
	rejectWith       error
	rejectEverything bool
	sells            []int
	buys             []int
}

func (c *shortfallFakeAPI) SellCargo(_ context.Context, shipSymbol, goodSymbol string, units int, _ string) (*domainPorts.SellResult, error) {
	c.sells = append(c.sells, units)
	if c.rejectWith != nil {
		return nil, c.rejectWith
	}
	if c.rejectEverything || units > c.serverUnits {
		return nil, cargoShortfall4219(shipSymbol, goodSymbol, units, c.serverUnits)
	}
	c.serverUnits -= units
	return &domainPorts.SellResult{TotalRevenue: units * c.pricePerUnit, UnitsSold: units}, nil
}

func (c *shortfallFakeAPI) PurchaseCargo(_ context.Context, shipSymbol, goodSymbol string, units int, _ string) (*domainPorts.PurchaseResult, error) {
	c.buys = append(c.buys, units)
	if c.rejectWith != nil {
		return nil, c.rejectWith
	}
	return nil, cargoShortfall4219(shipSymbol, goodSymbol, units, c.serverUnits)
}

// shortfallMarketRepo serves a fixed per-tranche transaction limit so a sale can be
// split across tranches (the shape the drift test needs). limit 0 serves no market at
// all, which makes GetTransactionLimit fall back to a single full-size transaction.
type shortfallMarketRepo struct {
	scoutingQuery.MarketRepository
	limit int
}

func (r *shortfallMarketRepo) GetMarketData(_ context.Context, _ string, _ int) (*market.Market, error) {
	if r.limit == 0 {
		return nil, nil
	}
	supply, activity := "MODERATE", "STRONG"
	// ask 200 / bid 100 — PurchasePrice (4th) is the ASK, the larger; the bid the
	// hull receives selling into this sink is SellPrice=100 (sp-en5h7).
	g, err := market.NewTradeGood(shortfallGood, &supply, &activity, 200, 100, r.limit, market.TradeTypeImport)
	if err != nil {
		return nil, err
	}
	return market.NewMarket(testBuyWaypoint, []market.TradeGood{*g}, time.Now())
}

// newLadenHull builds a docked hull whose CACHED hold claims units of good — the
// stale belief the server is about to contradict.
func newLadenHull(t *testing.T, good string, units, capacity int) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(capacity, units, []*shared.CargoItem{{Symbol: good, Units: units}})
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	waypoint, err := shared.NewWaypoint(testBuyWaypoint, 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(shortfallShip, shared.MustNewPlayerID(1), waypoint, fuel, 400, capacity,
		cargo, 30, "FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked)
	require.NoError(t, err)
	return ship
}

// runShortfallSell drives the real SellCargoHandler over the fixture and returns the
// response (nil on failure), the error, and the hull whose cached hold can be asserted.
func runShortfallSell(t *testing.T, api *shortfallFakeAPI, cachedUnits, sellUnits, trancheLimit int) (*SellCargoResponse, error, *navigation.Ship) {
	t.Helper()
	ship := newLadenHull(t, shortfallGood, cachedUnits, 80)
	shipRepo := &buyFakeShipRepo{ship: ship}
	playerRepo := &buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(1), "TORWIND", "tok")}
	h := NewSellCargoHandler(shipRepo, playerRepo, api, &shortfallMarketRepo{limit: trancheLimit}, &buyRecordingMediator{}, nil)

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	resp, err := h.Handle(ctx, &SellCargoCommand{
		ShipSymbol: shortfallShip, GoodSymbol: shortfallGood, Units: sellUnits, PlayerID: shared.MustNewPlayerID(1),
	})
	if err != nil {
		return nil, err, ship
	}
	return resp.(*SellCargoResponse), nil, ship
}

func heldUnits(ship *navigation.Ship, good string) int {
	if c := ship.Cargo(); c != nil {
		return c.GetItemUnits(good)
	}
	return 0
}

// THE REPRO (a): the cache claims 71 MACHINERY, the server holds 11. Unguarded,
// the sale aborted at zero transactions and the tour died with a full hold. Now the
// tranche is clamped to the server's count and retried once, booking 11 units of
// revenue. Mutation target: deleting the retry in retrySellClampedToServerCargo, or
// the whole shortfall branch in executeTransactions, fails this on both the error and
// the units.
func TestSellCargo_Shortfall4219_ClampsToServerCountAndRetriesOnce(t *testing.T) {
	api := &shortfallFakeAPI{serverUnits: 11, pricePerUnit: 3000}

	resp, err := mustSell(t, api, 71, 71, 0)

	if resp.UnitsSold != 11 {
		t.Fatalf("the clamped retry must sell the 11 units the server says are aboard, got %d", resp.UnitsSold)
	}
	if resp.TotalRevenue != 11*3000 {
		t.Fatalf("revenue must be booked for the clamped sale (11 x 3000), got %d", resp.TotalRevenue)
	}
	require.NoError(t, err)
	if len(api.sells) != 2 || api.sells[0] != 71 || api.sells[1] != 11 {
		t.Fatalf("expected the planned 71 then exactly one clamped retry of 11, got %v", api.sells)
	}
}

// HONEST PARTIAL REPORTING: a clamped sale is a PARTIAL success and must be reported
// as one everywhere — the clamp must never become a way to book the planned size. The
// contract-delivery path's stance is that a 4219 is never swallowed as a full success,
// and the ledger row is where that would leak: it is what the analyst grades revenue
// from. Mutation target: recording cmd.Units (71) rather than the units the retry
// actually sold (11) in recordCargoTransaction.
func TestSellCargo_Shortfall4219_LedgerRecordsOnlyTheUnitsActuallySold(t *testing.T) {
	api := &shortfallFakeAPI{serverUnits: 11, pricePerUnit: 3000}
	ship := newLadenHull(t, shortfallGood, 71, 80)
	med := &buyRecordingMediator{}
	h := NewSellCargoHandler(&buyFakeShipRepo{ship: ship},
		&buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(1), "TORWIND", "tok")},
		api, &shortfallMarketRepo{}, med, nil)

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	_, err := h.Handle(ctx, &SellCargoCommand{
		ShipSymbol: shortfallShip, GoodSymbol: shortfallGood, Units: 71, PlayerID: shared.MustNewPlayerID(1),
	})
	require.NoError(t, err)

	require.Len(t, med.recorded, 1, "the clamped sale writes exactly one ledger row")
	row := med.recorded[0]
	if got := row.Metadata["units"]; got != 11 {
		t.Fatalf("the ledger must record the 11 units that actually sold, not the 71 planned, got %v", got)
	}
	if row.Amount != 11*3000 {
		t.Fatalf("the ledger amount must be the realized revenue for 11 units, got %d", row.Amount)
	}
	if !strings.Contains(row.Description, "SELL 11 units") {
		t.Fatalf("the ledger description must state the units actually sold, got %q", row.Description)
	}
}

// mustSell is the success-path wrapper: it fails the test if the sale errored, so the
// assertions above read against a live response.
func mustSell(t *testing.T, api *shortfallFakeAPI, cachedUnits, sellUnits, trancheLimit int) (*SellCargoResponse, error) {
	t.Helper()
	resp, err, _ := runShortfallSell(t, api, cachedUnits, sellUnits, trancheLimit)
	if err != nil {
		t.Fatalf("a 4219 whose payload states the true hold must NOT fail the sale (the tour-killing abort): %v", err)
	}
	return resp, nil
}

// The cache must be reconciled to SERVER TRUTH, not merely decremented by what sold.
// Deducting the 11 sold units from the phantom 71 would leave 60 phantom units aboard
// and re-arm the identical rejection on the next leg. Mutation target: replacing the
// absolute reconcile in persistCargoDelta with the plain delta leaves 60 here.
func TestSellCargo_Shortfall4219_ReconcilesCachedHoldToServerTruth(t *testing.T) {
	api := &shortfallFakeAPI{serverUnits: 11, pricePerUnit: 3000}

	_, _, ship := runShortfallSell(t, api, 71, 71, 0)

	if held := heldUnits(ship, shortfallGood); held != 0 {
		t.Fatalf("the cached hold must be resynced to the server's post-sale truth (0), got %d phantom units", held)
	}
}

// THE GUARD (d): the server count is authoritative and the clamp is min(planned,
// actual) — it must NEVER become a path to requesting units we do not hold. A payload
// claiming MORE aboard than we asked for is self-contradictory, so it is distrusted
// entirely: no retry at all, and the original rejection stands. Mutation target:
// dropping the `onHand >= planned` refusal makes the handler re-send a tranche (and,
// if the clamp were written as a bare `onHand`, a LARGER one).
func TestSellCargo_Shortfall4219_NeverClampsUpFromServerCount(t *testing.T) {
	api := &shortfallFakeAPI{
		serverUnits:  11,
		pricePerUnit: 3000,
		// A contradictory rejection: "you asked for 40, and the hull has 200".
		rejectWith: cargoShortfall4219(shortfallShip, shortfallGood, 40, 200),
	}

	_, err, _ := runShortfallSell(t, api, 40, 40, 0)

	require.Error(t, err, "a self-contradictory shortfall payload must be distrusted, leaving the rejection standing")
	if len(api.sells) != 1 {
		t.Fatalf("no retry may be attempted from a count at or above the planned size, got %v", api.sells)
	}
	for _, units := range api.sells {
		if units > 40 {
			t.Fatalf("the handler must NEVER ask to sell more than it planned (never clamp upward), got %v", api.sells)
		}
	}
}

// A count EQUAL to the planned size is equally unusable — re-sending the identical
// tranche the server just refused is a wasted call, not a reconcile. Mutation target:
// weakening the refusal to `onHand > planned` admits this case.
func TestSellCargo_Shortfall4219_CountEqualToPlanned_NoRetry(t *testing.T) {
	api := &shortfallFakeAPI{
		serverUnits:  11,
		pricePerUnit: 3000,
		rejectWith:   cargoShortfall4219(shortfallShip, shortfallGood, 40, 40),
	}

	_, err, _ := runShortfallSell(t, api, 40, 40, 0)

	require.Error(t, err)
	if len(api.sells) != 1 {
		t.Fatalf("a count equal to the planned size must not trigger a retry, got %v", api.sells)
	}
}

// A hull that genuinely holds NONE of the good: the count is trusted (it heals the
// phantom cache) but no retry is possible, so the transaction fails — and critically
// no zero-unit sell is sent. Mutation target: removing the `onHand <= 0` branch sends
// a 0-unit tranche to the API.
func TestSellCargo_Shortfall4219_ZeroUnitsAboard_HealsCacheAndFailsWithoutRetry(t *testing.T) {
	api := &shortfallFakeAPI{serverUnits: 0, pricePerUnit: 3000}

	_, err, ship := runShortfallSell(t, api, 71, 71, 0)

	require.Error(t, err, "a hull holding none of the good cannot sell it")
	if len(api.sells) != 1 {
		t.Fatalf("no retry may be sent when the server reports an empty hold, got %v", api.sells)
	}
	if held := heldUnits(ship, shortfallGood); held != 0 {
		t.Fatalf("the phantom hold must still be resynced to 0 even though the sale failed, got %d", held)
	}
}

// (b): only if the CLAMPED retry also fails does the leg fail. The retry is attempted
// exactly once — never a second time. Mutation target: a retry loop instead of a
// single attempt shows more than two calls here.
func TestSellCargo_Shortfall4219_ClampedRetryAlsoFails_TransactionFails(t *testing.T) {
	api := &shortfallFakeAPI{serverUnits: 11, pricePerUnit: 3000, rejectEverything: true}

	_, err, _ := runShortfallSell(t, api, 71, 71, 0)

	require.Error(t, err, "a clamped retry that also fails must fail the transaction")
	if len(api.sells) != 2 {
		t.Fatalf("the clamped retry must be attempted exactly ONCE, got %v", api.sells)
	}
}

// FAIL CLOSED: the count is authoritative only for the good the payload names. A
// rejection describing a DIFFERENT good tells us nothing about this one, so no retry
// is attempted. Mutation target: dropping the tradeSymbol check in serverCargoUnits.
func TestSellCargo_Shortfall4219_PayloadNamesDifferentGood_NoRetry(t *testing.T) {
	api := &shortfallFakeAPI{
		serverUnits:  11,
		pricePerUnit: 3000,
		rejectWith:   cargoShortfall4219(shortfallShip, "FERTILIZER", 71, 11),
	}

	_, err, ship := runShortfallSell(t, api, 71, 71, 0)

	require.Error(t, err)
	if len(api.sells) != 1 {
		t.Fatalf("a shortfall naming a different good must not drive a retry of this one, got %v", api.sells)
	}
	if held := heldUnits(ship, shortfallGood); held != 71 {
		t.Fatalf("a rejection about another good must not touch this good's cached hold, got %d (was 71)", held)
	}
}

// FAIL CLOSED: a 4219 body with no cargoUnits states no count. An absent field must
// never be read as zero, and must never drive a retry. Mutation target: making
// CargoUnits a value (not a pointer) in cargoShortfallEnvelope reads absent as 0.
func TestSellCargo_Shortfall4219_PayloadWithoutCount_NoRetry(t *testing.T) {
	api := &shortfallFakeAPI{
		serverUnits:  11,
		pricePerUnit: 3000,
		rejectWith: fmt.Errorf("failed to sell cargo: %w", &domainPorts.APIError{StatusCode: 400,
			Body: `{"error":{"message":"Ship cargo does not contain 71 unit(s) of MACHINERY.","code":4219,"data":{"shipSymbol":"TORWIND-D7","tradeSymbol":"MACHINERY"}}}`}),
	}

	_, err, ship := runShortfallSell(t, api, 71, 71, 0)

	require.Error(t, err)
	if len(api.sells) != 1 {
		t.Fatalf("a 4219 that states no count must not drive a retry, got %v", api.sells)
	}
	// The harm an absent-reads-as-zero parse would do is not a stray retry — it is
	// silently wiping a hold the payload never described.
	if held := heldUnits(ship, shortfallGood); held != 71 {
		t.Fatalf("a payload stating no count must leave the cached hold untouched, got %d (was 71)", held)
	}
}

// A rejection that is NOT a cargo shortfall says nothing about the hold: unchanged
// behaviour, no retry. Mutation target: dropping the code check in serverCargoUnits.
func TestSellCargo_NonShortfallRejection_NoRetry(t *testing.T) {
	api := &shortfallFakeAPI{
		serverUnits:  11,
		pricePerUnit: 3000,
		rejectWith: fmt.Errorf("failed to sell cargo: %w", &domainPorts.APIError{StatusCode: 400,
			Body: `{"error":{"message":"Market does not import MACHINERY.","code":4602,"data":{"cargoUnits":11}}}`}),
	}

	_, err, ship := runShortfallSell(t, api, 71, 71, 0)

	require.Error(t, err)
	if len(api.sells) != 1 {
		t.Fatalf("a non-4219 rejection must not drive a clamp-and-retry, got %v", api.sells)
	}
	if held := heldUnits(ship, shortfallGood); held != 71 {
		t.Fatalf("a rejection that says nothing about the hold must not rewrite it, got %d (was 71)", held)
	}
}

// The reconcile is a SELL-side containment. A purchase rejection is left exactly as it
// was — a 4219 on a buy says nothing about what we may sell. Mutation target: dropping
// the transaction-type guard in retrySellClampedToServerCargo.
func TestPurchaseCargo_Shortfall4219_NoRetry(t *testing.T) {
	api := &shortfallFakeAPI{serverUnits: 11, pricePerUnit: 3000}
	shipRepo := &buyFakeShipRepo{ship: newLadenHull(t, shortfallGood, 0, 80)}
	playerRepo := &buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(1), "TORWIND", "tok")}
	h := NewPurchaseCargoHandler(shipRepo, playerRepo, api, &shortfallMarketRepo{}, &buyRecordingMediator{}, nil)

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	_, err := h.Handle(ctx, &PurchaseCargoCommand{
		ShipSymbol: shortfallShip, GoodSymbol: shortfallGood, Units: 40, PlayerID: shared.MustNewPlayerID(1),
	})

	require.Error(t, err)
	if len(api.buys) != 1 {
		t.Fatalf("a purchase rejection must not be clamped and retried, got %v", api.buys)
	}
}

// THE DRIFT (c): a multi-tranche sell whose LATER tranche fails used to return before
// the cargo persist, orphaning the units its earlier tranches had really sold — the
// ledger recorded them, the hold did not, and the cached count stayed permanently
// ahead of the server. That is a standing producer of the very 4219 above: it is how
// a hull comes to believe it holds 71 units it sold long ago.
//
// Here 60 of 80 units sell across three tranches of 20 and the fourth is rejected by
// an unrelated error. The cached hold must fall to 20. Mutation target: moving the
// persistCargoDelta call on the failure path back inside the success-only path leaves
// the full 80 aboard.
func TestSellCargo_PartialFailure_PersistsUnitsAlreadySold(t *testing.T) {
	api := &shortfallFakeAPI{serverUnits: 60, pricePerUnit: 100}
	// serverUnits 60 with a 20-unit tranche limit: tranches 1-3 sell (60 units), the
	// fourth finds an empty server hold. rejectWith stays nil so the rejection is the
	// genuine shortfall the miniature server raises.
	_, err, ship := runShortfallSell(t, api, 80, 80, 20)

	// The fourth tranche 4219s with cargoUnits=0, which the reconcile trusts: it heals
	// the cache to server truth (0) rather than merely deducting the 60 that sold.
	require.Error(t, err, "the good is exhausted server-side, so the sale cannot complete its planned size")
	if held := heldUnits(ship, shortfallGood); held != 0 {
		t.Fatalf("units sold by earlier tranches must be written back on the failure path (server truth 0), got %d", held)
	}
	if len(api.sells) != 4 {
		t.Fatalf("expected three selling tranches plus the exhausted fourth, got %v", api.sells)
	}
}

// The same drift, isolated from the 4219 reconcile: the later tranche fails with an
// error that carries NO server count, so only the plain delta can be written back.
// Mutation target: the failure-path persistCargoDelta call — without it the cache
// keeps all 80 units after 40 have really sold, which is the desync itself.
func TestSellCargo_PartialFailure_UnparseableError_StillPersistsSoldUnits(t *testing.T) {
	api := &shortfallFakeAPI{serverUnits: 40, pricePerUnit: 100}
	ship := newLadenHull(t, shortfallGood, 80, 80)
	shipRepo := &buyFakeShipRepo{ship: ship}
	playerRepo := &buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(1), "TORWIND", "tok")}

	// A transport-level failure after the selling tranches: no JSON, no count.
	poisoned := &poisonAfterNSells{inner: api, failAfter: 2, err: fmt.Errorf("max retries exceeded: connection reset")}
	h := NewSellCargoHandler(shipRepo, playerRepo, poisoned, &shortfallMarketRepo{limit: 20}, &buyRecordingMediator{}, nil)

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	_, err := h.Handle(ctx, &SellCargoCommand{
		ShipSymbol: shortfallShip, GoodSymbol: shortfallGood, Units: 80, PlayerID: shared.MustNewPlayerID(1),
	})

	require.Error(t, err)
	if held := heldUnits(ship, shortfallGood); held != 40 {
		t.Fatalf("the 40 units two tranches really sold must be deducted from the cache on the failure path, got %d held", held)
	}
}

// poisonAfterNSells lets the first failAfter sells through to the inner fake, then
// fails every later one with a non-API error (no parseable body).
type poisonAfterNSells struct {
	domainPorts.APIClient
	inner     *shortfallFakeAPI
	failAfter int
	err       error
	calls     int
}

func (c *poisonAfterNSells) SellCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*domainPorts.SellResult, error) {
	c.calls++
	if c.calls > c.failAfter {
		return nil, c.err
	}
	return c.inner.SellCargo(ctx, shipSymbol, goodSymbol, units, token)
}
