package commands

// treasury_reader_routing_test.go — the money guards read treasury through the injected
// reader, not through Get Agent (sp-muq66).
//
// The reader's own semantics (freshness bound, empty-vs-zero, total-failure) are covered
// where it lives, against a real ledger. What is provable ONLY here is the routing: that
// the guards actually consult it, that a guard consulting it makes NO API call, and that
// its error still fails the guard CLOSED rather than becoming a 0 headroom or a
// silently-uncapped spend.

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// fakeTreasury is the injected reader, instrumented so a test can prove the guard consulted
// IT and not the API client sitting right beside it.
type fakeTreasury struct {
	credits   int64
	err       error
	calls     int
	playerIDs []int
}

func (f *fakeTreasury) Credits(_ context.Context, playerID int) (int64, error) {
	f.calls++
	f.playerIDs = append(f.playerIDs, playerID)
	if f.err != nil {
		return 0, f.err
	}
	return f.credits, nil
}

func treasuryCtx() context.Context {
	return common.WithLogger(auth.WithPlayerToken(context.Background(), "TREASURY-ROUTING"), &laneLogCapturingLogger{})
}

// ACCEPTANCE 1 at the guard: with a reader wired, the working-capital floor makes NO API
// call and computes headroom from the reader's balance. The apiClient is deliberately
// wired too — a guard that quietly preferred it would still return a plausible number, so
// the two sources carry different balances and the call count is asserted.
func TestReserveHeadroom_ReadsTheInjectedTreasuryAndMakesNoAPICall(t *testing.T) {
	api := &tourSeqAPIClient{balances: []int{999_999_999}}
	tr := &fakeTreasury{credits: 1_000_000}
	h := &RunTradeRouteCoordinatorHandler{apiClient: api, treasury: tr}

	headroom, balance, available, readable := h.reserveHeadroom(treasuryCtx(), 7, 300_000)

	if !available || !readable {
		t.Fatalf("a wired, readable treasury must be available and readable, got available=%v readable=%v", available, readable)
	}
	if balance != 1_000_000 {
		t.Fatalf("balance = %d, want the injected reader's 1000000 (not the API client's)", balance)
	}
	if headroom != 700_000 {
		t.Fatalf("headroom = %d, want 1000000 − 300000 = 700000", headroom)
	}
	if api.calls != 0 {
		t.Fatalf("the common path must make NO API call, got %d GetAgent calls", api.calls)
	}
	if tr.calls != 1 {
		t.Fatalf("the guard must consult the injected reader exactly once, got %d", tr.calls)
	}
	if len(tr.playerIDs) != 1 || tr.playerIDs[0] != 7 {
		t.Fatalf("the read must be scoped to the caller's player, got %v", tr.playerIDs)
	}
}

// RULINGS #4 at the guard: an unreadable treasury is readable=false — the caller then
// refuses to spend. It must NOT become a readable zero balance, which would present as a
// negative headroom and read like a real, binding floor rather than a blind guard.
func TestReserveHeadroom_TreasuryErrorFailsClosedAndIsNotAZeroBalance(t *testing.T) {
	tr := &fakeTreasury{err: errors.New("ledger stale and live read failed")}
	h := &RunTradeRouteCoordinatorHandler{apiClient: &tourSeqAPIClient{balances: []int{5_000_000}}, treasury: tr}

	_, balance, available, readable := h.reserveHeadroom(treasuryCtx(), 1, 300_000)

	if !available {
		t.Fatal("a wired treasury is available even when the read fails")
	}
	if readable {
		t.Fatal("an unreadable treasury must report readable=false so the caller fails CLOSED")
	}
	if balance != 0 {
		t.Fatalf("the balance is meaningless on an unreadable read, got %d", balance)
	}
}

// The optional-port contract is unchanged: with NEITHER source wired the guard is simply
// unavailable and the caller proceeds unconstrained — the contract every nil-apiClient
// test relies on.
func TestReserveHeadroom_NoTreasurySourceLeavesTheGuardUnavailable(t *testing.T) {
	h := &RunTradeRouteCoordinatorHandler{}

	_, _, available, readable := h.reserveHeadroom(treasuryCtx(), 1, 300_000)

	if available || readable {
		t.Fatalf("no treasury source wired must leave the guard unavailable, got available=%v readable=%v", available, readable)
	}
}

// ...and with only an apiClient wired the guard still works exactly as it did before the
// reader existed. This is the path every existing test exercises.
func TestReserveHeadroom_FallsBackToTheDirectLiveReadWhenNoReaderIsWired(t *testing.T) {
	api := &tourSeqAPIClient{balances: []int{2_000_000}}
	h := &RunTradeRouteCoordinatorHandler{apiClient: api}

	headroom, balance, available, readable := h.reserveHeadroom(treasuryCtx(), 1, 300_000)

	if !available || !readable {
		t.Fatalf("an apiClient-only handler keeps the guard live, got available=%v readable=%v", available, readable)
	}
	if balance != 2_000_000 || headroom != 1_700_000 {
		t.Fatalf("balance/headroom = %d/%d, want 2000000/1700000", balance, headroom)
	}
	if api.calls != 1 {
		t.Fatalf("the fallback path must make exactly one live read, got %d", api.calls)
	}
}

// The tour's dynamic max-spend budget reads the injected reader too, with no API call, and its
// cap is derived from the reader's balance.
func TestDefaultMaxSpend_ReadsTheInjectedTreasuryAndMakesNoAPICall(t *testing.T) {
	api := &tourSeqAPIClient{balances: []int{999_999_999}}
	tr := &fakeTreasury{credits: 440_000}
	h := &RunTourCoordinatorHandler{apiClient: api, treasury: tr}

	got, unreadable := h.defaultMaxSpend(treasuryCtx(), 1, 300_000)

	if unreadable {
		t.Fatal("a readable treasury must not report unreadable")
	}
	if got != 84_000 {
		t.Fatalf("max-spend = %d, want trade's 60%% share of the 140000 the reader's 440000 leaves above the 300000 reserve = 84000", got)
	}
	if api.calls != 0 {
		t.Fatalf("the common path must make NO API call, got %d GetAgent calls", api.calls)
	}
}

// ...and an unreadable one still fails CLOSED: (0, unreadable=true), never an uncapped or
// zero-but-successful budget.
func TestDefaultMaxSpend_TreasuryErrorStillFailsClosed(t *testing.T) {
	h := &RunTourCoordinatorHandler{
		apiClient: &tourSeqAPIClient{balances: []int{5_000_000}},
		treasury:  &fakeTreasury{err: errors.New("treasury unreadable")},
	}

	got, unreadable := h.defaultMaxSpend(treasuryCtx(), 1, 300_000)

	if !unreadable || got != 0 {
		t.Fatalf("an unreadable treasury must yield (0, unreadable=true), got (%d, %v)", got, unreadable)
	}
}

// The pre-positioning capital ceiling reads the injected treasury too — the third money
// read on the tour path, and the one whose failure parks pre-positioning rather than
// sizing a deposit against a balance nobody read.
func TestDepositCapitalCeiling_ReadsTheInjectedTreasuryAndFailsClosedOnError(t *testing.T) {
	api := &tourSeqAPIClient{balances: []int{999_999_999}}
	tr := &fakeTreasury{credits: 4_000_000}
	h := &RunTourCoordinatorHandler{apiClient: api, treasury: tr, depositCeilingPct: 10}

	ceiling, known := h.depositCapitalCeiling(treasuryCtx(), 3, 300_000)

	if !known {
		t.Fatal("a readable treasury must yield a KNOWN ceiling")
	}
	if ceiling != 400_000 {
		t.Fatalf("ceiling = %d, want 10%% of the reader's 4000000 = 400000", ceiling)
	}
	if api.calls != 0 {
		t.Fatalf("the common path must make NO API call, got %d GetAgent calls", api.calls)
	}
	if len(tr.playerIDs) != 1 || tr.playerIDs[0] != 3 {
		t.Fatalf("the read must be scoped to the caller's player, got %v", tr.playerIDs)
	}

	blind := &RunTourCoordinatorHandler{
		apiClient:         api,
		treasury:          &fakeTreasury{err: errors.New("treasury unreadable")},
		depositCeilingPct: 10,
	}
	if ceiling, known := blind.depositCapitalCeiling(treasuryCtx(), 3, 300_000); known || ceiling != 0 {
		t.Fatalf("an unreadable treasury must park pre-positioning: want (0, known=false), got (%d, %v)", ceiling, known)
	}
}

// SetTreasuryReader on the tour coordinator must reach the MOVEMENT LEGS too, not just its
// own money reads: the buy-time working-capital floor lives on legs, so a setter that
// stopped at the tour handler would leave half the tour path still calling Get Agent.
func TestTourSetTreasuryReaderAlsoWiresTheMovementLegs(t *testing.T) {
	h := &RunTourCoordinatorHandler{legs: &RunTradeRouteCoordinatorHandler{}}
	tr := &fakeTreasury{credits: 123}

	h.SetTreasuryReader(tr)

	if h.treasury == nil {
		t.Fatal("the tour handler's own reader must be wired")
	}
	if h.legs.treasury == nil {
		t.Fatal("the movement legs' reader must be wired too — the buy-time floor reads through it")
	}
}
