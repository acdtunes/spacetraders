package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// desiredHulls applies release PACING on top of computeTarget so the fleet neither flaps at
// the SLA line nor thrashes the shared satellite pool. A raise or a fresh declaration lands
// immediately (freshness is the priority). Shedding a surplus (target < current) is tiered:
//   - a TELEMETRY-STARVED oversized post, or a TRUSTED post COMFORTABLY under its SLA (age
//     below the release-slack line), sheds ONE probe immediately — the starved post's age
//     cannot hold it (sp-iupr bug 1), the comfortable post has the margin to spare (the
//     original sp-orgp hysteresis);
//   - a TRUSTED post in the WARM band (under the SLA but past the slack line) whose measured
//     requirement fell below its budget sheds one probe only once the surplus has been STABLE
//     for the release window (sp-iupr bug 2) — a one-cycle demand dip clears the pending
//     window and sheds nothing, so the sizer never releases a hull the next tick re-buys.
//
// Every shed is one step, floored at the measured requirement (never below what the post
// needs), and lands as a resize-DOWN the scout reconciler un-mans — returning the hull to the
// shared pool where the frontier coordinator can claim it, never sold or retired.
func (h *RunMarketFreshnessSizerCoordinatorHandler) desiredHulls(key string, sz systemSizing, cfg sizerConfig) int {
	measuredAge := measuredAgeSeconds(sz.snap, cfg)
	target, starved := computeTarget(sz, cfg, measuredAge)

	if sz.current == 0 || target >= sz.current {
		h.clearReleasePending(key) // declaring, raising, or holding — no surplus to debounce.
		return target
	}

	// Surplus (target < current). Comfortably-fresh trusted posts and starved posts shed now.
	// "Comfortable" is judged on the P90 (sp-r57g), so a big system whose only stale markets are
	// the tolerated tail (its P90 sits under the slack line) is free to RELEASE the freed slug —
	// where the max would have pinned it stale forever.
	slackSeconds := sz.sla.Seconds() * float64(cfg.ReleaseSlackPercent) / 100
	if starved || measuredAge < slackSeconds {
		h.clearReleasePending(key)
		return stepDownToward(sz.current, target)
	}

	// Warm surplus: shed one probe only after it has held for the stable window (debounced).
	if h.releasePendingElapsed(key) < cfg.ReleaseStableWindow {
		return sz.current // pending, not yet stable — hold this tick.
	}
	h.markReleasePending(key) // reset the window so warm sheds pace at one probe per window.
	return stepDownToward(sz.current, target)
}

// releaseKey scopes the warm-surplus debounce per player and system (matching the scout
// reconciler's driftKey shape) so the singleton handler tracks each post independently.
func releaseKey(playerID int, system string) string {
	return fmt.Sprintf("%d|%s", playerID, system)
}

// releasePendingElapsed records the FIRST tick a warm post's surplus was seen and returns how
// long it has been pending. A key already tracked keeps its original timestamp — the window
// accumulates across ticks until the shed fires or the surplus resolves (clearReleasePending).
func (h *RunMarketFreshnessSizerCoordinatorHandler) releasePendingElapsed(key string) time.Duration {
	h.releaseMu.Lock()
	defer h.releaseMu.Unlock()
	if h.releasePendingSince == nil {
		h.releasePendingSince = make(map[string]time.Time)
	}
	now := h.clock.Now()
	since, ok := h.releasePendingSince[key]
	if !ok {
		h.releasePendingSince[key] = now
		return 0
	}
	return now.Sub(since)
}

// markReleasePending (re)starts a post's stable window at now — called right after a warm
// shed so the next shed must re-earn the full window (paces releases at one probe per window).
func (h *RunMarketFreshnessSizerCoordinatorHandler) markReleasePending(key string) {
	h.releaseMu.Lock()
	defer h.releaseMu.Unlock()
	if h.releasePendingSince == nil {
		h.releasePendingSince = make(map[string]time.Time)
	}
	h.releasePendingSince[key] = h.clock.Now()
}

// clearReleasePending forgets a post's pending window — called the moment its surplus
// resolves (target rose back to the budget, or it shed by the immediate path), so a later
// dip below the budget starts a FRESH window rather than inheriting a stale one.
func (h *RunMarketFreshnessSizerCoordinatorHandler) clearReleasePending(key string) {
	h.releaseMu.Lock()
	defer h.releaseMu.Unlock()
	delete(h.releasePendingSince, key)
}

// applyPost reconciles the desired-state post for one market-bearing system: declare (new),
// promote (a sweep_once that turned out to hold markets), or resize (an existing standing
// post). Resizes prefer the narrow hull-update seam so live manning is preserved.
func (h *RunMarketFreshnessSizerCoordinatorHandler) applyPost(ctx context.Context, cmd *RunMarketFreshnessSizerCoordinatorCommand, existing *domainScouting.ScoutPost, system string, desired int, sla time.Duration) {
	logger := common.LoggerFromContext(ctx)

	if existing == nil {
		post := &domainScouting.ScoutPost{
			PlayerID:        cmd.PlayerID.Value(),
			SystemSymbol:    system,
			FreshnessTarget: sla,
			Kind:            domainScouting.PostKindStanding,
			Hulls:           desired,
			CreatedAt:       h.clock.Now(),
		}
		if err := h.postRepo.Upsert(ctx, post); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to declare standing freshness post %s: %v", system, err), nil)
			return
		}
		logger.Log("INFO", fmt.Sprintf("Declared standing freshness post %s sized to %d probes (SLA %s) — reconciler will man and partition", system, desired, sla), map[string]interface{}{
			"action": "freshness_post_declared", "system_symbol": system, "hulls": desired,
		})
		return
	}

	if existing.Kind != domainScouting.PostKindStanding {
		// PROMOTION: a sweep_once post whose system turned out to hold markets becomes a
		// standing freshness post, sized to the model, with its manning preserved.
		existing.Kind = domainScouting.PostKindStanding
		existing.Hulls = desired
		existing.FreshnessTarget = sla
		if err := h.postRepo.Upsert(ctx, existing); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to promote %s to a standing freshness post: %v", system, err), nil)
			return
		}
		logger.Log("INFO", fmt.Sprintf("Promoted %s from sweep_once to a standing freshness post sized to %d probes", system, desired), map[string]interface{}{
			"action": "freshness_post_promoted", "system_symbol": system, "hulls": desired,
		})
		return
	}

	if existing.HullBudget() == desired && existing.FreshnessTarget == sla {
		return // stable — nothing to do.
	}

	// RESIZE. Prefer the narrow hull-update seam (manning-preserving, clobber-free) when the
	// SLA is unchanged; a SLA change needs the full row so it goes through Upsert.
	if h.hullUpdater != nil && existing.FreshnessTarget == sla {
		if err := h.hullUpdater.UpdateHulls(ctx, cmd.PlayerID.Value(), system, desired); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to resize freshness post %s to %d: %v", system, desired, err), nil)
			return
		}
	} else {
		existing.Hulls = desired
		existing.FreshnessTarget = sla
		if err := h.postRepo.Upsert(ctx, existing); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to resize freshness post %s to %d: %v", system, desired, err), nil)
			return
		}
	}
	logger.Log("INFO", fmt.Sprintf("Resized freshness post %s to %d probes (SLA %s)", system, desired, sla), map[string]interface{}{
		"action": "freshness_post_resized", "system_symbol": system, "hulls": desired,
	})
}

// retireMarketlessPosts removes every STANDING post whose system dropped out of the census
// (its markets retired), freeing its probes back to the pool. sweep_once posts are left to
// the frontier coordinator. Returns the count retired.
//
// sp-u8jc/sp-gucu HOLD GUARD: a system ABSENT from the SCANNED census is not automatically
// "markets gone". If it is charted WITH marketplace waypoints (chartedMarketplace[system] > 0)
// it has simply never been scanned — it NEEDS an initial scan, so its post is HELD, not retired,
// so the reconciler/relay can man it and the probe can make that first scan. chartedMarketplace
// is nil (⇒ zero for every lookup) unless the hold_unscanned_market_posts knob is armed AND the
// reader is wired, so this guard never fires by default — retire-as-gone stays byte-identical.
func (h *RunMarketFreshnessSizerCoordinatorHandler) retireMarketlessPosts(ctx context.Context, cmd *RunMarketFreshnessSizerCoordinatorCommand, posts []*domainScouting.ScoutPost, marketBearing map[string]bool, chartedMarketplace map[string]int, scope domainScouting.ScanScope) int {
	if cmd.DryRun {
		return 0
	}
	// FAIL-SAFE (the enumerate-the-rejected-class lesson): never mass-remove on an EMPTY
	// census. A cold start, an era gap, or a transient read that surfaced zero market-bearing
	// systems would otherwise remove EVERY standing post in one tick — a fleet-killer. With
	// no census to compare against, remove nothing and wait for it to repopulate.
	//
	// It covers the SCOPE pass as well as the retire sweep, and must run before both: the scope's
	// discovery tier is drawn from that same census, so an empty one yields a scope with no
	// discovery slots at all, and releasing against it un-mans everything outside the footprint.
	if len(marketBearing) == 0 {
		return 0
	}
	released := h.releaseOutOfScopePosts(ctx, cmd, posts, scope)
	logger := common.LoggerFromContext(ctx)
	retired := released
	for _, post := range posts {
		if post.Kind != domainScouting.PostKindStanding || marketBearing[post.SystemSymbol] {
			continue
		}
		if !scope.Includes(post.SystemSymbol) {
			continue // already released by the scope pass
		}
		if chartedMarketplace[post.SystemSymbol] > 0 {
			logger.Log("INFO", fmt.Sprintf("Held freshness post %s — charted with %d marketplace(s) but unscanned, awaiting its initial scan (not retired)", post.SystemSymbol, chartedMarketplace[post.SystemSymbol]), map[string]interface{}{
				"action": "freshness_post_held_unscanned", "system_symbol": post.SystemSymbol, "marketplaces": chartedMarketplace[post.SystemSymbol],
			})
			continue
		}
		if err := h.postRepo.Remove(ctx, cmd.PlayerID.Value(), post.SystemSymbol); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to retire marketless freshness post %s: %v", post.SystemSymbol, err), nil)
			continue
		}
		retired++
		logger.Log("INFO", fmt.Sprintf("Retired freshness post %s — its markets are gone, probes freed to the pool", post.SystemSymbol), map[string]interface{}{
			"action": "freshness_post_retired", "system_symbol": post.SystemSymbol,
		})
	}
	return retired
}

// releaseOutOfScopePosts removes every STANDING post whose system fell outside the sensing scope,
// freeing its probes back to the shared pool for the in-scope posts (and the frontier) to claim.
// This is the mechanism by which the scope cut converts into freshness: the same probes redistribute
// onto the systems the fleet actually trades in, rather than being spread over the whole map.
//
// Two guards. An UN-NARROWED scope releases nothing, so cold start and every evidence gap keep
// today's behavior. A post carrying a manning FLOOR is never released — the floor exists to keep
// bootstrap's home probes from being sized away, and releasing the post would strand them.
func (h *RunMarketFreshnessSizerCoordinatorHandler) releaseOutOfScopePosts(ctx context.Context, cmd *RunMarketFreshnessSizerCoordinatorCommand, posts []*domainScouting.ScoutPost, scope domainScouting.ScanScope) int {
	if !scope.Narrowed {
		return 0
	}
	logger := common.LoggerFromContext(ctx)
	released := 0
	for _, post := range posts {
		if post.Kind != domainScouting.PostKindStanding || scope.Includes(post.SystemSymbol) {
			continue
		}
		if post.MinHulls > 0 {
			continue // a floored post is pinned by the operator; the scope cut never strands it
		}
		if err := h.postRepo.Remove(ctx, cmd.PlayerID.Value(), post.SystemSymbol); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to release out-of-scope freshness post %s: %v", post.SystemSymbol, err), nil)
			continue
		}
		released++
		logger.Log("INFO", fmt.Sprintf("Released freshness post %s — outside the trading footprint and the discovery allowance, probes freed to the pool", post.SystemSymbol), map[string]interface{}{
			"action": "freshness_post_out_of_scope", "system_symbol": post.SystemSymbol,
		})
	}
	return released
}
