package commands

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// This file holds the SCAN side of the sp-pvw3 discovery_share split (formerly the sp-jide binary
// scan_only mode). It drains the dark-market backlog: systems with MARKETPLACE waypoints (charted)
// but no fresh player market_data. Where scan_only owned the WHOLE cycle (declare a sweep for every
// dark system, buy nothing, then idle), the scan side is now one half of a concurrent split —
// declaring dark sweeps up to the cycle's SCAN BUDGET (frontierCapacitySplit), running alongside the
// discovery side. It still buys no probe: draining discovered markets rides idle relays the scout
// reconciler mans, it never grows the fleet.

// ScanCandidate is one charted-but-unscanned MARKET system: a system with MARKETPLACE waypoints
// (charted) but no fresh player market_data, plus its market count (the ranking key). It is distinct
// from ExpansionCandidate: there is no hop distance and no charted/virgin flag, because the dark
// backlog is the COMPLETE discovered dark/stale set — every such system regardless of gate depth —
// not the hop-bounded expansion frontier the ranker surfaces.
type ScanCandidate struct {
	SystemSymbol string
	MarketCount  int
}

// DarkMarketScanner enumerates the FULL charted-but-price-unscanned MARKET backlog: every system
// with MARKETPLACE waypoints but no (or stale) player market_data, each with its market count. It is
// deliberately NOT hop-bounded (unlike ExpansionScanner's expansion-frontier BFS, which surfaces
// only the ~frontier subset) — it is the complete dark/stale backlog the scan side drains.
// Optional-injection: a nil scanner leaves the scan side inert (nothing to sweep).
type DarkMarketScanner interface {
	ChartedUnscannedMarketSystems(ctx context.Context, playerID int) ([]ScanCandidate, error)
}

// SetDarkMarketScanner wires the dark-market backlog enumerator. Leaving it unset makes the scan side
// idle every cycle (nothing to sweep) and, via graceful degradation, hands its budget to discovery.
func (h *RunFrontierExpansionCoordinatorHandler) SetDarkMarketScanner(s DarkMarketScanner) {
	h.darkScanner = s
}

// buildScanBacklog ranks the UNCOVERED dark-market backlog highest-market-count first (a
// deterministic system-symbol tiebreak keeps the head and the logged ranking stable across ticks).
// A system already carrying any post is covered and dropped — so the backlog shrinks as posts are
// declared and markets get scanned, which lets the scan side idle (and yield to discovery) once the
// discovered set is drained. A nil scanner or a failed scan yields an empty backlog (nothing to
// sweep this cycle), never a fall-through to discovery here — the caller's split does that.
func (h *RunFrontierExpansionCoordinatorHandler) buildScanBacklog(
	ctx context.Context,
	cmd *RunFrontierExpansionCoordinatorCommand,
	posts []*domainScouting.ScoutPost,
) []queueEntry {
	logger := common.LoggerFromContext(ctx)
	if h.darkScanner == nil {
		return nil
	}

	candidates, err := h.darkScanner.ChartedUnscannedMarketSystems(ctx, cmd.PlayerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Dark-market backlog scan failed: %v — scan side idle this cycle", err), nil)
		return nil
	}

	covered := postSystemSet(posts)
	entries := make([]queueEntry, 0, len(candidates))
	for _, candidate := range candidates {
		if covered[candidate.SystemSymbol] {
			continue // already has a post — do not re-declare (drains toward empty)
		}
		entries = append(entries, queueEntry{
			SystemSymbol: candidate.SystemSymbol,
			KnownMarkets: candidate.MarketCount,
			Score:        candidate.MarketCount, // scan side ranks purely by market count
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Score != entries[j].Score {
			return entries[i].Score > entries[j].Score
		}
		return entries[i].SystemSymbol < entries[j].SystemSymbol
	})

	for i, entry := range entries {
		if i >= rankingLogLimit {
			break
		}
		logger.Log("INFO", fmt.Sprintf("Dark-market backlog #%d: %s (%d markets)%s", i+1, entry.SystemSymbol, entry.KnownMarkets, chosenMarker(i)), map[string]interface{}{
			"action":        "frontier_scan_ranking",
			"rank":          i + 1,
			"system_symbol": entry.SystemSymbol,
			"markets":       entry.KnownMarkets,
			"chosen":        i == 0,
		})
	}
	return entries
}

// declareScanSweeps declares a single-hull sweep-once post for the top `budget` uncovered backlog
// entries through the SAME repository seam breadth uses (declareSweepOncePost), returning the count
// declared. The budget is this cycle's SCAN share (frontierCapacitySplit): the split bounds how much
// of the dark backlog is declared per cycle, and manning is bounded further downstream by idle-probe
// supply (the reconciler only mans what it has hulls for). Covered systems dropped out in
// buildScanBacklog, so a restart re-derives the same declarations from persisted state (RULINGS #2).
// In dry-run it logs the intent and counts it without writing, mirroring the breadth/depth heads.
func (h *RunFrontierExpansionCoordinatorHandler) declareScanSweeps(
	ctx context.Context,
	cmd *RunFrontierExpansionCoordinatorCommand,
	cfg frontierConfig,
	backlog []queueEntry,
	budget int,
) int {
	logger := common.LoggerFromContext(ctx)
	declared := 0
	for _, entry := range backlog {
		if declared >= budget {
			break // this cycle's scan budget is spent; the rest drains over subsequent cycles
		}
		if cmd.DryRun {
			logger.Log("INFO", fmt.Sprintf("DRY-RUN: would declare dark-market sweep-once post %s (%d markets) — draining discovered backlog", entry.SystemSymbol, entry.KnownMarkets), map[string]interface{}{
				"action":        "frontier_scan_declare_dryrun",
				"system_symbol": entry.SystemSymbol,
			})
			declared++
			continue
		}
		if err := h.declareSweepOncePost(ctx, cmd, cfg, entry.SystemSymbol); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to declare dark-market sweep-once post %s: %v", entry.SystemSymbol, err), nil)
			continue
		}
		declared++
		logger.Log("INFO", fmt.Sprintf("Declared dark-market sweep-once post %s (%d markets) — draining discovered backlog; reconciler will relay a probe", entry.SystemSymbol, entry.KnownMarkets), map[string]interface{}{
			"action":        "frontier_scan_post_declared",
			"system_symbol": entry.SystemSymbol,
			"markets":       entry.KnownMarkets,
		})
	}
	return declared
}
