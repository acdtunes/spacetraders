package scouting

import "sort"

// sensing.go holds the budgeted sensing-scope math for the probe-sensing
// coordinator: which systems earn a standing probe, judged by what their
// markets DEAL IN (the goods whitelist) and how much of it there is (the depth
// floor) — never by current prices, which are volatile and would drop a crushed
// market right before it recovers. Pure math, no I/O, like scope.go/demand.go.

// MarketDepthRow is one (waypoint, good) observation from the market cache.
type MarketDepthRow struct {
	System      string
	Waypoint    string
	Good        string
	TradeVolume int
	MidPrice    int // (purchase+sell)/2, computed by the adapter
}

// SystemSensingProfile is one system's rollup against the whitelist.
type SystemSensingProfile struct {
	System string
	// Depth is Σ TradeVolume×MidPrice over whitelisted goods only.
	Depth int64
	// HotMarkets is the count of distinct waypoints carrying ≥1 whitelisted good.
	HotMarkets int
}

// BuildSensingProfiles rolls the per-(waypoint,good) rows up to one profile per
// system. Rows whose Good is not whitelisted contribute nothing; rows with
// non-positive TradeVolume or MidPrice contribute nothing (fail closed on
// garbage). A system whose every row is filtered out has no profile at all.
// Deterministic: sorted by System ascending.
func BuildSensingProfiles(rows []MarketDepthRow, whitelist map[string]bool) []SystemSensingProfile {
	type rollup struct {
		depth int64
		hot   map[string]bool
	}
	bySystem := make(map[string]*rollup)
	for _, row := range rows {
		if row.System == "" || row.Waypoint == "" {
			continue
		}
		if !whitelist[row.Good] {
			continue
		}
		if row.TradeVolume <= 0 || row.MidPrice <= 0 {
			continue
		}
		agg := bySystem[row.System]
		if agg == nil {
			agg = &rollup{hot: make(map[string]bool)}
			bySystem[row.System] = agg
		}
		agg.depth += int64(row.TradeVolume) * int64(row.MidPrice)
		agg.hot[row.Waypoint] = true
	}

	profiles := make([]SystemSensingProfile, 0, len(bySystem))
	for system, agg := range bySystem {
		profiles = append(profiles, SystemSensingProfile{
			System:     system,
			Depth:      agg.depth,
			HotMarkets: len(agg.hot),
		})
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].System < profiles[j].System })
	return profiles
}

// SensingPlan is the desired standing-post set for one tick.
type SensingPlan struct {
	// Hulls is the probe count per in-scope system: 1, or 2 when HotMarkets
	// exceeds the second-probe threshold.
	Hulls map[string]int
	// TotalHulls is Σ Hulls — reported to the buyer as demand (clamped by the
	// probe budget N there, not here).
	TotalHulls int
}

// PlanSensing selects the in-scope systems and sizes each one. In scope ⇔
// Depth >= depthFloor AND HotMarkets >= 1. No ranking above the floor: every
// in-scope system is equal, because depth measures market size, not arbitrage
// opportunity, and ranking churns probes for nothing. depthFloor <= 0 disables
// the floor (everything with a hot market is in scope).
func PlanSensing(profiles []SystemSensingProfile, depthFloor int64, secondProbeThreshold int) SensingPlan {
	hulls := make(map[string]int)
	total := 0
	for _, profile := range profiles {
		if profile.HotMarkets < 1 {
			continue
		}
		if depthFloor > 0 && profile.Depth < depthFloor {
			continue
		}
		if _, seen := hulls[profile.System]; seen {
			continue // duplicate input profile: keep TotalHulls = Σ Hulls exact
		}
		count := 1
		if profile.HotMarkets > secondProbeThreshold {
			count = 2
		}
		hulls[profile.System] = count
		total += count
	}
	return SensingPlan{Hulls: hulls, TotalHulls: total}
}
