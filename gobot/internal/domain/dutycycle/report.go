// Package dutycycle computes the fleet's duty-cycle KPI: ship-hours EARNING
// vs idle per hull. Captain amendment (st-wisp-hvic) on sp-51ti: the analyst's
// decomposition of the 20x gap found our per-ship burst economics already
// competitive (within 1.3x of WHYANDO), so the real gap is duty cycle — 27.5k
// baseline vs 90k burst credits/ship-hr — and this KPI is what makes that gap
// (including qpmi's gap-compression) measurable going forward.
//
// This package is pure: ComputeReport takes a slice of point-in-time Samples
// (one per hull per sampling tick, recording whether that hull was earning at
// that instant) and derives earning/idle hours by multiplying sample counts by
// the fixed sampling interval. It measures FORWARD from whenever sampling
// began, not retroactively — no historical assignment audit trail exists to
// reconstruct duty-cycle before this instrumentation existed (see sp-51ti
// notes). Collecting samples off the live ship-assignment repository on a
// ticker is an adapter concern (internal/adapters/metrics).
package dutycycle

import (
	"sort"
	"time"
)

// Sample is one hull's earning/idle status observed at a single sampling
// tick. Earning means the hull was actively assigned to a working container
// at the moment of the sample.
type Sample struct {
	Hull    string
	Earning bool
}

// HullDutyCycle is one hull's accumulated duty-cycle over the observed
// window.
type HullDutyCycle struct {
	Hull         string  `json:"hull"`
	EarningHours float64 `json:"earning_hours"`
	IdleHours    float64 `json:"idle_hours"`
	// EarningPct is EarningHours over total observed hours for this hull; 0
	// (not NaN) when the hull has no observed hours yet.
	EarningPct  float64 `json:"earning_pct"`
	SampleCount int     `json:"sample_count"`
}

// Report is the fleet-wide duty-cycle snapshot.
type Report struct {
	// WindowHours is the longest observed history among all hulls (sample
	// interval * that hull's sample count) — hulls sampled since daemon start
	// only, so different hulls may have different observed windows if any
	// joined the fleet mid-window.
	WindowHours float64         `json:"window_hours"`
	Hulls       []HullDutyCycle `json:"hulls"` // sorted desc by EarningHours
}

// ComputeReport accumulates samples per hull, multiplying each sample by
// sampleInterval to derive earning/idle hours, and returns hulls sorted by
// earning hours descending (the busiest earners lead, matching the KPI's
// framing as "who's earning").
func ComputeReport(samples []Sample, sampleInterval time.Duration) Report {
	type accumulator struct {
		earningCount int
		idleCount    int
		total        int
	}
	byHull := make(map[string]*accumulator)
	order := make([]string, 0)

	for _, s := range samples {
		acc, ok := byHull[s.Hull]
		if !ok {
			acc = &accumulator{}
			byHull[s.Hull] = acc
			order = append(order, s.Hull)
		}
		acc.total++
		if s.Earning {
			acc.earningCount++
		} else {
			acc.idleCount++
		}
	}

	intervalHours := sampleInterval.Hours()
	report := Report{}
	hulls := make([]HullDutyCycle, 0, len(order))
	for _, hull := range order {
		acc := byHull[hull]
		hdc := HullDutyCycle{
			Hull:         hull,
			EarningHours: float64(acc.earningCount) * intervalHours,
			IdleHours:    float64(acc.idleCount) * intervalHours,
			SampleCount:  acc.total,
		}
		totalHours := float64(acc.total) * intervalHours
		if totalHours > 0 {
			hdc.EarningPct = hdc.EarningHours / totalHours * 100
		}
		if totalHours > report.WindowHours {
			report.WindowHours = totalHours
		}
		hulls = append(hulls, hdc)
	}

	sort.SliceStable(hulls, func(i, j int) bool {
		if hulls[i].EarningHours != hulls[j].EarningHours {
			return hulls[i].EarningHours > hulls[j].EarningHours
		}
		return hulls[i].Hull < hulls[j].Hull
	})
	report.Hulls = hulls

	return report
}
