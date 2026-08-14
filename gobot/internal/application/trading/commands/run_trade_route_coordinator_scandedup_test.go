package commands

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-7q61f: the scan-dedup A/B test. flyVisits' per-visit loop scans the SAME
// waypoint twice on the first visit of a lane commitment — an arrival scan via
// travel(), then ~20 lines later staleAskAborts' own unconditional Earning-class
// live scan — with nothing in between (no buy/sell, only a dock()) that could
// move the price. These tests prove the reuse this bead adds is provably safe:
// it never changes what the guard decides, only whether a request is spent, and
// only for ships an operator has explicitly armed.

const (
	dedupSystem = "X1-DEDUP"
	dedupSrc    = "X1-DEDUP-EXPORT" // BUY here; the hull docks HERE (= circuit source)
	dedupDst    = "X1-DEDUP-IMPORT" // SELL here
	dedupGood   = "ASSAULT_RIFLES"

	dedupRankedAsk = 500  // the ranked basis the lane was ranked on
	dedupStartBid  = 2608 // clears the floor against the ranked basis (2608-500=2108/u)
	dedupDstAsk    = 2700
	dedupVol       = 60
	dedupDecayStep = 600
)

// dedupMarketRepo serves a source ask whose "last scanned" timestamp is fully
// test-controlled, so a test can simulate either "this row reflects a scan from
// THIS visit's own arrival" (fresh, at/after the bracket floor) or "this row
// predates it" (stale) without depending on real wall-clock timing.
type dedupMarketRepo struct {
	market.MarketRepository
	mu         sync.Mutex
	srcAsk     int
	srcUpdated time.Time
	sellCount  int
}

func (r *dedupMarketRepo) FindAllMarketsInSystem(_ context.Context, _ string, _ int) ([]string, error) {
	return []string{dedupSrc, dedupDst}, nil
}

func (r *dedupMarketRepo) currentSrcAsk() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.srcAsk
}

func (r *dedupMarketRepo) setSrcAsk(ask int, updated time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.srcAsk = ask
	r.srcUpdated = updated
}

func (r *dedupMarketRepo) destBid() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return dedupStartBid - dedupDecayStep*r.sellCount
}

func (r *dedupMarketRepo) recordSell() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sellCount++
}

func (r *dedupMarketRepo) GetMarketData(_ context.Context, waypointSymbol string, _ int) (*market.Market, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	supply := "MODERATE"
	activity := "STRONG"
	switch waypointSymbol {
	case dedupSrc:
		good, err := market.NewTradeGood(dedupGood, &supply, &activity, r.srcAsk, r.srcAsk-20, dedupVol, market.TradeTypeExport)
		if err != nil {
			return nil, err
		}
		updated := r.srcUpdated
		if updated.IsZero() {
			updated = time.Now()
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, updated)
	case dedupDst:
		good, err := market.NewTradeGood(dedupGood, &supply, &activity, dedupDstAsk, dedupStartBid-dedupDecayStep*r.sellCount, dedupVol, market.TradeTypeImport)
		if err != nil {
			return nil, err
		}
		return market.NewMarket(waypointSymbol, []market.TradeGood{*good}, time.Now())
	}
	return nil, nil
}

// dedupRefresher stands in for the live MarketScanner: every call is counted
// (the observable proxy for "an API request was spent"), and on success it
// overwrites the source row with refreshAsk, stamped at the clock's current
// instant — modelling exactly what a real live GetMarket call would do.
type dedupRefresher struct {
	mu         sync.Mutex
	calls      int
	repo       *dedupMarketRepo
	refreshAsk int
	clock      *shared.MockClock
}

func (r *dedupRefresher) ScanAndSaveMarket(_ context.Context, _ uint, waypointSymbol string) error {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	if waypointSymbol == dedupSrc {
		r.repo.setSrcAsk(r.refreshAsk, r.clock.Now())
	}
	return nil
}

func (r *dedupRefresher) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// dedupMediator fakes the purchase/sell dispatch exactly as staleMediator does
// (run_trade_route_coordinator_staleask_test.go), reading the buy price off the
// market repo's CURRENT source ask so a test can observe whether a purchase
// landed at the stale cached ask or the freshly-scanned one.
type dedupMediator struct {
	mu        sync.Mutex
	repo      *dedupMarketRepo
	purchases []*shipCargo.PurchaseCargoCommand
	sells     []*shipCargo.SellCargoCommand
}

func (m *dedupMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipCargo.PurchaseCargoCommand:
		m.mu.Lock()
		m.purchases = append(m.purchases, cmd)
		m.mu.Unlock()
		return &shipCargo.PurchaseCargoResponse{TotalCost: cmd.Units * m.repo.currentSrcAsk(), UnitsAdded: cmd.Units, TransactionCount: 1}, nil
	case *shipCargo.SellCargoCommand:
		m.mu.Lock()
		m.sells = append(m.sells, cmd)
		m.mu.Unlock()
		bid := m.repo.destBid()
		m.repo.recordSell()
		return &shipCargo.SellCargoResponse{TotalRevenue: cmd.Units * bid, UnitsSold: cmd.Units, TransactionCount: 1}, nil
	default:
		return nil, nil
	}
}

func (m *dedupMediator) Register(_ reflect.Type, _ common.RequestHandler) error { return nil }
func (m *dedupMediator) RegisterMiddleware(_ common.Middleware)                 {}

// inMemoryScanDedupAllowlist is the test double for ScanDedupAllowlist: a plain
// set, satisfying the port's contract without any storage mechanism opinion.
type inMemoryScanDedupAllowlist struct {
	armed map[string]bool
}

func (a *inMemoryScanDedupAllowlist) IsArmed(_ context.Context, _ int, shipSymbol string) (bool, error) {
	return a.armed[shipSymbol], nil
}

func newDedupHarness(
	t *testing.T,
	ship *navigation.Ship,
	repo *dedupMarketRepo,
	refresher MarketRefresher,
	allowlist ScanDedupAllowlist,
	clock shared.Clock,
) (*RunTradeRouteCoordinatorHandler, *dedupMediator, *trFakeShipRepo) {
	t.Helper()
	mediator := &dedupMediator{repo: repo}
	shipRepo := &trFakeShipRepo{ship: ship}
	handler := NewRunTradeRouteCoordinatorHandler(mediator, shipRepo, repo, refresher, clock, nil)
	handler.SetScanDedupAllowlist(allowlist)
	return handler, mediator, shipRepo
}

// runDedupCircuit drives the handler through its ONE driving port (Handle) —
// port-to-port, Mandate 3 — and returns the typed response.
func runDedupCircuit(t *testing.T, handler *RunTradeRouteCoordinatorHandler, shipSymbol string) *RunTradeRouteCoordinatorResponse {
	t.Helper()
	resp, err := handler.Handle(context.Background(), &RunTradeRouteCoordinatorCommand{
		ShipSymbol:   shipSymbol,
		SystemSymbol: dedupSystem,
		PlayerID:     1,
	})
	require.NoError(t, err)
	coord, ok := resp.(*RunTradeRouteCoordinatorResponse)
	require.True(t, ok, "expected *RunTradeRouteCoordinatorResponse, got %T", resp)
	return coord
}

// --- Category 1: the guard is NOT weakened -----------------------------------

// TestScanDedup_ShipArmedButRowNotProvenFresh_StillDoesLiveScan proves the guard's
// protection survives even on an ARMED ship: when the cached row cannot prove it
// reflects THIS visit's own arrival (here: it predates the visit's travel
// bracket entirely, modelling either real elapsed time or that no fresh arrival
// scan actually landed), staleAskAborts falls through to its unconditional live
// scan exactly as before. The stale cached ask (500, within tolerance) would
// wrongly let the circuit proceed if dedup had incorrectly reused it; the fresh
// scan instead reveals 1800 (3.6x, an abort) — so an incorrect dedup hit would
// flip this test to a false negative, making it a real falsifiable proof, not a
// tautology.
func TestScanDedup_ShipArmedButRowNotProvenFresh_StillDoesLiveScan(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	repo := &dedupMarketRepo{}
	// The cached row is from just before this visit's own travel bracket would
	// open — old enough that it cannot be proven to be THIS visit's own arrival
	// scan (never eligible, whatever the allowlist says), yet fresh enough to
	// still clear the lane ranker's own listing-freshness gate, so the circuit
	// reaches the guard at all rather than exiting no_lanes beforehand.
	repo.setSrcAsk(dedupRankedAsk, clock.CurrentTime.Add(-30*time.Second))
	refresher := &dedupRefresher{repo: repo, refreshAsk: 1800, clock: clock} // 500 -> 1800 = 3.6x, beyond tolerance
	allowlist := &inMemoryScanDedupAllowlist{armed: map[string]bool{"DEDUP-1": true}}

	ship := newDiscHauler(t, "DEDUP-1", dedupSrc)
	handler, mediator, shipRepo := newDedupHarness(t, ship, repo, refresher, allowlist, clock)
	_ = shipRepo

	coord := runDedupCircuit(t, handler, ship.ShipSymbol())

	require.True(t, coord.StaleAskAbort, "the guard must still verify live and abort on the moved ask, dedup armed or not")
	require.Equal(t, 1800, coord.LiveSourceAsk, "the abort must be driven by the FRESH scan's value, not the stale cache")
	require.GreaterOrEqual(t, refresher.callCount(), 1, "an unprovable-fresh row must still spend a live scan, exactly as before dedup existed")
	require.Empty(t, mediator.purchases, "aborting on a moved basis must not buy")
}

// TestScanDedup_NotArmed_EmptyAllowlist_UnchangedFromToday proves the default-off
// path is byte-identical to current behavior: a non-nil allowlist that simply
// does not contain this ship must behave exactly like no allowlist at all — the
// row IS fresh enough to have been dedup-eligible (it satisfies every OTHER
// condition), yet because the ship is not armed, the live scan still fires. This
// is the "empty allowlist = current behavior, no diff" requirement made
// concrete: only allowlist membership may ever change the outcome.
func TestScanDedup_NotArmed_EmptyAllowlist_UnchangedFromToday(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	repo := &dedupMarketRepo{}
	repo.setSrcAsk(dedupRankedAsk, clock.CurrentTime) // freshest possible row — would be eligible if armed
	refresher := &dedupRefresher{repo: repo, refreshAsk: 550, clock: clock}
	allowlist := &inMemoryScanDedupAllowlist{armed: map[string]bool{}} // explicitly empty

	ship := newDiscHauler(t, "PLAIN-1", dedupSrc)
	handler, mediator, _ := newDedupHarness(t, ship, repo, refresher, allowlist, clock)

	coord := runDedupCircuit(t, handler, ship.ShipSymbol())

	require.False(t, coord.StaleAskAbort, "a +10% move is within tolerance")
	require.GreaterOrEqual(t, refresher.callCount(), 1, "an unarmed ship must always take the live scan — empty allowlist is byte-identical to no allowlist")
	require.NotEmpty(t, mediator.purchases, "a within-tolerance basis must actually buy")
}

// --- Category 2: correct dedup -----------------------------------------------

// TestScanDedup_ArmedShipBackToBackSameVisit_ReusesArrivalScan is the true
// back-to-back same-iteration case: an ALLOWLISTED ship whose visit's own
// travel bracket brackets a row proven fresh (LastUpdated at the bracket
// floor). staleAskAborts must reuse it — spending ZERO live scans — while
// still landing the SAME verdict a fresh scan would have (within tolerance,
// proceed and buy), and the saved-call counter must record the reuse.
func TestScanDedup_ArmedShipBackToBackSameVisit_ReusesArrivalScan(t *testing.T) {
	prevRegistry := metrics.Registry
	t.Cleanup(func() { metrics.Registry = prevRegistry })
	metrics.Registry = prometheus.NewRegistry()
	dedupCollector := metrics.NewScanDedupMetricsCollector()
	require.NoError(t, dedupCollector.Register())
	metrics.SetGlobalScanDedupCollector(dedupCollector)
	t.Cleanup(func() { metrics.SetGlobalScanDedupCollector(nil) })

	clock := &shared.MockClock{CurrentTime: time.Now()}
	repo := &dedupMarketRepo{}
	// Fresh as of the exact instant the bracket will open at — the true
	// back-to-back case: nothing separates the arrival scan from the guard check.
	repo.setSrcAsk(550, clock.CurrentTime)                                   // +10% from ranked 500 - within tolerance
	refresher := &dedupRefresher{repo: repo, refreshAsk: 9999, clock: clock} // would be beyond tolerance IF ever called
	allowlist := &inMemoryScanDedupAllowlist{armed: map[string]bool{"DEDUP-2": true}}

	ship := newDiscHauler(t, "DEDUP-2", dedupSrc)
	handler, mediator, _ := newDedupHarness(t, ship, repo, refresher, allowlist, clock)

	coord := runDedupCircuit(t, handler, ship.ShipSymbol())

	require.False(t, coord.StaleAskAbort, "the deduped read (550, +10%%) is within tolerance")
	require.Equal(t, 0, refresher.callCount(), "an eligible reuse must spend ZERO live scans for the guard check")
	require.NotEmpty(t, mediator.purchases, "the circuit must still fly and buy, using the reused price")
	require.GreaterOrEqual(t, coord.Visits, 1)

	families, err := metrics.Registry.Gather()
	require.NoError(t, err)
	found := false
	for _, f := range families {
		if f.GetName() != "spacetraders_daemon_scan_dedup_calls_saved_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			if m.GetCounter().GetValue() > 0 {
				found = true
			}
		}
	}
	require.True(t, found, "the scan_dedup_calls_saved_total counter must record the reuse")
}
