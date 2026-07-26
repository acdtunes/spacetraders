package commands

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- fakes -----------------------------------------------------------------
// Adversarial style: every fallible fake returns a WRONG value ALONGSIDE its
// error, so a coordinator that ignores the error and consumes the value is
// caught by the write/buy assertions, never masked by a convenient nil.

type fakeDepthReader struct {
	rows []domainScouting.MarketDepthRow
	err  error
}

func (f *fakeDepthReader) MarketDepthRows(_ context.Context, _ int) ([]domainScouting.MarketDepthRow, error) {
	return f.rows, f.err
}

// fakeSensingPostRepo records every write AND applies Upserts/Removes to its
// posts list (replace-by-system — the Upsert-never-merges contract), so a
// multi-tick test observes the same state a real repository would serve.
type fakeSensingPostRepo struct {
	posts   []*domainScouting.ScoutPost
	listErr error
	upserts []*domainScouting.ScoutPost
	removed []string
}

func newSensingPostRepo(posts ...*domainScouting.ScoutPost) *fakeSensingPostRepo {
	return &fakeSensingPostRepo{posts: posts}
}

func (f *fakeSensingPostRepo) ListActive(_ context.Context, _ int) ([]*domainScouting.ScoutPost, error) {
	return f.posts, f.listErr
}

func (f *fakeSensingPostRepo) Upsert(_ context.Context, post *domainScouting.ScoutPost) error {
	f.upserts = append(f.upserts, post)
	for i, existing := range f.posts {
		if existing.SystemSymbol == post.SystemSymbol {
			f.posts[i] = post
			return nil
		}
	}
	f.posts = append(f.posts, post)
	return nil
}

func (f *fakeSensingPostRepo) Remove(_ context.Context, _ int, systemSymbol string) error {
	f.removed = append(f.removed, systemSymbol)
	kept := f.posts[:0]
	for _, existing := range f.posts {
		if existing.SystemSymbol != systemSymbol {
			kept = append(kept, existing)
		}
	}
	f.posts = kept
	return nil
}

// upsertsFor filters the recorded upserts to one system, in write order.
func (f *fakeSensingPostRepo) upsertsFor(system string) []*domainScouting.ScoutPost {
	var out []*domainScouting.ScoutPost
	for _, post := range f.upserts {
		if post.SystemSymbol == system {
			out = append(out, post)
		}
	}
	return out
}

type fakeSensingFleet struct {
	ships []*navigation.Ship
	err   error
}

func (f *fakeSensingFleet) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return f.ships, f.err
}

type fakePressure struct{ wait time.Duration }

func (f *fakePressure) Current(_ time.Time) time.Duration { return f.wait }

type sensingBuyCall struct {
	demand int
	supply int
	dryRun bool
	target probebuy.ProbeTarget
}

type fakeSensingBuyer struct {
	calls   []sensingBuyCall
	outcome probebuy.Outcome
}

func (f *fakeSensingBuyer) MaybeBuy(_ context.Context, _ shared.PlayerID, demand, supply int, dryRun bool, target probebuy.ProbeTarget) probebuy.Outcome {
	f.calls = append(f.calls, sensingBuyCall{demand: demand, supply: supply, dryRun: dryRun, target: target})
	return f.outcome
}

// fakeSensingEventRecorder is adversarial: when err is set, Record FAILS yet
// still appends — a coordinator that retried or gated on the write result
// would double-record or go silent, and the exactly-one asserts would catch it.
type fakeSensingEventRecorder struct {
	recorded []*captain.Event
	err      error
}

func (f *fakeSensingEventRecorder) Record(_ context.Context, e *captain.Event) error {
	f.recorded = append(f.recorded, e)
	return f.err
}

func countSensingErrorLoops(rec *fakeSensingEventRecorder) int {
	n := 0
	for _, e := range rec.recorded {
		if e.Type == captain.EventCoordinatorErrorLoop {
			n++
		}
	}
	return n
}

type fakeSensingLedger struct{ txns []*ledger.Transaction }

func (f *fakeSensingLedger) Create(_ context.Context, _ *ledger.Transaction) error { return nil }
func (f *fakeSensingLedger) FindByID(_ context.Context, _ ledger.TransactionID, _ shared.PlayerID) (*ledger.Transaction, error) {
	return nil, nil
}
func (f *fakeSensingLedger) CountByPlayer(_ context.Context, _ shared.PlayerID, _ ledger.QueryOptions) (int, error) {
	return len(f.txns), nil
}
func (f *fakeSensingLedger) FindByPlayer(_ context.Context, _ shared.PlayerID, _ ledger.QueryOptions) ([]*ledger.Transaction, error) {
	return f.txns, nil
}

// ---- fixtures ----------------------------------------------------------------

// sensingShip builds an era-5-realistic hull. IsScoutType() keys on ROLE
// (SATELLITE), so the fixture must carry honest frames+roles: a COMMAND frigate
// is NOT a scout no matter its frame.
func sensingShip(t *testing.T, symbol, frame, role string, cargoCap int) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint("X1-AA1-A1", 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(cargoCap, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 100, cargoCap, cargo, 30, frame, role, nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	return ship
}

func sensingProbe(t *testing.T, symbol string) *navigation.Ship {
	return sensingShip(t, symbol, "FRAME_PROBE", "SATELLITE", 0)
}

func sensingFrigate(t *testing.T, symbol string) *navigation.Ship {
	return sensingShip(t, symbol, "FRAME_FRIGATE", "COMMAND", 40)
}

func depthRow(system, waypoint, good string, volume, mid int) domainScouting.MarketDepthRow {
	return domainScouting.MarketDepthRow{System: system, Waypoint: waypoint, Good: good, TradeVolume: volume, MidPrice: mid}
}

// richRows makes `system` in-scope: `hot` distinct waypoints, each one
// whitelisted good at 60×40000 = 2.4M depth (comfortably above the 2M floor).
func richRows(system string, hot int) []domainScouting.MarketDepthRow {
	rows := make([]domainScouting.MarketDepthRow, 0, hot)
	for i := 0; i < hot; i++ {
		rows = append(rows, depthRow(system, fmt.Sprintf("%s-W%d", system, i), "CLOTHING", 60, 40_000))
	}
	return rows
}

// thinRows makes `system` census-visible but BELOW the depth floor: one hot
// market at 10×1000 = 10k depth.
func thinRows(system string) []domainScouting.MarketDepthRow {
	return []domainScouting.MarketDepthRow{depthRow(system, system+"-W0", "CLOTHING", 10, 1_000)}
}

func sensingPost(system string, hulls int) *domainScouting.ScoutPost {
	return &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: system, Kind: domainScouting.PostKindStanding,
		Hulls: hulls, FreshnessTarget: time.Hour,
	}
}

func sensingCmd() *RunProbeSensingCoordinatorCommand {
	return &RunProbeSensingCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), ContainerID: "sensing-1"}
}

// newSensingHandler wires a handler whose buyer is the recording fake, so buy
// tests assert the exact demand/supply the coordinator computed.
func newSensingHandler(dr *fakeDepthReader, pr *fakeSensingPostRepo, fl *fakeSensingFleet, press *fakePressure) (*RunProbeSensingCoordinatorHandler, *fakeSensingBuyer) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	h := NewRunProbeSensingCoordinatorHandler(dr, pr, fl, press, &fakeSensingLedger{}, clock)
	buyer := &fakeSensingBuyer{}
	h.newBuyer = func(_ probebuy.Config) guardedBuyer { return buyer }
	return h, buyer
}

func calmFleet(t *testing.T) *fakeSensingFleet {
	return &fakeSensingFleet{ships: []*navigation.Ship{sensingProbe(t, "PROBE-A"), sensingProbe(t, "PROBE-B"), sensingProbe(t, "PROBE-C")}}
}

// ---- scope → posts diff ------------------------------------------------------

func TestSensing_DeclaresNewInScopeSystem(t *testing.T) {
	dr := &fakeDepthReader{rows: richRows("X1-AA1", 3)}
	pr := newSensingPostRepo()
	h, _ := newSensingHandler(dr, pr, calmFleet(t), &fakePressure{})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	require.Len(t, pr.upserts, 1, "one new in-scope system ⇒ exactly one declared post")
	post := pr.upserts[0]
	require.Equal(t, "X1-AA1", post.SystemSymbol)
	require.Equal(t, domainScouting.PostKindStanding, post.Kind)
	require.Equal(t, 1, post.Hulls, "at or under the second-probe threshold ⇒ one probe")
	require.Equal(t, time.Hour, post.FreshnessTarget, "posts carry the config-default freshness target")
	require.Equal(t, 1, post.PlayerID)
	require.False(t, post.Dormant, "no API pressure ⇒ born active")
	require.Empty(t, pr.removed)
}

func TestSensing_SecondProbeAboveThreshold(t *testing.T) {
	t.Run("new system past the threshold declares two hulls", func(t *testing.T) {
		dr := &fakeDepthReader{rows: richRows("X1-AA1", 13)} // 13 > threshold 12
		pr := newSensingPostRepo()
		h, _ := newSensingHandler(dr, pr, calmFleet(t), &fakePressure{})

		require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

		require.Len(t, pr.upserts, 1)
		require.Equal(t, 2, pr.upserts[0].Hulls)
	})

	t.Run("existing post crossing the threshold resizes 1→2", func(t *testing.T) {
		dr := &fakeDepthReader{rows: richRows("X1-AA1", 13)}
		pr := newSensingPostRepo(sensingPost("X1-AA1", 1))
		h, _ := newSensingHandler(dr, pr, calmFleet(t), &fakePressure{})

		require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

		require.Len(t, pr.upserts, 1)
		require.Equal(t, "X1-AA1", pr.upserts[0].SystemSymbol)
		require.Equal(t, 2, pr.upserts[0].Hulls)
	})
}

func TestSensing_BelowFloorPostRemoved(t *testing.T) {
	rows := append(richRows("X1-AA1", 3), thinRows("X1-TH1")...)
	dr := &fakeDepthReader{rows: rows}
	pr := newSensingPostRepo(sensingPost("X1-AA1", 1), sensingPost("X1-TH1", 1))
	h, _ := newSensingHandler(dr, pr, calmFleet(t), &fakePressure{})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	require.Equal(t, []string{"X1-TH1"}, pr.removed, "a system under the depth floor loses its standing post")
	require.Empty(t, pr.upserts, "the stable in-scope post is not rewritten")
}

func TestSensing_MinHullsPostNeverShrunkOrRemoved(t *testing.T) {
	home := func(hulls, minHulls int) *domainScouting.ScoutPost {
		post := sensingPost("X1-HOME", hulls)
		post.MinHulls = minHulls
		return post
	}

	t.Run("out of scope: kept, not removed, not rewritten", func(t *testing.T) {
		rows := append(richRows("X1-AA1", 3), thinRows("X1-HOME")...) // home below the floor
		pr := newSensingPostRepo(sensingPost("X1-AA1", 1), home(3, 3))
		h, _ := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{})

		require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

		require.Empty(t, pr.removed, "a MinHulls-floored post survives dropping out of scope (the INCOME-phase home story)")
		require.Empty(t, pr.upserts)
	})

	t.Run("in scope: never sized below the floor", func(t *testing.T) {
		rows := append(richRows("X1-AA1", 3), richRows("X1-HOME", 3)...) // plan wants 1 at home
		pr := newSensingPostRepo(sensingPost("X1-AA1", 1), home(3, 3))
		h, _ := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{})

		require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

		require.Empty(t, pr.upserts, "plan 1 floored to MinHulls 3 == current 3 ⇒ no write")
		require.Empty(t, pr.removed)
	})

	t.Run("in scope: a shrink stops AT the floor", func(t *testing.T) {
		rows := richRows("X1-HOME", 3)
		pr := newSensingPostRepo(home(5, 3))
		h, _ := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{})

		require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

		require.Len(t, pr.upserts, 1)
		require.Equal(t, 3, pr.upserts[0].Hulls, "5 → floor 3, never below")
		require.Equal(t, 3, pr.upserts[0].MinHulls, "Upsert never merges — the floor must survive the rewrite")
	})

	t.Run("out of scope while dormant: woken, still kept", func(t *testing.T) {
		dormantHome := home(3, 3)
		dormantHome.Dormant = true
		pr := newSensingPostRepo(sensingPost("X1-AA1", 1), dormantHome)
		rows := append(richRows("X1-AA1", 3), thinRows("X1-HOME")...)
		h, _ := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{})

		require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

		require.Empty(t, pr.removed)
		homeWrites := pr.upsertsFor("X1-HOME")
		require.Len(t, homeWrites, 1, "a kept post outside the rotation must not stay parked forever")
		require.False(t, homeWrites[0].Dormant)
		require.Equal(t, 3, homeWrites[0].Hulls, "the wake write never shrinks the post")
	})
}

func TestSensing_SweepOncePostsUntouched(t *testing.T) {
	sweep := func(system string) *domainScouting.ScoutPost {
		post := sensingPost(system, 1)
		post.Kind = domainScouting.PostKindSweepOnce
		return post
	}
	rows := append(richRows("X1-AA1", 3), richRows("X1-BB2", 3)...)
	pr := newSensingPostRepo(sweep("X1-AA1"), sweep("X1-CC3")) // one in scope, one out
	h, _ := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	require.Empty(t, pr.removed, "sweep-once posts are the frontier's, in scope or not")
	require.Empty(t, pr.upsertsFor("X1-AA1"), "declaring standing over a sweep-once row would clobber it (Upsert is keyed by system)")
	require.Empty(t, pr.upsertsFor("X1-CC3"))
	require.Len(t, pr.upserts, 1, "only the sweep-free in-scope system is declared")
	require.Equal(t, "X1-BB2", pr.upserts[0].SystemSymbol)
}

// ---- dormancy rotation --------------------------------------------------------

// fourRichSystems is a 4-system in-scope census with matching stable posts.
func fourRichSystems() ([]domainScouting.MarketDepthRow, []*domainScouting.ScoutPost) {
	systems := []string{"X1-AA1", "X1-BB2", "X1-CC3", "X1-DD4"}
	var rows []domainScouting.MarketDepthRow
	posts := make([]*domainScouting.ScoutPost, 0, len(systems))
	for _, s := range systems {
		rows = append(rows, richRows(s, 3)...)
		posts = append(posts, sensingPost(s, 1))
	}
	return rows, posts
}

func TestSensing_DormancyWritesOnlyDeltas(t *testing.T) {
	t.Run("pressure flips exactly the rotated-out posts", func(t *testing.T) {
		rows, posts := fourRichSystems()
		pr := newSensingPostRepo(posts...)
		// wait = 4×waitHigh ⇒ share 0.5 ⇒ 2 of 4 active from cursor 0: ring
		// [AA1 BB2 CC3 DD4] ⇒ dormant {CC3, DD4}.
		h, _ := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{wait: 4 * time.Second})

		require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

		require.Empty(t, pr.removed)
		require.Len(t, pr.upserts, 2, "only the two posts whose Dormant bit changed are written")
		var flipped []string
		for _, post := range pr.upserts {
			require.True(t, post.Dormant)
			require.Equal(t, 1, post.Hulls, "a dormancy flip never perturbs the hull budget")
			flipped = append(flipped, post.SystemSymbol)
		}
		sort.Strings(flipped)
		require.Equal(t, []string{"X1-CC3", "X1-DD4"}, flipped)
	})

	t.Run("bits already matching the rotation ⇒ zero writes", func(t *testing.T) {
		rows, posts := fourRichSystems()
		posts[2].Dormant = true // X1-CC3
		posts[3].Dormant = true // X1-DD4
		pr := newSensingPostRepo(posts...)
		h, _ := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{wait: 4 * time.Second})

		require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

		require.Empty(t, pr.upserts, "steady state under constant pressure ⇒ zero writes (write-amplification guard)")
		require.Empty(t, pr.removed)
	})

	t.Run("new posts are born carrying the rotation's bit", func(t *testing.T) {
		rows, _ := fourRichSystems()
		pr := newSensingPostRepo()
		h, _ := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{wait: 4 * time.Second})

		require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

		require.Len(t, pr.upserts, 4, "declaration and dormancy land in ONE write per post")
		bits := map[string]bool{}
		for _, post := range pr.upserts {
			bits[post.SystemSymbol] = post.Dormant
		}
		require.Equal(t, map[string]bool{"X1-AA1": false, "X1-BB2": false, "X1-CC3": true, "X1-DD4": true}, bits)
	})
}

func TestSensing_SteadyStateZeroWrites(t *testing.T) {
	rows := append(richRows("X1-AA1", 3), richRows("X1-BB2", 13)...)
	pr := newSensingPostRepo(sensingPost("X1-AA1", 1), sensingPost("X1-BB2", 2))
	h, buyer := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	require.Empty(t, pr.upserts, "a converged tick writes nothing")
	require.Empty(t, pr.removed)
	require.Len(t, buyer.calls, 1, "the guarded buyer is still consulted exactly once per tick")
}

func TestSensing_RotationAdvancesAcrossTicks(t *testing.T) {
	rows := append(richRows("X1-AA1", 3), richRows("X1-BB2", 3)...)
	pr := newSensingPostRepo(sensingPost("X1-AA1", 1), sensingPost("X1-BB2", 1))
	// share 0.5 over 2 systems ⇒ 1 active per tick, round-robin.
	h, _ := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{wait: 4 * time.Second})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))
	require.Len(t, pr.upserts, 1, "tick 1: cursor 0 keeps AA1 active, BB2 goes dormant")
	require.Equal(t, "X1-BB2", pr.upserts[0].SystemSymbol)
	require.True(t, pr.upserts[0].Dormant)

	pr.upserts = nil
	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))
	require.Len(t, pr.upserts, 2, "tick 2: the cursor advanced, so BOTH bits flip (AA1 sheds, BB2 wakes)")
	bits := map[string]bool{}
	for _, post := range pr.upserts {
		bits[post.SystemSymbol] = post.Dormant
	}
	require.Equal(t, map[string]bool{"X1-AA1": true, "X1-BB2": false}, bits,
		"round-robin: every system scans within ceil(1/share) cycles, none is starved")
}

func TestSensing_ExtremePressureNeverFullyDormant(t *testing.T) {
	rows, posts := fourRichSystems()
	pr := newSensingPostRepo(posts...)
	// 1000×waitHigh: ActiveShare floors at 0.5 — degradation is bounded, so a
	// pressure spike can never park the whole sensing fleet.
	h, _ := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{wait: 1000 * time.Second})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	require.Len(t, pr.upserts, 2, "share floor 0.5 keeps half the ring scanning at ANY wait")
	for _, post := range pr.upserts {
		require.True(t, post.Dormant)
	}
}

// ---- era-gap fail-safe ---------------------------------------------------------

func TestSensing_EmptyCensusFailSafe(t *testing.T) {
	run := func(t *testing.T, rows []domainScouting.MarketDepthRow) {
		pr := newSensingPostRepo(sensingPost("X1-OLD", 1), sensingPost("X1-AA1", 2))
		h, buyer := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{})

		require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

		require.Empty(t, pr.removed, "an empty census must never mass-retire standing posts (fleet-killer guard)")
		require.Empty(t, pr.upserts)
		require.Empty(t, buyer.calls, "no census ⇒ no demand signal ⇒ no buy attempt")
	}

	t.Run("no rows at all", func(t *testing.T) { run(t, nil) })
	t.Run("rows exist but none whitelisted", func(t *testing.T) {
		run(t, []domainScouting.MarketDepthRow{
			depthRow("X1-ORE1", "X1-ORE1-W0", "QUARTZ_SAND", 200, 50),
			depthRow("X1-ORE1", "X1-ORE1-W1", "IRON_ORE", 200, 50),
		})
	})
}

// ---- the budgeted buy -----------------------------------------------------------

func TestSensing_DemandClampedAtBudget(t *testing.T) {
	// Five systems past the second-probe threshold ⇒ the plan wants 10 hulls.
	var rows []domainScouting.MarketDepthRow
	for _, s := range []string{"X1-AA1", "X1-BB2", "X1-CC3", "X1-DD4", "X1-EE5"} {
		rows = append(rows, richRows(s, 13)...)
	}
	dr := &fakeDepthReader{rows: rows}
	fl := &fakeSensingFleet{ships: []*navigation.Ship{sensingProbe(t, "PROBE-A"), sensingProbe(t, "PROBE-B")}}
	h, buyer := newSensingHandler(dr, newSensingPostRepo(), fl, &fakePressure{})

	cmd := sensingCmd()
	cmd.ProbeBudget = 3
	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	require.Len(t, buyer.calls, 1, "exactly one MaybeBuy per tick")
	call := buyer.calls[0]
	require.Equal(t, 3, call.demand, "demand = min(plan total 10, N 3) — N is the single budget dial")
	require.Equal(t, 2, call.supply)
	require.False(t, call.dryRun, "shipped ARMED — there is no dry-run seam")
}

func TestSensing_SupplyCountsOnlySatellites(t *testing.T) {
	// Era-5 fleet: the COMMAND frigate is NOT scout-type (role, not frame,
	// decides), and a contract-dedicated probe is not sensing supply.
	pinned := sensingProbe(t, "PROBE-PINNED")
	pinned.SetDedicatedFleet("contract")
	fl := &fakeSensingFleet{ships: []*navigation.Ship{
		sensingFrigate(t, "AGENT-1"),
		sensingProbe(t, "PROBE-A"),
		sensingProbe(t, "PROBE-B"),
		pinned,
	}}
	h, buyer := newSensingHandler(&fakeDepthReader{rows: richRows("X1-AA1", 3)}, newSensingPostRepo(), fl, &fakePressure{})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	require.Len(t, buyer.calls, 1)
	require.Equal(t, 2, buyer.calls[0].supply, "frigate and contract-pinned probe are excluded from supply")
}

func TestSensing_DemandNeverExceedsPlanWhileDiscoveryUnfunded(t *testing.T) {
	rows := append(richRows("X1-AA1", 3), richRows("X1-BB2", 3)...)
	posts := []*domainScouting.ScoutPost{sensingPost("X1-AA1", 1), sensingPost("X1-BB2", 1)}

	cases := []struct {
		name        string
		wait        time.Duration
		wantDormant int
	}{
		{"headroom (discovery allowed)", 0, 0},
		{"mid pressure (discovery shed first)", 500 * time.Millisecond, 0},
		{"high pressure (scanning sheds too)", 4 * time.Second, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := newSensingPostRepo(posts[0], posts[1])
			posts[0].Dormant, posts[1].Dormant = false, false
			h, buyer := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{wait: tc.wait})

			require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

			require.Len(t, buyer.calls, 1)
			require.Equal(t, 2, buyer.calls[0].demand,
				"discovery demand is 0 in every pressure regime until the discovery pass funds it — buy demand is exactly the plan total")
			dormantWrites := 0
			for _, post := range pr.upserts {
				if post.Dormant {
					dormantWrites++
				}
			}
			require.Equal(t, tc.wantDormant, dormantWrites)
		})
	}
}

func TestSensing_BuyTargetNamesNeediestSystem(t *testing.T) {
	// AA1 wants 2 and has no post (gap 2); BB2 wants 1 and has 1 (gap 0).
	rows := append(richRows("X1-AA1", 13), richRows("X1-BB2", 3)...)
	pr := newSensingPostRepo(sensingPost("X1-BB2", 1))
	h, buyer := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{})

	cmd := sensingCmd()
	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	require.Len(t, buyer.calls, 1)
	target := buyer.calls[0].target
	require.Equal(t, "X1-AA1", target.System, "the buy hint names the largest unmet gap")
	require.Equal(t, probebuy.DefaultHopPenaltyCredits, target.HopPenaltyCredits)
	require.Equal(t, probebuy.DefaultSiblingPriceMarginCredits, target.SiblingPriceMarginCredits)
	require.Equal(t, cmd.ContainerID, target.ClaimOwnerContainerID)
}

// ---- adversarial reader failures -------------------------------------------------

func TestSensing_ReaderFailuresFailClosed(t *testing.T) {
	t.Run("depth read error aborts the tick", func(t *testing.T) {
		dr := &fakeDepthReader{rows: richRows("X1-AA1", 3), err: errors.New("census down")}
		pr := newSensingPostRepo(sensingPost("X1-OLD", 1))
		h, buyer := newSensingHandler(dr, pr, calmFleet(t), &fakePressure{})

		require.Error(t, h.ReconcileOnce(context.Background(), sensingCmd()))
		require.Empty(t, pr.upserts, "rows returned alongside an error must never be consumed")
		require.Empty(t, pr.removed)
		require.Empty(t, buyer.calls)
	})

	t.Run("post list error aborts the tick", func(t *testing.T) {
		pr := newSensingPostRepo(sensingPost("X1-OLD", 1))
		pr.listErr = errors.New("posts down")
		h, buyer := newSensingHandler(&fakeDepthReader{rows: richRows("X1-AA1", 3)}, pr, calmFleet(t), &fakePressure{})

		require.Error(t, h.ReconcileOnce(context.Background(), sensingCmd()))
		require.Empty(t, pr.upserts, "a posts view returned alongside an error is not a diff base — writing against it mass-declares")
		require.Empty(t, pr.removed)
		require.Empty(t, buyer.calls)
	})

	t.Run("fleet read error blocks the buy", func(t *testing.T) {
		fl := &fakeSensingFleet{ships: []*navigation.Ship{sensingProbe(t, "PROBE-A")}, err: errors.New("fleet down")}
		h, buyer := newSensingHandler(&fakeDepthReader{rows: richRows("X1-AA1", 3)}, newSensingPostRepo(), fl, &fakePressure{})

		require.Error(t, h.ReconcileOnce(context.Background(), sensingCmd()))
		require.Empty(t, buyer.calls, "an unverifiable supply must never reach the buyer (fail closed)")
	})
}

// ---- error-streak health monitor ---------------------------------------------------

// TestSensingStreak_ReconcileFailsRepeatedly_EmitsErrorLoopEvent pins the streak
// wiring at the sensing reconcile checkpoint: a pass failing with the identical
// error for DefaultStreakThreshold consecutive ticks crosses exactly once and
// emits one interrupt-class coordinator error-loop event — under the wake model
// that event is the ONLY standing sensor for a persistently failing loop.
func TestSensingStreak_ReconcileFailsRepeatedly_EmitsErrorLoopEvent(t *testing.T) {
	// Adversarial recorder: the outbox write FAILS every time yet still lands —
	// emission must stay edge-triggered (exactly one), never retried or gated.
	rec := &fakeSensingEventRecorder{err: errors.New("outbox flaky")}
	h, _ := newSensingHandler(&fakeDepthReader{}, newSensingPostRepo(), calmFleet(t), &fakePressure{})
	h.SetEventRecorder(rec)

	ctx := context.Background()
	cmd := sensingCmd()
	errMon := health.NewMonitor(health.DefaultStreakThreshold)
	sameErr := errors.New("failed to read market depth census: db down")

	for i := 1; i < health.DefaultStreakThreshold; i++ {
		h.noteReconcile(ctx, cmd, errMon, sameErr)
	}
	require.Empty(t, rec.recorded, "no event before the streak threshold")

	h.noteReconcile(ctx, cmd, errMon, sameErr)
	require.Equal(t, 1, countSensingErrorLoops(rec), "exactly one error-loop event at the threshold (edge-triggered)")
	event := rec.recorded[0]
	require.Equal(t, captain.EventCoordinatorErrorLoop, event.Type)
	require.Equal(t, cmd.ContainerID, event.Ship, "the event is container-scoped to this coordinator")
	require.Equal(t, cmd.PlayerID.Value(), event.PlayerID)
}

// TestSensingStreak_SuccessResetsStreak pins reset-on-success: a healthy pass
// between failures restarts the streak, so an intermittent census gap never
// falsely escalates to the captain.
func TestSensingStreak_SuccessResetsStreak(t *testing.T) {
	rec := &fakeSensingEventRecorder{}
	h, _ := newSensingHandler(&fakeDepthReader{}, newSensingPostRepo(), calmFleet(t), &fakePressure{})
	h.SetEventRecorder(rec)

	ctx := context.Background()
	cmd := sensingCmd()
	errMon := health.NewMonitor(health.DefaultStreakThreshold)
	sameErr := errors.New("failed to read market depth census: db down")

	for i := 1; i < health.DefaultStreakThreshold; i++ {
		h.noteReconcile(ctx, cmd, errMon, sameErr)
	}
	h.noteReconcile(ctx, cmd, errMon, nil) // success resets

	for i := 1; i < health.DefaultStreakThreshold; i++ {
		h.noteReconcile(ctx, cmd, errMon, sameErr)
	}
	require.Zero(t, countSensingErrorLoops(rec), "the success must have reset the streak (no event yet)")

	h.noteReconcile(ctx, cmd, errMon, sameErr)
	require.Equal(t, 1, countSensingErrorLoops(rec), "exactly one event after the post-reset streak re-crossed")
}

// ---- config resolution -------------------------------------------------------------

func TestSensing_ConfigDefaults(t *testing.T) {
	cfg := resolveSensingConfig(&RunProbeSensingCoordinatorCommand{})

	wantWhitelist := []string{
		"CLOTHING", "LAB_INSTRUMENTS", "FABRICS", "FOOD", "ADVANCED_CIRCUITRY",
		"MEDICINE", "EQUIPMENT", "URANITE", "MICROPROCESSORS", "SHIP_PLATING",
		"MACHINERY", "ELECTRONICS",
	}
	require.Len(t, cfg.Whitelist, len(wantWhitelist))
	for _, good := range wantWhitelist {
		require.True(t, cfg.Whitelist[good], "default whitelist must carry %s", good)
	}
	require.Equal(t, int64(2_000_000), cfg.DepthFloor)
	require.Equal(t, 150, cfg.ProbeBudget)
	require.Equal(t, 12, cfg.SecondProbeThreshold)
	require.Equal(t, time.Hour, cfg.FreshnessTarget)
	require.Equal(t, 30*time.Second, cfg.Tick)
	require.Equal(t, 50*time.Millisecond, cfg.WaitLow)
	require.Equal(t, time.Second, cfg.WaitHigh)
	require.Equal(t, 150, cfg.Buy.MaxProbeFleet, "N is also the buyer's satellite cap")
	require.Equal(t, 10*time.Second, cfg.Buy.PurchaseCooldown)
	require.Equal(t, 500_000, cfg.Buy.MaxSpendPerCycle)
	require.Equal(t, time.Hour, cfg.Buy.SpendWindow)
	require.Equal(t, int64(common.ImmutableReserveFloor), cfg.Buy.ReserveFloor,
		"probe buys leave the immutable working-capital reserve spendable (RULINGS #4/#5)")
}

func TestSensing_BuyerConfigFromCommand(t *testing.T) {
	dr := &fakeDepthReader{rows: richRows("X1-AA1", 3)}
	h, buyer := newSensingHandler(dr, newSensingPostRepo(), calmFleet(t), &fakePressure{})
	var captured probebuy.Config
	h.newBuyer = func(cfg probebuy.Config) guardedBuyer {
		captured = cfg
		return buyer
	}

	cmd := sensingCmd()
	cmd.ProbeBudget = 40
	cmd.PurchaseCooldownSecs = 25
	cmd.MaxSpendPerCycle = 123_456
	cmd.SpendWindowSecs = 777
	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	require.Equal(t, 40, captured.MaxProbeFleet)
	require.Equal(t, 25*time.Second, captured.PurchaseCooldown)
	require.Equal(t, 123_456, captured.MaxSpendPerCycle)
	require.Equal(t, 777*time.Second, captured.SpendWindow)
	require.Equal(t, int64(common.ImmutableReserveFloor), captured.ReserveFloor)
}
