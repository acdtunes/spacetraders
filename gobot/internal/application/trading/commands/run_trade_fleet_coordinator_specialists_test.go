package commands

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

// tfIdleShipAt is tfIdleShip with the hull's system under the test's control — the
// specialist plan picks by proximity, so the waypoint is an input, not scenery.
func tfIdleShipAt(t *testing.T, symbol, fleet, waypoint string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint(waypoint, 1, 1)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 400, 40, cargo, 30, "FRAME_FRIGATE", "HAULER", nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	ship.SetDedicatedFleet(fleet)
	return ship
}

// tfLadenShipAt is a parked hull still holding cargo — idle by the fleet view, mid-load
// in fact.
func tfLadenShipAt(t *testing.T, symbol, fleet, waypoint string, units int) *navigation.Ship {
	t.Helper()
	ship := tfIdleShipAt(t, symbol, fleet, waypoint)
	item, err := shared.NewCargoItem("GOLD", "Gold", "", units)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(40, units, []*shared.CargoItem{item})
	require.NoError(t, err)
	ship.SetCargo(cargo)
	return ship
}

func symbols(ships []*navigation.Ship) []string {
	out := []string{}
	for _, s := range ships {
		out = append(out, s.ShipSymbol())
	}
	return out
}

func TestPlanSpecialists_PromotesClosestIdleMVTHull(t *testing.T) {
	a := tfIdleShipAt(t, "M-A", "trade-mvt", "X1-A-1")
	b := tfIdleShipAt(t, "M-B", "trade-mvt", "X1-B-1")
	c := tfIdleShipAt(t, "M-C", "trade-mvt", "X1-C-1")
	running := tfIdleShipAt(t, "M-RUN", "trade-mvt", "X1-A-2") // in all, not idle
	fat := []mvt.LaneStat{{Source: "X1-B", Sink: "X1-A", Good: "GOLD", MarginPerTranche: 9000}}
	promote, demote := planSpecialists([]*navigation.Ship{a, b, c, running}, []*navigation.Ship{a, b, c}, fat, 1, nil)
	if got := symbols(promote); len(got) != 1 || got[0] != "M-B" {
		t.Fatalf("promote = %v, want the hull in the lane's source", got)
	}
	if len(demote) != 0 {
		t.Fatalf("demote = %v", symbols(demote))
	}
	// No hull at source: sink wins; none at either: lowest symbol.
	promote, _ = planSpecialists([]*navigation.Ship{a, c}, []*navigation.Ship{a, c}, fat, 1, nil)
	if got := symbols(promote); len(got) != 1 || got[0] != "M-A" {
		t.Fatalf("promote = %v, want the sink hull", got)
	}
	promote, _ = planSpecialists([]*navigation.Ship{c}, []*navigation.Ship{c}, fat, 1, nil)
	if got := symbols(promote); len(got) != 1 || got[0] != "M-C" {
		t.Fatalf("promote = %v", got)
	}
}

func TestPlanSpecialists_ShrinkDemotesLowestMarginAndSelfDemotesOrphans(t *testing.T) {
	l1 := tfIdleShipAt(t, "L-1", "trade-lane", "X1-A-1")
	l2 := tfIdleShipAt(t, "L-2", "trade-lane", "X1-B-1")
	l3 := tfIdleShipAt(t, "L-3", "trade-lane", "X1-Z-1") // no fat lane touches X1-Z
	lRun := tfIdleShipAt(t, "L-RUN", "trade-lane", "X1-Z-2")
	fat := []mvt.LaneStat{{Source: "X1-B", Sink: "X1-A", Good: "GOLD"}}
	margins := map[string]float64{"L-1": 100, "L-2": 5000}
	promote, demote := planSpecialists([]*navigation.Ship{l1, l2, l3, lRun}, []*navigation.Ship{l1, l2, l3}, fat, 1, margins)
	if len(promote) != 0 {
		t.Fatalf("promote = %v", symbols(promote))
	}
	got := symbols(demote)
	// L-3 self-demotes (orphan); pool 1 with 4 specialists → 2 more must go but only idle
	// ones can: L-1 (lowest margin) goes; L-2 stays because L-RUN cannot be touched yet.
	want := map[string]bool{"L-3": true, "L-1": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("demote = %v, want L-3 and L-1", got)
	}
}

func TestPlanSpecialists_PoolZeroTouchesNothing(t *testing.T) {
	m := tfIdleShipAt(t, "M-A", "trade-mvt", "X1-A-1")
	promote, demote := planSpecialists([]*navigation.Ship{m}, []*navigation.Ship{m}, nil, 0, nil)
	if len(promote)+len(demote) != 0 {
		t.Fatal("N=1 → pool 0 → no changes")
	}
}

func TestPlanSpecialists_NeverTagsAHullMidLoad(t *testing.T) {
	// A demoted laden specialist's next tour is scope-pinned to one system and cannot
	// reach the sink its cargo was bought for; a promoted laden hull is the mirror.
	fat := []mvt.LaneStat{{Source: "X1-B", Sink: "X1-A", Good: "GOLD"}}
	laden := []*navigation.Ship{
		tfLadenShipAt(t, "L-1", "trade-lane", "X1-Z-1", 12), // orphan: would demote if empty
		tfLadenShipAt(t, "M-A", "trade-mvt", "X1-B-1", 5),   // at the source: would promote if empty
	}
	promote, demote := planSpecialists(laden, laden, fat, 1, nil)
	require.Empty(t, symbols(promote))
	require.Empty(t, symbols(demote))

	drained := []*navigation.Ship{
		tfIdleShipAt(t, "L-1", "trade-lane", "X1-Z-1"),
		tfIdleShipAt(t, "M-A", "trade-mvt", "X1-B-1"),
	}
	promote, demote = planSpecialists(drained, drained, fat, 1, nil)
	require.Equal(t, []string{"M-A"}, symbols(promote), "caught on the cadence after it drains")
	require.Equal(t, []string{"L-1"}, symbols(demote))
}

func TestPlanSpecialists_ExcessDemotionSkipsALadenSpecialist(t *testing.T) {
	// The third door: neither hull is an orphan, so the excess rule ranks them and the
	// laden one sorts first on a nil margin. Demoting it strands cargo bought for a sink
	// its next (single-system) tour cannot reach.
	fat := []mvt.LaneStat{{Source: "X1-B", Sink: "X1-A", Good: "GOLD"}}
	laden := tfLadenShipAt(t, "L-1", tradeFleetLane, "X1-B-1", 12)
	empty := tfIdleShipAt(t, "L-2", tradeFleetLane, "X1-B-1")
	ships := []*navigation.Ship{laden, empty}

	// The laden hull ranks worst (tie broken by symbol) and keeps its tag until it drains,
	// exactly as a low-ranked RUNNING specialist does.
	_, demote := planSpecialists(ships, ships, fat, 1, nil)
	require.Empty(t, symbols(demote))

	// Rank the empty hull worst and the excess still comes out — the skip is about cargo,
	// not about refusing to shrink.
	_, demote = planSpecialists(ships, ships, fat, 1, map[string]float64{"L-1": 5000, "L-2": 100})
	require.Equal(t, []string{"L-2"}, symbols(demote))
}

// ---- reconcileSpecialists fakes --------------------------------------------

type fakeSpecialistShipRepo struct {
	navigation.ShipRepository
	assigned  map[string]string
	saves     int
	assignErr error
	failFor   string // when set, only this hull's write fails
}

func (r *fakeSpecialistShipRepo) AssignFleet(_ context.Context, shipSymbol, fleet string, _ shared.PlayerID) error {
	if r.assignErr != nil && (r.failFor == "" || r.failFor == shipSymbol) {
		return r.assignErr
	}
	if r.assigned == nil {
		r.assigned = map[string]string{}
	}
	r.assigned[shipSymbol] = fleet
	return nil
}

func (r *fakeSpecialistShipRepo) Save(context.Context, *navigation.Ship) error {
	r.saves++
	return nil
}

type fakeSpecialistTelemetry struct {
	legs  []trading.TourLegTelemetry
	err   error
	reads int
}

func (f *fakeSpecialistTelemetry) RecordLeg(context.Context, trading.TourLegTelemetry) error {
	return nil
}

func (f *fakeSpecialistTelemetry) ListByPlayer(context.Context, int, time.Time) ([]trading.TourLegTelemetry, error) {
	f.reads++
	return f.legs, f.err
}

type fakeSpecialistFees map[string]int64

func (f fakeSpecialistFees) GateFees(context.Context, int) map[string]int64 { return f }

type fakeSpecialistClaims struct{ released []string }

func (c *fakeSpecialistClaims) Upsert(context.Context, int, string, string, time.Time) error {
	return nil
}
func (c *fakeSpecialistClaims) MarkArrived(context.Context, int, string, time.Time) error {
	return nil
}
func (c *fakeSpecialistClaims) Release(_ context.Context, _ int, hull string) error {
	c.released = append(c.released, hull)
	return nil
}
func (c *fakeSpecialistClaims) Get(context.Context, int, string) (mvt.Claim, bool, error) {
	return mvt.Claim{}, false, nil
}
func (c *fakeSpecialistClaims) InTransit(context.Context, int) (map[string]int, error) {
	return nil, nil
}

// specialistLegs is one cross-system tranche (X1-B → X1-A, +9000) and one intra-system
// tranche (+100), the minimum that makes a lane fat against a real baseline.
func specialistLegs(withIntra bool) []trading.TourLegTelemetry {
	leg := func(wp, good string, isBuy bool, price int, offset time.Duration) trading.TourLegTelemetry {
		at := baseTime.Add(offset)
		return trading.TourLegTelemetry{ShipSymbol: "T-1", Waypoint: wp, Good: good, IsBuy: isBuy, RealizedUnits: 10, RealizedUnitPrice: price, PlannedAt: at, RealizedAt: at}
	}
	legs := []trading.TourLegTelemetry{
		leg("X1-B-1", "GOLD", true, 100, 0),
		leg("X1-A-1", "GOLD", false, 1000, time.Minute),
	}
	if withIntra {
		legs = append(legs, leg("X1-A-1", "SILVER", true, 10, 2*time.Minute), leg("X1-A-2", "SILVER", false, 20, 3*time.Minute))
	}
	return legs
}

func newSpecialistHandler(repo navigation.ShipRepository, claims *fakeSpecialistClaims, legs []trading.TourLegTelemetry) *RunTradeFleetCoordinatorHandler {
	h, _ := newSpecialistHandlerWithTelemetry(repo, claims, legs)
	return h
}

func newSpecialistHandlerWithTelemetry(repo navigation.ShipRepository, claims *fakeSpecialistClaims, legs []trading.TourLegTelemetry) (*RunTradeFleetCoordinatorHandler, *fakeSpecialistTelemetry) {
	h := NewRunTradeFleetCoordinatorHandler(repo, clockAt(0))
	tel := &fakeSpecialistTelemetry{legs: legs}
	h.SetSpecialistPorts(claims, tel, fakeSpecialistFees{})
	return h, tel
}

func TestReconcileSpecialists_PersistsThePromotionThroughAssignFleet(t *testing.T) {
	// The row is the whole point. ShipRepository.Save re-reads the persisted
	// dedicated_fleet and discards the outgoing one, so a Save-based pool is inert:
	// assert what the repository was ASKED to write, not the in-memory tag.
	m := tfIdleShipAt(t, "M-A", tradeFleetMVT, "X1-B-1")
	all := []*navigation.Ship{m}
	repo, claims := &fakeSpecialistShipRepo{}, &fakeSpecialistClaims{}
	h := newSpecialistHandler(repo, claims, specialistLegs(true))
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), SpecialistFractionPct: 100}

	promoted, demoted, retags := h.reconcileSpecialists(context.Background(), cmd, all, all, baseTime, &tradeCaptureLogger{})

	require.Equal(t, 1, promoted)
	require.Equal(t, 0, demoted)
	require.Equal(t, map[string]string{"M-A": tradeFleetLane}, repo.assigned)
	require.Zero(t, repo.saves, "Save never writes dedicated_fleet")
	require.Equal(t, []string{"M-A"}, claims.released)
	require.Equal(t, tradeFleetLane, retags["M-A"], "this tick's launch must see the new tag")
}

func TestReconcileSpecialists_NeverMutatesTheCachedShipEntity(t *testing.T) {
	// FindAllByPlayer's 15s shipListCache copies the SLICE, not the ships, so the pointers
	// handed to this pass are the same ones ~25 other call sites across the daemon are
	// reading concurrently. The new tag comes back in the return map instead.
	m := tfIdleShipAt(t, "M-A", tradeFleetMVT, "X1-B-1")
	all := []*navigation.Ship{m}
	repo, claims := &fakeSpecialistShipRepo{}, &fakeSpecialistClaims{}
	h := newSpecialistHandler(repo, claims, specialistLegs(true))
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), SpecialistFractionPct: 100}

	promoted, _, retags := h.reconcileSpecialists(context.Background(), cmd, all, all, baseTime, &tradeCaptureLogger{})

	require.Equal(t, 1, promoted)
	require.Equal(t, tradeFleetMVT, m.DedicatedFleet(), "the shared entity must be untouched")
	require.Equal(t, map[string]string{"M-A": tradeFleetLane}, repo.assigned, "the row is where the tag moves")
	require.Equal(t, tradeFleetLane, retags["M-A"])
	// And the launch loop reads the committed tag off the map, not off the stale entity.
	require.Equal(t, tradeFleetLane, effectiveFleetTag(m, retags))
	require.Equal(t, tradeFleetMVT, effectiveFleetTag(m, nil))
}

func TestReconcileSpecialists_AFailedWriteLeavesTheTagAsItWas(t *testing.T) {
	m := tfIdleShipAt(t, "M-A", tradeFleetMVT, "X1-B-1")
	all := []*navigation.Ship{m}
	repo, claims := &fakeSpecialistShipRepo{assignErr: errors.New("row locked")}, &fakeSpecialistClaims{}
	h := newSpecialistHandler(repo, claims, specialistLegs(true))
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), SpecialistFractionPct: 100}

	promoted, demoted, _ := h.reconcileSpecialists(context.Background(), cmd, all, all, baseTime, &tradeCaptureLogger{})

	require.Zero(t, promoted+demoted)
	require.Equal(t, tradeFleetMVT, m.DedicatedFleet())
	require.Empty(t, claims.released, "an uncommitted promotion must not strip the hull's claim")
}

func TestReconcileSpecialists_NoIntraBaselineLeavesEverySpecialistAlone(t *testing.T) {
	// Absence of evidence, not evidence of absence: with no baseline every lane fails
	// IsFatLane, and demoting on that would empty the pool after any telemetry prune.
	for _, tc := range []struct {
		name string
		legs []trading.TourLegTelemetry
	}{
		{"empty window", nil},
		{"all cross-system", specialistLegs(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lane := tfIdleShipAt(t, "L-1", tradeFleetLane, "X1-Z-1") // orphan: demotes on a real read
			all := []*navigation.Ship{lane}
			repo, claims := &fakeSpecialistShipRepo{}, &fakeSpecialistClaims{}
			h := newSpecialistHandler(repo, claims, tc.legs)
			logger := &tradeCaptureLogger{}
			cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), SpecialistFractionPct: 100}

			promoted, demoted, _ := h.reconcileSpecialists(context.Background(), cmd, all, all, baseTime, logger)

			require.Zero(t, promoted+demoted)
			require.Empty(t, repo.assigned)
			require.Equal(t, tradeFleetLane, lane.DedicatedFleet())
			require.True(t, logger.loggedContaining("no intra-system baseline"))
		})
	}
}

func TestReconcileSpecialists_SizesThePoolOffTheMigratedCohortOnly(t *testing.T) {
	// Ten legacy 'trade' hulls plus a one-hull cohort. Counting the whole trade fleet
	// gives floor(11 × 0.10) = 1 and promotes the cohort's only hull off the MVT loop;
	// the cohort itself gives floor(1 × 0.10) = 0, so nothing moves.
	m := tfIdleShipAt(t, "M-A", tradeFleetMVT, "X1-B-1")
	all := []*navigation.Ship{m}
	for i := 0; i < 10; i++ {
		all = append(all, tfIdleShipAt(t, fmt.Sprintf("T-%02d", i), tradeFleet, "X1-B-1"))
	}
	repo, claims := &fakeSpecialistShipRepo{}, &fakeSpecialistClaims{}
	h := newSpecialistHandler(repo, claims, specialistLegs(true))
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1)}

	promoted, demoted, _ := h.reconcileSpecialists(context.Background(), cmd, all, all, baseTime, &tradeCaptureLogger{})

	require.Zero(t, promoted+demoted)
	require.Empty(t, repo.assigned)
	require.Equal(t, tradeFleetMVT, m.DedicatedFleet())
}

func TestReconcileSpecialists_InertBelowTenMigratedHullsWithoutAFlag(t *testing.T) {
	// No arm flag (RULINGS #22): the pool is min(fat lanes, floor(N × 10%)) over the migrated
	// cohort. Five hulls and one fat lane derive 0 and nothing moves — the pass RUNS and says
	// so; the tenth hull opens the first seat and the hull at the lane's source takes it.
	cohort := func(n int) []*navigation.Ship {
		all := []*navigation.Ship{}
		for i := 0; i < n; i++ {
			all = append(all, tfIdleShipAt(t, fmt.Sprintf("M-%02d", i), tradeFleetMVT, "X1-B-1"))
		}
		return all
	}
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1)}

	five := cohort(5)
	repo, claims := &fakeSpecialistShipRepo{}, &fakeSpecialistClaims{}
	h, tel := newSpecialistHandlerWithTelemetry(repo, claims, specialistLegs(true))
	logger := &tradeCaptureLogger{}
	promoted, demoted, retags := h.reconcileSpecialists(context.Background(), cmd, five, five, baseTime, logger)
	require.Zero(t, promoted+demoted)
	require.Empty(t, retags)
	require.Empty(t, repo.assigned)
	require.Empty(t, claims.released)
	require.Equal(t, 1, tel.reads, "the count holds the pool at 0, not a gate ahead of the read")
	require.True(t, logger.loggedContaining("Specialist pool: sized"))

	ten := cohort(10)
	repo, claims = &fakeSpecialistShipRepo{}, &fakeSpecialistClaims{}
	h = newSpecialistHandler(repo, claims, specialistLegs(true))
	promoted, demoted, retags = h.reconcileSpecialists(context.Background(), cmd, ten, ten, baseTime, &tradeCaptureLogger{})
	require.Equal(t, 1, promoted)
	require.Zero(t, demoted)
	require.Equal(t, map[string]string{"M-00": tradeFleetLane}, repo.assigned)
	require.Equal(t, map[string]string{"M-00": tradeFleetLane}, retags)
	require.Equal(t, []string{"M-00"}, claims.released)
}

func TestReconcileSpecialists_NoMigratedHullSkipsTheTelemetryScan(t *testing.T) {
	// A legacy fleet (only "trade" tags) can never promote or demote, so the hourly pass
	// must not read 24h of telemetry or log a sizing line: byte- and I/O-identical to main.
	legacy := []*navigation.Ship{
		tfIdleShipAt(t, "T-00", tradeFleet, "X1-B-1"),
		tfIdleShipAt(t, "T-01", tradeFleet, "X1-A-1"),
	}
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1)}
	repo, claims := &fakeSpecialistShipRepo{}, &fakeSpecialistClaims{}
	h, tel := newSpecialistHandlerWithTelemetry(repo, claims, specialistLegs(true))
	logger := &tradeCaptureLogger{}
	promoted, demoted, retags := h.reconcileSpecialists(context.Background(), cmd, legacy, legacy, baseTime, logger)
	require.Zero(t, promoted+demoted)
	require.Empty(t, retags)
	require.Zero(t, tel.reads, "no cohort means nothing can move: skip the scan")
	require.False(t, logger.loggedContaining("Specialist pool: sized"))
	require.True(t, h.specialistsAt.IsZero(), "an unrun pass must not consume the cadence")
}

func TestReconcileSpecialists_ATransientReadErrorRetriesNextTick(t *testing.T) {
	// The cadence stamp is the pass's own rate limiter: stamping it before the read means
	// one unreadable window costs a full hour, not one tick.
	m := tfIdleShipAt(t, "M-A", tradeFleetMVT, "X1-B-1")
	all := []*navigation.Ship{m}
	repo, claims := &fakeSpecialistShipRepo{}, &fakeSpecialistClaims{}
	h, tel := newSpecialistHandlerWithTelemetry(repo, claims, specialistLegs(true))
	tel.err = errors.New("db unreachable")
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), SpecialistFractionPct: 100}

	promoted, demoted, _ := h.reconcileSpecialists(context.Background(), cmd, all, all, baseTime, &tradeCaptureLogger{})
	require.Zero(t, promoted+demoted)
	require.True(t, h.specialistsAt.IsZero(), "a failed read must not consume the cadence")

	// One tick later — far inside the 1h cadence — the pass runs again and succeeds.
	tel.err = nil
	promoted, _, _ = h.reconcileSpecialists(context.Background(), cmd, all, all, baseTime.Add(30*time.Second), &tradeCaptureLogger{})
	require.Equal(t, 1, promoted)
	require.Equal(t, baseTime.Add(30*time.Second), h.specialistsAt, "stamped only once it had a window")
}

func TestReconcileSpecialists_PartialWiringLeavesThePoolInert(t *testing.T) {
	// Once armed, the pass dereferences all three ports; a nil one must leave the pool
	// unwired rather than panic the coordinator goroutine.
	m := tfIdleShipAt(t, "M-A", tradeFleetMVT, "X1-B-1")
	all := []*navigation.Ship{m}
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), SpecialistFractionPct: 100}
	for _, tc := range []struct {
		name      string
		claims    mvt.ClaimRegistry
		telemetry trading.TourTelemetryRepository
		fees      GateFeeReader
	}{
		{"no claims", nil, &fakeSpecialistTelemetry{legs: specialistLegs(true)}, fakeSpecialistFees{}},
		{"no telemetry", &fakeSpecialistClaims{}, nil, fakeSpecialistFees{}},
		{"no fees", &fakeSpecialistClaims{}, &fakeSpecialistTelemetry{legs: specialistLegs(true)}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeSpecialistShipRepo{}
			h := NewRunTradeFleetCoordinatorHandler(repo, clockAt(0))
			h.SetSpecialistPorts(tc.claims, tc.telemetry, tc.fees)

			promoted, demoted, retags := h.reconcileSpecialists(context.Background(), cmd, all, all, baseTime, &tradeCaptureLogger{})

			require.Zero(t, promoted+demoted)
			require.Empty(t, retags)
			require.Empty(t, repo.assigned)
		})
	}
}

func TestReconcileSpecialists_AFailedDemoteDoesNotOpenASeat(t *testing.T) {
	// seats are counted off the demotes that COMMITTED: a demote whose write failed leaves
	// its hull in the pool, so promoting into its seat would overfill the pool by one.
	orphan := tfIdleShipAt(t, "L-1", tradeFleetLane, "X1-Z-1") // no fat lane touches X1-Z
	cand := tfIdleShipAt(t, "M-A", tradeFleetMVT, "X1-B-1")
	all := []*navigation.Ship{orphan, cand}
	// Only the demote's write fails; the promotion's would have gone through.
	repo := &fakeSpecialistShipRepo{assignErr: errors.New("row locked"), failFor: "L-1"}
	claims := &fakeSpecialistClaims{}
	h := newSpecialistHandler(repo, claims, specialistLegs(true))
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), SpecialistFractionPct: 50}

	promoted, demoted, retags := h.reconcileSpecialists(context.Background(), cmd, all, all, baseTime, &tradeCaptureLogger{})

	require.Zero(t, demoted, "the demote write failed")
	require.Zero(t, promoted, "so its seat never opened — the pool would have held two on a pool of one")
	require.Empty(t, retags)
	require.Empty(t, repo.assigned)
	require.Empty(t, claims.released)
	require.Equal(t, tradeFleetLane, orphan.DedicatedFleet())
	require.Equal(t, tradeFleetMVT, cand.DedicatedFleet())
}
