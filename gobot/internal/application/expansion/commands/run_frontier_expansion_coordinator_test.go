package commands

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- fakes -----------------------------------------------------------------

type fakePostRepo struct {
	posts   []*domainScouting.ScoutPost
	upserts []*domainScouting.ScoutPost
	removed []string
	err     error
}

func (f *fakePostRepo) ListActive(_ context.Context, _ int) ([]*domainScouting.ScoutPost, error) {
	return f.posts, f.err
}

func (f *fakePostRepo) Upsert(_ context.Context, post *domainScouting.ScoutPost) error {
	f.upserts = append(f.upserts, post)
	f.posts = append(f.posts, post)
	return nil
}

func (f *fakePostRepo) Remove(_ context.Context, _ int, systemSymbol string) error {
	f.removed = append(f.removed, systemSymbol)
	return nil
}

type fakeFleetRepo struct {
	idle []*navigation.Ship
	all  []*navigation.Ship
	err  error
}

func (f *fakeFleetRepo) FindIdleByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return f.idle, f.err
}

func (f *fakeFleetRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	if f.all != nil {
		return f.all, f.err
	}
	return f.idle, f.err
}

// fakeLedgerRepo mimics the GORM transaction repo's relevant read semantics: it filters
// FindByPlayer by StartDate, orders by timestamp DESC, and applies Limit — so cooldown
// and spend-window derivations behave as they would against the real store.
type fakeLedgerRepo struct {
	txns []*ledger.Transaction
	err  error
}

func (f *fakeLedgerRepo) Create(_ context.Context, _ *ledger.Transaction) error { return nil }
func (f *fakeLedgerRepo) FindByID(_ context.Context, _ ledger.TransactionID, _ shared.PlayerID) (*ledger.Transaction, error) {
	return nil, nil
}
func (f *fakeLedgerRepo) CountByPlayer(_ context.Context, _ shared.PlayerID, _ ledger.QueryOptions) (int, error) {
	return len(f.txns), nil
}

func (f *fakeLedgerRepo) FindByPlayer(_ context.Context, _ shared.PlayerID, opts ledger.QueryOptions) ([]*ledger.Transaction, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]*ledger.Transaction, 0, len(f.txns))
	for _, t := range f.txns {
		if opts.StartDate != nil && t.Timestamp().Before(*opts.StartDate) {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp().After(out[j].Timestamp()) })
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

type fakeTreasury struct {
	credits int
	err     error
}

func (f *fakeTreasury) LiveCredits(_ context.Context, _ shared.PlayerID) (int, error) {
	return f.credits, f.err
}

type fakePurchaser struct {
	quotePrice int
	quoteYard  string
	quoteErr   error

	buyPrice  int
	buySymbol string
	buyErr    error

	quoteCalls int
	buyCalls   int
	lastBudget int
}

func (f *fakePurchaser) QuoteProbe(_ context.Context, _ shared.PlayerID) (int, string, error) {
	f.quoteCalls++
	return f.quotePrice, f.quoteYard, f.quoteErr
}

func (f *fakePurchaser) BuyProbe(_ context.Context, _ shared.PlayerID, maxBudget int) (int, string, error) {
	f.buyCalls++
	f.lastBudget = maxBudget
	if f.buyErr != nil {
		return 0, "", f.buyErr
	}
	price := f.buyPrice
	if price == 0 {
		price = f.quotePrice
	}
	return price, f.buySymbol, nil
}

type fakeScanner struct {
	candidates []ExpansionCandidate
	err        error
	calls      int
}

func (f *fakeScanner) ExpansionCandidates(_ context.Context, _ int, _ int) ([]ExpansionCandidate, error) {
	f.calls++
	return f.candidates, f.err
}

// ---- helpers ---------------------------------------------------------------

func newProbe(t *testing.T, symbol, waypoint string) *navigation.Ship {
	t.Helper()
	return newFleetShip(t, symbol, waypoint, "SATELLITE", "FRAME_PROBE")
}

func newFleetShip(t *testing.T, symbol, waypoint, role, frame string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(0, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 100, 0, cargo, 30, frame, role, nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	return ship
}

func probeTxn(t *testing.T, ts time.Time, price int) *ledger.Transaction {
	t.Helper()
	tx, err := ledger.NewTransaction(
		shared.MustNewPlayerID(1),
		ts,
		ledger.TransactionTypePurchaseShip,
		-price,
		price+10, // balanceBefore
		10,       // balanceAfter = before + amount
		"Purchased SHIP_PROBE",
		map[string]interface{}{"ship_type": probeShipType},
		"", "", "fleet expansion",
	)
	require.NoError(t, err)
	return tx
}

func standingPost(system, hull string) *domainScouting.ScoutPost {
	return &domainScouting.ScoutPost{PlayerID: 1, SystemSymbol: system, Kind: domainScouting.PostKindStanding, AssignedHull: hull}
}

func testCmd() *RunFrontierExpansionCoordinatorCommand {
	return &RunFrontierExpansionCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), ContainerID: "frontier-1"}
}

// newHandler wires a handler with all optional collaborators. Individual tests nil out
// what they want to exercise the fail-closed / degraded paths.
func newHandler(pr *fakePostRepo, fr *fakeFleetRepo, lr *fakeLedgerRepo, clock shared.Clock) *RunFrontierExpansionCoordinatorHandler {
	return NewRunFrontierExpansionCoordinatorHandler(pr, fr, lr, clock)
}

// ---- tests: declaration + ranking -----------------------------------------

// Pin #1/#3 + "queue-head system gets a sweep_once post declared through the real path":
// the top-ranked uncovered frontier system is declared as a single-hull sweep-once post
// via the ScoutPostRepository Upsert seam.
func TestFrontier_DeclaresTopRankedFrontierPost(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}
	fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}} // supply covers, so no buy — isolate declaration
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-LOW", Hops: 1, KnownMarkets: 1, Charted: true},
		{SystemSymbol: "X1-HIGH", Hops: 1, KnownMarkets: 5, Charted: true}, // highest score
		{SystemSymbol: "X1-MID", Hops: 1, KnownMarkets: 3, Charted: true},
	}})

	require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))

	require.Len(t, pr.upserts, 1, "exactly one frontier post declared (the head)")
	got := pr.upserts[0]
	require.Equal(t, "X1-HIGH", got.SystemSymbol, "highest-scored system is declared")
	require.Equal(t, domainScouting.PostKindSweepOnce, got.Kind, "frontier posts are sweep-once")
	require.Equal(t, 1, got.Hulls, "sweep-once is single-hull")
	require.Equal(t, defaultFrontierFreshness, got.FreshnessTarget, "default freshness applied")
}

// Pin #1: ranking honors the configured weights — a virgin bonus + hop penalty can
// outrank a market-rich but distant system.
func TestFrontier_RankingRespectsConfigWeights(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}
	fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		// distant market-rich: 3 markets @ 3 hops. score = 3*10 - 3*20 = -30
		{SystemSymbol: "X1-FAR", Hops: 3, KnownMarkets: 3, Charted: true},
		// near virgin: 0 markets @ 1 hop, virgin. score = 0 - 1*20 + 100 = 80
		{SystemSymbol: "X1-VIRGIN", Hops: 1, KnownMarkets: 0, Charted: false},
	}})

	cmd := testCmd()
	cmd.WeightKnownMarket = 10
	cmd.WeightHopPenalty = 20
	cmd.WeightVirginBonus = 100

	require.NoError(t, h.reconcileOnce(context.Background(), cmd))

	require.Len(t, pr.upserts, 1)
	require.Equal(t, "X1-VIRGIN", pr.upserts[0].SystemSymbol, "config weights make the near virgin outrank the far market cluster")
}

// A system that already has a post is covered — never re-declared.
func TestFrontier_CoveredSystemExcludedFromQueue(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{standingPost("X1-HIGH", "P9")}}
	fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-HIGH", Hops: 1, KnownMarkets: 5, Charted: true}, // already posted
		{SystemSymbol: "X1-MID", Hops: 1, KnownMarkets: 3, Charted: true},
	}})

	require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))

	require.Len(t, pr.upserts, 1)
	require.Equal(t, "X1-MID", pr.upserts[0].SystemSymbol, "the covered X1-HIGH is skipped; next best declared")
}

// sp-dc50 gap 2 / sp-gb7h KEEP side: a reachable, uncovered system whose gate we have charted
// (Charted=true) but whose full waypoint set was NEVER swept (Scanned=false, KnownMarkets=0) is
// an UNSCANNED scout target — NOT "known marketless." The old skip ("charted but marketless —
// nothing to scan") keyed on gate-edge presence, not on actual sweep knowledge, so it silently
// dropped every hop-2+ system the BFS reached over a charted gate but had never scanned — the
// frontier froze at the pre-charted boundary and the expansion queue emptied. Such a system may
// well hold markets AND shipyards (including the heavy-freighter yard the expansion is hunting),
// so it must be scouted to find out, not discarded. This is the !Scanned half of the sp-gb7h
// pair; its Scanned=true mirror (TestFrontier_ScannedMarketlessSystemNotDeclared) differs ONLY in
// the Scanned flag, isolating exactly the scanned/never-scanned discriminator.
func TestFrontier_ChartedButUnscannedSystemIsScouted(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}
	fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}} // supply covers → isolate declaration
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-UNSCANNED", Hops: 2, KnownMarkets: 0, Charted: true, Scanned: false}, // gate charted, waypoints never swept
	}})

	require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))

	require.Len(t, pr.upserts, 1, "a charted-gate-but-unscanned system is a scout target, not dropped")
	require.Equal(t, "X1-UNSCANNED", pr.upserts[0].SystemSymbol,
		"the unscanned system is declared so a probe scouts its markets/shipyards")
}

// sp-gb7h DROP side: a reachable, uncovered system whose full waypoint set WAS swept
// (Scanned=true) and holds NO marketplace anywhere (KnownMarkets=0) is genuinely barren — its
// markets were looked for and none exist. It must be DROPPED from the queue, not re-declared:
// sp-dc50's gap-2 fix removed the charted-marketless skip entirely, so such a system was
// re-declared → swept-once → no market found → post retired → re-declared every cycle (a
// wasteful barren re-scout loop). The candidate here is byte-identical to
// TestFrontier_ChartedButUnscannedSystemIsScouted's EXCEPT Scanned=true, so the pair pins the
// exact drop condition: Scanned && KnownMarkets==0. With the sole candidate dropped the queue is
// empty, so nothing is declared.
func TestFrontier_ScannedMarketlessSystemNotDeclared(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}
	fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}} // supply covers → isolate declaration
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-BARREN", Hops: 2, KnownMarkets: 0, Charted: true, Scanned: true}, // swept, genuinely marketless
	}})

	require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))

	require.Empty(t, pr.upserts,
		"a scanned-and-genuinely-marketless system is dropped, not re-declared every cycle")
}

// sp-njwy STARVATION: the frontier must NOT auto-declare a post for a system it ALREADY
// OCCUPIES (a hop-0 anchor — the HQ or any system the fleet already sits in). Such a system
// is coverable in-system with no relay; declaring it as a frontier post spins up a local
// in-system sweep tour that ABSORBS every freshly-bought probe — the scout reconciler mans
// in-system posts before it relays a probe to a cross-system one — so the genuine virgin
// frontier is starved of the probes it can only reach by gate-jump. Expansion targets NEW
// systems. Here the occupied home outranks the virgin on raw score, yet the virgin (the real
// frontier) must be the post that gets declared, leaving fresh probes idle-and-claimable for
// the relay.
func TestFrontier_OccupiedAnchorSystemNotDeclared(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}
	fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}} // supply covers → isolate declaration
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-HOME", Hops: 0, KnownMarkets: 5, Charted: true},    // occupied anchor, TOP raw score
		{SystemSymbol: "X1-VIRGIN", Hops: 1, KnownMarkets: 0, Charted: false}, // the genuine cross-system frontier
	}})

	require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))

	require.Len(t, pr.upserts, 1, "exactly one frontier post declared")
	require.Equal(t, "X1-VIRGIN", pr.upserts[0].SystemSymbol,
		"the occupied hop-0 anchor is excluded from expansion; the cross-system virgin is declared instead")
}

// Pin #3: declaration is bounded by MaxFrontierPostsInFlight so it never outruns manning.
func TestFrontier_DeclarationCappedByInFlight(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	// Two sweep-once posts already outstanding; cap is 2 → no new declaration.
	existing := []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-S1", Kind: domainScouting.PostKindSweepOnce},
		{PlayerID: 1, SystemSymbol: "X1-S2", Kind: domainScouting.PostKindSweepOnce},
	}
	pr := &fakePostRepo{posts: existing}
	fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-NEW", Hops: 1, KnownMarkets: 5, Charted: true},
	}})

	cmd := testCmd()
	cmd.MaxFrontierPostsInFlight = 2

	require.NoError(t, h.reconcileOnce(context.Background(), cmd))
	require.Empty(t, pr.upserts, "declaration blocked at the in-flight cap")
}

// ---- tests: purchase gate --------------------------------------------------

// "no-target → no buy": no unmanned slots and no expansion queue → nothing to serve.
func TestFrontier_NoTarget_NoBuy(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{} // no posts
	fr := &fakeFleetRepo{}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 1000, quoteYard: "X1-HOME-SY", buySymbol: "NEW"}
	h.SetProbePurchaser(buyer)
	h.SetExpansionScanner(&fakeScanner{candidates: nil})

	require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))
	require.Zero(t, buyer.buyCalls, "no target → no buy")
	require.Empty(t, pr.upserts, "nothing to declare")
}

// "idle-probe-available → no buy": an unmanned slot exists but an idle probe can serve it.
func TestFrontier_IdleProbeAvailable_NoBuy(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	// One standing post with an unmanned primary slot (AssignedHull == "").
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}}
	fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-B-1")}, all: []*navigation.Ship{newProbe(t, "P1", "X1-B-1")}}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 1000, quoteYard: "X1-HOME-SY", buySymbol: "NEW"}
	h.SetProbePurchaser(buyer)

	require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))
	require.Zero(t, buyer.buyCalls, "an idle probe covers the open slot → the reconciler relays it, no buy")
}

// "treasury-unreadable → no buy (fail-closed)": demand exceeds supply but the live
// balance read errors → the money guard refuses to spend.
func TestFrontier_TreasuryUnreadable_NoBuy(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}}
	fr := &fakeFleetRepo{idle: nil, all: nil} // no idle probes → fleet short
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{err: errors.New("api down")})
	buyer := &fakePurchaser{quotePrice: 1000, quoteYard: "X1-HOME-SY", buySymbol: "NEW"}
	h.SetProbePurchaser(buyer)

	require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))
	require.Zero(t, buyer.buyCalls, "unreadable treasury fails closed — no buy")
}

// A nil treasury reader is the same fail-closed refusal (guard unavailable → no spend).
func TestFrontier_NoTreasuryReader_NoBuy(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}}
	fr := &fakeFleetRepo{}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	buyer := &fakePurchaser{quotePrice: 1000, quoteYard: "X1-HOME-SY", buySymbol: "NEW"}
	h.SetProbePurchaser(buyer)
	// No treasury reader wired.

	require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))
	require.Zero(t, buyer.buyCalls, "no treasury reader → fail closed")
}

// "25%-rule enforced": price above 25% of live treasury blocks the buy; at/below it fills.
func TestFrontier_TwentyFivePercentRule(t *testing.T) {
	run := func(price, credits int) *fakePurchaser {
		clock := &shared.MockClock{CurrentTime: time.Now()}
		pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}}
		fr := &fakeFleetRepo{}
		lr := &fakeLedgerRepo{}
		h := newHandler(pr, fr, lr, clock)
		h.SetTreasuryReader(&fakeTreasury{credits: credits})
		buyer := &fakePurchaser{quotePrice: price, quoteYard: "X1-HOME-SY", buySymbol: "NEW", buyPrice: price}
		h.SetProbePurchaser(buyer)
		require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))
		return buyer
	}

	// 30000 > 25% of 100000 → blocked.
	require.Zero(t, run(30000, 100000).buyCalls, "price above 25% of treasury is blocked")
	// 25000 == 25% of 100000 → allowed (the boundary is inclusive).
	over := run(25000, 100000)
	require.Equal(t, 1, over.buyCalls, "price at exactly 25% of treasury fills")
	require.Equal(t, 25000, over.lastBudget, "the buy budget is the 25% treasury ceiling")
}

// "fleet-cap enforced": at the satellite cap, no buy even under demand.
func TestFrontier_FleetCapEnforced(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}}
	// Two satellites owned, cap 2 — none idle (all manning elsewhere), so fleet is short but capped.
	sat1 := newProbe(t, "S1", "X1-Z-1")
	sat2 := newProbe(t, "S2", "X1-Z-2")
	fr := &fakeFleetRepo{idle: nil, all: []*navigation.Ship{sat1, sat2}}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 1000, quoteYard: "X1-HOME-SY", buySymbol: "NEW"}
	h.SetProbePurchaser(buyer)

	cmd := testCmd()
	cmd.MaxProbeFleet = 2

	require.NoError(t, h.reconcileOnce(context.Background(), cmd))
	require.Zero(t, buyer.buyCalls, "fleet cap reached → no buy")
}

// "cycle-spend enforced": trailing-window probe spend + price over the cap blocks the buy.
func TestFrontier_CycleSpendCapEnforced(t *testing.T) {
	now := time.Now()
	clock := &shared.MockClock{CurrentTime: now}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}}
	fr := &fakeFleetRepo{}
	// A probe was bought 2 minutes ago for 90000 — OUTSIDE the 30s cooldown (so the cooldown
	// is clear) but INSIDE the 1h spend window, so it folds into the spend cap.
	lr := &fakeLedgerRepo{txns: []*ledger.Transaction{probeTxn(t, now.Add(-2*time.Minute), 90000)}}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 30000, quoteYard: "X1-HOME-SY", buySymbol: "NEW"}
	h.SetProbePurchaser(buyer)

	cmd := testCmd()
	cmd.MaxSpendPerCycle = 100000 // 90000 already spent this window + 30000 price > 100000
	cmd.PurchaseCooldownSecs = 30 // 30s cooldown → the 2-min-old buy is outside cooldown
	cmd.SpendWindowSecs = 3600    // 1h spend window → the 2-min-old buy IS inside it
	cmd.MaxProbeFleet = 40

	require.NoError(t, h.reconcileOnce(context.Background(), cmd))
	require.Zero(t, buyer.buyCalls, "window spend + price exceeds the per-cycle cap → no buy")
}

// "cooldown enforced" AND the core of restart-idempotency: a probe bought within the
// cooldown window (read from the persisted ledger, not memory) blocks another buy.
func TestFrontier_CooldownEnforced_FromLedger(t *testing.T) {
	now := time.Now()
	clock := &shared.MockClock{CurrentTime: now}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}}
	fr := &fakeFleetRepo{}
	lr := &fakeLedgerRepo{txns: []*ledger.Transaction{probeTxn(t, now.Add(-2*time.Minute), 1000)}} // 2 min ago
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 1000, quoteYard: "X1-HOME-SY", buySymbol: "NEW"}
	h.SetProbePurchaser(buyer)

	cmd := testCmd()
	cmd.PurchaseCooldownSecs = int((10 * time.Minute).Seconds()) // 10 min cooldown; last buy 2 min ago

	require.NoError(t, h.reconcileOnce(context.Background(), cmd))
	require.Zero(t, buyer.buyCalls, "within cooldown (derived from the ledger) → no buy")
}

// Restart idempotency: a FRESH handler (no in-memory state) with a recent ledger
// purchase re-derives the cooldown and refuses to double-buy — exactly the mid-cycle
// restart scenario (RULINGS #2).
func TestFrontier_RestartMidCycle_NoDoubleBuy(t *testing.T) {
	now := time.Now()
	cmd := testCmd()
	cmd.PurchaseCooldownSecs = int((10 * time.Minute).Seconds())

	// A probe was just bought (30s ago) and its ledger row persisted.
	lr := &fakeLedgerRepo{txns: []*ledger.Transaction{probeTxn(t, now.Add(-30*time.Second), 1000)}}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}}
	fr := &fakeFleetRepo{}

	// Simulate a restart: build a brand-new handler instance, no carried-over memory.
	h := newHandler(pr, fr, lr, &shared.MockClock{CurrentTime: now})
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 1000, quoteYard: "X1-HOME-SY", buySymbol: "NEW"}
	h.SetProbePurchaser(buyer)

	require.NoError(t, h.reconcileOnce(context.Background(), cmd))
	require.Zero(t, buyer.buyCalls, "post-restart, the ledger-derived cooldown prevents a double-buy")
}

// Ledger unreadable → fail closed (cannot verify the cooldown/spend → do not spend).
func TestFrontier_LedgerUnreadable_NoBuy(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}}
	fr := &fakeFleetRepo{}
	lr := &fakeLedgerRepo{err: errors.New("db down")}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 1000, quoteYard: "X1-HOME-SY", buySymbol: "NEW"}
	h.SetProbePurchaser(buyer)

	require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))
	require.Zero(t, buyer.buyCalls, "unreadable purchase ledger fails closed")
}

// ---- tests: happy path, claims-no-hulls, dry-run ---------------------------

// The happy path: fleet short, every guard passes → exactly one probe bought with the
// 25% ceiling as the hard budget.
func TestFrontier_BuysProbeWhenShortAndGuardsPass(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}}
	fr := &fakeFleetRepo{}  // no idle probes → short
	lr := &fakeLedgerRepo{} // no prior purchases → cooldown clear
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 400000})
	buyer := &fakePurchaser{quotePrice: 20000, quoteYard: "X1-HOME-SY", buySymbol: "PROBE-NEW", buyPrice: 20000}
	h.SetProbePurchaser(buyer)

	require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))
	require.Equal(t, 1, buyer.buyCalls, "one probe bought")
	require.Equal(t, 100000, buyer.lastBudget, "budget is 25% of the 400000 treasury")
}

// "coordinator claims no hulls": across a full buy cycle it never mutates a ship — the
// idle probe it counted as supply is left untouched (still idle, unclaimed). The
// FleetReader port exposes no write method, so this is enforced structurally; the test
// documents that the counted hull is not claimed.
func TestFrontier_ClaimsNoHulls(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	// Two unmanned slots, one idle probe → still short by one, so it buys; the idle probe
	// must remain idle (the reconciler, not this coordinator, relays it).
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding},
		{PlayerID: 1, SystemSymbol: "X1-B", Kind: domainScouting.PostKindStanding},
	}}
	idle := newProbe(t, "P1", "X1-C-1")
	fr := &fakeFleetRepo{idle: []*navigation.Ship{idle}, all: []*navigation.Ship{idle}}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 20000, quoteYard: "X1-HOME-SY", buySymbol: "PROBE-NEW", buyPrice: 20000}
	h.SetProbePurchaser(buyer)

	require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))
	require.Equal(t, 1, buyer.buyCalls, "short by one → buys one")
	require.True(t, idle.IsIdle(), "the counted idle probe is never claimed by this coordinator")
	require.Empty(t, idle.DedicatedFleet(), "and never dedicated by it")
}

// "dry-run acts on nothing": a cycle that WOULD declare and buy neither upserts a post
// nor calls the purchaser's buy.
func TestFrontier_DryRun_ActsOnNothing(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}  // empty → the queue head would be declared
	fr := &fakeFleetRepo{} // no idle probes → would buy
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 20000, quoteYard: "X1-HOME-SY", buySymbol: "PROBE-NEW", buyPrice: 20000}
	h.SetProbePurchaser(buyer)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-HIGH", Hops: 1, KnownMarkets: 5, Charted: true},
	}})

	cmd := testCmd()
	cmd.DryRun = true

	require.NoError(t, h.reconcileOnce(context.Background(), cmd))
	require.Empty(t, pr.upserts, "dry-run declares nothing")
	require.Zero(t, buyer.buyCalls, "dry-run buys nothing")
	require.Positive(t, buyer.quoteCalls, "dry-run still evaluates (quotes) the decision")
}

// With no scanner wired, the coordinator degrades to serving unmanned-slot demand only —
// it still buys when a declared post is short, and declares nothing.
func TestFrontier_NoScanner_ServesSlotDemandOnly(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}}
	fr := &fakeFleetRepo{}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 20000, quoteYard: "X1-HOME-SY", buySymbol: "PROBE-NEW", buyPrice: 20000}
	h.SetProbePurchaser(buyer)
	// No scanner.

	require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))
	require.Empty(t, pr.upserts, "no scanner → no expansion declarations")
	require.Equal(t, 1, buyer.buyCalls, "unmanned-slot demand still drives a buy")
}

// Repositioning slots (a relay already in flight) are NOT counted as open demand.
func TestFrontier_RepositioningSlotNotDemand(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	// The post's primary slot is unmanned but has a relay airborne → being served.
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding, RepositionContainerID: "relay-1"},
	}}
	fr := &fakeFleetRepo{}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 20000, quoteYard: "X1-HOME-SY", buySymbol: "NEW", buyPrice: 20000}
	h.SetProbePurchaser(buyer)

	require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))
	require.Zero(t, buyer.buyCalls, "a slot with a relay in flight is being served — not demand, no buy")
}

// sp-njwy OVER-BUY: an occupied (hop-0) system is coverable in-system, so the frontier must
// never BUY a probe to "serve" it. Before the fix the anchor was auto-declared as a sweep-once
// post whose unmanned slot counted as buy-demand, so with no idle probe on hand the coordinator
// bought a probe the system never needed — the credits-wasting over-buy the bead flags. With the
// occupied anchor excluded from expansion there is no such demand and no buy. (The demand guard's
// subtraction of idle probes + in-flight relays is already covered by
// TestFrontier_IdleProbeAvailable_NoBuy and TestFrontier_RepositioningSlotNotDemand; this pins the
// remaining over-buy vector — spurious demand from a system we already occupy.)
func TestFrontier_OccupiedAnchorSystem_NoSpuriousBuy(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}  // no posts
	fr := &fakeFleetRepo{} // no idle probes → the cycle would look "short" and buy
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 20000, quoteYard: "X1-HOME-SY", buySymbol: "NEW", buyPrice: 20000}
	h.SetProbePurchaser(buyer)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-HOME", Hops: 0, KnownMarkets: 5, Charted: true}, // only candidate: the occupied anchor
	}})

	require.NoError(t, h.reconcileOnce(context.Background(), testCmd()))

	require.Empty(t, pr.upserts, "the occupied anchor is not declared")
	require.Zero(t, buyer.buyCalls, "and no probe is bought to serve an in-system-coverable occupied system")
}
