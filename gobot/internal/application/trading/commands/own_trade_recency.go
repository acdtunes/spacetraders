package commands

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// own_trade_recency.go de-ranks a reposition candidate by how recently the fleet itself last
// traded in it, so hulls stop queueing onto ground another hull has just worked.
//
// NOT THE ANTI-HERD CAP AGAIN. That cap counts hulls RESIDENT at the instant of the check, so
// a queue of hulls each staying minutes and leaving never trips it while the system is drained
// continuously; the gap between just-worked and rested ground is present even at zero
// residents, where the cap cannot be binding. It bounds occupancy, this bounds throughput.
//
// A HAIRCUT, NOT A COOLDOWN. Half-hour spacing needs about as many systems in rotation as the
// fleet reaches in an hour, so an eligibility cooldown would sit on the edge of starving the
// ranker on a thin frontier, and a hull with nowhere to go is worse than a hull on crowded
// ground. A multiplier cannot remove a candidate: the set is the same size and the floor gate
// is untouched — only the ORDER candidates are offered a solver call in changes.
//
// LINEAR IN LOG-TIME because that is the scale realised margin moves on, flattening a few
// hours out, which is where the penalty reaches zero.

// OwnTradeRecencyReader supplies the per-SYSTEM "when did we last trade here" table the
// reposition pre-rank de-ranks against. It returns a map rather than an error for
// GateFeeReader's reason: an unreadable table leaves every candidate unpenalised, and a hull
// must never fail to reposition because a preference could not be read.
type OwnTradeRecencyReader interface {
	LastTradeBySystem(ctx context.Context, playerID int) map[string]time.Time
}

const (
	// ownTradePenaltyPctDefault is the largest share of its pre-rank score a candidate can lose
	// for having been traded moments ago. Sized UNDER the measured margin gap on purpose, so
	// the haircut re-orders candidates the board scored close together and cannot overturn a
	// genuinely richer ground: a just-worked system must be beaten on the board by roughly half
	// again to be displaced. A named knob, not a magic constant (RULINGS #5); retune as the
	// fleet's crossing rate and the reachable frontier shift.
	ownTradePenaltyPctDefault = 35

	// ownTradeColdMinutesDefault is the age at which ground counts as rested and the penalty
	// reaches zero. Past this horizon the realised-margin curve flattens, so a longer memory
	// would charge for staleness the fleet's own prices no longer show.
	ownTradeColdMinutesDefault = 240

	// ownTradeColdMinutesMax bounds the configurable horizon because that horizon is ALSO the
	// ledger scan's lookback — the aggregate is cheap over hours and expensive over days, on a
	// database that concurrently serves the live planner.
	ownTradeColdMinutesMax = 12 * 60

	// ownTradeAgeFloorMinutes keeps a just-booked trade off the log's singularity at zero and,
	// with it, off the zero-means-unknown sentinel the candidate carries.
	ownTradeAgeFloorMinutes = 0.5

	// ownTradeCacheTTL bounds how often the aggregate re-runs. The penalty grades ground in tens
	// of minutes, so a table a minute old ranks the same candidates the same way, while a read
	// per reposition would put a grouped ledger scan on a hot path for no change in the answer.
	ownTradeCacheTTL = 60 * time.Second
)

// resolveOwnTradePenaltyPct applies the 0/absent → default rule to
// reposition_own_trade_penalty_pct so the default lives in ONE place (RULINGS #5). A configured
// value is clamped to [0,100]: >100 would invert the ranking by driving the multiplier negative,
// which would make the most-recently-drained ground rank BEST.
func resolveOwnTradePenaltyPct(configured int) int {
	if configured <= 0 {
		return ownTradePenaltyPctDefault
	}
	if configured > 100 {
		return 100
	}
	return configured
}

// resolveOwnTradeColdMinutes applies the 0/absent → default rule to
// reposition_own_trade_cold_minutes, clamped to the scan bound above.
func resolveOwnTradeColdMinutes(configured int) int {
	if configured <= 0 {
		return ownTradeColdMinutesDefault
	}
	if configured > ownTradeColdMinutesMax {
		return ownTradeColdMinutesMax
	}
	return configured
}

// ownTradeFreshnessMultiplier is what a candidate's pre-rank score is multiplied by given how
// many minutes ago the fleet last traded in it: 1 for rested or unknown ground, falling to
// (1 - maxPct/100) as the gap closes on zero, linear in log-time between the two.
//
// ageMinutes <= 0 is the UNKNOWN sentinel and is never penalised — a missing timestamp is not
// evidence of crowding, and reading it as crowding would de-rank the ground nobody has touched.
func ownTradeFreshnessMultiplier(ageMinutes float64, maxPct, coldMinutes int) float64 {
	if ageMinutes <= 0 || maxPct <= 0 {
		return 1
	}
	cold := float64(coldMinutes)
	if cold <= 0 || ageMinutes >= cold {
		return 1
	}
	// Both ends are log1p so the mapping is continuous at the horizon and finite at the floor.
	staleShare := math.Log1p(ageMinutes) / math.Log1p(cold)
	if staleShare > 1 {
		staleShare = 1
	}
	return 1 - (float64(maxPct)/100)*(1-staleShare)
}

// ownTradeAgeMinutes converts a last-trade stamp into the age the multiplier reads. The zero
// time is ground with no recorded trade and returns the unknown sentinel; a stamp in the
// future is a clock fault and floors, because it must not read as unknown either.
func ownTradeAgeMinutes(lastTrade, now time.Time) float64 {
	if lastTrade.IsZero() {
		return 0
	}
	if age := now.Sub(lastTrade).Minutes(); age >= ownTradeAgeFloorMinutes {
		return age
	}
	return ownTradeAgeFloorMinutes
}

// ownTradeAgeNote renders a candidate's last-own-trade age for the ranking line; the unknown
// sentinel renders as nothing, keeping the line identical on an unwired daemon.
func ownTradeAgeNote(ageMinutes float64) string {
	if ageMinutes <= 0 {
		return ""
	}
	return fmt.Sprintf(",own-trade=%.0fm", ageMinutes)
}

// OwnTradeRecencyLookback is how far back the scan must reach for a configured cold horizon:
// exactly that horizon, since older ground scores the same as never-traded ground. Exported so
// the daemon sizes the scan from the SAME knob the coordinator resolves against, rather than a
// second constant that could drift below it and report worked ground as rested.
func OwnTradeRecencyLookback(configuredColdMinutes int) time.Duration {
	return time.Duration(resolveOwnTradeColdMinutes(configuredColdMinutes)) * time.Minute
}

// ownTradeRecencyTable is the handler's nil-safe read. An unwired reader yields a nil map,
// whose every lookup is the zero time and therefore the unknown sentinel.
func (h *RunTourCoordinatorHandler) ownTradeRecencyTable(ctx context.Context, playerID int) map[string]time.Time {
	if h == nil || h.ownTradeRecency == nil {
		return nil
	}
	return h.ownTradeRecency.LastTradeBySystem(ctx, playerID)
}

// LedgerOwnTradeRecencyReader learns the table from recorded cargo trades and caches it.
//
// FAIL-OPEN THROUGHOUT: an unreadable ledger, an empty one, an unparseable player all yield an
// empty table, which leaves every candidate at multiplier 1. This is a ranking preference, not
// a money guard — there is no spend to fail closed on, and starving the planner would cost
// more than the crowding it relieves.
type LedgerOwnTradeRecencyReader struct {
	repo     ledger.OwnTradeRecencyReader
	clock    shared.Clock
	lookback time.Duration

	mu     sync.Mutex
	cached map[int]ownTradeSnapshot
}

type ownTradeSnapshot struct {
	lastBySystem map[string]time.Time
	expiresAt    time.Time
}

// NewLedgerOwnTradeRecencyReader wires the ledger-backed reader. A nil clock means the real
// one (the daemon's `nil = use RealClock` idiom); a non-positive lookback falls to the default
// cold horizon, the oldest age that can still change a multiplier.
func NewLedgerOwnTradeRecencyReader(
	repo ledger.OwnTradeRecencyReader, clock shared.Clock, lookback time.Duration,
) *LedgerOwnTradeRecencyReader {
	if clock == nil {
		clock = &shared.RealClock{}
	}
	if lookback <= 0 {
		lookback = time.Duration(ownTradeColdMinutesDefault) * time.Minute
	}
	return &LedgerOwnTradeRecencyReader{
		repo:     repo,
		clock:    clock,
		lookback: lookback,
		cached:   make(map[int]ownTradeSnapshot),
	}
}

// LastTradeBySystem returns the cached table, re-reading it when the entry has expired. The
// returned map is never mutated after publication, so concurrent tour containers share it.
func (r *LedgerOwnTradeRecencyReader) LastTradeBySystem(ctx context.Context, playerID int) map[string]time.Time {
	if r == nil || r.repo == nil || r.clock == nil {
		return nil
	}
	// The coordinator carries a plain-int identity; the repository wants the value object.
	// An identity that will not parse cannot address a ledger, so there is nothing to learn.
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return nil
	}
	now := r.clock.Now()

	r.mu.Lock()
	snap, ok := r.cached[playerID]
	r.mu.Unlock()
	if ok && now.Before(snap.expiresAt) {
		return snap.lastBySystem
	}

	byWaypoint, err := r.repo.LastTradeByWaypoint(ctx, pid, now.Add(-r.lookback))
	if err != nil {
		// Serve the last good table if we hold one: an expired copy only ever UNDERSTATES
		// freshness, so it can misjudge worked ground as rested but never invent crowding.
		if ok {
			return snap.lastBySystem
		}
		return nil
	}

	bySystem := make(map[string]time.Time, len(byWaypoint))
	for waypoint, at := range byWaypoint {
		system := shared.ExtractSystemSymbol(waypoint)
		if system == "" {
			continue
		}
		// A system's clock is its most recent market: one hull working one waypoint already
		// moved prices the ranker reads across that system's whole board.
		if prev, seen := bySystem[system]; !seen || at.After(prev) {
			bySystem[system] = at
		}
	}

	r.mu.Lock()
	r.cached[playerID] = ownTradeSnapshot{lastBySystem: bySystem, expiresAt: now.Add(ownTradeCacheTTL)}
	r.mu.Unlock()
	return bySystem
}
