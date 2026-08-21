package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	tradingsvc "github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

const (
	// farSinkMaxSystems bounds how many out-of-horizon sink systems ONE plan may admit.
	// The bound is the point: the global scan is unbounded by construction, and wiring it
	// raw is what stranded hulls at a horizon nobody could fly. Two is sized off the live
	// per-tour distinct-system cap (max_tour_systems 4, of which the hull's own system is
	// one): it gives the solver a genuine choice between the two richest hidden lanes
	// while keeping the far, staleness-prone markets a minority of the snapshot.
	farSinkMaxSystems = 2
	// farSinkCandidateTopN bounds how many of the richest hidden lanes are EXAMINED (each
	// costs one bounded BFS plus one system snapshot read). Admission is by value order,
	// so examining a few more than can be admitted lets a rejected top lane yield to the
	// next-richest instead of forfeiting the pass.
	farSinkCandidateTopN = 6
	// farSinkUnreachableReason labels sinks refused because an already-admitted system sits
	// further from them than the executor's strict flight bound. This counter staying
	// non-zero is the guard working, not a fault.
	farSinkUnreachableReason = "far_sink_beyond_flight_horizon"
	// farSinkExpiredReason labels rows refused because they would age past THEIR OWN
	// activity's fitted cap during the haul — priced fresh, dead on arrival.
	farSinkExpiredReason = "far_sink_expires_before_arrival"
	// farSinkLogTopN caps how many admitted lanes are named per plan.
	farSinkLogTopN = 2
)

// farSinkAdmission is what one plan's far-sink capture adds to the tour graph: the extra
// systems, their surviving market rows and coordinates, and the gate-hop distances the
// admission was GRANTED on — carried forward so the solver prices each crossing off the
// same distances the reachability check passed, never a re-derived second opinion.
type farSinkAdmission struct {
	systems   []string
	rows      []routing.TourGoodSnapshot
	waypoints []routing.TourWaypoint
	hops      []routing.InterSystemHopDistance
}

// farSinkReach is one candidate sink's proven geometry: the worst-case gate distance from
// any already-admitted system (the longest leg a hull could fly into it) and the >1-hop
// pair distances that must ride onto the solver's travel matrix.
type farSinkReach struct {
	maxHops   int
	distances []routing.InterSystemHopDistance
}

// travel is the worst-case wall-clock to reach this sink from anywhere in the tour graph,
// predicted at the ARRIVAL BOUND (trading.ArrivalBoundHopModel) — the fitted crossing shape at
// its measured p90 — rather than at the solver's price.
//
// THOSE ARE DIFFERENT QUESTIONS, AND THE CONSTANT THIS REPLACES CONFLATED THEM. It
// aged 1800s per hop "mirroring the solver's INTER_SYSTEM_TRAVEL_SECONDS", on the stated ground
// that the ager must never diverge from the price. But the price answers "is this crossing worth
// its time" and is rightly a MEDIAN, while the only thing this feeds — survivesArrival — asks
// "will the quote still be fresh when we get there", where a median arrival is wrong half the
// time. The two were equal by coincidence, not by construction, and sp-smbgd broke even that
// when it retired the flat constant for an affine 750 + 650*hops charge: the ager was left
// ageing a 5-hop sink by 9,000s against a 4,000s crossing. Conservative, so never a money hazard
// — but it discards exactly the deep sinks the affine model was shipped to unlock (+21.4% on 3+
// hop lanes in replay).
//
// The p90 bound composes with the p90-fitted RankerAgeCaps on the other side of that inequality
// instead of mixing confidence levels, and it is looser than the retired 1800*hops at every
// depth the flight bound admits (1..gategraph.MaxJumpPath), by far the most at depth — which is
// where the suppression was.
func (r farSinkReach) travel() time.Duration {
	hours := trading.ArrivalBoundHopModel().CrossingHours(r.maxHops)
	return time.Duration(hours * float64(time.Hour))
}

// admitFarSinks turns the out-of-horizon lane DIAGNOSTIC into a capture path: the same
// global best-sink scan that already names the lanes the gate-neighbour horizon hides now
// feeds those sinks back into the tour graph — behind an explicit bound, so the planner can
// act on the value instead of only logging it.
//
// Every admitted sink is REACHABLE AND PRICED AT THE HORIZON THE EXECUTOR ACTUALLY FLIES,
// by construction rather than by convention:
//   - the flight bound is gategraph.MaxJumpPath, the cap the tour's own travel() legs
//     resolve against — not a number invented for admission;
//   - reach is proven against EVERY already-admitted system, so each leg the solver could
//     sequence (into the sink or out of it) is one the executor can resolve;
//   - the distances that pass ride into the solver's travel matrix, so the crossing is
//     priced on the very hops admission was granted on.
//
// Fail closed throughout (RULINGS #4): an unreadable scan, an unresolvable distance, an
// unlabelled activity or an unknown observation age all EXCLUDE the sink. Widening what the
// planner may consider is only safe while the checks that bound it are absolute.
func (h *RunTourCoordinatorHandler) admitFarSinks(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	allowed []string,
	snapshot []routing.TourGoodSnapshot,
) farSinkAdmission {
	var out farSinkAdmission
	// The solver charges a crossing at gate_hops x the per-crossing rate ONLY while the
	// per-pair matrix is live (tourInterSystemHops, MaxTourSystems > 2). Below that every
	// crossing prices as a single hop, so a 3-hop sink would rank three times too well. No
	// honest pricing, no admission.
	if cmd.MaxTourSystems <= 2 || h.sinkScanner == nil || h.legs.gateGraph == nil || len(allowed) == 0 {
		return out
	}
	goods := inScopeSourcedGoods(snapshot)
	if len(goods) == 0 {
		return out
	}
	now := h.clock.Now()
	// Scan at the WIDEST fitted cap — the same backstop the tour snapshot's per-activity
	// pass uses. A WEAK sink stays rankable for 8h, so clipping the scan to one flat window
	// would hide exactly the long-lived far rows this path exists to capture. Each candidate
	// is then measured against ITS OWN activity below, so the wider scan never admits a row
	// that activity would reject.
	sinks, err := h.sinkScanner.BestSinksAcrossSystems(ctx, goods, cmd.PlayerID, h.rankerAgeCaps.Widest(), now)
	if err != nil || len(sinks) == 0 {
		return out // unreadable global scan — admit nothing
	}
	candidates := computeUnreachableLanes(allowed, snapshot, sinks) // richest spread first
	if len(candidates) > farSinkCandidateTopN {
		candidates = candidates[:farSinkCandidateTopN]
	}
	admitted := append([]string(nil), allowed...)
	probes := map[string]farSinkProbe{}
	var captured []unreachableLane
	for _, lane := range candidates {
		if len(out.systems) >= farSinkMaxSystems {
			break
		}
		if systemInGraph(admitted, lane.SinkSystem) {
			continue
		}
		// Several goods often name the SAME rich sink system, so the walk and the system read
		// are memoized per system — but only while the tour graph is unchanged, since reach is
		// proven against the graph as it GROWS and an admission invalidates that proof.
		probe, cached := probes[lane.SinkSystem]
		if !cached || probe.provenAgainst != len(admitted) {
			probe = h.probeFarSink(ctx, cmd, lane.SinkSystem, admitted, now)
			probes[lane.SinkSystem] = probe
		}
		if !probe.ok {
			continue
		}
		// The lane that JUSTIFIED reaching this far must itself survive the haul. This is a
		// per-LANE verdict, never a verdict on the system: a system whose richest good expires
		// in flight is still worth reaching for a good that does not.
		if !hasLiveSinkFor(probe.rows, lane.Good) {
			continue
		}
		admitted = append(admitted, lane.SinkSystem)
		captured = append(captured, lane)
		out.systems = append(out.systems, lane.SinkSystem)
		out.rows = append(out.rows, probe.rows...)
		out.waypoints = append(out.waypoints, probe.waypoints...)
		out.hops = append(out.hops, probe.reach.distances...)
	}
	h.logFarSinkAdmissions(ctx, captured)
	return out
}

// farSinkProbe is one candidate system's verdict: whether the executor can fly to it from
// every system already in the tour graph, and what of its market survives the trip.
// provenAgainst records the graph size the reach was proven against, so a proof is never
// reused once the graph it was proven against has grown.
type farSinkProbe struct {
	reach         farSinkReach
	rows          []routing.TourGoodSnapshot
	waypoints     []routing.TourWaypoint
	provenAgainst int
	ok            bool
}

// probeFarSink resolves one candidate: reach first (a system the executor cannot fly to is
// refused before anything is read of it), then its market rows filtered down to those still
// inside their own activity's cap on arrival.
func (h *RunTourCoordinatorHandler) probeFarSink(
	ctx context.Context, cmd *RunTourCoordinatorCommand, sinkSystem string, admitted []string, now time.Time,
) farSinkProbe {
	probe := farSinkProbe{provenAgainst: len(admitted)}
	reach, ok := h.farSinkReachAt(ctx, sinkSystem, admitted)
	if !ok {
		metrics.RecordTourCandidateDropped(cmd.PlayerID, farSinkUnreachableReason, 1)
		return probe
	}
	rows, waypoints, err := tradingsvc.BuildTourSnapshot(ctx, h.marketRepo, h.waypointRepo,
		[]string{sinkSystem}, cmd.PlayerID, now, h.rankerAgeCaps)
	if err != nil {
		return probe // an unreadable system contributes nothing
	}
	rows = filterBlocklistedCargo(rows, h.effectiveCargoBlocklist(ctx, cmd.PlayerID))
	rows, expired := keepRowsSurvivingArrival(rows, reach.travel(), h.rankerAgeCaps, now)
	if expired > 0 {
		metrics.RecordTourCandidateDropped(cmd.PlayerID, farSinkExpiredReason, expired)
	}
	probe.reach = reach
	probe.rows = rows
	probe.waypoints = waypointsWithRows(waypoints, rows)
	probe.ok = true
	return probe
}

// farSinkReachAt resolves the candidate's gate distance to every already-admitted system over
// the PERSISTED gate adjacency, bounded by the executor's strict flight cap. It reports
// ok=false the moment ONE admitted system is not resolvable within that bound: an unproven or
// over-long leg is a refusal, never an optimistic guess.
//
// The distances come from the store alone (StoredHopDistances). Proving reach must not cost
// API: a far sink sits in an under-explored region, so a fetch-through walk rooted there
// misses its cache on most nodes and turns every admission attempt into a topology-fetch burst
// — taken straight from a request budget the fleet needs for selling. Uncached topology is
// therefore read as UNPROVEN and refused, which keeps the guard fail-closed at zero cost.
func (h *RunTourCoordinatorHandler) farSinkReachAt(
	ctx context.Context, sinkSystem string, admitted []string,
) (farSinkReach, bool) {
	if h.legs.gateGraph == nil {
		return farSinkReach{}, false
	}
	nearest, err := h.legs.gateGraph.StoredHopDistances(ctx, sinkSystem, admitted, gategraph.MaxJumpPath)
	if err != nil {
		return farSinkReach{}, false // the graph is unreadable — prove nothing, admit nothing
	}
	var reach farSinkReach
	for _, sys := range admitted {
		hops, resolved := nearest[sys]
		// A zero distance would mean the sink IS this admitted system, which would price the
		// haul as free and age its rows by nothing — refuse rather than let it through.
		if !resolved || hops <= 0 || hops > gategraph.MaxJumpPath {
			return farSinkReach{}, false // the executor cannot fly this leg — refuse the sink
		}
		if hops > reach.maxHops {
			reach.maxHops = hops
		}
		// Only >1-hop pairs need correcting; a 1-hop pair already prices exactly at the
		// solver's default single-crossing charge (base + per_hop).
		if hops > 1 {
			from, to := sinkSystem, sys
			if to < from {
				from, to = to, from
			}
			reach.distances = append(reach.distances, routing.InterSystemHopDistance{
				FromSystem: from, ToSystem: to, GateHops: hops,
			})
		}
	}
	return reach, true
}

// keepRowsSurvivingArrival drops the rows that would age out mid-flight and reports how
// many were dropped, so the exclusions are countable rather than silent.
func keepRowsSurvivingArrival(
	rows []routing.TourGoodSnapshot, travel time.Duration, caps trading.RankerAgeCaps, now time.Time,
) ([]routing.TourGoodSnapshot, int) {
	kept := make([]routing.TourGoodSnapshot, 0, len(rows))
	expired := 0
	for _, row := range rows {
		if !survivesArrival(row, travel, caps, now) {
			expired++
			continue
		}
		kept = append(kept, row)
	}
	return kept, expired
}

// survivesArrival reports whether a row is still inside ITS OWN activity's fitted freshness
// cap when the hull ARRIVES — plan-time age plus real travel time — rather than merely at
// plan time. This is what a long haul changes: a WEAK row (8h cap) crosses three gate hops
// untouched while a STRONG one (30min) is already dead on arrival, so one global horizon
// must either forfeit the WEAK majority or buy cargo into an expired STRONG sink. An
// unlabelled activity or an unknown observation time is unmeasurable, and unmeasurable is
// excluded — the tour snapshot's own "unknown age reads as fresh" default is safe for a
// row re-verified minutes later at execution, not for one bet on across a multi-hop haul.
func survivesArrival(row routing.TourGoodSnapshot, travel time.Duration, caps trading.RankerAgeCaps, now time.Time) bool {
	if !fittedActivity(row.Activity) || row.ObservedAt.IsZero() {
		return false
	}
	return now.Sub(row.ObservedAt)+travel <= caps.For(row.Activity)
}

// fittedActivity reports whether the label is one the cap table actually fits a window for.
// RankerAgeCaps.For deliberately reads anything else as RESTRICTED; that conservative middle
// is right for the near ranker, but here it would put a real number on an unknown market and
// let a far haul ride on it.
func fittedActivity(activity string) bool {
	switch shared.ActivityLevel(activity) {
	case shared.ActivityLevelWeak, shared.ActivityLevelRestricted,
		shared.ActivityLevelGrowing, shared.ActivityLevelStrong:
		return true
	}
	return false
}

// hasLiveSinkFor reports whether the good can actually be SOLD in the surviving rows (a
// positive bid — EXPORT rows are already zeroed by the snapshot builder).
func hasLiveSinkFor(rows []routing.TourGoodSnapshot, good string) bool {
	for _, row := range rows {
		if row.Good == good && row.Bid > 0 {
			return true
		}
	}
	return false
}

// waypointsWithRows keeps only the coordinates of waypoints that still carry a surviving
// row, so an expired market never becomes a routable node with nothing to trade.
func waypointsWithRows(waypoints []routing.TourWaypoint, rows []routing.TourGoodSnapshot) []routing.TourWaypoint {
	live := make(map[string]bool, len(rows))
	for _, row := range rows {
		live[row.Waypoint] = true
	}
	kept := make([]routing.TourWaypoint, 0, len(waypoints))
	for _, wp := range waypoints {
		if live[wp.Symbol] {
			kept = append(kept, wp)
		}
	}
	return kept
}

func systemInGraph(systems []string, want string) bool {
	for _, sys := range systems {
		if sys == want {
			return true
		}
	}
	return false
}

// mergeInterSystemHops unions the matrix computed over the tour graph with the distances
// far-sink admission proved, letting the PROVEN ones win: they are the distances the
// reachability check passed, so pricing cannot disagree with admission even where the
// breadth-bounded matrix walk fails to re-find a pair. Deterministic order, matching the
// matrix builder's own from/to sort.
func mergeInterSystemHops(base, proven []routing.InterSystemHopDistance) []routing.InterSystemHopDistance {
	if len(proven) == 0 {
		return base
	}
	merged := make(map[[2]string]int, len(base)+len(proven))
	for _, d := range base {
		merged[[2]string{d.FromSystem, d.ToSystem}] = d.GateHops
	}
	for _, d := range proven {
		merged[[2]string{d.FromSystem, d.ToSystem}] = d.GateHops
	}
	out := make([]routing.InterSystemHopDistance, 0, len(merged))
	for pair, hops := range merged {
		out = append(out, routing.InterSystemHopDistance{FromSystem: pair[0], ToSystem: pair[1], GateHops: hops})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FromSystem != out[j].FromSystem {
			return out[i].FromSystem < out[j].FromSystem
		}
		return out[i].ToSystem < out[j].ToSystem
	})
	return out
}

// logFarSinkAdmissions names the captured lanes so the counterpart of the long-standing
// "Tour horizon dropped ..." line is equally legible: what the horizon loses and
// what it takes are readable from the same log.
func (h *RunTourCoordinatorHandler) logFarSinkAdmissions(ctx context.Context, captured []unreachableLane) {
	if len(captured) == 0 {
		return
	}
	named := captured
	if len(named) > farSinkLogTopN {
		named = named[:farSinkLogTopN]
	}
	parts := make([]string, 0, len(named))
	systems := make([]string, 0, len(captured))
	for _, lane := range named {
		parts = append(parts, fmt.Sprintf("%s %s(%d)->%s@%s(%d) spread %d/u",
			lane.Good, lane.SourceWaypoint, lane.Ask, lane.SinkWaypoint, lane.SinkSystem, lane.Bid, lane.Spread))
	}
	for _, lane := range captured {
		systems = append(systems, lane.SinkSystem)
	}
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Tour admitted %d far sink system(s) beyond the gate-neighbor graph, reachable and priced at the executor's flight horizon: %s",
		len(captured), strings.Join(parts, "; ")),
		map[string]interface{}{
			"action":         "tour_far_sinks_admitted",
			"count":          len(captured),
			"sink_systems":   strings.Join(systems, ","),
			"max_admissions": farSinkMaxSystems,
		})
}
