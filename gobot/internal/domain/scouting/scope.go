package scouting

import "sort"

// scope.go holds the SENSING-SCOPE math for the market-freshness sizer: which systems the
// fleet spends its scan budget on. Like circuit.go and demand.go this is pure set/rank math
// with no I/O — the application maps its telemetry and fleet reads into the slim shapes here.
//
// The constraint it exists for: the API budget is server-capped and cannot be raised, while
// the charted map grows without bound. Sensing every market-bearing system therefore spends a
// fixed budget over an ever-larger set, and per-system freshness decays as the map grows. The
// scope is what bounds that set — the fleet's OPERATING footprint plus a bounded allowance for
// systems it does not yet trade in.
//
// The cut is by SCOPE, never by probe COUNT. Inside the retained scope the sizer's own
// machinery is untouched, so the same probes over fewer systems buy proportionally more
// freshness where the fleet actually earns.

// TradeVisit is one realized trade leg reduced to what the footprint needs: the SYSTEM it
// happened in and how long ago it realized. BUY legs count as much as SELL legs — a system the
// fleet sources from is as much a part of its operating footprint as one it sells into.
type TradeVisit struct {
	System     string
	AgeSeconds float64
}

// DiscoveryCandidate is one market-bearing system considered for a DISCOVERY slot: a system
// outside the trading footprint that is kept under a cheap standing watch so the fleet retains
// options it can trade into. Weight is the system's INTRINSIC value prior — Σ(trade_volume ×
// price) across its markets, the same census weight the freshness percentile uses — not
// realized demand, which is zero for every candidate by construction.
type DiscoveryCandidate struct {
	System string
	Weight float64
}

// ScanScope is the set of systems the sizer senses this tick, split by tier.
//
// Narrowed false means "sense every market-bearing system" — the un-narrowed fallback used
// whenever there is no evidence to narrow on. Narrowing is the risky act (a system outside the
// scope stops being scanned and goes dark), so absent evidence the scope must not narrow.
type ScanScope struct {
	// Narrowed reports whether the scope actually restricts anything. False ⇒ Includes is true
	// for every system and no post is ever out of scope.
	Narrowed bool
	// Footprint is where the fleet operates: systems it has traded in recently, plus systems
	// its non-scout hulls occupy. Sized by the full freshness machinery.
	Footprint map[string]bool
	// Discovery is the bounded allowance of out-of-footprint systems kept under a cheap watch.
	Discovery map[string]bool
}

// Includes reports whether system is sensed this tick. An un-narrowed scope includes everything.
func (s ScanScope) Includes(system string) bool {
	if !s.Narrowed {
		return true
	}
	return s.Footprint[system] || s.Discovery[system]
}

// IsDiscovery reports whether system holds a DISCOVERY slot rather than a footprint slot —
// the tier the caller sizes to the cheap standing watch instead of the full SLA model. A
// system in both tiers is a footprint system: the footprint always wins.
func (s ScanScope) IsDiscovery(system string) bool {
	if !s.Narrowed {
		return false
	}
	return s.Discovery[system] && !s.Footprint[system]
}

// TradedFootprint returns the set of systems holding at least one realized trade within
// retentionSeconds.
//
// Retention is deliberately LONG relative to any freshness SLA. Market prices are volatile and
// non-monotone: a market the fleet crushed by dumping into it reverts on its own, and the only
// way to observe that recovery is to keep scanning it. A retention shorter than the reversion
// time would evict a crushed market precisely while it recovers, and it could never re-enter —
// nothing would scan it, so nothing would trade it, so it would never re-earn its place.
//
// A non-positive retention disables the age filter. A negative age is floored to 0 so clock
// skew can never evict a system, and an empty system symbol is skipped.
func TradedFootprint(visits []TradeVisit, retentionSeconds float64) map[string]bool {
	footprint := make(map[string]bool, len(visits))
	for _, visit := range visits {
		if visit.System == "" {
			continue
		}
		age := visit.AgeSeconds
		if age < 0 {
			age = 0
		}
		if retentionSeconds > 0 && age > retentionSeconds {
			continue
		}
		footprint[visit.System] = true
	}
	return footprint
}

// SelectDiscovery picks the discovery allowance: up to allowance out-of-footprint systems,
// taken by descending intrinsic value so the slots watch the richest markets the fleet does
// not yet trade in.
//
// Membership rotates on SUCCESS rather than on a timer: a discovery system the fleet actually
// trades enters the footprint, is sized by the full model, and releases its slot to the next
// candidate. Roving discovery over UNCHARTED space is the frontier coordinator's job — these
// slots watch already-charted markets the fleet has not yet earned through.
//
// Ordering is deterministic (value desc, then system asc) so the same census yields the same
// slots across ticks and across a restart. A non-positive allowance selects nothing; a system
// already in the footprint is never a candidate.
func SelectDiscovery(candidates []DiscoveryCandidate, footprint map[string]bool, allowance int) map[string]bool {
	selected := make(map[string]bool)
	if allowance <= 0 {
		return selected
	}
	eligible := make([]DiscoveryCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.System == "" || footprint[candidate.System] {
			continue
		}
		eligible = append(eligible, candidate)
	}
	sortDiscovery(eligible)
	if len(eligible) > allowance {
		eligible = eligible[:allowance]
	}
	for _, candidate := range eligible {
		selected[candidate.System] = true
	}
	return selected
}

// BuildScanScope assembles the tick's scope: the footprint (traded ∪ occupied) plus the
// discovery allowance drawn from candidates.
//
// An EMPTY footprint returns an un-narrowed scope. That is the cold-start and no-evidence
// path: before the fleet has traded anywhere and while it has no hull placed, there is nothing
// to scope against, so the sizer senses the whole census exactly as it did before — a cold
// start can never be starved of sensing by its own lack of history.
func BuildScanScope(traded, occupied map[string]bool, candidates []DiscoveryCandidate, allowance int) ScanScope {
	footprint := make(map[string]bool, len(traded)+len(occupied))
	for system := range traded {
		if system != "" {
			footprint[system] = true
		}
	}
	for system := range occupied {
		if system != "" {
			footprint[system] = true
		}
	}
	if len(footprint) == 0 {
		return ScanScope{}
	}
	return ScanScope{
		Narrowed:  true,
		Footprint: footprint,
		Discovery: SelectDiscovery(candidates, footprint, allowance),
	}
}

// sortDiscovery orders candidates by descending weight, then ascending system symbol.
func sortDiscovery(candidates []DiscoveryCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Weight != candidates[j].Weight {
			return candidates[i].Weight > candidates[j].Weight
		}
		return candidates[i].System < candidates[j].System
	})
}
