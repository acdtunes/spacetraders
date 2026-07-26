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
	// HotWaypoints is the sorted (asc) waypoint set carrying ≥1 whitelisted
	// good — the stage-2 circuit stamped onto the system's standing post.
	// Membership is goods-based ONLY: no price, value, or spread term may enter
	// it, because a crushed market still deals its goods and must stay in the
	// circuit while its prices recover — so even a garbled-price row proves
	// membership. It can therefore be a SUPERSET of the HotMarkets count, which
	// stays validity-screened so garbage can never inflate probe demand:
	// sensing fails open, sizing fails closed.
	HotWaypoints []string
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
	// Circuit membership accumulates separately from the rollup, on the
	// whitelist term ALONE: a garbled-price row proves what a market DEALS IN,
	// but it must not create a profile — profile existence keeps the validity
	// screen, so the sensing scope and the era-gap fail-safe are unmoved.
	hotGoodsBySystem := make(map[string]map[string]bool)
	for _, row := range rows {
		if row.System == "" || row.Waypoint == "" {
			continue
		}
		if !whitelist[row.Good] {
			continue
		}
		hotGoods := hotGoodsBySystem[row.System]
		if hotGoods == nil {
			hotGoods = make(map[string]bool)
			hotGoodsBySystem[row.System] = hotGoods
		}
		hotGoods[row.Waypoint] = true
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
			System:       system,
			Depth:        agg.depth,
			HotMarkets:   len(agg.hot),
			HotWaypoints: sortedWaypointSet(hotGoodsBySystem[system]),
		})
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].System < profiles[j].System })
	return profiles
}

// sortedWaypointSet flattens a waypoint set to a sorted (asc) list; empty ⇒
// nil, so a stage-1 profile carries no list at all.
func sortedWaypointSet(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for waypoint := range set {
		out = append(out, waypoint)
	}
	sort.Strings(out)
	return out
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
