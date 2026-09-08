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
	require.Empty(t, symbols(promote))
	// L-3 self-demotes (orphan); pool 1 with 4 specialists → 2 more must go, and the running
	// L-RUN is chosen alongside L-1 rather than skipped for being busy: its tag moves when it
	// next parks drained. L-2 out-earns both and keeps its seat.
	require.ElementsMatch(t, []string{"L-3", "L-RUN", "L-1"}, symbols(demote))
}

func TestPlanSpecialists_PrefersAHullThatCanTakeTheTagNow(t *testing.T) {
	// Safe-first ordering. A busy hull standing at the lane's source does not outrank a free
	// hull elsewhere: the busy one will be wherever its current tour ends by the time its tag
	// moves, so its position at this instant carries no information.
	busyAtSource := tfIdleShipAt(t, "M-A", tradeFleetMVT, "X1-B-1")
	freeElsewhere := tfIdleShipAt(t, "M-Z", tradeFleetMVT, "X1-Q-1")
	fat := []mvt.LaneStat{{Source: "X1-B", Sink: "X1-A", Good: "GOLD"}}

	promote, _ := planSpecialists([]*navigation.Ship{busyAtSource, freeElsewhere}, []*navigation.Ship{freeElsewhere}, fat, 1, nil)

	require.Equal(t, []string{"M-Z"}, symbols(promote))
}

func TestPlanSpecialists_PoolZeroTouchesNothing(t *testing.T) {
	m := tfIdleShipAt(t, "M-A", "trade-mvt", "X1-A-1")
	promote, demote := planSpecialists([]*navigation.Ship{m}, []*navigation.Ship{m}, nil, 0, nil)
	if len(promote)+len(demote) != 0 {
		t.Fatal("N=1 → pool 0 → no changes")
	}
}

func TestPlanSpecialists_EarmarksALadenHullInsteadOfSkippingIt(t *testing.T) {
	// A laden hull is a legitimate CHOICE; only the write is withheld, until the load is gone
	// (applySpecialistTag is that gate). Skipping it here is what starved the pool: on a
	// working fleet every candidate is either laden or flying.
	fat := []mvt.LaneStat{{Source: "X1-B", Sink: "X1-A", Good: "GOLD"}}
	laden := []*navigation.Ship{
		tfLadenShipAt(t, "L-1", "trade-lane", "X1-Z-1", 12), // orphan
		tfLadenShipAt(t, "M-A", "trade-mvt", "X1-B-1", 5),   // at the source
	}
	promote, demote := planSpecialists(laden, laden, fat, 1, nil)
	require.Equal(t, []string{"M-A"}, symbols(promote))
	require.Equal(t, []string{"L-1"}, symbols(demote))

	// Drained, the same two are chosen: the plan is stable across the boundary, so the
	// earmark and the settle that follows it agree on who moves.
	drained := []*navigation.Ship{
		tfIdleShipAt(t, "L-1", "trade-lane", "X1-Z-1"),
		tfIdleShipAt(t, "M-A", "trade-mvt", "X1-B-1"),
	}
	promote, demote = planSpecialists(drained, drained, fat, 1, nil)
	require.Equal(t, []string{"M-A"}, symbols(promote))
	require.Equal(t, []string{"L-1"}, symbols(demote))
}

func TestPlanSpecialists_ExcessDemotionRanksOnMarginNotOnBeingFree(t *testing.T) {
	// The third door: neither hull is an orphan, so the excess rule ranks them and the laden
	// one sorts first on a nil margin. It is chosen — being busy is no longer a reprieve, it
	// only moves WHEN the tag changes.
	fat := []mvt.LaneStat{{Source: "X1-B", Sink: "X1-A", Good: "GOLD"}}
	laden := tfLadenShipAt(t, "L-1", tradeFleetLane, "X1-B-1", 12)
	empty := tfIdleShipAt(t, "L-2", tradeFleetLane, "X1-B-1")
	ships := []*navigation.Ship{laden, empty}

	_, demote := planSpecialists(ships, ships, fat, 1, nil)
	require.Equal(t, []string{"L-1"}, symbols(demote))

	// Rank the empty hull worst and it is the one that goes: the ranking is on earnings, not
	// on which hull happens to be free.
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

// ---- deferred re-tags ------------------------------------------------------

// tfCohort is n trade-mvt hulls standing at the fat lane's source.
func tfCohort(t *testing.T, n int) []*navigation.Ship {
	t.Helper()
	out := []*navigation.Ship{}
	for i := 0; i < n; i++ {
		out = append(out, tfIdleShipAt(t, fmt.Sprintf("M-%02d", i), tradeFleetMVT, "X1-B-1"))
	}
	return out
}

func TestReconcileSpecialists_ABusyFleetPromotesAtTheNextSafeBoundary(t *testing.T) {
	// The starvation this fixes. A fully-utilised fleet holds no idle, empty hull at the
	// instant the hourly pass runs, so a promotion that had to happen right then never
	// happened at all — the pass sized a pool and moved nothing, cadence after cadence.
	cohort := tfCohort(t, 10)
	repo, claims := &fakeSpecialistShipRepo{}, &fakeSpecialistClaims{}
	h := newSpecialistHandler(repo, claims, specialistLegs(true))
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1)}

	// Every hull is mid-tour: the fleet is in `all`, the idle bucket is empty.
	promoted, demoted, retags := h.reconcileSpecialists(context.Background(), cmd, cohort, nil, baseTime, &tradeCaptureLogger{})

	require.Zero(t, promoted+demoted, "nothing may be re-tagged while every hull is flying")
	require.Empty(t, repo.assigned)
	require.Empty(t, retags)
	require.Equal(t, map[string]string{"M-00": tradeFleetLane}, h.specialistPending, "the seat is earmarked, not forgotten")

	// One tour ends. The very next tick — far inside the 1h cadence, so no pass runs — settles.
	promoted, demoted, retags = h.reconcileSpecialists(context.Background(), cmd, cohort, cohort[:1], baseTime.Add(30*time.Second), &tradeCaptureLogger{})

	require.Equal(t, 1, promoted)
	require.Zero(t, demoted)
	require.Equal(t, map[string]string{"M-00": tradeFleetLane}, repo.assigned)
	require.Equal(t, tradeFleetLane, retags["M-00"], "this tick's launch already flies the lane path")
	require.Equal(t, []string{"M-00"}, claims.released)
	require.Empty(t, h.specialistPending, "settled, not left to fire again")
	require.Equal(t, tradeFleetMVT, cohort[0].DedicatedFleet(), "the daemon-shared entity stays untouched")
}

func TestReconcileSpecialists_ALadenHullAtItsBoundaryKeepsItsTag(t *testing.T) {
	// The boundary is idle AND empty. A hull that parks still holding the load it bought for
	// its current path is not re-tagged; the earmark simply waits for the tick it parks drained.
	cohort := tfCohort(t, 10)
	cohort[0] = tfLadenShipAt(t, "M-00", tradeFleetMVT, "X1-B-1", 12)
	repo, claims := &fakeSpecialistShipRepo{}, &fakeSpecialistClaims{}
	h := newSpecialistHandler(repo, claims, specialistLegs(true))
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1)}

	_, _, _ = h.reconcileSpecialists(context.Background(), cmd, cohort, nil, baseTime, &tradeCaptureLogger{})
	require.Equal(t, map[string]string{"M-00": tradeFleetLane}, h.specialistPending)

	// It parks LADEN. The write seam refuses it and the earmark survives.
	promoted, demoted, retags := h.reconcileSpecialists(context.Background(), cmd, cohort, cohort[:1], baseTime.Add(30*time.Second), &tradeCaptureLogger{})
	require.Zero(t, promoted+demoted)
	require.Empty(t, repo.assigned, "re-tagging it would strand the load")
	require.Empty(t, retags)
	require.Empty(t, claims.released)
	require.Equal(t, map[string]string{"M-00": tradeFleetLane}, h.specialistPending)

	// It sells and parks drained: now the tag moves.
	cohort[0] = tfIdleShipAt(t, "M-00", tradeFleetMVT, "X1-B-1")
	promoted, _, retags = h.reconcileSpecialists(context.Background(), cmd, cohort, cohort[:1], baseTime.Add(60*time.Second), &tradeCaptureLogger{})
	require.Equal(t, 1, promoted)
	require.Equal(t, map[string]string{"M-00": tradeFleetLane}, repo.assigned)
	require.Equal(t, tradeFleetLane, retags["M-00"])
}

func TestApplySpecialistTag_RefusesALadenHullWhicheverPathReachesIt(t *testing.T) {
	// The guarantee lives in the write, not in one caller's candidate filter: every re-tag in
	// the pool goes through here, and a hull with anything in its hold is refused.
	repo := &fakeSpecialistShipRepo{}
	h := newSpecialistHandler(repo, &fakeSpecialistClaims{}, specialistLegs(true))
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1)}
	retags := map[string]string{}

	// One unit is enough: the gate is on carrying cargo at all, not on how much.
	laden := tfLadenShipAt(t, "M-A", tradeFleetMVT, "X1-B-1", 1)
	require.False(t, h.applySpecialistTag(context.Background(), cmd, laden, tradeFleetLane, retags, &tradeCaptureLogger{}))
	require.Empty(t, repo.assigned)
	require.Empty(t, retags)

	drained := tfIdleShipAt(t, "M-A", tradeFleetMVT, "X1-B-1")
	require.True(t, h.applySpecialistTag(context.Background(), cmd, drained, tradeFleetLane, retags, &tradeCaptureLogger{}))
	require.Equal(t, map[string]string{"M-A": tradeFleetLane}, repo.assigned)
	require.Equal(t, map[string]string{"M-A": tradeFleetLane}, retags)
}

func TestReconcileSpecialists_DemotesAMidTourSpecialistAtItsNextBoundary(t *testing.T) {
	// Symmetry: a deferred promotion that cannot be undone is worse than none. When the pool
	// shrinks, a specialist that is flying sheds its tag when it next parks drained, instead of
	// keeping it because no cadence ever caught it parked.
	l1 := tfIdleShipAt(t, "L-1", tradeFleetLane, "X1-B-1")
	l2 := tfIdleShipAt(t, "L-2", tradeFleetLane, "X1-B-1")
	all := []*navigation.Ship{l1, l2}
	repo, claims := &fakeSpecialistShipRepo{}, &fakeSpecialistClaims{}
	h := newSpecialistHandler(repo, claims, specialistLegs(true))
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), SpecialistFractionPct: 100}

	// Two specialists, one seat, and both are mid-tour.
	promoted, demoted, _ := h.reconcileSpecialists(context.Background(), cmd, all, nil, baseTime, &tradeCaptureLogger{})
	require.Zero(t, promoted+demoted)
	require.Empty(t, repo.assigned)
	require.Equal(t, map[string]string{"L-1": tradeFleetMVT}, h.specialistPending, "the excess is earmarked")

	promoted, demoted, retags := h.reconcileSpecialists(context.Background(), cmd, all, all[:1], baseTime.Add(30*time.Second), &tradeCaptureLogger{})
	require.Equal(t, 1, demoted)
	require.Zero(t, promoted)
	require.Equal(t, map[string]string{"L-1": tradeFleetMVT}, repo.assigned)
	require.Equal(t, tradeFleetMVT, retags["L-1"])
	require.Empty(t, claims.released, "only a promotion strips the hull's system claim")
	require.Empty(t, h.specialistPending)
}

func TestReconcileSpecialists_ANewPassReplacesAnUnsettledEarmark(t *testing.T) {
	// A deferred change must be revocable. Each pass REPLACES the earmarked set, so an intent
	// the pool no longer wants is dropped rather than settling later against a seat that has
	// closed — the earmark's own lifetime is one cadence.
	cohort := tfCohort(t, 10)
	repo, claims := &fakeSpecialistShipRepo{}, &fakeSpecialistClaims{}
	h := newSpecialistHandler(repo, claims, specialistLegs(true))
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1)}

	_, _, _ = h.reconcileSpecialists(context.Background(), cmd, cohort, nil, baseTime, &tradeCaptureLogger{})
	require.Equal(t, map[string]string{"M-00": tradeFleetLane}, h.specialistPending)

	// A cadence later the cohort has shrunk below the fraction's floor: the pool is 0.
	shrunk := cohort[:5]
	promoted, demoted, retags := h.reconcileSpecialists(context.Background(), cmd, shrunk, nil, baseTime.Add(61*time.Minute), &tradeCaptureLogger{})
	require.Zero(t, promoted+demoted)
	require.Empty(t, h.specialistPending, "the seat closed, so the earmark is gone")

	// Even now that it parks drained, the withdrawn promotion does not land.
	promoted, _, retags = h.reconcileSpecialists(context.Background(), cmd, shrunk, shrunk[:1], baseTime.Add(62*time.Minute), &tradeCaptureLogger{})
	require.Zero(t, promoted)
	require.Empty(t, repo.assigned)
	require.Empty(t, retags)
	require.Empty(t, claims.released)
}

func TestReconcileSpecialists_TheFeeCeilingStillKillsALaneSoNothingIsEarmarked(t *testing.T) {
	// The fat-lane definition, its fee-share ceiling and the pool arithmetic are untouched by
	// the deferral: a lane whose gate eats more than its share of the crossing is still not a
	// lane, so there is no seat, and a busy fleet is earmarked for nothing.
	cohort := tfCohort(t, 10)
	repo, claims := &fakeSpecialistShipRepo{}, &fakeSpecialistClaims{}
	h := NewRunTradeFleetCoordinatorHandler(repo, clockAt(0))
	h.SetSpecialistPorts(claims, &fakeSpecialistTelemetry{legs: specialistLegs(true)}, fakeSpecialistFees{"X1-B": 1_000_000})
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1)}
	logger := &tradeCaptureLogger{}

	promoted, demoted, _ := h.reconcileSpecialists(context.Background(), cmd, cohort, nil, baseTime, logger)

	require.Zero(t, promoted+demoted)
	require.Empty(t, h.specialistPending)
	require.Empty(t, repo.assigned)
	require.True(t, logger.loggedContaining("Specialist pool: sized"), "the pass still runs and still says so")

	// And parking the whole fleet changes nothing: there was never a seat to settle into.
	promoted, _, _ = h.reconcileSpecialists(context.Background(), cmd, cohort, cohort, baseTime.Add(30*time.Second), &tradeCaptureLogger{})
	require.Zero(t, promoted)
	require.Empty(t, repo.assigned)
}

// tfRetiringShipAt is a hull the operator marked retiring, parked and drained — the exact
// profile the pass ranks first and the exact profile that never launches again.
func tfRetiringShipAt(t *testing.T, symbol, fleet, waypoint string) *navigation.Ship {
	t.Helper()
	ship := tfIdleShipAt(t, symbol, fleet, waypoint)
	at := baseTime
	ship.SetRetiringAt(&at)
	return ship
}

func TestPlanSpecialists_NeverPicksARetiringHull(t *testing.T) {
	// A retiring hull is idle and empty, so every proximity and safe-first rule ranks it
	// first — and it stands down for good the moment its hold is empty, so the seat would
	// buy nothing. The hull further from the lane is chosen instead.
	fat := []mvt.LaneStat{{Source: "X1-B", Sink: "X1-A", Good: "GOLD"}}
	retiringAtSource := tfRetiringShipAt(t, "M-A", tradeFleetMVT, "X1-B-1")
	working := tfIdleShipAt(t, "M-Z", tradeFleetMVT, "X1-Q-1")
	ships := []*navigation.Ship{retiringAtSource, working}

	promote, demote := planSpecialists(ships, ships, fat, 1, nil)

	require.Equal(t, []string{"M-Z"}, symbols(promote))
	require.Empty(t, symbols(demote))

	// Cancel the mark and the same hull is the pick again: it is the retirement that
	// disqualifies it, not anything about the hull.
	retiringAtSource.CancelRetirement()
	promote, _ = planSpecialists(ships, ships, fat, 1, nil)
	require.Equal(t, []string{"M-A"}, symbols(promote))
}

func TestPlanSpecialists_ARetiringHullIsNotEvenAFallbackPick(t *testing.T) {
	// The last-resort pick takes any cohort hull at all. A seat left OPEN is strictly better
	// than a seat earmarked to a hull no boundary can ever settle: unlike a laden hull's, a
	// retiring hull's refusal never lapses, so the earmark would fire every cadence forever.
	fat := []mvt.LaneStat{{Source: "X1-B", Sink: "X1-A", Good: "GOLD"}}
	only := []*navigation.Ship{tfRetiringShipAt(t, "M-A", tradeFleetMVT, "X1-Q-1")}

	promote, demote := planSpecialists(only, only, fat, 1, nil)

	require.Empty(t, symbols(promote), "the seat stays open rather than being spent")
	require.Empty(t, symbols(demote))
}

func TestPlanSpecialists_DemotesASpecialistThatEntersRetirement(t *testing.T) {
	// The other door onto the same waste: a hull that already holds the tag when the mark
	// lands. It is no orphan (it stands on a fat lane) and no excess (the pool is exactly its
	// size), so no existing rule would ever move it — and it will never fly the lane again.
	// The seat is worth more than the tag.
	fat := []mvt.LaneStat{{Source: "X1-B", Sink: "X1-A", Good: "GOLD"}}
	retiring := tfRetiringShipAt(t, "L-1", tradeFleetLane, "X1-B-1")
	working := tfIdleShipAt(t, "L-2", tradeFleetLane, "X1-A-1")
	spare := tfIdleShipAt(t, "M-A", tradeFleetMVT, "X1-B-1")
	ships := []*navigation.Ship{retiring, working, spare}

	promote, demote := planSpecialists(ships, ships, fat, 2, nil)

	require.Equal(t, []string{"L-1"}, symbols(demote))
	require.Equal(t, []string{"M-A"}, symbols(promote), "the freed seat is refilled the same pass")
}

func TestPlanSpecialists_DemotesARetiringSpecialistThatIsStillFlying(t *testing.T) {
	// Retirement is a durable mark on the hull, not a fact about where it is parked, so
	// unlike the orphan test it binds mid-tour: the demotion is earmarked now and settles at
	// the hull's next boundary rather than waiting for a cadence that catches it parked.
	fat := []mvt.LaneStat{{Source: "X1-B", Sink: "X1-A", Good: "GOLD"}}
	flying := tfRetiringShipAt(t, "L-1", tradeFleetLane, "X1-B-1")
	all := []*navigation.Ship{flying}

	_, demote := planSpecialists(all, nil, fat, 1, nil)

	require.Equal(t, []string{"L-1"}, symbols(demote))
}

func TestApplySpecialistTag_RefusesToPromoteARetiringHullButLetsItShedTheTag(t *testing.T) {
	// The backstop, at the pool's only tag write, alongside the cargo guard: whichever path
	// reaches here, a marked hull does not take the specialist tag. One-way, because refusing
	// the demotion too would trap the seat in the hull it is meant to free.
	repo := &fakeSpecialistShipRepo{}
	h := newSpecialistHandler(repo, &fakeSpecialistClaims{}, specialistLegs(true))
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1)}
	retags := map[string]string{}
	ctx := context.Background()

	retiring := tfRetiringShipAt(t, "M-A", tradeFleetMVT, "X1-B-1")
	require.False(t, h.applySpecialistTag(ctx, cmd, retiring, tradeFleetLane, retags, &tradeCaptureLogger{}))
	require.Empty(t, repo.assigned)
	require.Empty(t, retags)

	// Same hull, already a specialist: the demotion goes through, which is the free seat.
	shedding := tfRetiringShipAt(t, "L-1", tradeFleetLane, "X1-B-1")
	require.True(t, h.applySpecialistTag(ctx, cmd, shedding, tradeFleetMVT, retags, &tradeCaptureLogger{}))
	require.Equal(t, map[string]string{"L-1": tradeFleetMVT}, repo.assigned)
	require.Equal(t, map[string]string{"L-1": tradeFleetMVT}, retags)

	// And the cargo guard is unweakened and still outranks the direction: a hull holding
	// anything is refused BOTH ways, retiring or not.
	repo.assigned, retags = nil, map[string]string{}
	require.False(t, h.applySpecialistTag(ctx, cmd, tfLadenShipAt(t, "M-B", tradeFleetMVT, "X1-B-1", 1), tradeFleetLane, retags, &tradeCaptureLogger{}))
	require.False(t, h.applySpecialistTag(ctx, cmd, tfLadenShipAt(t, "L-2", tradeFleetLane, "X1-B-1", 1), tradeFleetMVT, retags, &tradeCaptureLogger{}))
	require.Empty(t, repo.assigned)
	require.Empty(t, retags)
}

func TestReconcileSpecialists_ARetiringHullNeverTakesASeatAcrossCadences(t *testing.T) {
	// End to end, and the reason this stopped being theoretical: a standing loop retires a
	// hull on every buy cycle, so the marked hull is present most of the time. The pass
	// re-derives its picks each cadence, so without the guard the same hull is re-picked for
	// the same seat forever.
	cohort := tfCohort(t, 10)
	cohort[0] = tfRetiringShipAt(t, "M-00", tradeFleetMVT, "X1-B-1") // sorts first, drained
	repo, claims := &fakeSpecialistShipRepo{}, &fakeSpecialistClaims{}
	h := newSpecialistHandler(repo, claims, specialistLegs(true))
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1)}

	promoted, demoted, retags := h.reconcileSpecialists(context.Background(), cmd, cohort, cohort, baseTime, &tradeCaptureLogger{})

	require.Equal(t, 1, promoted)
	require.Zero(t, demoted)
	require.Equal(t, map[string]string{"M-01": tradeFleetLane}, repo.assigned, "the seat goes to a hull that will fly")
	require.Equal(t, tradeFleetLane, retags["M-01"])
	require.Empty(t, h.specialistPending)

	// A cadence later — the seat now filled, as the persisted row has it — the mark is still
	// there and still earns nothing. This is the part that never self-healed: the pass
	// re-derives its picks every cadence, and a marked hull stays idle and empty forever.
	cohort[1] = tfIdleShipAt(t, "M-01", tradeFleetLane, "X1-B-1")
	repo.assigned = nil
	promoted, demoted, _ = h.reconcileSpecialists(context.Background(), cmd, cohort, cohort, baseTime.Add(61*time.Minute), &tradeCaptureLogger{})
	require.Zero(t, promoted+demoted)
	require.Empty(t, repo.assigned)
	require.Empty(t, h.specialistPending, "no seat is left earmarked to the marked hull")
}

func TestReconcileSpecialists_ASpecialistMarkedRetiringFreesItsSeatAtItsNextBoundary(t *testing.T) {
	// The demotion, through the deferred settle: the mark lands while the hull is flying, the
	// seat is earmarked free, and the tag moves the tick the hull parks drained.
	retiring := tfRetiringShipAt(t, "L-1", tradeFleetLane, "X1-B-1")
	spare := tfIdleShipAt(t, "M-A", tradeFleetMVT, "X1-B-1")
	all := []*navigation.Ship{retiring, spare}
	repo, claims := &fakeSpecialistShipRepo{}, &fakeSpecialistClaims{}
	h := newSpecialistHandler(repo, claims, specialistLegs(true))
	cmd := &RunTradeFleetCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), SpecialistFractionPct: 100}

	// Both mid-tour: nothing can move yet, but the plan already knows who does.
	promoted, demoted, _ := h.reconcileSpecialists(context.Background(), cmd, all, nil, baseTime, &tradeCaptureLogger{})
	require.Zero(t, promoted+demoted)
	require.Equal(t, map[string]string{"L-1": tradeFleetMVT, "M-A": tradeFleetLane}, h.specialistPending)

	// Both park drained. The demotion settles first, and the seat it frees is taken the same
	// tick by the hull that will actually fly the lane.
	promoted, demoted, retags := h.reconcileSpecialists(context.Background(), cmd, all, all, baseTime.Add(30*time.Second), &tradeCaptureLogger{})
	require.Equal(t, 1, promoted)
	require.Equal(t, 1, demoted)
	require.Equal(t, map[string]string{"L-1": tradeFleetMVT, "M-A": tradeFleetLane}, repo.assigned)
	require.Equal(t, tradeFleetMVT, retags["L-1"])
	require.Equal(t, tradeFleetLane, retags["M-A"])
	require.Equal(t, []string{"M-A"}, claims.released, "only the promotion strips a system claim")
	require.Empty(t, h.specialistPending)
}
