package commands

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// remanStalledPosts is the manning watchdog: it re-mans a standing post that reads
// IsFullyManned() yet has produced NO new scan telemetry for a full CIRCUIT PERIOD.
// The signal is the SystemsFreshness census's OldestAgeSeconds (worst-case market
// staleness): a fully-manned post whose worst-case age BREACHES its own FreshnessTarget and is
// NOT improving (no re-scan pulling it back — telemetry is not advancing) has a
// wedged tour whose container may read RUNNING while its hull no longer scans, invisible to
// pass 1. The FreshnessTarget breach gate is what keeps a healthy, correctly-sized post (whose
// worst-case age stays within its own contract) OUT of the watchdog's sights; the improvement
// check additionally spares a post that is over its SLA but already RECOVERING on its own.
//
// The window is the post's OWN circuit period (manningStallWindowCycles), not a flat cycle
// count, because the worst-case age is a circuit-period signal: it cannot fall until a probe
// comes back round to the market it scanned first, so judging a post over any shorter window
// mistakes a tour that has not finished its first lap for one that has died.
//
// The corrective action REUSES tearDownSlots (the single-hull-freshness teardown): stop
// the wedged tour, reclaim the hull, clear the slot so THIS SAME tick's passes re-man it fresh —
// a different idle in-system hull if one is free, else the reclaimed hull on a fresh tour
// container. It never repositions an in-system hull or reinvents claiming.
//
// Anti-thrash: after each re-man the consecutive-cycle counter resets, so the next re-man is a
// full window away (never every tick); and after ManningStallCorrectionCap re-mans that did
// not restore telemetry the watchdog BACKS OFF — it keeps emitting the deferred
// scout.post_manning_stalled event (so the stuck post stays VISIBLE) but stops churning a tour a
// genuinely unreachable market will only wedge again. Scope: standing, fully-manned posts with a
// positive freshness target and a census entry; an under-manned/unmanned post is the sizer's /
// normal manning's job and is explicitly left alone (forgetManningStall).
//
// Optional-injection: no census reader wired (nil) makes this a no-op. A census read
// error is logged and swallowed — a metrics gap must never abort a reconcile or tear
// down a tour on no evidence.
func (h *RunScoutPostCoordinatorHandler) remanStalledPosts(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, posts []*domainScouting.ScoutPost) {
	if h.systemFreshnessReader == nil {
		return
	}
	logger := common.LoggerFromContext(ctx)
	snapshots, err := h.systemFreshnessReader.SystemsFreshness(ctx, cmd.PlayerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Manning watchdog: failed to read the freshness census: %v — skipping this tick", err), nil)
		return
	}
	census := make(map[string]domainScouting.SystemFreshnessSnapshot, len(snapshots))
	for _, snap := range snapshots {
		census[snap.SystemSymbol] = snap
	}
	stallCycles, correctionCap := resolveManningStallConfig(cmd, h.liveConfigSnapshot(ctx, cmd))
	tick, avgHop := scoutPostTick(cmd), scoutAvgHop(cmd)

	for _, post := range posts {
		key := driftKey(cmd.PlayerID.Value(), post.SystemSymbol)

		// Scope: only a standing, FULLY-manned post with a freshness contract to breach.
		// An under-manned/unmanned post (or a sweep-once) is normal manning's / the
		// sizer's job.
		if post.Kind != domainScouting.PostKindStanding || !post.IsFullyManned() || post.FreshnessTarget <= 0 {
			h.forgetManningStall(key)
			continue
		}
		snap, ok := census[post.SystemSymbol]
		if !ok || snap.MarketCount <= 0 {
			h.forgetManningStall(key) // no census for this system yet — nothing to judge against
			continue
		}
		if !h.manningStallBreaching(key, snap.OldestAgeSeconds, post.FreshnessTarget) {
			continue // within its freshness contract, or worst-case age is improving (advancing)
		}
		window := manningStallWindowCycles(stallCycles, tick, post, snap.MarketCount, avgHop)
		if h.noteManningStallCycle(key) < window {
			continue // still inside the post's own circuit window — the age could not have improved yet
		}
		h.resetManningStallCycle(key) // rate-limit: the next re-man is another window away
		attempts := h.manningStallCorrections(key)
		h.emitManningStalled(ctx, cmd, post, snap, window, attempts, correctionCap)
		if attempts >= correctionCap {
			continue // backed off — the event carries it, no more tour churn on an unreachable market
		}
		h.tearDownSlots(ctx, cmd, post)
		if err := h.postRepo.Upsert(ctx, post); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Manning watchdog re-manned post %s but failed to persist the teardown: %v", post.SystemSymbol, err), nil)
		}
		h.bumpManningStallCorrections(key)
	}
}

// manningStallWindowCycles is how many CONSECUTIVE breach-without-improvement cycles a post
// must show before its tour is judged silent: the configured debounce, raised to the post's own
// CIRCUIT PERIOD. The watchdog's only evidence is the worst-case market age, and that age cannot
// fall until a probe closes a full circuit back to the market it scanned first — every market
// ahead of the probe only ages meanwhile. A window shorter than one circuit therefore demands an
// improvement that cannot exist yet, and re-mans a healthy tour still on its first lap; worst at
// cold start, where every market is stale from the opening tick and a just-manned probe is still
// flying to its first market, so the teardown lands faster than any tour could ever report.
//
// Raising, never lowering, keeps manning_stall_cycles honest: a longer tuned window still stands.
// An un-assessable circuit (no census markets and no freshness contract) leaves the configured
// debounce as the only window there is.
func manningStallWindowCycles(configured int, tick time.Duration, post *domainScouting.ScoutPost, markets int, avgHop time.Duration) int {
	period := domainScouting.CircuitPeriod(markets, post.HullBudget(), avgHop, post.FreshnessTarget)
	if period <= 0 || tick <= 0 {
		return configured
	}
	cycles := int(math.Ceil(float64(period) / float64(tick)))
	if cycles > configured {
		return cycles
	}
	return configured
}

// manningStallBreaching records this tick's worst-case age for a post and reports whether it is
// a STALL cycle: the age BREACHES the post's freshness target AND did not IMPROVE
// (drop) since last tick. A within-target or improving age is not a stall — it clears the post's
// debounce (both the consecutive-cycle count and the correction backoff), so the watchdog only
// ever fires on a sustained, non-recovering breach. The age baseline is always refreshed so the
// next tick can detect an improvement; the counters, not the baseline, are cleared on the
// healthy path (keeping the baseline avoids a first-observation flicker between clear and note).
func (h *RunScoutPostCoordinatorHandler) manningStallBreaching(key string, ageSeconds float64, target time.Duration) bool {
	h.stallMu.Lock()
	defer h.stallMu.Unlock()
	if h.stallLastAgeSeconds == nil {
		h.stallLastAgeSeconds = make(map[string]float64)
	}
	prev, hadPrev := h.stallLastAgeSeconds[key]
	h.stallLastAgeSeconds[key] = ageSeconds
	improving := hadPrev && ageSeconds < prev
	if ageSeconds <= target.Seconds() || improving {
		delete(h.stallCycles, key)
		delete(h.stallCorrections, key)
		return false
	}
	return true
}

// noteManningStallCycle increments and returns a post's consecutive stall-cycle count:
// the debounce that requires N cycles of unbroken, non-improving SLA breach before a re-man.
func (h *RunScoutPostCoordinatorHandler) noteManningStallCycle(key string) int {
	h.stallMu.Lock()
	defer h.stallMu.Unlock()
	if h.stallCycles == nil {
		h.stallCycles = make(map[string]int)
	}
	h.stallCycles[key]++
	return h.stallCycles[key]
}

// resetManningStallCycle clears only a post's consecutive-cycle count after a re-man fires,
// so the next re-man must re-earn the full N-cycle window — paces corrections at one
// per window (anti-thrash). The correction count and age baseline are deliberately kept.
func (h *RunScoutPostCoordinatorHandler) resetManningStallCycle(key string) {
	h.stallMu.Lock()
	defer h.stallMu.Unlock()
	delete(h.stallCycles, key)
}

// manningStallCorrections returns how many times the watchdog has already re-manned a post in
// the current stall episode — the failed-correction backoff counter.
func (h *RunScoutPostCoordinatorHandler) manningStallCorrections(key string) int {
	h.stallMu.Lock()
	defer h.stallMu.Unlock()
	return h.stallCorrections[key]
}

// bumpManningStallCorrections records one more re-man of a post; once this reaches the
// correction cap the watchdog backs off to the event only.
func (h *RunScoutPostCoordinatorHandler) bumpManningStallCorrections(key string) {
	h.stallMu.Lock()
	defer h.stallMu.Unlock()
	if h.stallCorrections == nil {
		h.stallCorrections = make(map[string]int)
	}
	h.stallCorrections[key]++
}

// forgetManningStall drops a post's entire stall episode — its age baseline, cycle
// count, and correction count — when it falls out of the watchdog's scope (no longer standing,
// no longer fully manned, or no census). A later re-entry starts a fresh episode.
func (h *RunScoutPostCoordinatorHandler) forgetManningStall(key string) {
	h.stallMu.Lock()
	defer h.stallMu.Unlock()
	delete(h.stallLastAgeSeconds, key)
	delete(h.stallCycles, key)
	delete(h.stallCorrections, key)
}

// emitManningStalled records the DEFERRED scout.post_manning_stalled captain event for a stalled
// post and logs it, so a silent fully-manned post is VISIBLE rather than quietly stale
// — and stays visible after the watchdog has backed off (backedOff carries that state). Mirrors
// warnUndersizedPosts' event idiom; a nil event store (unwired) is a no-op, so the re-man still
// happens without observability wired.
func (h *RunScoutPostCoordinatorHandler) emitManningStalled(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, post *domainScouting.ScoutPost, snap domainScouting.SystemFreshnessSnapshot, stallCycles, attempts, correctionCap int) {
	backedOff := attempts >= correctionCap
	if h.eventStore != nil {
		_ = h.eventStore.Record(ctx, &captain.Event{
			Type: captain.EventScoutPostManningStalled, Ship: post.SystemSymbol, PlayerID: cmd.PlayerID.Value(),
			Payload: fmt.Sprintf(`{"system":%q,"markets":%d,"hulls":%d,"oldest_age_secs":%d,"freshness_secs":%d,"stall_cycles":%d,"cycle_samples":%d,"corrections":%d,"backed_off":%t}`,
				post.SystemSymbol, snap.MarketCount, post.HullBudget(), int(snap.OldestAgeSeconds), int(post.FreshnessTarget.Seconds()), stallCycles, snap.CycleSamples, attempts, backedOff),
		})
	}
	action := "scout_post_manning_stalled"
	verb := "re-manning it"
	if backedOff {
		verb = "backing off — the tour keeps going silent on a likely-unreachable market; needs operator attention"
	}
	common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Scout post %s fully manned but silent: worst-case market age %ds past its %s freshness target, no new telemetry for %d cycles — %s", post.SystemSymbol, int(snap.OldestAgeSeconds), post.FreshnessTarget.Round(time.Second), stallCycles, verb), map[string]interface{}{
		"action":          action,
		"system_symbol":   post.SystemSymbol,
		"oldest_age_secs": int(snap.OldestAgeSeconds),
		"stall_cycles":    stallCycles,
		"corrections":     attempts,
		"backed_off":      backedOff,
	})
}
