package services

// production_executor_treasury_routing_test.go — BOTH factory money guards read treasury
// through the injected reader, not through Get Agent (sp-45s6f).
//
// sp-muq66 routed the trade-side guards and left the factory ones calling Get Agent before
// every input tranche: the per-buy spend floor (spendFloorBreached) and the cross-container
// concurrent-spend cap (reserveConcurrentSpendOrPark). Both are routed here. The reader's own
// semantics (freshness bound, empty-vs-zero, total-failure) are covered where it lives; what
// is provable ONLY here is the routing.
//
// EVERY read-path test wires the apiClient TOO, carrying a balance that yields the OPPOSITE
// verdict — so a guard that quietly preferred the API still returns a plausible number and
// still fails the test.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// countingAPIClient is the live path, instrumented. spendFloorFakeAPIClient (the sibling
// suite's fake) carries no call counter, and the counter is the whole point here: the
// acceptance property is that the common path makes ZERO API calls, which is unprovable
// from the returned balance alone.
type countingAPIClient struct {
	domainPorts.APIClient
	credits int
	calls   int
}

func (c *countingAPIClient) GetAgent(_ context.Context, _ string) (*player.AgentData, error) {
	c.calls++
	return &player.AgentData{Credits: c.credits}, nil
}

// routingFakeTreasury is the injected reader, instrumented so a test can prove the guard
// consulted IT and not the API client sitting right beside it.
type routingFakeTreasury struct {
	credits   int64
	err       error
	calls     int
	playerIDs []int
}

func (f *routingFakeTreasury) Credits(_ context.Context, playerID int) (int64, error) {
	f.calls++
	f.playerIDs = append(f.playerIDs, playerID)
	if f.err != nil {
		return 0, f.err
	}
	return f.credits, nil
}

// recordingSpendLedger captures the treasury figure the concurrent cap hands the ledger —
// the argument that decides whether combined in-flight spend breaches the reserve. Asserting
// on it is what proves the LEDGER balance, not the API one, is what the cap serializes against.
type recordingSpendLedger struct {
	gotCredits int
	gotReserve int
	calls      int
	ok         bool
	err        error
}

func (l *recordingSpendLedger) Reserve(_ context.Context, _ int, _ string, _, liveCredits, reserveFloor int) (string, bool, error) {
	l.calls++
	l.gotCredits = liveCredits
	l.gotReserve = reserveFloor
	if l.err != nil {
		return "", false, l.err
	}
	return "reservation-1", l.ok, nil
}

func (l *recordingSpendLedger) Release(_ context.Context, _ string) error { return nil }

func (l *recordingSpendLedger) ExpireStale(_ context.Context, _ time.Duration) (int, error) {
	return 0, nil
}

// treasuryRoutingCtx carries a RESOLVABLE player token, so a guard that fell back to the live
// path would SUCCEED at it rather than failing on a missing token. The test must distinguish
// "read the reader" from "the live path happened to be unavailable" — without the token, every
// fail-closed assertion below would pass for the wrong reason.
func treasuryRoutingCtx() context.Context {
	return common.WithLogger(common.WithPlayerToken(context.Background(), "TOKEN-45S6F"), &dwellCapturingLogger{})
}

// ---------------------------------------------------------------------------
// Guard 1: the per-buy spend floor.
// ---------------------------------------------------------------------------

// ACCEPTANCE, no-breach direction: the reader's balance clears the floor while the API
// client's does not. A guard still reading the API would park the buy.
func TestFactorySpendFloor_ReadsTheInjectedTreasuryAndMakesNoAPICall(t *testing.T) {
	api := &countingAPIClient{credits: 100} // would breach on every cost
	tr := &routingFakeTreasury{credits: 1_000_000}
	e := &ProductionExecutor{apiClient: api, treasury: tr}

	breached, reserve := e.spendFloorBreached(treasuryRoutingCtx(), 7, 100)

	if breached {
		t.Fatal("1000000 clears the floor comfortably; the reader's balance must decide, not the API client's 100")
	}
	if reserve != defaultWorkingCapitalReserve {
		t.Fatalf("reserve = %d, want the flat floor %d (no work sensor wired)", reserve, defaultWorkingCapitalReserve)
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

// ...and the breach direction, which is the one that protects money: the reader BREACHES
// while the API clears. A guard that quietly preferred the API would let this buy through.
func TestFactorySpendFloor_BreachIsDecidedByTheInjectedTreasuryNotTheAPI(t *testing.T) {
	api := &countingAPIClient{credits: 999_999_999} // would clear every floor
	e := &ProductionExecutor{apiClient: api, treasury: &routingFakeTreasury{credits: defaultWorkingCapitalReserve + 50}}

	breached, _ := e.spendFloorBreached(treasuryRoutingCtx(), 7, 100)

	if !breached {
		t.Fatalf("a balance of reserve+50 cannot absorb a 100-credit buy; the guard read the API client instead of the reader")
	}
	if api.calls != 0 {
		t.Fatalf("the common path must make NO API call, got %d GetAgent calls", api.calls)
	}
}

// RULINGS #4: an unreadable treasury PARKS the buy. It must never be converted into a zero
// balance (which would also park, but by inventing a bankrupt agent) or let the buy through.
func TestFactorySpendFloor_TreasuryErrorParksFailClosed(t *testing.T) {
	e := &ProductionExecutor{
		apiClient: &countingAPIClient{credits: 999_999_999},
		treasury:  &routingFakeTreasury{err: errors.New("ledger stale and live read failed")},
	}

	breached, reserve := e.spendFloorBreached(treasuryRoutingCtx(), 7, 100)

	if !breached {
		t.Fatal("an unreadable treasury must PARK the input buy (fail-closed), never let it through")
	}
	if reserve != defaultWorkingCapitalReserve {
		t.Fatalf("a blind park still reports the flat floor, got %d", reserve)
	}
}

// The optional-port contract is unchanged: with NEITHER source wired the guard is unavailable
// and the buy proceeds — the fail-OPEN contract the package's nil-passing fixtures rely on.
func TestFactorySpendFloor_NoTreasurySourceFailsOpen(t *testing.T) {
	e := &ProductionExecutor{}

	if breached, _ := e.spendFloorBreached(treasuryRoutingCtx(), 7, 100); breached {
		t.Fatal("no treasury source wired must leave the guard unavailable (fail-open), not park the buy")
	}
}

// ...and with only an apiClient wired the floor still works exactly as it did before.
func TestFactorySpendFloor_FallsBackToTheDirectLiveReadWhenNoReaderIsWired(t *testing.T) {
	api := &countingAPIClient{credits: defaultWorkingCapitalReserve + 50}
	e := &ProductionExecutor{apiClient: api}

	if breached, _ := e.spendFloorBreached(treasuryRoutingCtx(), 7, 100); !breached {
		t.Fatal("reserve+50 cannot absorb a 100-credit buy on the live path either")
	}
	if api.calls != 1 {
		t.Fatalf("the fallback path must make exactly one live read, got %d", api.calls)
	}
}

// ---------------------------------------------------------------------------
// Guard 2: the cross-container concurrent-spend cap.
// ---------------------------------------------------------------------------

// ACCEPTANCE at the concurrent cap: the reader's balance — not the API client's — is what
// the reservation ledger serializes against, and the common path makes no API call.
func TestFactoryConcurrentSpendCap_ReadsTheInjectedTreasuryAndMakesNoAPICall(t *testing.T) {
	api := &countingAPIClient{credits: 999_999_999}
	tr := &routingFakeTreasury{credits: 1_000_000}
	ledger := &recordingSpendLedger{ok: true}
	e := &ProductionExecutor{apiClient: api, treasury: tr, spendLedger: ledger}

	resID, parked := e.reserveConcurrentSpendOrPark(treasuryRoutingCtx(), 7, 100, "X1-TEST-MARKET", "IRON")

	if parked {
		t.Fatal("a ledger that accepts the reservation must not park the buy")
	}
	if resID != "reservation-1" {
		t.Fatalf("reservation id = %q, want the ledger's id back for the caller to Release", resID)
	}
	if ledger.gotCredits != 1_000_000 {
		t.Fatalf("the cap handed the ledger %d, want the READER's 1000000 — the API client's balance must never reach it", ledger.gotCredits)
	}
	if ledger.gotReserve != defaultWorkingCapitalReserve {
		t.Fatalf("reserve floor = %d, want the flat floor %d (no work sensor wired)", ledger.gotReserve, defaultWorkingCapitalReserve)
	}
	if api.calls != 0 {
		t.Fatalf("the common path must make NO API call, got %d GetAgent calls", api.calls)
	}
	if tr.calls != 1 {
		t.Fatalf("the cap must consult the injected reader exactly once, got %d", tr.calls)
	}
	if len(tr.playerIDs) != 1 || tr.playerIDs[0] != 7 {
		t.Fatalf("the read must be scoped to the caller's player, got %v", tr.playerIDs)
	}
}

// RULINGS #4 at the cap: an unreadable treasury PARKS, and the ledger is never consulted —
// a cap that reserved against a balance nobody read would be worse than no cap at all.
func TestFactoryConcurrentSpendCap_TreasuryErrorParksAndNeverReservesBlind(t *testing.T) {
	ledger := &recordingSpendLedger{ok: true}
	e := &ProductionExecutor{
		apiClient:   &countingAPIClient{credits: 999_999_999},
		treasury:    &routingFakeTreasury{err: errors.New("ledger stale and live read failed")},
		spendLedger: ledger,
	}

	resID, parked := e.reserveConcurrentSpendOrPark(treasuryRoutingCtx(), 7, 100, "X1-TEST-MARKET", "IRON")

	if !parked {
		t.Fatal("an unreadable treasury must PARK the input buy (fail-closed)")
	}
	if resID != "" {
		t.Fatalf("a parked buy must hold no reservation, got %q", resID)
	}
	if ledger.calls != 0 {
		t.Fatalf("the cap must never reserve against an unread balance, got %d Reserve calls", ledger.calls)
	}
}

// The optional-port contract is unchanged: no treasury source at all leaves the cap
// unavailable (fail-open), exactly as a nil apiClient did before.
func TestFactoryConcurrentSpendCap_NoTreasurySourceFailsOpen(t *testing.T) {
	ledger := &recordingSpendLedger{ok: true}
	e := &ProductionExecutor{spendLedger: ledger}

	if _, parked := e.reserveConcurrentSpendOrPark(treasuryRoutingCtx(), 7, 100, "X1-TEST-MARKET", "IRON"); parked {
		t.Fatal("no treasury source wired must leave the cap unavailable (fail-open), not park the buy")
	}
	if ledger.calls != 0 {
		t.Fatalf("an unavailable cap must not touch the ledger, got %d Reserve calls", ledger.calls)
	}
}

// ...and with only an apiClient wired the cap still reserves against the live balance.
func TestFactoryConcurrentSpendCap_FallsBackToTheDirectLiveReadWhenNoReaderIsWired(t *testing.T) {
	api := &countingAPIClient{credits: 750_000}
	ledger := &recordingSpendLedger{ok: true}
	e := &ProductionExecutor{apiClient: api, spendLedger: ledger}

	if _, parked := e.reserveConcurrentSpendOrPark(treasuryRoutingCtx(), 7, 100, "X1-TEST-MARKET", "IRON"); parked {
		t.Fatal("an accepting ledger must not park on the live path either")
	}
	if ledger.gotCredits != 750_000 {
		t.Fatalf("the cap handed the ledger %d, want the live 750000", ledger.gotCredits)
	}
	if api.calls != 1 {
		t.Fatalf("the fallback path must make exactly one live read, got %d", api.calls)
	}
}

// SetTreasuryReader is the daemon's wiring seam: it must actually install the reader, or the
// whole routing is inert while every test above (which sets the field directly) still passes.
func TestFactorySetTreasuryReaderInstallsTheReader(t *testing.T) {
	api := &countingAPIClient{credits: 999_999_999}
	e := &ProductionExecutor{apiClient: api}

	e.SetTreasuryReader(&routingFakeTreasury{credits: 1_000_000})

	if _, _ = e.spendFloorBreached(treasuryRoutingCtx(), 7, 100); api.calls != 0 {
		t.Fatalf("after SetTreasuryReader the guard must stop calling Get Agent, got %d calls", api.calls)
	}
}
