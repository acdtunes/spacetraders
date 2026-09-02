// Package replay runs the MVT ranker and departure rule over recorded tour legs and
// reports what the loop would have done against what the fleet did. Its primary metric is
// jumps; the ship gate is "jumps down and margin per hull not down".
package replay

import (
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

type Config struct {
	Window            time.Duration
	Horizon           time.Duration
	BoundaryGap       time.Duration
	YieldWindowSells  int
	YieldMinSells     int
	ClaimReachHops    int
	TollSecondsPerHop int
	GateFee           int64
}

// Decision is one visit boundary. ActualCredit is the hull's own lot-matched margin over
// the forward horizon; LoopCredit is the same units valued at LoopNext's forward rate.
// Stranded marks a destination — a stay included — where nobody sold in the forward
// horizon: the counterfactual is unobservable, so LoopCredit falls back to the trailing
// rate the ranker scored it on (zero when that window is empty too).
type Decision struct {
	Hull            string
	At              time.Time
	From            string
	ActualNext      string
	LoopNext        string
	Reason          string
	YieldHere       float64
	BestAlternative float64
	TravelCost      float64
	ActualCredit    float64
	LoopCredit      float64
	Stranded        bool
}

type Report struct {
	Hulls, Boundaries   int
	ActualJumps         int
	LoopJumps           int
	ActualMarginPerHull float64
	LoopMarginPerHull   float64
	Stranded            []Decision
	Decisions           []Decision
}

// Gate is the spec §7 ship gate on the headline (trailing-rate) valuation.
func (r Report) Gate() (bool, string) {
	return gate(r.Boundaries, r.ActualJumps, r.LoopJumps, r.ActualMarginPerHull, r.LoopMarginPerHull)
}

func gate(boundaries, actualJumps, loopJumps int, actualPerHull, loopPerHull float64) (bool, string) {
	if boundaries == 0 {
		return false, "no boundaries in the data"
	}
	if loopJumps >= actualJumps {
		return false, fmt.Sprintf("jumps not down: loop %d vs actual %d", loopJumps, actualJumps)
	}
	if loopPerHull < actualPerHull {
		return false, fmt.Sprintf("margin per hull down: loop %.0f vs actual %.0f", loopPerHull, actualPerHull)
	}
	return true, fmt.Sprintf("jumps %d→%d, margin/hull %.0f→%.0f", actualJumps, loopJumps, actualPerHull, loopPerHull)
}

// Valuation is the gate read under one treatment of the unobservable decisions. The
// treatments disagreeing is a verdict on the instrument, not on the loop.
type Valuation struct {
	Name                string
	Boundaries          int
	ActualJumps         int
	LoopJumps           int
	ActualMarginPerHull float64
	LoopMarginPerHull   float64
}

func (v Valuation) Gate() (bool, string) {
	return gate(v.Boundaries, v.ActualJumps, v.LoopJumps, v.ActualMarginPerHull, v.LoopMarginPerHull)
}

// Valuations re-reads the decisions four ways, the headline first: unobservable decisions
// credited on the trailing rate, dropped, credited the hull's own actual, credited zero.
func (r Report) Valuations() []Valuation {
	if r.Hulls == 0 {
		return nil
	}
	all := func(Decision) bool { return true }
	asIs := func(d Decision) float64 { return d.LoopCredit }
	read := func(name string, keep func(Decision) bool, loop func(Decision) float64) Valuation {
		v := Valuation{Name: name}
		actual, credit := 0.0, 0.0
		for _, d := range r.Decisions {
			if !keep(d) {
				continue
			}
			v.Boundaries++
			if d.ActualNext != d.From {
				v.ActualJumps++
			}
			if d.LoopNext != d.From {
				v.LoopJumps++
			}
			actual += d.ActualCredit
			credit += loop(d)
		}
		v.ActualMarginPerHull = actual / float64(r.Hulls)
		v.LoopMarginPerHull = credit / float64(r.Hulls)
		return v
	}
	return []Valuation{
		read("trailing-rate", all, asIs),
		read("observable", func(d Decision) bool { return !d.Stranded }, asIs),
		read("neutral", all, func(d Decision) float64 {
			if d.Stranded {
				return d.ActualCredit
			}
			return d.LoopCredit
		}),
		read("zero-credit", all, func(d Decision) float64 {
			if d.Stranded {
				return 0
			}
			return d.LoopCredit
		}),
	}
}

// Robust passes only when every valuation passes the gate.
func (r Report) Robust() (bool, string) {
	vs := r.Valuations()
	if len(vs) == 0 {
		return false, "no boundaries in the data"
	}
	for _, v := range vs {
		if pass, why := v.Gate(); !pass {
			return false, v.Name + ": " + why
		}
	}
	return true, "every valuation passes"
}

type sell struct {
	hull   string
	at     time.Time
	units  int
	margin float64
}

type lot struct{ units, price int }

type claim struct {
	system  string
	arrival time.Time
}

func hopsFrom(neighbours map[string][]string, origin string, maxHops int) map[string]int {
	dist := map[string]int{origin: 0}
	frontier := []string{origin}
	for len(frontier) > 0 && maxHops > 0 {
		var next []string
		for _, s := range frontier {
			for _, n := range neighbours[s] {
				if _, seen := dist[n]; seen {
					continue
				}
				dist[n] = dist[s] + 1
				if dist[n] < maxHops {
					next = append(next, n)
				}
			}
		}
		frontier = next
	}
	return dist
}

func Run(legs []trading.TourLegTelemetry, neighbours map[string][]string, cfg Config) Report {
	byHull := map[string][]trading.TourLegTelemetry{}
	lastByHull := map[string]time.Time{}
	for _, l := range legs {
		if l.RealizedUnits <= 0 {
			continue
		}
		byHull[l.ShipSymbol] = append(byHull[l.ShipSymbol], l)
		if l.RealizedAt.After(lastByHull[l.ShipSymbol]) {
			lastByHull[l.ShipSymbol] = l.RealizedAt
		}
	}
	r := Report{Hulls: len(byHull)}
	if r.Hulls == 0 {
		return r
	}
	stats := mvt.ComputeFleetStats(legs, cfg.Window)

	// Pass 1: lot-match every sell so system yields are known for any window.
	sellsBySystem := map[string][]sell{}
	sellsByHull := map[string][]sell{}
	capacity := map[string]int{}
	type visit struct {
		system string
		start  int
		end    int // inclusive leg index
	}
	visits := map[string][]visit{}
	marginPerUnitOf := map[string]map[int]float64{} // hull → leg index → margin/unit (sells only)
	for hull, hl := range byHull {
		sort.Slice(hl, func(i, j int) bool { return hl[i].RealizedAt.Before(hl[j].RealizedAt) })
		lots := map[string][]lot{}
		marginPerUnitOf[hull] = map[int]float64{}
		cur := visit{system: "", start: 0}
		for i, l := range hl {
			if l.RealizedUnits > capacity[hull] {
				capacity[hull] = l.RealizedUnits
			}
			sys := shared.ExtractSystemSymbol(l.Waypoint)
			gap := i > 0 && l.RealizedAt.Sub(hl[i-1].RealizedAt) > cfg.BoundaryGap
			if sys != cur.system || gap {
				if cur.system != "" {
					cur.end = i - 1
					visits[hull] = append(visits[hull], cur)
				}
				cur = visit{system: sys, start: i}
			}
			if l.IsBuy {
				lots[l.Good] = append(lots[l.Good], lot{l.RealizedUnits, l.RealizedUnitPrice})
				continue
			}
			q := lots[l.Good]
			need, consumed, margin := l.RealizedUnits, 0, 0.0
			for need > 0 && len(q) > 0 {
				take := q[0].units
				if take > need {
					take = need
				}
				margin += float64(l.RealizedUnitPrice-q[0].price) * float64(take)
				consumed += take
				need -= take
				q[0].units -= take
				if q[0].units == 0 {
					q = q[1:]
				}
			}
			lots[l.Good] = q
			if consumed == 0 {
				continue
			}
			s := sell{hull: hull, at: l.RealizedAt, units: consumed, margin: margin}
			sellsBySystem[sys] = append(sellsBySystem[sys], s)
			sellsByHull[hull] = append(sellsByHull[hull], s)
			marginPerUnitOf[hull][i] = margin / float64(consumed)
		}
		if cur.system != "" {
			cur.end = len(hl) - 1
			visits[hull] = append(visits[hull], cur)
		}
	}
	// Map order fed these lists; a fixed order keeps summation and ties reproducible.
	for _, ss := range sellsBySystem {
		sort.SliceStable(ss, func(i, j int) bool {
			if !ss[i].at.Equal(ss[j].at) {
				return ss[i].at.Before(ss[j].at)
			}
			return ss[i].hull < ss[j].hull
		})
	}
	yield := func(system string, t0, t1 time.Time) (float64, int) {
		m, u := 0.0, 0
		for _, s := range sellsBySystem[system] {
			if !s.at.Before(t0) && s.at.Before(t1) {
				m += s.margin
				u += s.units
			}
		}
		return m, u
	}
	hullYield := func(hull string, t0, t1 time.Time) (float64, int) {
		m, u := 0.0, 0
		for _, s := range sellsByHull[hull] {
			if !s.at.Before(t0) && s.at.Before(t1) {
				m += s.margin
				u += s.units
			}
		}
		return m, u
	}

	// Pass 2: decisions at every visit boundary, in time order across hulls so simulated
	// claims penalise later decisions.
	type boundary struct {
		hull string
		v    visit
		next string
		at   time.Time
	}
	var bounds []boundary
	for hull, vs := range visits {
		hl := byHull[hull]
		for i, v := range vs {
			endAt := hl[v.end].RealizedAt
			next := v.system
			if i+1 < len(vs) {
				next = vs[i+1].system
			} else if lastByHull[hull].Sub(endAt) < cfg.Horizon {
				// A hull's own last visit has no observed successor, and its forward
				// horizon holds none of its own sells — scoring it would credit the loop
				// against an actual margin that is zero by construction.
				continue
			}
			bounds = append(bounds, boundary{hull: hull, v: v, next: next, at: endAt})
		}
	}
	sort.SliceStable(bounds, func(i, j int) bool {
		if !bounds[i].at.Equal(bounds[j].at) {
			return bounds[i].at.Before(bounds[j].at)
		}
		if bounds[i].hull != bounds[j].hull {
			return bounds[i].hull < bounds[j].hull
		}
		return bounds[i].v.start < bounds[j].v.start
	})

	var claims []claim
	actualMargin, loopMargin := 0.0, 0.0
	for _, b := range bounds {
		hl := byHull[b.hull]
		tracker := mvt.NewYieldTracker(cfg.YieldWindowSells, cfg.YieldMinSells)
		for i := b.v.start; i <= b.v.end; i++ {
			if mpu, ok := marginPerUnitOf[b.hull][i]; ok {
				tracker.Observe(mpu, hl[i].RealizedUnits, hl[i].RealizedAt)
			}
		}
		reach := hopsFrom(neighbours, b.v.system, cfg.ClaimReachHops)
		var cands []mvt.Candidate
		for sys, hops := range reach {
			credits, units := yield(sys, b.at.Add(-cfg.Horizon), b.at)
			if units == 0 {
				continue
			}
			inTransit := 0
			for _, c := range claims {
				if c.system == sys && c.arrival.After(b.at) {
					inTransit++
				}
			}
			cands = append(cands, mvt.Candidate{System: sys, Hops: hops, YieldCredits: credits, DepthUnits: units, InTransit: inTransit})
		}
		capHull := capacity[b.hull]
		if capHull < 1 {
			capHull = 1
		}
		hull := mvt.Hull{Symbol: b.hull, System: b.v.system, CargoCapacity: capHull, CreditsPerSec: tracker.CreditsPerSec(b.at)}
		costs := mvt.Costs{TollSecondsPerHop: cfg.TollSecondsPerHop, GateFeeFromCurrent: cfg.GateFee,
			FleetDrawPerVisit: stats.MeanMarginPerSystemVisit, FleetCreditsPerSec: stats.CreditsPerHullSec}
		ranked := mvt.Rank(hull, cands, costs)
		alt, hasAlt := mvt.BestAlternative(ranked, b.v.system)
		d := mvt.Decide(tracker, alt.Score, hasAlt)
		dec := Decision{Hull: b.hull, At: b.at, From: b.v.system, ActualNext: b.next, LoopNext: b.v.system,
			Reason: d.Reason, YieldHere: d.YieldHere, BestAlternative: alt.Score, TravelCost: alt.TravelPerUnit}
		exhausted := tracker.Sells() == 0 && len(ranked) > 0 && ranked[0].System != b.v.system
		if d.Leave || exhausted {
			// Decide weighs the hull's REALISED yield against the alternative while Rank
			// scores this system on its predicted depth, so a Leave can still rank here
			// first. The loop claims ranked[0] and treats ranked[0] == here as a stay;
			// record that as a stay, not as a departure that never happened.
			target := ranked[0]
			if target.System == b.v.system {
				dec.Reason = mvt.ReasonStay
			} else {
				dec.LoopNext = target.System
				if exhausted && !d.Leave {
					dec.Reason = "empty"
				}
				claims = append(claims, claim{system: target.System, arrival: b.at.Add(time.Duration(target.Hops*cfg.TollSecondsPerHop) * time.Second)})
			}
		}
		r.Boundaries++
		if dec.ActualNext != dec.From {
			r.ActualJumps++
		}
		if dec.LoopNext != dec.From {
			r.LoopJumps++
		}
		am, hu := hullYield(b.hull, b.at, b.at.Add(cfg.Horizon))
		dec.ActualCredit = am
		actualMargin += am
		// Stay and jump are valued alike: the hull's own forward units at the destination's
		// forward rate, else at its trailing rate — an unobserved counterfactual is not
		// evidence that the system earned nothing.
		lm, lu := yield(dec.LoopNext, b.at, b.at.Add(cfg.Horizon))
		if lu == 0 {
			dec.Stranded = true
			lm, lu = yield(dec.LoopNext, b.at.Add(-cfg.Horizon), b.at)
		}
		if lu > 0 {
			dec.LoopCredit = float64(hu) * lm / float64(lu)
			loopMargin += dec.LoopCredit
		}
		if dec.Stranded {
			r.Stranded = append(r.Stranded, dec)
		}
		r.Decisions = append(r.Decisions, dec)
	}
	r.ActualMarginPerHull = actualMargin / float64(r.Hulls)
	r.LoopMarginPerHull = loopMargin / float64(r.Hulls)
	return r
}
