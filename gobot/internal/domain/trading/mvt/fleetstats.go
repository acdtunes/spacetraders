package mvt

import (
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// LaneStat is a realised cross-system (source system, sink system, good) lane.
type LaneStat struct {
	Source, Sink, Good string
	Tranches           int
	MarginPerTranche   float64
	MeanTransitSeconds float64
}

// FleetStats are the fleet-level terms the ranker and the specialist pool read.
type FleetStats struct {
	Hulls                    int
	MarginTotal              float64
	MeanMarginPerSystemVisit float64
	IntraMarginPerTranche    float64
	CreditsPerHullSec        float64
	PerHullMargin            map[string]float64
	Lanes                    []LaneStat
}

type lot struct {
	units  int
	price  int
	system string
	at     time.Time
}

type laneAcc struct {
	tranches int
	margin   float64
	transit  float64 // unit-weighted seconds
	units    int
}

// ComputeFleetStats lot-matches the legs FIFO per (hull, good) and derives visit, tranche,
// lane and rate statistics. window is the span the legs were collected over.
func ComputeFleetStats(legs []trading.TourLegTelemetry, window time.Duration) FleetStats {
	byHull := map[string][]trading.TourLegTelemetry{}
	for _, l := range legs {
		if l.RealizedUnits <= 0 {
			continue
		}
		byHull[l.ShipSymbol] = append(byHull[l.ShipSymbol], l)
	}
	st := FleetStats{PerHullMargin: map[string]float64{}}
	lanes := map[[3]string]*laneAcc{}
	visits, visitMargin := 0, 0.0
	intraTranches, intraMargin := 0, 0.0
	for hull, hl := range byHull {
		sort.Slice(hl, func(i, j int) bool { return hl[i].RealizedAt.Before(hl[j].RealizedAt) })
		lots := map[string][]lot{}
		curSystem, curMargin := "", 0.0
		for _, l := range hl {
			sys := shared.ExtractSystemSymbol(l.Waypoint)
			if sys != curSystem {
				if curSystem != "" {
					visits++
					visitMargin += curMargin
				}
				curSystem, curMargin = sys, 0
			}
			if l.IsBuy {
				lots[l.Good] = append(lots[l.Good], lot{l.RealizedUnits, l.RealizedUnitPrice, sys, l.RealizedAt})
				continue
			}
			q := lots[l.Good]
			if len(q) == 0 {
				continue
			}
			need := l.RealizedUnits
			margin, intraUnits, crossUnits := 0.0, 0, 0
			cross := map[string]*laneAcc{} // source system → accumulator for this sell
			for need > 0 && len(q) > 0 {
				take := q[0].units
				if take > need {
					take = need
				}
				m := float64(l.RealizedUnitPrice-q[0].price) * float64(take)
				margin += m
				if q[0].system == sys {
					intraUnits += take
				} else {
					crossUnits += take
					a := cross[q[0].system]
					if a == nil {
						a = &laneAcc{}
						cross[q[0].system] = a
					}
					a.margin += m
					a.units += take
					a.transit += l.RealizedAt.Sub(q[0].at).Seconds() * float64(take)
				}
				q[0].units -= take
				need -= take
				if q[0].units == 0 {
					q = q[1:]
				}
			}
			lots[l.Good] = q
			st.MarginTotal += margin
			st.PerHullMargin[hull] += margin
			curMargin += margin
			if intraUnits >= crossUnits {
				intraTranches++
				intraMargin += margin
				continue
			}
			for src, a := range cross {
				key := [3]string{src, sys, l.Good}
				acc := lanes[key]
				if acc == nil {
					acc = &laneAcc{}
					lanes[key] = acc
				}
				acc.tranches++
				acc.margin += a.margin
				acc.transit += a.transit
				acc.units += a.units
			}
		}
		if curSystem != "" {
			visits++
			visitMargin += curMargin
		}
		if _, ok := st.PerHullMargin[hull]; !ok {
			st.PerHullMargin[hull] = 0
		}
	}
	st.Hulls = len(byHull)
	if visits > 0 {
		st.MeanMarginPerSystemVisit = visitMargin / float64(visits)
	}
	if intraTranches > 0 {
		st.IntraMarginPerTranche = intraMargin / float64(intraTranches)
	}
	if st.Hulls > 0 && window > 0 {
		st.CreditsPerHullSec = st.MarginTotal / (float64(st.Hulls) * window.Seconds())
	}
	for k, a := range lanes {
		ls := LaneStat{Source: k[0], Sink: k[1], Good: k[2], Tranches: a.tranches, MarginPerTranche: a.margin / float64(a.tranches)}
		if a.units > 0 {
			ls.MeanTransitSeconds = a.transit / float64(a.units)
		}
		st.Lanes = append(st.Lanes, ls)
	}
	sort.Slice(st.Lanes, func(i, j int) bool {
		a, b := st.Lanes[i], st.Lanes[j]
		if a.MarginPerTranche != b.MarginPerTranche {
			return a.MarginPerTranche > b.MarginPerTranche
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Sink != b.Sink {
			return a.Sink < b.Sink
		}
		return a.Good < b.Good
	})
	return st
}
