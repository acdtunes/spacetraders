package commands

// Own-trade recency de-ranking, driven through the SAME observable seam the reach suite uses:
// the candidate set buildRepositionCandidates returns. What matters at this level is that the
// haircut re-ORDERS without ever narrowing, and that it stays a separate axis from the
// resident-count cap rather than a second charge for the same crowding.

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

type fakeRecency struct{ lastTrade map[string]time.Time }

func (f *fakeRecency) LastTradeBySystem(context.Context, int) map[string]time.Time {
	return f.lastTrade
}

func recencyHandler(t *testing.T, nearScore, farScore int, worked map[string]time.Duration, hulls ...activeHull) (*RunTourCoordinatorHandler, context.Context) {
	t.Helper()
	fx := reachFixture(nearScore, farScore)
	fx.activeHulls = hulls
	h := newTourHandler(t, fx, &tourFakeRoutingClient{}, &tourFakeTelemetry{})
	h.SetGateGraph(reachGateGraph())
	if worked != nil {
		table := make(map[string]time.Time, len(worked))
		now := h.clock.Now()
		for sys, ago := range worked {
			table[sys] = now.Add(-ago)
		}
		h.SetOwnTradeRecencyReader(&fakeRecency{lastTrade: table})
	}
	return h, common.WithLogger(context.Background(), &tradeCaptureLogger{})
}

// The lever itself, plus the bound on it. X1-FAR sits three gate hops out, so the existing
// deadhead decay already charges it 0.85^3; a farScore of 1800 leaves it modestly ahead of the
// 1-hop X1-NEAR and therefore inside the haircut's reach, while 2500 puts it far enough ahead
// that no amount of recency may overturn it.
func TestRepositionRecency_DeRanksGroundTheFleetJustWorked(t *testing.T) {
	cases := []struct {
		name      string
		farScore  int
		worked    map[string]time.Duration
		wantFirst string
	}{
		{"nobody worked either ground: the richer distant one wins as before", 1800, nil, "X1-FAR"},
		{"every ground equally rested: the richer one still wins", 1800,
			map[string]time.Duration{"X1-FAR": 5 * time.Hour, "X1-NEAR": 5 * time.Hour}, "X1-FAR"},
		{"the rich ground was drained minutes ago: the rested near one wins", 1800,
			map[string]time.Duration{"X1-FAR": 2 * time.Minute}, "X1-NEAR"},
		{"the rich ground has since rested past the horizon: it wins again", 1800,
			map[string]time.Duration{"X1-FAR": time.Duration(ownTradeColdMinutesDefault) * time.Minute}, "X1-FAR"},
		{"a MUCH richer drained ground still wins: the haircut is bounded", 2500,
			map[string]time.Duration{"X1-FAR": time.Second}, "X1-FAR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, ctx := recencyHandler(t, 1000, tc.farScore, tc.worked)
			cmd := &RunTourCoordinatorCommand{ShipSymbol: "RECENCY-RANK", PlayerID: 1, RepositionReachEnabled: true}

			cands := h.buildRepositionCandidates(ctx, cmd, "X1-ORIG")

			if cands[0].system != tc.wantFirst {
				t.Fatalf("ranked %s first, want %s (candidates=%v)", cands[0].system, tc.wantFirst, reachCandidateSystems(cands))
			}
		})
	}
}

// The supply guarantee. A cooldown could starve a hull on a thin frontier; a haircut cannot,
// because it only ever sorts. Even with EVERY reachable ground freshly drained, the candidate
// set is the same set — same members, same length — as with none of it drained.
func TestRepositionRecency_NeverNarrowsTheCandidateSet(t *testing.T) {
	cmd := &RunTourCoordinatorCommand{ShipSymbol: "RECENCY-SUPPLY", PlayerID: 1, RepositionReachEnabled: true}

	unpenalised, ctx := recencyHandler(t, 1000, 1800, nil)
	baseline := reachCandidateSystems(unpenalised.buildRepositionCandidates(ctx, cmd, "X1-ORIG"))

	allDrained, ctx2 := recencyHandler(t, 1000, 1800, map[string]time.Duration{
		"X1-NEAR": time.Second, "X1-FAR": time.Second, "X1-H1": time.Second, "X1-H2": time.Second,
	})
	drained := reachCandidateSystems(allDrained.buildRepositionCandidates(ctx2, cmd, "X1-ORIG"))

	if len(baseline) == 0 {
		t.Fatal("fixture produced no candidates; the test proves nothing")
	}
	if len(drained) != len(baseline) {
		t.Fatalf("the haircut changed the candidate COUNT: %v (drained) vs %v (rested)", drained, baseline)
	}
	for _, sys := range baseline {
		if !containsSystem(drained, sys) {
			t.Fatalf("%s dropped out when every ground was drained; the penalty must sort, never filter", sys)
		}
	}
}

// The two crowding signals stay separate. The resident cap REMOVES its candidates before any
// ranking runs, so a system that survives it is never additionally charged for the same hulls:
// here X1-FAR is drained AND carries residents under the cap, and the recency haircut is the
// only thing that moves it — the cap either excludes a candidate outright or leaves it alone.
func TestRepositionRecency_DoesNotDoubleChargeTheResidentCap(t *testing.T) {
	cmd := &RunTourCoordinatorCommand{
		ShipSymbol: "RECENCY-HERD", PlayerID: 1,
		RepositionReachEnabled: true, RepositionReachMaxHullsPerSystem: 3,
	}
	worked := map[string]time.Duration{"X1-FAR": 2 * time.Minute}

	withoutResidents, ctx := recencyHandler(t, 1000, 1800, worked)
	a := withoutResidents.buildRepositionCandidates(ctx, cmd, "X1-ORIG")

	withResidents, ctx2 := recencyHandler(t, 1000, 1800, worked,
		activeHull{"X1-FAR", tradeFleet}, activeHull{"X1-FAR", tradeFleet})
	b := withResidents.buildRepositionCandidates(ctx2, cmd, "X1-ORIG")

	if !reachHasSystem(b, "X1-FAR") {
		t.Fatalf("X1-FAR is under the cap and must survive it, got %v", reachCandidateSystems(b))
	}
	if got, want := reachCandidateSystems(b), reachCandidateSystems(a); !sameOrder(got, want) {
		t.Fatalf("residents under the cap changed the ranking (%v vs %v); crowding must be charged once", got, want)
	}
}

func sameOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
