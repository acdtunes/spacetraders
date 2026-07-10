package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// sp-lxwn VERDICT (B) — the reposition pre-flight is WORKING AS DESIGNED: it correctly returns
// the solver's "no_profitable_tour" for a tapped ground. Two defects made that verdict OPAQUE
// and let the pre-rank nominate mirages:
//   1. the ranking log discarded the solver's own reason, printing a bare "infeasible";
//   2. the pre-rank never age-filtered, so a candidate's headline lane could price off a
//      >75-min-stale market the solver's snapshot (BuildTourSnapshot, 75-min cap) drops —
//      a healthy-looking score for a ground the solver finds no profitable tour on (field:
//      X1-ZC66 pre-ranked 157500 off a 131-min-stale source, solver-infeasible).
// These tests pin the fixes: the log now names the specific reason, and the pre-rank scores
// only on fresh listings.

// freshListings must drop rows past maxListingAge so the pre-rank scores only markets the
// solver's snapshot would also admit — the exact staleness parity the field bug turned on:
// before the fix a fat STALE lane out-ranked a modest FRESH one, so a hull was nominated to a
// ground whose "spread" was a mirage the solver's snapshot had already dropped.
func TestReposition_FreshListings_ExcludesStaleBestLane(t *testing.T) {
	now := time.Now()
	stale := now.Add(-2 * maxListingAge) // well past the 75-min snapshot cap
	fresh := now.Add(-1 * time.Minute)

	// A FAT stale lane (spread 500 x vol 300 = 150000) and a MODEST fresh lane (spread 50 x
	// vol 100 = 5000), both non-EXPORT so they are eligible sell destinations.
	listings := []trading.GoodListing{
		{Good: "STALEGOOD", Waypoint: "X1-C-1", TradeType: "IMPORT", Ask: 100, Bid: 100, Volume: 300, ObservedAt: stale},
		{Good: "STALEGOOD", Waypoint: "X1-C-2", TradeType: "IMPORT", Ask: 100, Bid: 600, Volume: 300, ObservedAt: stale},
		{Good: "FRESHGOOD", Waypoint: "X1-C-1", TradeType: "IMPORT", Ask: 100, Bid: 100, Volume: 100, ObservedAt: fresh},
		{Good: "FRESHGOOD", Waypoint: "X1-C-2", TradeType: "IMPORT", Ask: 100, Bid: 150, Volume: 100, ObservedAt: fresh},
	}

	// Precondition (the bug): the UNFILTERED pre-rank scores the stale fat lane — the mirage.
	if _, score := bestInSystemLane(listings); score != 150000 {
		t.Fatalf("precondition: the unfiltered pre-rank must see the stale fat lane (150000), got %d", score)
	}

	// After age-filtering only the fresh lane survives, so the pre-rank reflects tradeable depth
	// the solver can actually realise (5000), never the stale 150000 mirage.
	kept := freshListings(listings, now, maxListingAge)
	if len(kept) != 2 {
		t.Fatalf("age filter must keep exactly the 2 fresh rows, kept %d", len(kept))
	}
	if _, score := bestInSystemLane(kept); score != 5000 {
		t.Fatalf("the age-filtered pre-rank must score only the FRESH lane (5000), got %d", score)
	}

	// A zero ObservedAt means "unknown age" and is kept (fail-open, matching BuildTourSnapshot).
	unstamped := []trading.GoodListing{{Good: "G", Waypoint: "X1-C-1", TradeType: "IMPORT", Ask: 1, Bid: 1, Volume: 1}}
	if len(freshListings(unstamped, now, maxListingAge)) != 1 {
		t.Fatalf("a zero-ObservedAt row must be treated as fresh (kept), not discarded")
	}
}

// repositionCandidateReason must turn the pre-flight outcome into the SPECIFIC reason the
// ranking log needs — never the pre-fix opaque bare "infeasible" — and must DISTINGUISH a
// solver verdict from a pre-flight CALL failure (the two the old code silently conflated).
func TestReposition_CandidateReason_DisambiguatesVerdictFromCallError(t *testing.T) {
	// A feasible plan is a contender, not a rejection.
	if got := repositionCandidateReason(&routing.TourPlan{Feasible: true}, nil); got != "" {
		t.Fatalf("a feasible plan must yield no reason, got %q", got)
	}

	// The solver's OWN reason surfaces verbatim (the tapped-ground signal).
	if got := repositionCandidateReason(&routing.TourPlan{Feasible: false, InfeasibleReason: "no_profitable_tour"}, nil); got != "no_profitable_tour" {
		t.Fatalf("the solver's infeasibility reason must surface, got %q", got)
	}

	// The solver's best rejected tour is appended (barely-negative vs nothing-at-all is the tell),
	// and its commas/parens are neutralised so the ranking line's format cannot fracture.
	got := repositionCandidateReason(&routing.TourPlan{
		Feasible: false, InfeasibleReason: "no_profitable_tour",
		TopRejected: []string{"X1-A→X1-B — profit -500 (1,234/hr)"},
	}, nil)
	if !strings.Contains(got, "no_profitable_tour") || !strings.Contains(got, "best:") || !strings.Contains(got, "profit -500") {
		t.Fatalf("the reason must carry the solver reason and its best rejected tour, got %q", got)
	}
	if strings.ContainsAny(got, "(),") {
		t.Fatalf("the reason token must not contain the line delimiters ( ) , — got %q", got)
	}

	// A pre-flight CALL failure is a categorically different failure, marked distinctly.
	if got := repositionCandidateReason(nil, errors.New("gRPC OptimizeTradeTour failed: unavailable")); !strings.HasPrefix(got, "planner-error:") {
		t.Fatalf("a pre-flight call error must be marked planner-error (not a solver verdict), got %q", got)
	}

	// A nil plan with no error is its own marker (never a silent empty reason).
	if got := repositionCandidateReason(nil, nil); got != "no-plan" {
		t.Fatalf("a nil plan with no error must yield the no-plan marker, got %q", got)
	}
}

// End-to-end: a "chosen none" reposition episode must LOG the solver's specific reason in the
// message text (which `container logs` keeps — sp-149h), so the opaque bare "infeasible" that
// cost diagnosis time on the field episodes is gone. This is the primary sp-lxwn diagnostic.
func TestTour_Reposition_RankingLog_NamesSolverReasonNotOpaqueInfeasible(t *testing.T) {
	fx := repositionFixture()
	homeCalls := 0
	planner := &tourFakeRoutingClient{planFn: func(ship routing.TourShipState) *routing.TourPlan {
		switch ship.CurrentSystem {
		case "X1-S1":
			homeCalls++
			if homeCalls == 1 {
				return roundTripS1() // one productive tour, then margins die (3-strike)
			}
			return infeasibleTour()
		default:
			return infeasibleTour() // the candidate ground is tapped → no_profitable_tour
		}
	}}
	h := newTourHandler(t, fx, planner, &tourFakeTelemetry{})

	logger := &laneLogCapturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	if _, err := h.Handle(ctx, &RunTourCoordinatorCommand{
		ShipSymbol: "TOUR-LXWN", PlayerID: 1, ContainerID: "ctr-lxwn", Iterations: -1,
		ModelArtifactPath: writeTourArtifact(t),
	}); err != nil {
		t.Fatalf("reposition run returned error: %v", err)
	}

	var ranking string
	for _, e := range logger.entries {
		if strings.HasPrefix(e.message, "Reposition ranking from") {
			ranking = e.message
			break
		}
	}
	if ranking == "" {
		t.Fatalf("expected a 'Reposition ranking' log entry, got %+v", logger.entries)
	}
	// The candidate token must carry the solver's OWN reason, not the pre-fix opaque "infeasible".
	if !strings.Contains(ranking, "no_profitable_tour") {
		t.Fatalf("the ranking line must name the solver's specific reason (no_profitable_tour), got %q", ranking)
	}
	if strings.Contains(ranking, ",infeasible)") {
		t.Fatalf("the pre-fix opaque bare 'infeasible' token must be gone when the solver gave a reason, got %q", ranking)
	}
}
