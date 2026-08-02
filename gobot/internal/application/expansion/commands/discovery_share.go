package commands

import (
	"context"

	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// This file holds the DISCOVERY/SCAN budget-split. The sp-tlekc operator dial is discover_scan_balance
// (sp-pvw3's discovery_share promoted 1:1): each cycle declares BOTH kinds of post — splitting the
// per-cycle post-declaration capacity by a 0-100 ratio — with GRACEFUL DEGRADATION so capacity never
// idles because one side ran dry. The legacy discovery_share key survives ONLY as a read-through
// migration alias for the rename (a persisted discovery_share still resolves to the equivalent share
// until it is re-tuned as discover_scan_balance). The scan_only knob is retired.

const (
	// defaultDiscoveryShare is the balanced 50/50 concurrent split default (charting virgin systems
	// AND draining the dark-market backlog at once). Faster discovery with no scanning just grows a
	// dark-market pile the optimizer can't use — the live failure the split fixes.
	defaultDiscoveryShare = 50

	// discoveryShareMax / discoveryShareMin bound the ratio. 100 = pure discovery; 0 = pure
	// backlog-scan. The tune mechanism treats a live 0 as "revert to default", so the pure-scan
	// endpoint (share 0) is reached via a launch value, NOT `tune discover_scan_balance 0` — which
	// reverts to the default 50, exactly like every other knob whose 0 means unset.
	discoveryShareMax = 100
	discoveryShareMin = 0
)

// resolveDiscoveryShare folds the sp-tlekc discover_scan_balance dial and the legacy discovery_share
// migration alias into one effective share in [0,100]. Priority is discover_scan_balance (THE dial) →
// legacy discovery_share → the documented default: the first positive value wins (the launch verb and
// a tune share the config column, so a positive value there is the live control), and everything unset
// leaves the documented default (a balanced split). A share above 100 clamps down; this keeps the
// coordinator's split arithmetic total-conserving.
func resolveDiscoveryShare(discoverScanBalance, legacyDiscoveryShare int) int {
	if discoverScanBalance > 0 {
		if discoverScanBalance > discoveryShareMax {
			return discoveryShareMax
		}
		return discoverScanBalance
	}
	if legacyDiscoveryShare > 0 {
		if legacyDiscoveryShare > discoveryShareMax {
			return discoveryShareMax
		}
		return legacyDiscoveryShare
	}
	return defaultDiscoveryShare
}

// frontierCapacitySplit divides one cycle's post-declaration capacity between DISCOVERY (chart
// virgin systems) and SCAN (drain the dark-market backlog) by the discovery_share ratio, then
// applies GRACEFUL DEGRADATION. It is a TARGET split, not a hard one: a side with no work yields
// its whole budget to the side that has work, so a cycle never leaves capacity idle because one
// backlog ran dry (backlog empty → all to discovery; no reachable virgin frontier → all to scan).
// When NEITHER side has work the split is empty (nothing to declare). share is clamped to [0,100]
// and capacity floored at 0, so the returned budgets always sum to capacity whenever any work
// exists — the invariant the caller relies on to never over- or under-declare.
func frontierCapacitySplit(share, capacity int, discoveryHasWork, scanHasWork bool) (discovery, scan int) {
	if share < discoveryShareMin {
		share = discoveryShareMin
	}
	if share > discoveryShareMax {
		share = discoveryShareMax
	}
	if capacity < 0 {
		capacity = 0
	}

	if !discoveryHasWork && !scanHasWork {
		return 0, 0
	}
	if !scanHasWork {
		return capacity, 0 // backlog dry → the whole cycle charts virgin
	}
	if !discoveryHasWork {
		return 0, capacity // no reachable virgin → the whole cycle drains the backlog
	}

	discovery = (share*capacity + 50) / 100 // round to nearest
	scan = capacity - discovery
	return discovery, scan
}

// capacityPlan is one cycle's resolved discovery/scan split: how many posts each side may declare
// this cycle (after the ratio + graceful degradation) plus the ranked backlogs the declaration steps
// consume, so each side's scanner is consulted at most once per cycle.
type capacityPlan struct {
	discoveryBudget int
	scanBudget      int
	discoveryQueue  []queueEntry
	scanBacklog     []queueEntry
}

// planCapacitySplit resolves this cycle's discovery/scan split (sp-pvw3). It divides the per-cycle
// post-declaration capacity (MaxFrontierPostsInFlight) by discovery_share, consulting each side's
// backlog LAZILY — a side with no target budget is scanned only when graceful degradation might
// redirect the other (dry) side's budget to it. So a pure-discovery cycle never touches the dark
// scanner and a pure-scan cycle never touches the expansion scanner (preserving the deprecated
// scan_only call profile and keeping the extremes byte-cheap). It then applies frontierCapacitySplit
// so a dry side yields its whole budget to the working side.
func (h *RunFrontierExpansionCoordinatorHandler) planCapacitySplit(
	ctx context.Context,
	cmd *RunFrontierExpansionCoordinatorCommand,
	cfg frontierConfig,
	posts []*domainScouting.ScoutPost,
) capacityPlan {
	capacity := cfg.MaxFrontierPostsInFlight
	share := cfg.DiscoveryShare
	targetDiscovery := (share*capacity + 50) / 100
	targetScan := capacity - targetDiscovery

	var discoveryQueue, scanBacklog []queueEntry
	discoveryConsulted, scanConsulted := false, false
	if targetDiscovery > 0 {
		discoveryQueue = h.buildExpansionQueue(ctx, cmd, cfg, posts)
		discoveryConsulted = true
	}
	if targetScan > 0 {
		scanBacklog = h.buildScanBacklog(ctx, cmd, posts)
		scanConsulted = true
	}
	discoveryHasWork := len(discoveryQueue) > 0
	scanHasWork := len(scanBacklog) > 0

	// Graceful degradation may hand a dry side's budget to the OTHER side; consult that other side
	// now (only if not already consulted) so the redirect has real work to declare against.
	if targetScan > 0 && !scanHasWork && !discoveryConsulted {
		discoveryQueue = h.buildExpansionQueue(ctx, cmd, cfg, posts)
		discoveryHasWork = len(discoveryQueue) > 0
	}
	if targetDiscovery > 0 && !discoveryHasWork && !scanConsulted {
		scanBacklog = h.buildScanBacklog(ctx, cmd, posts)
		scanHasWork = len(scanBacklog) > 0
	}

	discoveryBudget, scanBudget := frontierCapacitySplit(share, capacity, discoveryHasWork, scanHasWork)
	return capacityPlan{
		discoveryBudget: discoveryBudget,
		scanBudget:      scanBudget,
		discoveryQueue:  discoveryQueue,
		scanBacklog:     scanBacklog,
	}
}
