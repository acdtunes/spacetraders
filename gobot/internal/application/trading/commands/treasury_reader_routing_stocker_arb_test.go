package commands

// treasury_reader_routing_stocker_arb_test.go — the LAST two trading-side money guards read
// treasury through the injected reader, not through Get Agent (sp-45s6f).
//
// sp-muq66 routed the tour coordinator and the trade-route circuit; the stocker's capital
// ceiling and the one-shot arb's spend floor were left calling Get Agent directly, which is
// why the measured drop understated the change. The reader's own semantics (freshness bound,
// empty-vs-zero, total-failure) are covered where it lives, against a real ledger. What is
// provable ONLY here is the routing.
//
// EVERY read-path test wires the apiClient TOO, carrying a balance that yields the OPPOSITE
// verdict. A guard that quietly preferred the API would still return a plausible number and
// would still pass a test that only checked "some number came back" — so the assertions are
// the value, the API call count, and (for the arb) the breach decision itself flipping.

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Stocker: the pre-positioning capital ceiling.
// ---------------------------------------------------------------------------

// ACCEPTANCE at the stocker: with a reader wired, the capital ceiling makes NO API call and
// derives the ceiling from the reader's balance. The API client is wired with a balance
// ~250x larger, so a guard still reading it would produce a wildly different ceiling.
func TestStockerCapitalCeiling_ReadsTheInjectedTreasuryAndMakesNoAPICall(t *testing.T) {
	api := &tourSeqAPIClient{balances: []int{999_999_999}}
	tr := &fakeTreasury{credits: 4_000_000}
	h := &RunStockerCoordinatorHandler{apiClient: api, treasury: tr, ceilingPct: 10}

	ceiling, known := h.capitalCeiling(treasuryCtx(), 3, 300_000)

	if !known {
		t.Fatal("a readable treasury must yield a KNOWN ceiling")
	}
	if ceiling != 400_000 {
		t.Fatalf("ceiling = %d, want 10%% of the reader's 4000000 = 400000 (not the API client's balance)", ceiling)
	}
	if api.calls != 0 {
		t.Fatalf("the common path must make NO API call, got %d GetAgent calls", api.calls)
	}
	if tr.calls != 1 {
		t.Fatalf("the guard must consult the injected reader exactly once, got %d", tr.calls)
	}
	if len(tr.playerIDs) != 1 || tr.playerIDs[0] != 3 {
		t.Fatalf("the read must be scoped to the caller's player, got %v", tr.playerIDs)
	}
}

// The ceiling is junior to the working-capital reserve, and that subtraction is made against
// the READER's balance too — not just the percentage. With treasury 320000 and reserve 300000
// the 10% ceiling (32000) is clipped to the 20000 of headroom above the reserve.
func TestStockerCapitalCeiling_HoldsTheCeilingJuniorToTheReserveUsingTheReadersBalance(t *testing.T) {
	api := &tourSeqAPIClient{balances: []int{999_999_999}}
	h := &RunStockerCoordinatorHandler{apiClient: api, treasury: &fakeTreasury{credits: 320_000}, ceilingPct: 10}

	ceiling, known := h.capitalCeiling(treasuryCtx(), 3, 300_000)

	if !known || ceiling != 20_000 {
		t.Fatalf("ceiling = (%d, known=%v), want 20000 — 320000 − 300000 reserve, junior to the floor", ceiling, known)
	}
	if api.calls != 0 {
		t.Fatalf("the common path must make NO API call, got %d GetAgent calls", api.calls)
	}
}

// RULINGS #4 at the stocker: an unreadable treasury parks the whole pass — known=false, so
// pick() stocks nothing. It must NOT become a readable zero ceiling, which reads like a
// legitimately exhausted budget rather than a blind guard.
func TestStockerCapitalCeiling_TreasuryErrorFailsClosed(t *testing.T) {
	h := &RunStockerCoordinatorHandler{
		apiClient:  &tourSeqAPIClient{balances: []int{5_000_000}},
		treasury:   &fakeTreasury{err: errors.New("ledger stale and live read failed")},
		ceilingPct: 10,
	}

	ceiling, known := h.capitalCeiling(treasuryCtx(), 3, 300_000)

	if known || ceiling != 0 {
		t.Fatalf("an unreadable treasury must stock nothing: want (0, known=false), got (%d, %v)", ceiling, known)
	}
}

// The optional-port contract is unchanged: with NEITHER source wired the ceiling is unknown
// and the pass stocks nothing — the pre-existing nil-apiClient behaviour.
func TestStockerCapitalCeiling_NoTreasurySourceFailsClosed(t *testing.T) {
	h := &RunStockerCoordinatorHandler{ceilingPct: 10}

	if ceiling, known := h.capitalCeiling(treasuryCtx(), 3, 300_000); known || ceiling != 0 {
		t.Fatalf("no treasury source wired must yield (0, known=false), got (%d, %v)", ceiling, known)
	}
}

// ...and with only an apiClient wired the ceiling still resolves exactly as it did before the
// reader existed. This is the path every existing stocker test exercises.
func TestStockerCapitalCeiling_FallsBackToTheDirectLiveReadWhenNoReaderIsWired(t *testing.T) {
	api := &tourSeqAPIClient{balances: []int{4_000_000}}
	h := &RunStockerCoordinatorHandler{apiClient: api, ceilingPct: 10}

	ceiling, known := h.capitalCeiling(treasuryCtx(), 3, 300_000)

	if !known || ceiling != 400_000 {
		t.Fatalf("the apiClient-only path must still resolve the ceiling, got (%d, %v)", ceiling, known)
	}
	if api.calls != 1 {
		t.Fatalf("the fallback path must make exactly one live read, got %d", api.calls)
	}
}

// The stocker builds its OWN movement legs (the daemon passes nil), and those legs run the
// buy-time working-capital floor. A setter that stopped at this handler would leave the
// stocker's actual BUYS still calling Get Agent — half the path unrouted.
func TestStockerSetTreasuryReaderAlsoWiresTheMovementLegs(t *testing.T) {
	h := &RunStockerCoordinatorHandler{legs: &RunTradeRouteCoordinatorHandler{}}

	h.SetTreasuryReader(&fakeTreasury{credits: 123})

	if h.treasury == nil {
		t.Fatal("the stocker's own reader must be wired")
	}
	if h.legs.treasury == nil {
		t.Fatal("the movement legs' reader must be wired too — the buy-time floor reads through it")
	}
}

// ---------------------------------------------------------------------------
// One-shot arb: the pre-buy spend floor.
// ---------------------------------------------------------------------------

// ACCEPTANCE at the arb, no-breach direction: the reader's balance clears the floor while the
// API client's does NOT. A guard still reading the API would abort the buy; routing means it
// proceeds, with no API call.
func TestArbSpendFloorBreached_ReadsTheInjectedTreasuryAndMakesNoAPICall(t *testing.T) {
	api := &tourSeqAPIClient{balances: []int{100}} // would breach on every cost
	tr := &fakeTreasury{credits: 1_000_000}
	h := &RunArbCoordinatorHandler{apiClient: api, treasury: tr}
	response := &RunArbCoordinatorResponse{}

	breached := h.spendFloorBreached(treasuryCtx(), 7, 100_000, 300_000, response)

	if breached {
		t.Fatal("1000000 − 100000 = 900000 clears the 300000 floor; the reader's balance must decide, not the API client's 100")
	}
	if response.Aborted || response.SpendFloorAbort {
		t.Fatalf("a clearing buy must not be aborted, got Aborted=%v SpendFloorAbort=%v", response.Aborted, response.SpendFloorAbort)
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

// ...and the breach direction, which is the one that protects money: the reader's balance
// BREACHES the floor while the API client's clears it comfortably. A guard that quietly
// preferred the API would let this buy through.
func TestArbSpendFloorBreached_BreachIsDecidedByTheInjectedTreasuryNotTheAPI(t *testing.T) {
	api := &tourSeqAPIClient{balances: []int{999_999_999}} // would clear every floor
	h := &RunArbCoordinatorHandler{apiClient: api, treasury: &fakeTreasury{credits: 350_000}}
	response := &RunArbCoordinatorResponse{}

	breached := h.spendFloorBreached(treasuryCtx(), 7, 100_000, 300_000, response)

	if !breached {
		t.Fatal("350000 − 100000 = 250000 breaches the 300000 floor; the guard read the API client instead of the reader")
	}
	if !response.Aborted || !response.SpendFloorAbort {
		t.Fatalf("a breach must abort the buy, got Aborted=%v SpendFloorAbort=%v", response.Aborted, response.SpendFloorAbort)
	}
	if response.TreasuryAtAbort != 350_000 {
		t.Fatalf("TreasuryAtAbort = %d, want the reader's 350000 — the balance the decision was actually made against", response.TreasuryAtAbort)
	}
	if response.ReserveFloor != 300_000 {
		t.Fatalf("ReserveFloor = %d, want 300000", response.ReserveFloor)
	}
	if api.calls != 0 {
		t.Fatalf("the common path must make NO API call, got %d GetAgent calls", api.calls)
	}
}

// RULINGS #4 at the arb: a treasury that cannot be read aborts the buy. TreasuryAtAbort stays
// ZERO — no figure was ever obtained, and reporting one would invent a balance nobody read.
func TestArbSpendFloorBreached_TreasuryErrorFailsClosed(t *testing.T) {
	h := &RunArbCoordinatorHandler{
		apiClient: &tourSeqAPIClient{balances: []int{999_999_999}},
		treasury:  &fakeTreasury{err: errors.New("ledger stale and live read failed")},
	}
	response := &RunArbCoordinatorResponse{}

	if !h.spendFloorBreached(treasuryCtx(), 7, 100_000, 300_000, response) {
		t.Fatal("an unreadable treasury must abort the buy (fail-closed), never let it through")
	}
	if !response.Aborted || !response.SpendFloorAbort {
		t.Fatalf("a blind guard must set the abort fields, got Aborted=%v SpendFloorAbort=%v", response.Aborted, response.SpendFloorAbort)
	}
	if response.TreasuryAtAbort != 0 {
		t.Fatalf("TreasuryAtAbort = %d, want 0 — no treasury figure was ever obtained", response.TreasuryAtAbort)
	}
	if response.ReserveFloor != 300_000 {
		t.Fatalf("ReserveFloor = %d, want 300000", response.ReserveFloor)
	}
}

// The optional-port contract is unchanged: with NEITHER source wired the guard is unavailable
// and the buy proceeds unconstrained — the contract every nil-apiClient arb test relies on.
func TestArbSpendFloorBreached_NoTreasurySourceLeavesTheGuardUnavailable(t *testing.T) {
	h := &RunArbCoordinatorHandler{}
	response := &RunArbCoordinatorResponse{}

	if h.spendFloorBreached(treasuryCtx(), 7, 100_000, 300_000, response) {
		t.Fatal("no treasury source wired must leave the guard unavailable (fail-open), not abort the buy")
	}
	if response.Aborted || response.SpendFloorAbort {
		t.Fatal("an unavailable guard must not touch the abort fields")
	}
}

// ...and with only an apiClient wired the floor still works exactly as it did before the
// reader existed.
func TestArbSpendFloorBreached_FallsBackToTheDirectLiveReadWhenNoReaderIsWired(t *testing.T) {
	api := &tourSeqAPIClient{balances: []int{350_000}}
	h := &RunArbCoordinatorHandler{apiClient: api}
	response := &RunArbCoordinatorResponse{}

	if !h.spendFloorBreached(treasuryCtx(), 7, 100_000, 300_000, response) {
		t.Fatal("350000 − 100000 breaches the 300000 floor on the live path too")
	}
	if response.TreasuryAtAbort != 350_000 {
		t.Fatalf("TreasuryAtAbort = %d, want the live 350000", response.TreasuryAtAbort)
	}
	if api.calls != 1 {
		t.Fatalf("the fallback path must make exactly one live read, got %d", api.calls)
	}
}

// The arb builds its OWN movement legs (the daemon passes nil), and those legs run the
// buy-time working-capital floor — so the setter must reach them, exactly as the tour's does.
func TestArbSetTreasuryReaderAlsoWiresTheMovementLegs(t *testing.T) {
	h := &RunArbCoordinatorHandler{legs: &RunTradeRouteCoordinatorHandler{}}

	h.SetTreasuryReader(&fakeTreasury{credits: 123})

	if h.treasury == nil {
		t.Fatal("the arb's own reader must be wired")
	}
	if h.legs.treasury == nil {
		t.Fatal("the movement legs' reader must be wired too — the buy-time floor reads through it")
	}
}
