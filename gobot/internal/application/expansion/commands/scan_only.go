package commands

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// This file holds the sp-jide SCAN-ONLY mode: a live-tunable knob that DECOUPLES scanning the
// discovered-market backlog from expanding to virgin. Today the coordinator entangles the two —
// the only off-switch for expansion is stopping the whole container, which also stops all market
// scanning. scan_only=1 keeps the scan machinery running (breadth sweep-once posts, manned by the
// scout reconciler) while declaring NO depth pathfinder and buying NO probe: it drains the FULL
// charted-but-unscanned MARKET backlog to zero, then idles. scan_only=0 (default) is byte-identical
// to pre-sp-jide. It is a self-contained add-on to ReconcileOnce that never touches the probe-buy
// path (there is none in scan-only) or the expansion-frontier ranker.

// ScanCandidate is one charted-but-unscanned MARKET system for scan-only mode: a system with
// MARKETPLACE waypoints (charted) but zero player market_data (never scanned), plus its market
// count (the ranking key). It is distinct from ExpansionCandidate: there is no hop distance and no
// charted/virgin flag, because the scan-only backlog is the COMPLETE discovered dark set — every
// dark system regardless of gate depth — not the hop-bounded expansion frontier the ranker surfaces.
type ScanCandidate struct {
	SystemSymbol string
	MarketCount  int
}

// DarkMarketScanner enumerates the FULL charted-but-unscanned MARKET backlog for scan-only mode
// (sp-jide): every system that has MARKETPLACE waypoints but zero player market_data, each with its
// market count. It is deliberately NOT hop-bounded (unlike ExpansionScanner's expansion-frontier
// BFS, which surfaces only the ~frontier subset) — it is the complete discovered "dark" backlog the
// operator drains when expansion is paused. Optional-injection: a nil scanner (or scan_only=0)
// leaves the coordinator byte-identical to pre-sp-jide.
type DarkMarketScanner interface {
	ChartedUnscannedMarketSystems(ctx context.Context, playerID int) ([]ScanCandidate, error)
}

// SetDarkMarketScanner wires the scan-only backlog enumerator. Leaving it unset makes scan_only
// idle every cycle (nothing to sweep); scan_only=0 ignores it entirely.
func (h *RunFrontierExpansionCoordinatorHandler) SetDarkMarketScanner(s DarkMarketScanner) {
	h.darkScanner = s
}

// reconcileScanOnly is the whole scan-only reconcile pass — reached only when cfg.ScanOnly > 0. It
// declares a breadth sweep-once post for EVERY uncovered dark-market system (the reconciler mans
// them and scans the markets), and does nothing else: no depth pathfinder, no probe purchase, no
// off-gate explorer demand — expansion is paused. When the backlog is fully scanned it idles with a
// NoWork reason rather than reaching for virgin. Idempotent: a system already carrying a post is
// excluded, and declareSweepOncePost is keyed by (player, system), so a restart re-derives the same
// declarations from persisted state (RULINGS #2).
func (h *RunFrontierExpansionCoordinatorHandler) reconcileScanOnly(
	ctx context.Context,
	cmd *RunFrontierExpansionCoordinatorCommand,
	cfg frontierConfig,
	posts []*domainScouting.ScoutPost,
) error {
	logger := common.LoggerFromContext(ctx)

	backlog := h.buildScanOnlyQueue(ctx, cmd, posts)
	declared := h.declareScanOnlySweeps(ctx, cmd, cfg, backlog)

	outcome := scanOnlyOutcome(len(backlog), declared)
	logger.Log("INFO", fmt.Sprintf("Frontier scan-only cycle: %d dark-market system(s) in the discovered backlog — %s", len(backlog), outcome), map[string]interface{}{
		"action":   "frontier_scan_only_cycle",
		"backlog":  len(backlog),
		"declared": declared,
		"dry_run":  cmd.DryRun,
		"outcome":  outcome,
	})
	return nil
}

// buildScanOnlyQueue ranks the UNCOVERED dark-market backlog highest-market-count first (a
// deterministic system-symbol tiebreak keeps the head and the logged ranking stable across ticks).
// A system already carrying any post is covered and dropped — so the backlog shrinks to empty as
// posts are declared and markets get scanned, which is what makes the coordinator idle once the
// discovered set is drained. A nil scanner or a failed scan yields an empty backlog (idle this
// cycle), never a fall-through to expansion.
func (h *RunFrontierExpansionCoordinatorHandler) buildScanOnlyQueue(
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
		logger.Log("WARNING", fmt.Sprintf("Scan-only dark-market scan failed: %v — idle this cycle", err), nil)
		return nil
	}

	covered := postSystemSet(posts)
	entries := make([]queueEntry, 0, len(candidates))
	for _, candidate := range candidates {
		if covered[candidate.SystemSymbol] {
			continue // already has a post — do not re-declare (drains toward idle)
		}
		entries = append(entries, queueEntry{
			SystemSymbol: candidate.SystemSymbol,
			KnownMarkets: candidate.MarketCount,
			Score:        candidate.MarketCount, // scan-only ranks purely by market count
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
		logger.Log("INFO", fmt.Sprintf("Scan-only backlog #%d: %s (%d markets)%s", i+1, entry.SystemSymbol, entry.KnownMarkets, chosenMarker(i)), map[string]interface{}{
			"action":        "frontier_scan_only_ranking",
			"rank":          i + 1,
			"system_symbol": entry.SystemSymbol,
			"markets":       entry.KnownMarkets,
			"chosen":        i == 0,
		})
	}
	return entries
}

// declareScanOnlySweeps declares a single-hull sweep-once post for EVERY backlog entry through the
// SAME repository seam breadth uses (declareSweepOncePost), returning the count declared. Unlike the
// expansion path's one-per-cycle head it declares the whole uncovered backlog: manning is bounded by
// idle-probe supply either way (the reconciler only mans what it has hulls for), and scan-only buys
// no probes, so declaring the finite discovered set at once simply lets the reconciler drain it as
// hulls free — never a runaway, since covered systems drop out next cycle. In dry-run it logs the
// intent and counts it without writing, mirroring the breadth/depth heads.
func (h *RunFrontierExpansionCoordinatorHandler) declareScanOnlySweeps(
	ctx context.Context,
	cmd *RunFrontierExpansionCoordinatorCommand,
	cfg frontierConfig,
	backlog []queueEntry,
) int {
	logger := common.LoggerFromContext(ctx)
	declared := 0
	for _, entry := range backlog {
		if cmd.DryRun {
			logger.Log("INFO", fmt.Sprintf("DRY-RUN: would declare scan-only sweep-once post %s (%d markets) — draining discovered backlog", entry.SystemSymbol, entry.KnownMarkets), map[string]interface{}{
				"action":        "frontier_scan_only_declare_dryrun",
				"system_symbol": entry.SystemSymbol,
			})
			declared++
			continue
		}
		if err := h.declareSweepOncePost(ctx, cmd, cfg, entry.SystemSymbol); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to declare scan-only sweep-once post %s: %v", entry.SystemSymbol, err), nil)
			continue
		}
		declared++
		logger.Log("INFO", fmt.Sprintf("Declared scan-only sweep-once post %s (%d markets) — draining discovered backlog; reconciler will relay a probe", entry.SystemSymbol, entry.KnownMarkets), map[string]interface{}{
			"action":        "frontier_scan_only_post_declared",
			"system_symbol": entry.SystemSymbol,
			"markets":       entry.KnownMarkets,
		})
	}
	return declared
}

// scanOnlyOutcome is the per-cycle human summary: the NoWork idle reason when the discovered
// backlog is fully scanned, else the count of sweep-once posts declared this cycle. Either way it
// states plainly that scan-only expands to no virgin and spends nothing on probes.
func scanOnlyOutcome(backlog, declared int) string {
	if backlog == 0 {
		return "discovered backlog fully scanned — idle (no expansion, no probe purchase)"
	}
	return fmt.Sprintf("declared %d dark-market sweep-once post(s); no expansion, no probe purchase", declared)
}
