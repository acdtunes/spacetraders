package commands

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
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
	lastTarget probebuy.ProbeTarget
}

func (f *fakePurchaser) QuoteProbe(_ context.Context, _ shared.PlayerID, target probebuy.ProbeTarget) (int, string, error) {
	f.quoteCalls++
	f.lastTarget = target
	return f.quotePrice, f.quoteYard, f.quoteErr
}

func (f *fakePurchaser) BuyProbe(_ context.Context, _ shared.PlayerID, maxBudget int, target probebuy.ProbeTarget) (int, string, error) {
	f.buyCalls++
	f.lastBudget = maxBudget
	f.lastTarget = target
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

// cfgWith returns the default (balanced-preset) resolved config with an optional mutation applied —
// the seam for driving the reach/depth/off-gate/reuse ENGINE directly through reconcile now that the
// granular operator knobs are gone (sp-tlekc). A test overrides just the field it exercises; the
// engine MECHANISM is byte-for-byte unchanged, so these tests still pin exactly what they did.
func cfgWith(mutate func(*frontierConfig)) frontierConfig {
	c := resolveConfig(testCmd(), nil) // balanced default: breadth 65, reuse/snowball armed, reap 1800s
	if mutate != nil {
		mutate(&c)
	}
	return c
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

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))

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

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

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

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))

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

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))

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

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))

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

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))

	require.Len(t, pr.upserts, 1, "exactly one frontier post declared")
	require.Equal(t, "X1-VIRGIN", pr.upserts[0].SystemSymbol,
		"the occupied hop-0 anchor is excluded from expansion; the cross-system virgin is declared instead")
}

// Pin #3: declaration is bounded by MaxFrontierPostsInFlight so it never outruns manning.
func TestFrontier_DeclarationCappedByInFlight(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	// Two FRESH sweep-once posts already outstanding; cap is 2 → no new declaration. (CreatedAt=now so
	// the balanced-default reap keeps them — they legitimately occupy the cap, not wedged/reapable.)
	existing := []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-S1", Kind: domainScouting.PostKindSweepOnce, CreatedAt: clock.Now()},
		{PlayerID: 1, SystemSymbol: "X1-S2", Kind: domainScouting.PostKindSweepOnce, CreatedAt: clock.Now()},
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

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))
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

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))
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

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))
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

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))
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

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))
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
		require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))
		return buyer
	}

	// 30000 > 25% of 100000 → blocked.
	require.Zero(t, run(30000, 100000).buyCalls, "price above 25% of treasury is blocked")
	// 25000 == 25% of 100000 → allowed (the boundary is inclusive).
	over := run(25000, 100000)
	require.Equal(t, 1, over.buyCalls, "price at exactly 25% of treasury fills")
	require.Equal(t, 25000, over.lastBudget, "the buy budget is the 25% treasury ceiling")
}

// sp-tlekc §2E working-capital floor: the frontier now enforces the standing 50k reserve every
// other coordinator does — a buy that would leave (treasury − price) below the immutable floor is
// REFUSED even when the 25% rule passes. It is fail-closed and immutable (no tune seam). The two
// rows isolate the floor as the sole blocker: a thin treasury where the price clears 25% but breaches
// the floor is refused, and the SAME price on a fat treasury buys. Mutation guard: drop the floor
// gate and the thin-treasury row buys → the first assertion fails.
func TestFrontier_WorkingCapitalFloorEnforced(t *testing.T) {
	run := func(price, credits int) *fakePurchaser {
		clock := &shared.MockClock{CurrentTime: time.Now()}
		pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}}
		fr := &fakeFleetRepo{}
		lr := &fakeLedgerRepo{}
		h := newHandler(pr, fr, lr, clock)
		h.SetTreasuryReader(&fakeTreasury{credits: credits})
		buyer := &fakePurchaser{quotePrice: price, quoteYard: "X1-HOME-SY", buySymbol: "NEW", buyPrice: price}
		h.SetProbePurchaser(buyer)
		require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))
		return buyer
	}

	// price 12000 clears 25% of 60000 (=15000), but 60000 − 12000 = 48000 < 50000 floor → refused.
	require.Zero(t, run(12000, 60000).buyCalls, "a buy that would breach the 50k working-capital floor is refused (even within the 25% rule)")
	// fat treasury: 200000 − 12000 = 188000 ≥ 50000 → the SAME price buys, isolating the floor.
	require.Equal(t, 1, run(12000, 200000).buyCalls, "with ample treasury the same buy clears the floor")
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

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))
	require.Zero(t, buyer.buyCalls, "fleet cap reached → no buy")
}

// The two ledger-derived RATE GOVERNORS (per-buy cooldown + per-window spend cap) are REMOVED
// (sp-tlekc §2A — proven redundant): the frontier buy gate no longer reads the PURCHASE_SHIP ledger.
// Their tests (cycle-spend cap, cooldown-from-ledger, restart-no-double-buy, ledger-unreadable-fail-
// closed) are retired with the mechanism. Probe buys stay bounded by one-buy-per-tick + the fleet cap
// + the 25% rule + the new 50k working-capital floor (TestFrontier_WorkingCapitalFloorEnforced) + the
// API limiter — the four surviving bounds §2A relies on.

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

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))
	require.Equal(t, 1, buyer.buyCalls, "one probe bought")
	require.Equal(t, 100000, buyer.lastBudget, "budget is 25% of the 400000 treasury")
	// sp-hej4: the buy carries the demand-proximal target — the unmanned-slot post's system, with
	// the default per-hop penalty knob (testCmd sets none → the documented default).
	require.Equal(t, "X1-A", buyer.lastTarget.System, "the target is the post whose unmanned slot the probe serves")
	require.Equal(t, hopPenaltyCredits, buyer.lastTarget.HopPenaltyCredits, "the internal proximal-yard penalty const is applied")
	// sp-1bme8: the buy owns its exclusive single-writer journey claim by the DRIVING coordinator's
	// container id, so the freshness sizer's selection can never grab this buyer mid-relay.
	require.Equal(t, "frontier-1", buyer.lastTarget.ClaimOwnerContainerID, "the journey claim is owned by the frontier container id")
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

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))
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

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))
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

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))
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

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))
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

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))

	require.Empty(t, pr.upserts, "the occupied anchor is not declared")
	require.Zero(t, buyer.buyCalls, "and no probe is bought to serve an in-system-coverable occupied system")
}

// ---- sp-iopd: symmetric reserved FRESHNESS floor -------------------------------------

// THE sp-iopd MVP (frontier side): the symmetric freshness floor makes the frontier DISCOUNT
// reserved_freshness_floor idle probes as reserved for the market-freshness sizer — so an aggressive
// frontier GROWS the pool with a guarded buy rather than cannibalizing scanning's baseline. Five
// standing posts carry one open slot each (demand 5) and the fleet has exactly five idle probes
// (supply 5): under normal counting the idle probes cover the demand and the frontier does not buy.
// floor 0 is exact pre-sp-iopd behavior (no buy); floor 3 discounts three idle probes so the frontier
// counts only 2 toward its demand → short → buys, keeping 3 idle available for freshness. The floor-3
// row is the mutation guard: removing the −floor discount makes 5 cover 5 → no buy → it fails.
func TestFrontier_ReservedFreshnessFloorReservesIdleProbesForFreshness(t *testing.T) {
	cases := []struct {
		name     string
		floor    int
		wantBuys int
	}{
		{"floor 0 is pre-sp-iopd: 5 idle cover 5 slots, no buy", 0, 0},
		{"floor 3 reserves 3 idle for freshness — the frontier buys to cover its own demand", 3, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &shared.MockClock{CurrentTime: time.Now()}
			pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{
				{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding},
				{PlayerID: 1, SystemSymbol: "X1-B", Kind: domainScouting.PostKindStanding},
				{PlayerID: 1, SystemSymbol: "X1-C", Kind: domainScouting.PostKindStanding},
				{PlayerID: 1, SystemSymbol: "X1-D", Kind: domainScouting.PostKindStanding},
				{PlayerID: 1, SystemSymbol: "X1-E", Kind: domainScouting.PostKindStanding},
			}}
			idle := []*navigation.Ship{
				newProbe(t, "P1", "X1-Z-1"), newProbe(t, "P2", "X1-Z-2"), newProbe(t, "P3", "X1-Z-3"),
				newProbe(t, "P4", "X1-Z-4"), newProbe(t, "P5", "X1-Z-5"),
			}
			fr := &fakeFleetRepo{idle: idle, all: idle}
			lr := &fakeLedgerRepo{}
			h := newHandler(pr, fr, lr, clock)
			h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
			buyer := &fakePurchaser{quotePrice: 20000, quoteYard: "X1-HOME-SY", buySymbol: "NEW", buyPrice: 20000}
			h.SetProbePurchaser(buyer)

			cmd := testCmd()
			cmd.ReservedFreshnessFloor = tc.floor

			require.NoError(t, h.ReconcileOnce(context.Background(), cmd))
			require.Equal(t, tc.wantBuys, buyer.buyCalls,
				"the frontier discounts reserved_freshness_floor idle probes from the supply covering its demand")
		})
	}
}

// sp-iopd bead scenario 2: the reserved_frontier_floor is the FRESHNESS sizer's knob — the frontier
// has no such deduction, so the probes freshness releases stay fully AVAILABLE to the frontier.
// Six such idle probes cover six open slots and the frontier does not buy — the reserved probes
// reach the frontier. This is the complement of the sizer's frontier-floor release (freshness frees
// six; the frontier uses six), proving the loop is actually broken end to end.
func TestFrontier_ReservedFrontierProbesRemainAvailableToTheFrontier(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding},
		{PlayerID: 1, SystemSymbol: "X1-B", Kind: domainScouting.PostKindStanding},
		{PlayerID: 1, SystemSymbol: "X1-C", Kind: domainScouting.PostKindStanding},
		{PlayerID: 1, SystemSymbol: "X1-D", Kind: domainScouting.PostKindStanding},
		{PlayerID: 1, SystemSymbol: "X1-E", Kind: domainScouting.PostKindStanding},
		{PlayerID: 1, SystemSymbol: "X1-F", Kind: domainScouting.PostKindStanding},
	}}
	// Six idle probes — the ones the freshness sizer released for the frontier.
	idle := []*navigation.Ship{
		newProbe(t, "P1", "X1-Z-1"), newProbe(t, "P2", "X1-Z-2"), newProbe(t, "P3", "X1-Z-3"),
		newProbe(t, "P4", "X1-Z-4"), newProbe(t, "P5", "X1-Z-5"), newProbe(t, "P6", "X1-Z-6"),
	}
	fr := &fakeFleetRepo{idle: idle, all: idle}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 20000, quoteYard: "X1-HOME-SY", buySymbol: "NEW", buyPrice: 20000}
	h.SetProbePurchaser(buyer)

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd())) // reserved_freshness_floor defaults 0

	require.Zero(t, buyer.buyCalls,
		"the frontier's idle supply includes the probes freshness reserved for it — no frontier-side deduction, no needless buy")
}

// sp-iopd config wiring (frontier side): the reserved_freshness_floor knob is live-tunable.
// resolveConfig reads it from the tick's live-config snapshot (live > launch), and with NO snapshot
// falls back to the launch command, else the documented default 0 (floor OFF) — guarding the
// registry↔overlay drift that would leave the knob registered but silently ineffective.
func TestResolveFrontierConfig_ReadsReservedFreshnessFloorLiveWithDefaultFallback(t *testing.T) {
	def := resolveConfig(testCmd(), nil)
	require.Equal(t, defaultReservedFreshnessFloor, def.ReservedFreshnessFloor,
		"no snapshot, no launch value → the documented default (0, floor OFF)")

	launch := testCmd()
	launch.ReservedFreshnessFloor = 2
	require.Equal(t, 2, resolveConfig(launch, nil).ReservedFreshnessFloor,
		"no snapshot → the launch command value governs")

	live := liveconfig.Snapshot{"reserved_freshness_floor": 3}
	require.Equal(t, 3, resolveConfig(launch, live).ReservedFreshnessFloor,
		"a live snapshot overrides the launch value next tick")
}

// sp-tlekc §2C: the IMMUTABLE per-unit probe price ceiling (100k const, always on) DEFERS a buy whose
// final chosen quote exceeds it — the anti-overpay backstop for the deep-frontier tail whose only
// reachable yard is a depleted deep one. Treasury 10M so ONLY the ceiling can decide the over-priced
// row (mutation guard: delete the check and the 235k quote passes every other gate and wrongly buys).
// A deferral is a normal no-op: ReconcileOnce returns no error, the post simply stays dark this cycle.
// The ceiling can NEVER be disabled now (it is a const, not a knob), so there is no "0 = off" row.
func TestFrontier_ProbePriceCeilingDefersOverpricedBuy(t *testing.T) {
	cases := []struct {
		name     string
		quote    int
		wantBuys bool
	}{
		{name: "final quote over the 100k ceiling defers (deep depleted yard)", quote: 235000, wantBuys: false},
		{name: "quote under the ceiling still buys (cheap near yard flows)", quote: 23000, wantBuys: true},
		{name: "quote at exactly the ceiling buys (boundary inclusive — defer is strict >)", quote: maxProbePrice, wantBuys: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &shared.MockClock{CurrentTime: time.Now()}
			pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}}
			fr := &fakeFleetRepo{} // no idle probes → short by one
			lr := &fakeLedgerRepo{}
			h := newHandler(pr, fr, lr, clock)
			h.SetTreasuryReader(&fakeTreasury{credits: 10_000_000}) // 25% = 2.5M + floor clear, above every quote here
			buyer := &fakePurchaser{quotePrice: tc.quote, quoteYard: "X1-DEEP-SY", buySymbol: "NEW", buyPrice: tc.quote}
			h.SetProbePurchaser(buyer)

			require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()), "a ceiling defer is a normal no-op — never errors or strands the loop")
			if tc.wantBuys {
				require.Equal(t, 1, buyer.buyCalls, "quote %d within the 100k ceiling must still buy", tc.quote)
				return
			}
			require.Zero(t, buyer.buyCalls, "quote %d over the 100k ceiling must defer — the post stays dark", tc.quote)
		})
	}
}
