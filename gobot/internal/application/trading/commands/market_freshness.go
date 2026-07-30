package commands

import (
	"context"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// market_freshness.go resolves the trade path's market-freshness caps (sp-k4z5b).
//
// WHY THIS EXISTS. The caps used to be minute counts written into the source: a
// 75-minute const here, a 75-minute config default there. That is only correct at
// one map size. The fleet's market-scan budget is a fixed req/s (sp-ntgfj), so a
// market's scan interval is an OUTPUT of budget ÷ markets known — and when the
// charted map reached 4,389 markets an even rotation stretched to ~105 minutes.
// Every consumer comparing against 75 minutes then discarded four fifths of the
// map as worthless: 980 fail-closed refusals in fifteen minutes and trade
// throughput down ~87%, with nothing actually wrong with any of the data.
//
// Raising the scan budget cannot fix that and never will. A budget bounds COST; it
// does not bound STALENESS, and staleness is what these consumers need bounded. So
// the cap is derived from the rotation instead — marketscan.FreshnessCap, the same
// anti-starvation bound the scanner's own escape hatch enforces — and the minute
// count survives only as a FLOOR the operator can raise live.

const (
	// TuneKeyListingMaxAgeMinutes is the live knob under the freshListings-based
	// paths: the tour lookback, held-cargo offload, reposition/relocation pre-rank,
	// distress liquidation, the candidate shortlist, cross-system sink scanning and
	// the stocker. Registered on the trade fleet coordinator (`tune --operation tour`).
	TuneKeyListingMaxAgeMinutes = "listing_max_age_minutes"

	// TuneKeySinkFreshnessMaxMinutes is the live knob under the firm-sink buy gate's
	// FRESH clause (sp-tgll8 item 2) — a fail-closed money guard. It shares the key
	// NAME with [trade_fleet].sink_freshness_max_minutes deliberately: the operator
	// who reached for that config key in an incident reaches for the same words here,
	// and gets them without a daemon bounce.
	TuneKeySinkFreshnessMaxMinutes = "sink_freshness_max_minutes"
)

// listingAgeFloor is the FLOOR under the freshness-gated helper paths that are NOT
// the activity-conditioned ranker: the freshListings-based
// sink/offload/reposition/distress/candidate scans and the stocker. The UNDIRECTED
// lane ranker (partitionListingsByAge) and the tour snapshot (BuildTourSnapshot)
// sit on the per-activity RankerAgeCaps table instead (sp-t5sh5) — the analyst's
// era3/4 fit showed staleness cost is activity-dependent, so one uniform threshold
// is the wrong SHAPE for ranking.
//
// It is a floor and no longer a cap (sp-k4z5b). The EFFECTIVE cap is
// marketscan.FreshnessCap(floor, budget, marketsKnown): on a small map, where the
// rotation is quick, 75 minutes stays the operative number and behaviour is
// unchanged; on today's 4,000+ market map the rotation bound dominates and the
// floor never binds. Never a silent invalidation either way.
//
// Even for the ranker no cap silently vetoes an operator-directed --dest lane,
// which is re-verified LIVE at execution (staleAskAborts + the per-visit margin
// re-check) — see scanLanes.
const listingAgeFloor = defaultListingMaxAgeMinutes * time.Minute

const (
	// defaultListingMaxAgeMinutes is the documented default of
	// TuneKeyListingMaxAgeMinutes, and the floor that applies when nothing is tuned.
	defaultListingMaxAgeMinutes = 75

	// defaultSinkFreshnessFloorMinutes is the documented default of
	// TuneKeySinkFreshnessMaxMinutes. It mirrors config.defaultSinkFreshnessMaxMinutes
	// — the boot floor SetSinkFreshness injects — so `tune --show` reports the same
	// number the daemon arms with when [trade_fleet] carries no override.
	defaultSinkFreshnessFloorMinutes = 75

	// freshnessSnapshotTTL bounds how long a resolved floor is reused before the
	// container config column is consulted again.
	//
	// It is what keeps this a live knob rather than a per-row database query: the
	// freshListings paths resolve a cap inside loops over candidate systems, and a
	// read per iteration would put a query on the tour's hot path. Five seconds is
	// far inside any coordinator tick, so a tune still lands on the next tick — the
	// latency the knob's own description promises.
	freshnessSnapshotTTL = 5 * time.Second
)

// TradeFleetTunableDefaults maps every LIVE-tunable trade-fleet-coordinator knob to
// its documented default — the value that applies when the live container config
// carries no positive one. The daemon's tune bounds registry reads THIS map
// (mirroring SensingTunableDefaults / ScoutPostTunableDefaults), so the
// defaults-of-record stay next to the consts they mirror and a registry drift test
// can pin them.
func TradeFleetTunableDefaults() map[string]int {
	return map[string]int{
		TuneKeyListingMaxAgeMinutes:    defaultListingMaxAgeMinutes,
		TuneKeySinkFreshnessMaxMinutes: defaultSinkFreshnessFloorMinutes,
	}
}

// ScanRotationSource reports the fleet's market-scan allowance and the live size of
// the map sharing it. *ship.ScanBudget satisfies it; a narrow optional port rather
// than an import so the trading package never depends on the scanner's own package.
type ScanRotationSource interface {
	RotationInputs(ctx context.Context) (marketscan.Budget, int)
}

// FreshnessFloorSource snapshots the persisted container config carrying the tuned
// floors. It is liveconfig.Reader resolved BY COORDINATOR TYPE rather than by
// container id: the tour handler is one daemon-global instance serving every TRADING
// container, so it has no id of its own to read, while the standing trade fleet
// coordinator that owns these knobs has exactly one active row per player.
type FreshnessFloorSource interface {
	Snapshot(ctx context.Context, playerID int) (liveconfig.Snapshot, error)
}

// MarketFreshness resolves a freshness cap from the live rotation and the live
// operator floor.
//
// The zero value is not usable; a NIL *MarketFreshness is, and reports the caller's
// own floor unchanged. That is the optional-port contract every test relies on: a
// handler nobody wired behaves exactly as it did before this type existed.
type MarketFreshness struct {
	rotation ScanRotationSource
	floors   FreshnessFloorSource
	clock    shared.Clock

	mu    sync.Mutex
	cache map[int]freshnessSnapshot
}

type freshnessSnapshot struct {
	config  liveconfig.Snapshot
	takenAt time.Time
}

// NewMarketFreshness wires the resolver. Either source may be nil: without a
// rotation source every cap is its floor, and without a floor source every floor is
// the caller's boot value — both fail-safe toward existing behaviour rather than
// toward a widened cap.
func NewMarketFreshness(rotation ScanRotationSource, floors FreshnessFloorSource, clock shared.Clock) *MarketFreshness {
	return &MarketFreshness{
		rotation: rotation,
		floors:   floors,
		clock:    clock,
		cache:    make(map[int]freshnessSnapshot),
	}
}

// Cap resolves one freshness cap: the larger of the live floor and the age the
// current rotation cannot explain.
//
// bootFloor is what applies when the knob is untuned — the daemon's config-resolved
// value for the sink guard, the documented default for the listing paths. A
// non-positive bootFloor is passed straight through so a caller that uses "<= 0" to
// mean "this clause is INERT" keeps that meaning: arming is a boot concern, and no
// tune may arm a clause the daemon left off (nor, because the floor is a floor,
// disarm one it left on).
func (f *MarketFreshness) Cap(ctx context.Context, playerID int, key string, bootFloor time.Duration) time.Duration {
	if f == nil || bootFloor <= 0 {
		return bootFloor
	}
	floor := bootFloor
	if minutes, ok := f.snapshot(ctx, playerID).PositiveInt(key); ok {
		floor = time.Duration(minutes) * time.Minute
	}
	budget, marketsKnown := f.rotationInputs(ctx)
	return marketscan.FreshnessCap(floor, budget, marketsKnown)
}

// RotationBound is the age the current rotation cannot explain, with no floor
// applied — the number worth naming in a refusal log so an operator can see at once
// whether their floor is the binding term or the map is. Zero when no rotation
// source is wired.
func (f *MarketFreshness) RotationBound(ctx context.Context) time.Duration {
	if f == nil {
		return 0
	}
	budget, marketsKnown := f.rotationInputs(ctx)
	return marketscan.FreshnessCap(0, budget, marketsKnown)
}

func (f *MarketFreshness) rotationInputs(ctx context.Context) (marketscan.Budget, int) {
	if f.rotation == nil {
		return marketscan.Budget{}, 0
	}
	return f.rotation.RotationInputs(ctx)
}

// snapshot returns the player's tuned floors, re-reading the config column at most
// once per freshnessSnapshotTTL. A failed read reuses the previous snapshot rather
// than clearing it: a transient database hiccup must not silently collapse every
// floor back to its default mid-incident, which is the moment an operator is most
// likely to have just tuned one.
func (f *MarketFreshness) snapshot(ctx context.Context, playerID int) liveconfig.Snapshot {
	if f.floors == nil {
		return nil
	}

	now := f.now()
	f.mu.Lock()
	cached, ok := f.cache[playerID]
	f.mu.Unlock()
	if ok && now.Sub(cached.takenAt) < freshnessSnapshotTTL {
		return cached.config
	}

	config, err := f.floors.Snapshot(ctx, playerID)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil {
		// Stamp the attempt either way so a persistently failing reader is retried on
		// the TTL rather than on every single resolve.
		cached.takenAt = now
		f.cache[playerID] = cached
		return cached.config
	}
	f.cache[playerID] = freshnessSnapshot{config: config, takenAt: now}
	return config
}

func (f *MarketFreshness) now() time.Time {
	if f.clock == nil {
		return time.Now()
	}
	return f.clock.Now()
}

// SetMarketFreshness wires the rotation-derived freshness caps and their live
// operator floors (sp-k4z5b). The daemon calls it UNCONDITIONALLY at boot — there is
// no config key and no arming step between the scan budget and a derived cap.
// Leaving it unset is the test path, and holds every cap at listingAgeFloor / the
// SetSinkFreshness value exactly as before.
func (h *RunTourCoordinatorHandler) SetMarketFreshness(freshness *MarketFreshness) {
	h.freshness = freshness
}

// listingMaxAge is the effective freshness cap for the freshListings-based paths:
// listingAgeFloor (or the operator's tuned floor) widened to whatever the current
// scan rotation cannot beat.
func (h *RunTourCoordinatorHandler) listingMaxAge(ctx context.Context, playerID int) time.Duration {
	return h.freshness.Cap(ctx, playerID, TuneKeyListingMaxAgeMinutes, listingAgeFloor)
}

// sinkMaxAge is the effective FRESH-clause cap on the firm-sink buy gate (sp-tgll8
// item 2, a fail-closed money guard). h.sinkFreshnessMaxAge remains the ARM: a
// non-positive boot value keeps the clause inert and no tune can change that, and a
// positive one can never be tuned back down below the rotation bound (RULINGS #4).
func (h *RunTourCoordinatorHandler) sinkMaxAge(ctx context.Context, playerID int) time.Duration {
	return h.freshness.Cap(ctx, playerID, TuneKeySinkFreshnessMaxMinutes, h.sinkFreshnessMaxAge)
}

// SetMarketFreshness wires the stocker's half of the same resolver — the stocker
// picks its storage-refill source off freshListings against the identical cap, so
// the two must not diverge. nil leaves it on listingAgeFloor, unchanged.
func (h *RunStockerCoordinatorHandler) SetMarketFreshness(freshness *MarketFreshness) {
	h.freshness = freshness
}

// listingMaxAge is the stocker's effective freshness cap. cmd.MaxMarketAgeMinutes
// still overrides it outright: that is a per-run operator instruction on a one-shot
// container, not a fleet-wide default, so it is honoured as given.
func (h *RunStockerCoordinatorHandler) listingMaxAge(ctx context.Context, playerID int) time.Duration {
	return h.freshness.Cap(ctx, playerID, TuneKeyListingMaxAgeMinutes, listingAgeFloor)
}
