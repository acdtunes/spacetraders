// detectors_scout.go — sp-k7q5 planner-staleness detectors (layer 2:
// scout.staleness_hiding_revenue; layer 3: scout.post_proposal) over one shared
// market_data freshness rollup. Split out of detectors.go for navigability; behavior unchanged.
package watchkeeper

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// systemMarketFreshness is one system's market-freshness rollup for the sp-k7q5
// staleness detectors: how many of its waypoints are priced (present in market_data at
// all) and how many of those are stale (their most-recent scan older than the cutoff).
type systemMarketFreshness struct {
	priced int
	stale  int
}

// detectScoutStaleness runs the sp-k7q5 planner-staleness detectors (layer 2:
// scout.staleness_hiding_revenue; layer 3: scout.post_proposal) off ONE market_data
// rollup per tick. Both key on per-system priced-market counts, so the rollup and its
// sort are computed once and shared; layer 2 additionally reads the stale count. When
// BOTH are disabled it returns before touching market_data (zero overhead when unset,
// mirroring detectRegimeShift's no-tripwires gate).
//
// The two live in the watchkeeper (not the tour coordinator that emits the exclusion
// COUNTER) because the counter is in-process in the daemon while these must wake the
// captain from a separate process: both derive the SAME underlying condition — a
// market-rich system whose lanes the planner is dropping for staleness — from the
// shared market_data table's freshness, the cross-process source of truth.
func detectScoutStaleness(ctx context.Context, db *gorm.DB, store captain.EventStore, cfg DetectorConfig, now time.Time) error {
	layer2 := cfg.StalenessHidingStaleAge > 0 && cfg.StalenessHidingMinPricedMarkets > 0 && cfg.StalenessHidingThreshold > 0
	layer3 := cfg.PostProposalMinPricedMarkets > 0 && cfg.PostProposalFreshness > 0
	if !layer2 && !layer3 {
		return nil // both disabled — no market_data scan at all.
	}

	// The observations are loaded BEFORE the cutoff is chosen, because the cutoff is
	// derived from how many markets there are (sp-k4z5b). This detector's whole claim is
	// "the tour planner is dropping these lanes", so its boundary has to be the planner's
	// OWN boundary — and the planner's is no longer a constant: the scan budget is a fixed
	// req/s, so a market's interval is an output of budget / markets known. Against a 4,389
	// market map a 75-minute cutoff called four fifths of a healthy map stale and would
	// have reported that fiction to the captain on every poll.
	latest, err := latestMarketObservations(ctx, db, cfg.PlayerID)
	if err != nil {
		return err
	}
	staleAge := resolvedPlannerStaleAge(ctx, db, cfg, len(latest))

	// staleCutoff drives layer 2's stale count; layer 3 reads only `priced` (cutoff-
	// independent), so a layer-3-only tick simply never marks anything stale. A market
	// whose newest scan predates staleCutoff is one the tour planner is dropping — the
	// exact boundary BuildTourSnapshot / freshListings enforce.
	bySystem := rollupFreshness(latest, now.Add(-staleAge))
	systems := sortedFreshnessKeys(bySystem)

	if layer2 {
		if err := emitStalenessHidingRevenue(ctx, store, cfg, now, staleAge, bySystem, systems); err != nil {
			return err
		}
	}
	if layer3 {
		posted, err := postedSystems(ctx, db, cfg.PlayerID)
		if err != nil {
			return err
		}
		if err := emitPostProposals(ctx, store, cfg, now, bySystem, systems, posted); err != nil {
			return err
		}
	}
	return nil
}

// emitStalenessHidingRevenue records scout.staleness_hiding_revenue for each market-rich
// system whose stale-lane count clears the threshold (sp-k7q5 layer 2), deduped per
// system via HasSince over the cooldown so a persistent gap re-queues the DEFERRED event
// at most once per window, not every poll (the detectIncomeStall idiom).
func emitStalenessHidingRevenue(ctx context.Context, store captain.EventStore, cfg DetectorConfig, now time.Time, staleAge time.Duration, bySystem map[string]systemMarketFreshness, systems []string) error {
	cooldown := cfg.StalenessHidingCooldown
	if cooldown <= 0 {
		cooldown = staleAge
	}
	for _, system := range systems {
		stats := bySystem[system]
		if stats.priced < cfg.StalenessHidingMinPricedMarkets {
			continue // not market-rich — not the XT71/UQ87 class worth an alarm.
		}
		if stats.stale < cfg.StalenessHidingThreshold {
			continue // too few lanes hidden to matter yet.
		}
		recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventStalenessHidingRevenue, system, now.Add(-cooldown))
		if err != nil {
			return err
		}
		if recent {
			continue
		}
		if err := store.Record(ctx, &captain.Event{
			Type: captain.EventStalenessHidingRevenue, Ship: system, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"system":%q,"priced_markets":%d,"stale_markets":%d,"stale_age_minutes":%d}`,
				system, stats.priced, stats.stale, int(staleAge.Minutes())),
		}); err != nil {
			return err
		}
	}
	return nil
}

// emitPostProposals records scout.post_proposal for each market-rich system with NO
// existing post (sp-k7q5 layer 3), deduped per system via HasSince over the cooldown.
// posted covers standing AND sweep-once posts, so this never proposes over a system the
// frontier expansion coordinator has already declared — coordinate, don't collide. The
// proposed hull count comes from the circuit math (RequiredHulls), never a default of 1.
func emitPostProposals(ctx context.Context, store captain.EventStore, cfg DetectorConfig, now time.Time, bySystem map[string]systemMarketFreshness, systems []string, posted map[string]bool) error {
	avgHop := cfg.PostProposalAvgHop
	if avgHop <= 0 {
		avgHop = defaultPostProposalAvgHop
	}
	cooldown := cfg.PostProposalCooldown
	if cooldown <= 0 {
		cooldown = defaultPostProposalCooldown
	}
	for _, system := range systems {
		stats := bySystem[system]
		if stats.priced < cfg.PostProposalMinPricedMarkets {
			continue // not yet market-rich enough to warrant a standing post.
		}
		if posted[system] {
			continue // already covered by a standing or frontier sweep-once post.
		}
		required := scouting.RequiredHulls(stats.priced, avgHop, cfg.PostProposalFreshness)
		recent, err := store.HasSince(ctx, cfg.PlayerID, captain.EventScoutPostProposal, system, now.Add(-cooldown))
		if err != nil {
			return err
		}
		if recent {
			continue
		}
		if err := store.Record(ctx, &captain.Event{
			Type: captain.EventScoutPostProposal, Ship: system, PlayerID: cfg.PlayerID,
			Payload: fmt.Sprintf(`{"system":%q,"priced_markets":%d,"proposed_hulls":%d,"freshness_secs":%d}`,
				system, stats.priced, required, int(cfg.PostProposalFreshness.Seconds())),
		}); err != nil {
			return err
		}
	}
	return nil
}

// latestMarketObservations loads each priced WAYPOINT's most-recent scan time for the
// player: one (waypoint, last_updated) pair per market_data row (the same whole-table
// read detectRegimeShift does), reduced to the max per waypoint. Computing the max in Go
// rather than SQL keeps it dialect-portable across the SQLite test DB and the production
// store.
//
// Its LENGTH is the map size the freshness cutoff is derived from (sp-k4z5b), which is
// why the load is separate from the rollup: the cutoff cannot be chosen until the
// denominator is known, and reading market_data twice to learn it would be wasteful.
func latestMarketObservations(ctx context.Context, db *gorm.DB, playerID int) (map[string]time.Time, error) {
	var rows []persistence.MarketData
	if err := db.WithContext(ctx).
		Select("waypoint_symbol", "last_updated").
		Where("player_id = ?", playerID).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	latest := make(map[string]time.Time, len(rows))
	for _, r := range rows {
		if cur, ok := latest[r.WaypointSymbol]; !ok || r.LastUpdated.After(cur) {
			latest[r.WaypointSymbol] = r.LastUpdated
		}
	}
	return latest, nil
}

// rollupFreshness groups priced waypoints into systems via the waypoint→system
// convention (shared.ExtractSystemSymbol) and counts, per system, how many are priced
// and how many are stale — a waypoint being stale when its newest scan predates
// staleCutoff.
func rollupFreshness(latest map[string]time.Time, staleCutoff time.Time) map[string]systemMarketFreshness {
	out := make(map[string]systemMarketFreshness)
	for waypoint, last := range latest {
		system := shared.ExtractSystemSymbol(waypoint)
		s := out[system]
		s.priced++
		if last.Before(staleCutoff) {
			s.stale++
		}
		out[system] = s
	}
	return out
}

// resolvedPlannerStaleAge is the age past which this detector calls a market stale: the
// same marketscan.FreshnessCap the tour planner itself resolves, re-derived here because
// the detector runs in the watchkeeper process and cannot read the daemon's in-memory
// scan budget (sp-k4z5b).
//
// Both terms of the planner's resolve are reachable from here. The rotation term comes
// from cfg.MarketScanBudget (the same [market_scan] stanza the daemon builds its budget
// from) against the map size the caller just counted. The FLOOR term is read from the
// trade fleet coordinator's live config column, so ONE `tune --operation tour
// listing_max_age_minutes N` moves the planner and the detector watching it together —
// which is the only way this detector can keep the claim it makes. An unreadable or
// untuned column falls back to cfg.StalenessHidingStaleAge, so the detector degrades to
// its own documented floor rather than to silence.
func resolvedPlannerStaleAge(ctx context.Context, db *gorm.DB, cfg DetectorConfig, marketsKnown int) time.Duration {
	floor := cfg.StalenessHidingStaleAge
	if tuned := tunedPlannerListingFloor(ctx, db, cfg.PlayerID); tuned > 0 {
		floor = tuned
	}
	return marketscan.FreshnessCap(floor, cfg.MarketScanBudget, marketsKnown)
}

// tunedPlannerListingFloor reads the operator's tuned listing-age floor off the active
// trade fleet coordinator's persisted config — the row `spacetraders tune --operation
// tour` writes. Returns 0 for "nothing tuned", which includes every failure mode
// (no coordinator running, unreadable row, unparseable config): this is a detector
// threshold, so an unreadable row must leave the documented floor standing rather than
// error a supervisor tick.
func tunedPlannerListingFloor(ctx context.Context, db *gorm.DB, playerID int) time.Duration {
	var rows []persistence.ContainerModel
	if err := db.WithContext(ctx).
		Select("config").
		Where("container_type = ? AND player_id = ? AND status IN (?, ?)",
			string(container.ContainerTypeTradeFleetCoordinator), playerID, "PENDING", "RUNNING").
		Order("heartbeat_at DESC").
		Limit(1).
		Find(&rows).Error; err != nil || len(rows) == 0 || rows[0].Config == "" {
		return 0
	}
	config := map[string]interface{}{}
	if err := json.Unmarshal([]byte(rows[0].Config), &config); err != nil {
		return 0
	}
	minutes, ok := liveconfig.Snapshot(config).PositiveInt(plannerListingFloorKey)
	if !ok {
		return 0
	}
	return time.Duration(minutes) * time.Minute
}

// plannerListingFloorKey is the tune key whose floor the tour planner's freshListings
// paths resolve against. Pinned here rather than imported so the watchkeeper binary does
// not depend on the trading application package; the pairing is asserted by test.
const plannerListingFloorKey = "listing_max_age_minutes"

// postedSystems returns the set of systems the player has a scout post over IN THE OPEN
// ERA — standing AND sweep-once alike (sp-k7q5 layer 3). It scopes to the open era
// exactly as the scout-post repository's ListActive does: era close wipes market_data
// but leaves closed-era scout_posts rows behind, so an un-scoped read would let a
// dead-era post suppress a live-era proposal. Between eras (no open era) nothing is
// posted — and market_data is empty then anyway, so no proposal fires regardless.
func postedSystems(ctx context.Context, db *gorm.DB, playerID int) (map[string]bool, error) {
	var eras []persistence.EraModel
	if err := db.WithContext(ctx).
		Where("closed_at IS NULL").Order("era_id DESC").Limit(1).
		Find(&eras).Error; err != nil {
		return nil, err
	}
	if len(eras) == 0 {
		return map[string]bool{}, nil // between eras — nothing is live.
	}

	var rows []persistence.ScoutPostModel
	if err := db.WithContext(ctx).
		Select("system_symbol").
		Where("player_id = ? AND era_id = ?", playerID, eras[0].EraID).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	posted := make(map[string]bool, len(rows))
	for _, r := range rows {
		posted[r.SystemSymbol] = true
	}
	return posted, nil
}

// sortedFreshnessKeys returns the rollup's systems in deterministic order so the
// detectors fire (and the tests assert) in a stable sequence rather than Go's random
// map iteration order.
func sortedFreshnessKeys(m map[string]systemMarketFreshness) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
