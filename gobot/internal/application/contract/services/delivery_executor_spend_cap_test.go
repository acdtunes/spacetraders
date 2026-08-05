package services

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// sp-ps2oc acceptance 4: THREE OPERATIONS SHARE ONE TREASURY.
//
// construction_supply, contract and tour all bought inside the same 90-minute window —
// PURCHASE_CARGO -761,919 against SELL_CARGO +326,938 — with no arbitration between them. A cap
// that serialised construction against itself would still have left a contract source-buy free
// to race the same float, so this suite drives the contract side of the SHARED cap.
//
// affordableSourceBuyLot is untouched and still enforces the 50k contract floor per buy. What
// is added is the cross-operation reservation, and the asymmetry between the two floors is
// deliberate and preserved: construction reserves against the 150k non-contract floor while
// contract reserves against the 50k one, so the contract-exclusive 50k-150k band (sp-q8bon)
// survives a shared ledger intact.

// recordingCapLedger is a scripted ConcurrentSpendLedger. It INVOKES readBudget, deliberately:
// the real ledger reads the balance inside its own critical section, and a fake that skipped
// the callback would leave the contract side's budget resolution entirely unexercised.
type recordingCapLedger struct {
	mu           sync.Mutex
	ok           bool
	err          error
	reserveCalls int
	released     []string
	gotCredits   int64
	gotFloor     int
	gotCost      int
	gotPlayerIDs []int
}

func (l *recordingCapLedger) Reserve(ctx context.Context, playerID int, _ string, projectedCost int, readBudget func(context.Context) (int64, int, error)) (string, bool, error) {
	l.mu.Lock()
	l.reserveCalls++
	l.gotCost = projectedCost
	l.gotPlayerIDs = append(l.gotPlayerIDs, playerID)
	l.mu.Unlock()

	credits, floor, err := readBudget(ctx)
	l.mu.Lock()
	l.gotCredits, l.gotFloor = credits, floor
	l.mu.Unlock()
	if err != nil {
		return "", false, err
	}
	if l.err != nil {
		return "", false, l.err
	}
	if !l.ok {
		return "", false, nil
	}
	return "CONTRACT-RES-1", true, nil
}

func (l *recordingCapLedger) Release(_ context.Context, _ int, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.released = append(l.released, id)
	return nil
}

// runContractSourceBuy drives one contract source-buy through the real delivery path with the
// given cap wired, and reports the units actually purchased plus whether the buy parked.
func runContractSourceBuy(t *testing.T, ledger ConcurrentSpendLedger, liveCredits, unitAsk int) (purchased []int, parked bool, logs *capturingLogger) {
	t.Helper()

	ship := buildShipWithIronOre(t, 0)
	shipRepo := &reconcileFakeShipRepo{cached: ship, server: ship}
	med := &sourceFloorFakeMediator{navShip: ship, liveCredits: liveCredits}
	executor := NewDeliveryExecutor(med, shipRepo, NewCargoManager(med, shipRepo),
		WithSourceBuyFloor(), WithConcurrentSpendCap(ledger))

	logs = &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logs)

	delivery := domainContract.Delivery{
		TradeSymbol: "IRON_ORE", DestinationSymbol: "X1-TEST-A1", UnitsRequired: 18,
	}
	profit := &contractQueries.ProfitabilityResult{
		PurchaseCost:           18 * unitAsk,
		CheapestMarketWaypoint: "X1-TEST-M1",
		MarketPrices:           map[string]int{"IRON_ORE": unitAsk},
	}

	_, err := executor.ProcessSingleDelivery(ctx, "TORWIND-1", shared.MustNewPlayerID(1), nil, delivery, profit, &RunWorkflowResponse{}, nil)
	var insufficient *ErrInsufficientCredits
	return med.purchasedUnits, errors.As(err, &insufficient), logs
}

// ACCEPTANCE 4. A contract source-buy consults the SHARED cap, and parks when the combined
// in-flight spend across operations would breach — even though its own cost clears its floor.
func TestContractSourceBuy_ParksOnAggregateHeadroom(t *testing.T) {
	// 500k treasury against a 36k lot: the per-buy floor passes comfortably (500k-36k = 464k,
	// far above the 50k contract floor), so a park here can ONLY come from the aggregate cap.
	ledger := &recordingCapLedger{ok: false}
	purchased, parked, logs := runContractSourceBuy(t, ledger, 500_000, 2_000)

	require.True(t, parked, "a contract buy refused by the aggregate cap must PARK, not proceed")
	require.Empty(t, purchased, "a cap-parked buy must dispatch ZERO purchases")
	require.Equal(t, 1, ledger.reserveCalls, "the contract source-buy must consult the shared cap exactly once")
	require.Empty(t, ledger.released, "a rejected reservation is rolled back by the ledger — the executor must not release it")

	// The park must be legible without ledger archaeology: that is the whole point of
	// acceptance 5, and the container log renderer drops the metadata map.
	require.True(t, warningsContain(logs, "concurrent spend cap"),
		"expected a WARNING naming the concurrent spend cap, got %v", logs.warnings())
}

// ACCEPTANCE 4, floor direction. The contract side must reserve against ITS OWN 50k floor, not
// construction's 150k one. Sharing a ledger must not silently raise the contract floor — that
// would recreate exactly the full-economy deadlock the contract-exclusive band prevents
// (sp-q8bon), where the sole earner parks and nothing can refill the treasury.
func TestContractSourceBuy_ReservesAgainstTheContractFloorNotTheConstructionFloor(t *testing.T) {
	ledger := &recordingCapLedger{ok: true}
	_, parked, _ := runContractSourceBuy(t, ledger, 500_000, 2_000)

	require.False(t, parked, "an accepted reservation must let the buy proceed")
	require.Equal(t, common.ImmutableReserveFloor, ledger.gotFloor,
		"the contract source-buy must reserve against the 50k contract floor. Passing construction's %d non-contract floor would starve the sole earner out of the band reserved for it",
		common.NonContractWorkingCapitalFloor)
	require.NotEqual(t, common.NonContractWorkingCapitalFloor, ledger.gotFloor,
		"the two floors must stay distinct on a shared ledger")
	require.Equal(t, int64(500_000), ledger.gotCredits, "the cap must judge against the live treasury it read")
}

// ACCEPTANCE 2. The reservation is taken BEFORE the buy and RELEASED afterwards — including
// when the buy fails. A reservation leaked on the failure path holds budget nobody frees and
// wedges every other spender until the staleness sweep.
func TestContractSourceBuy_ReleasesReservationAfterTheBuy(t *testing.T) {
	ledger := &recordingCapLedger{ok: true}
	purchased, parked, _ := runContractSourceBuy(t, ledger, 500_000, 2_000)

	require.False(t, parked)
	require.NotEmpty(t, purchased, "the buy must proceed when the cap accepts")
	require.Equal(t, []string{"CONTRACT-RES-1"}, ledger.released,
		"the reservation must be released exactly once after the buy completes")
}

// A ledger error must fail CLOSED. A cap that waved a buy through when its own bookkeeping
// failed would defeat its purpose (RULINGS #4).
func TestContractSourceBuy_FailsClosedOnLedgerError(t *testing.T) {
	ledger := &recordingCapLedger{err: errors.New("ledger unavailable")}
	purchased, parked, logs := runContractSourceBuy(t, ledger, 500_000, 2_000)

	require.True(t, parked, "a cap error must park the buy (fail-closed)")
	require.Empty(t, purchased, "a fail-closed park must dispatch ZERO purchases")
	require.True(t, warningsContain(logs, "fail-closed"), "expected a fail-closed WARNING, got %v", logs.warnings())
}

// The optional-port contract: no cap wired leaves the contract source-buy exactly as it was.
// Every existing caller and test constructs the executor without it.
func TestContractSourceBuy_NoCapWiredIsUnchanged(t *testing.T) {
	purchased, parked, _ := runContractSourceBuy(t, nil, 500_000, 2_000)

	require.False(t, parked, "an unwired cap must not park anything")
	require.NotEmpty(t, purchased, "an unwired cap must leave the buy byte-identical")
}

// ACCEPTANCE 4, THE REAL ARBITRATION. A construction_supply reservation in flight and a
// contract source-buy racing it must serialise against the SAME float — the claim a
// construction-only cap cannot make.
//
// This drives the REAL ledger, not a fake, because the thing under test IS the shared state:
// the construction side holds a reservation against the 150k non-contract floor, and the
// contract buy must see that in-flight exposure in its own check.
//
// Numbers: treasury 200,000. A construction buy of 145,000 is in flight (already reserved).
// The contract lot costs 36,000. Against the contract's own 50k floor the lot alone is fine
// (200,000 − 36,000 = 164,000), but 200,000 − 145,000 − 36,000 = 19,000 is BELOW it. Only the
// combined exposure breaches, and only a shared ledger can see it.
func TestContractSourceBuy_SerialisesAgainstAnInFlightConstructionBuy(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	sharedCap := persistence.NewSpendReservationLedger(db)

	const (
		treasury          = 200_000
		constructionSpend = 145_000
		unitAsk           = 2_000 // 18 units wanted, hull capacity 20 -> a 36,000 lot
	)

	// The construction_supply buy takes its reservation first and HOLDS it across its purchase,
	// exactly as buyInputTranche does. What this test pins is that the CONTRACT side sees that
	// exposure, so the construction reservation only has to be genuinely admitted and in
	// flight — the floor it was admitted under is not the subject here, and 50k is used simply
	// because it is the figure that admits a 145,000 buy against a 200,000 treasury.
	constructionRes, ok, err := sharedCap.Reserve(context.Background(), 1, "construction-gate", constructionSpend,
		func(context.Context) (int64, int, error) { return treasury, common.ImmutableReserveFloor, nil })
	require.NoError(t, err)
	require.True(t, ok, "the construction reservation must be admitted so it is genuinely IN FLIGHT for the contract check")
	require.NotEmpty(t, constructionRes)

	purchased, parked, logs := runContractSourceBuy(t, sharedCap, treasury, unitAsk)

	require.True(t, parked,
		"a contract source-buy must park while a %d construction_supply buy is in flight: its own %d lot clears the %d contract floor against a %d treasury, but the COMBINED exposure leaves %d. This is the arbitration a construction-only cap cannot perform",
		constructionSpend, 18*unitAsk, common.ImmutableReserveFloor, treasury, treasury-constructionSpend-18*unitAsk)
	require.Empty(t, purchased, "the parked contract buy must dispatch ZERO purchases")
	require.True(t, warningsContain(logs, "concurrent spend cap"), "expected the cross-operation park WARNING, got %v", logs.warnings())

	// And once construction's buy completes and releases, the contract buy is admitted again:
	// the cap must throttle, never permanently wedge, the sole earner.
	require.NoError(t, sharedCap.Release(context.Background(), 1, constructionRes))
	purchasedAfter, parkedAfter, _ := runContractSourceBuy(t, sharedCap, treasury, unitAsk)
	require.False(t, parkedAfter, "once the competing reservation releases, the contract buy must proceed — a cap that never re-admits is a deadlock")
	require.NotEmpty(t, purchasedAfter)
}

// warningsContain reports whether any captured WARNING mentions substr.
func warningsContain(l *capturingLogger, substr string) bool {
	for _, w := range l.warnings() {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// ACCEPTANCE 5, contract side. The contract cap must emit its own labelled denial. A single
// merged series could not answer WHICH operation is being turned away, which is the only
// question an operator has when three spenders contend for one treasury.
func TestContractCapDenial_EmitsTheAggregateDenialCounter(t *testing.T) {
	collector := metrics.NewSpendCapMetricsCollector()
	metrics.SetGlobalSpendCapCollector(collector)
	t.Cleanup(func() { metrics.SetGlobalSpendCapCollector(nil) })

	before := collector.AggregateDenialCount("contract")
	beforeConstruction := collector.AggregateDenialCount("construction_supply")

	_, parked, _ := runContractSourceBuy(t, &recordingCapLedger{ok: false}, 500_000, 2_000)
	require.True(t, parked, "fixture check: the cap must actually deny, or this test proves nothing")

	require.Equal(t, before+1, collector.AggregateDenialCount("contract"),
		"a contract aggregate denial must increment the contract series")
	require.Equal(t, beforeConstruction, collector.AggregateDenialCount("construction_supply"),
		"it must NOT be counted as construction — the label is what makes the counter actionable")
}
