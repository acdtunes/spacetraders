package services

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// sp-ps2oc. THE AGGREGATE BREACH, reproduced against the REAL ledger.
//
// The existing cap suite (production_executor_spend_cap_test.go) proves the executor<->ledger
// CONTRACT with a scripted fake: park on rejection, park on error, release after the buy. What
// no fake can show is the thing that actually happened in staging — N buyers each passing a
// guard that is individually correct, and breaching the reserve in AGGREGATE:
//
//	13:41:49.306986  PURCHASE_CARGO  construction_supply  -99,660  balance_after 115542
//	13:41:49.374545  PURCHASE_CARGO  construction_supply  -98,714  balance_after 115542
//	13:41:49.374580  PURCHASE_CARGO  construction_supply  -98,714  balance_after 115542
//
// Two of those are 35 MICROSECONDS apart and all three record an IDENTICAL balance_after: each
// read the same pre-buy treasury and none observed the others.
//
// The fixture below is built to make that race REAL rather than described. racingTreasury holds
// every buyer at a one-shot barrier until ALL of them have read the balance, so each buyer's
// floor check provably sees the same pre-buy figure the incident shows — a fixture that let
// them run sequentially would pass against the unguarded code and prove nothing.
//
// Numbers are the incident's, against the real enforced floor:
//
//	treasury = reserve + 262,000 · 3 buyers × 99,000: each buy clears the reserve alone,
//	only their sum breaches it.
const (
	aggBuyCost = 99_000
	aggBuyers  = 3
)

var aggTreasury = effectiveReserveFloor() + 262_000

// racingTreasury models the treasury the money guards read, with the incident's timing.
//
// Credits reports initial MINUS committed spend — faithful to the ledger-backed reader, which
// serves the balance from the transaction ledger and therefore DOES see committed buys. The
// race is not that committed spend is invisible; it is that a sibling's spend has not committed
// yet when you read. The barrier reproduces exactly that window: the first `barrier` readers
// all block until the last one arrives, so every buyer's guard sees the same pre-buy balance,
// then they contend.
type racingTreasury struct {
	mu       sync.Mutex
	initial  int
	spent    int
	arrived  int
	barrier  int
	released chan struct{}
}

func newRacingTreasury(initial, barrier int) *racingTreasury {
	return &racingTreasury{initial: initial, barrier: barrier, released: make(chan struct{})}
}

// Credits satisfies TreasuryReader.
//
// The first `barrier` callers are the buyers' per-buy floor checks. They are held until the
// last one arrives and then ALL receive the same pre-buy balance — the incident's identical
// balance_after, and the only shape in which the aggregate breach can occur. Deliberately no
// re-read on wake: a buyer that re-read after a sibling committed would be observing that
// sibling's spend, which is precisely what the racing buyers did NOT do.
//
// Every later read (the concurrent cap's own read, which happens after the floor check)
// reports the LIVE initial − committed balance. That is faithful to the ledger-backed reader,
// which serves from the transaction ledger and does see committed buys — and it is what makes
// the cap's invariant hold under every interleaving rather than only the simultaneous one.
func (r *racingTreasury) Credits(_ context.Context, _ int) (int64, error) {
	r.mu.Lock()
	r.arrived++
	inBarrier := r.arrived <= r.barrier
	if r.arrived == r.barrier {
		close(r.released) // the last buyer arrives: everyone proceeds together
	}
	gate := r.released
	r.mu.Unlock()

	if inBarrier {
		<-gate // already closed for the last arriver; holds the earlier ones
		return int64(r.initial), nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return int64(r.initial - r.spent), nil
}

// commit records a completed buy, exactly as the cargo transaction handler records the
// transaction synchronously before the buy returns (and before the reservation is released).
func (r *racingTreasury) commit(cost int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spent += cost
}

func (r *racingTreasury) finalBalance() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.initial - r.spent
}

func (r *racingTreasury) totalSpent() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.spent
}

// raceOutcome is what one run of the concurrent-buy harness produced.
type raceOutcome struct {
	finalBalance int
	totalSpent   int
	parked       int
	reserve      int
}

// runConcurrentBuys drives `buyers` simultaneous input buys through the SAME guard sequence
// production runs — the per-buy floor (spendFloorBreached) and then the concurrent cap
// (reserveConcurrentSpendOrPark), committing the spend and releasing the reservation exactly
// where buyInputTranche does. withCap selects whether the ledger is wired at all; unwired is
// the sp-ps2oc production state, where reserveConcurrentSpendOrPark short-circuits fail-open.
func runConcurrentBuys(t *testing.T, playerID, buyers int, withCap bool) raceOutcome {
	t.Helper()

	treasury := newRacingTreasury(aggTreasury, buyers)
	executor := NewProductionExecutor(nil, nil, nil, nil, nil, nil)
	executor.SetTreasuryReader(treasury)

	var parkedMu sync.Mutex
	parked := 0

	if withCap {
		db, err := database.NewTestConnection()
		require.NoError(t, err)
		executor.SetSpendLedger(persistence.NewSpendReservationLedger(db))
		t.Cleanup(func() {
			var n int64
			require.NoError(t, db.Model(&persistence.SpendReservationModel{}).Count(&n).Error)
			require.Zero(t, n, "every reservation must be released or rolled back — a leaked row wedges the shared budget until the staleness sweep")
		})
	}

	// The container id the reservation is attributed to. It is BEST-EFFORT attribution only —
	// the ledger sums per PLAYER, not per container — which is why buys from a single
	// construction container serialise against each other exactly as N containers would.
	ctx := shared.WithOperationContext(context.Background(), &shared.OperationContext{
		ContainerID:   "construction-gate-X1-KP46",
		OperationType: "construction_supply",
	})

	var wg sync.WaitGroup
	for i := 0; i < buyers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// 1. The per-buy floor, exactly as production_executor.go's tranche loop runs it.
			//    Every buyer clears this: 412,000 − 99,000 = 313,000 ≥ 150,000.
			if breached, _ := executor.spendFloorBreached(ctx, playerID, aggBuyCost); breached {
				parkedMu.Lock()
				parked++
				parkedMu.Unlock()
				return
			}

			// 2. The concurrent cap, exactly as buyInputTranche runs it.
			resID, capParked := executor.reserveConcurrentSpendOrPark(ctx, playerID, aggBuyCost, "X1-KP46-MKT", "FAB_MATS")
			if capParked {
				parkedMu.Lock()
				parked++
				parkedMu.Unlock()
				return
			}

			// 3. The buy commits, then the reservation is released — the order the guard's own
			//    soundness argument depends on (no window where a spend is in neither ledger).
			treasury.commit(aggBuyCost)
			executor.releaseSpendReservation(ctx, playerID, resID)
		}()
	}
	wg.Wait()

	parkedMu.Lock()
	defer parkedMu.Unlock()
	return raceOutcome{
		finalBalance: treasury.finalBalance(),
		totalSpent:   treasury.totalSpent(),
		parked:       parked,
		reserve:      effectiveReserveFloor(),
	}
}

// ACCEPTANCE CRITERION 1 + 3. N simultaneous construction_supply buys whose SUM exceeds the
// reserve headroom must leave treasury at or above the reserve.
//
// THE COMPANION BELOW IS WHAT MAKES THIS TEST HONEST: the identical harness with the cap
// unwired DOES breach. Without that, a fixture that quietly serialised its buyers would pass
// here and prove nothing at all.
func TestConcurrentConstructionSupplyBuys_CannotBreachReserveInAggregate(t *testing.T) {
	out := runConcurrentBuys(t, 9201, aggBuyers, true)

	// The criterion, asserted first: behaviour before bookkeeping, so a later assertion can
	// never render this one unreachable.
	require.GreaterOrEqual(t, out.finalBalance, out.reserve,
		"%d concurrent construction_supply buys of %d each left treasury at %d, BELOW the working-capital reserve %d. Each buy is individually affordable against a %d treasury; only their sum breaches, which is exactly the sp-ps2oc drain (297,088 credits in 68ms, three buys recording an identical balance_after)",
		aggBuyers, aggBuyCost, out.finalBalance, out.reserve, aggTreasury)

	// The cap must have ACTED, not merely been present. Without this a harness whose buyers
	// never actually contended would satisfy the assertion above for the wrong reason.
	//
	// HOW MANY PARK IS NOT FIXED, AND PINNING IT TO ONE ASSERTS SOMETHING THE GUARD NEVER
	// PROMISED. A buy commits before its reservation is released — deliberately, so a spend is
	// never in neither ledger — which leaves a window where it is in BOTH: subtracted once from
	// the live treasury it just reduced, and again as its own still-open reservation. A sibling
	// evaluating inside that window sees the same spend twice and refuses on headroom that has
	// really already returned. That is the overlap the no-gap ordering costs, and it is the safe
	// direction: double-counting can only ever refuse a buy, never admit one.
	//
	// So the count depends on where the goroutines happen to land, and only its BOUNDS are the
	// guard's actual contract. Both ends are load-bearing:
	//   - none parked would mean every buyer got through. When completion implies spend that is
	//     the breach itself and the criterion above sees it first, but the two can come apart —
	//     a run where nothing is actually spent leaves treasury looking healthy while the cap
	//     has plainly not acted, and only this bound catches that.
	//   - all parked would mean the cap stalled the fleet instead of stopping the buy that
	//     breached — it would satisfy the reserve trivially, by spending nothing at all.
	require.GreaterOrEqual(t, out.parked, 1,
		"the aggregate cap never acted: all %d buyers completed, so treasury sits above the reserve by luck of timing rather than because anything stopped the breaching buy", aggBuyers)
	require.Less(t, out.parked, aggBuyers,
		"the cap parked ALL %d buyers — it must stop the buy that breaches the reserve, not the whole fleet; a guard that refuses everything satisfies the reserve by spending nothing", aggBuyers)

	// Spend has to agree with the count actually observed, which is what keeps the range above
	// from being a licence for the two to drift apart.
	require.Equal(t, (aggBuyers-out.parked)*aggBuyCost, out.totalSpent,
		"%d of %d buyers parked, so exactly %d buys should have completed (%d credits), but treasury records %d spent",
		out.parked, aggBuyers, aggBuyers-out.parked, (aggBuyers-out.parked)*aggBuyCost, out.totalSpent)
}

// THE PROOF THAT THE TEST ABOVE CAN FAIL. The same harness, same race, cap unwired — the
// sp-ps2oc production state, where SetSpendLedger was never called and
// reserveConcurrentSpendOrPark returns its fail-open ("", false) on every buy.
//
// This asserts the BUG: per-buy-only checking lets all three through and lands treasury below
// the reserve. If this test ever goes green, the harness has stopped racing and the criterion-1
// test above has become vacuous.
func TestConcurrentConstructionSupplyBuys_PerBuyOnlyCheckingBreachesInAggregate(t *testing.T) {
	out := runConcurrentBuys(t, 9202, aggBuyers, false)

	require.Less(t, out.finalBalance, out.reserve,
		"the per-buy-only harness must REPRODUCE the breach (that is what makes the guarded test meaningful). Treasury landed at %d against reserve %d — if this is now above the reserve, the fixture has stopped racing and the aggregate test is no longer proving anything",
		out.finalBalance, out.reserve)
	require.Zero(t, out.parked,
		"with no cap wired every buy must pass its individually-correct per-buy floor: that is precisely why the aggregate breach was invisible")
	require.Equal(t, aggBuyers*aggBuyCost, out.totalSpent,
		"all %d buys must land, totalling %d — the 297k-in-68ms shape", aggBuyers, aggBuyers*aggBuyCost)
}

// ACCEPTANCE CRITERION 6. A single buy with ample headroom is unchanged: it proceeds, and its
// reservation is released rather than left to wedge the shared budget. The cap adds one short
// DB round-trip and no serialisation wait on the common path — there is no sibling to wait for.
func TestSingleBuyWithAmpleHeadroom_IsUnaffectedByTheCap(t *testing.T) {
	out := runConcurrentBuys(t, 9203, 1, true)

	require.Zero(t, out.parked, "a lone buy with ample headroom must NOT be parked by the concurrent cap")
	require.Equal(t, aggBuyCost, out.totalSpent, "the lone buy must complete in full")
	require.GreaterOrEqual(t, out.finalBalance, out.reserve, "and must leave treasury above the reserve")
}

// ACCEPTANCE 5, at the guard rather than at the collector. The counter must be incremented by
// the construction cap ITSELF on an aggregate denial.
//
// metrics/spend_cap_metrics_test.go proves the collector counts what it is told. That is a
// different claim from "the guard tells it", and only this one fails if the emit call is
// dropped — a mitigation whose counter is never incremented looks exactly like a cap that
// never fires, which is how the sp-ps2oc drain stayed invisible until the treasury was gone.
func TestConstructionCapDenial_EmitsTheAggregateDenialCounter(t *testing.T) {
	collector := metrics.NewSpendCapMetricsCollector()
	metrics.SetGlobalSpendCapCollector(collector)
	t.Cleanup(func() { metrics.SetGlobalSpendCapCollector(nil) })

	before := collector.AggregateDenialCount("construction_supply")

	// A ledger that rejects: treasury clears the per-buy floor, so the ONLY refusal is the
	// aggregate cap — exactly the event the counter is for.
	executor := NewProductionExecutor(nil, nil, nil, nil, nil, nil)
	executor.SetTreasuryReader(newRacingTreasury(aggTreasury, 1))
	executor.SetSpendLedger(&rejectingLedger{})

	_, parked := executor.reserveConcurrentSpendOrPark(context.Background(), 9204, aggBuyCost, "X1-KP46-MKT", "FAB_MATS")
	require.True(t, parked, "fixture check: the cap must actually deny, or this test proves nothing")

	require.Equal(t, before+1, collector.AggregateDenialCount("construction_supply"),
		"an AGGREGATE denial must increment the counter. Without the emit call the cap is unobservable: a coordinator losing every buy to the cap is indistinguishable from an idle one")
}

// rejectingLedger admits nothing. It still resolves readBudget, because the real ledger does
// and a fake that skipped it would leave the budget resolution unexercised.
type rejectingLedger struct{}

func (rejectingLedger) Reserve(ctx context.Context, _ int, _ string, _ int, readBudget func(context.Context) (int64, int, error)) (string, bool, error) {
	if _, _, err := readBudget(ctx); err != nil {
		return "", false, err
	}
	return "", false, nil
}
func (rejectingLedger) Release(context.Context, int, string) error { return nil }
func (rejectingLedger) ExpireStale(context.Context, time.Duration) (int, error) {
	return 0, nil
}
