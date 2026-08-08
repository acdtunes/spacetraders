package queries

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/fleetgrowth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// tradeFleetTag is the dedication a hull carries when it belongs to the trade-tour pool.
const tradeFleetTag = "trade"

// ProfitableLaneCounter counts the profitable, feasible lanes ranked across the given systems,
// read-only, off the persisted market cache. Satisfied by ProfitableLaneReader.
type ProfitableLaneCounter interface {
	CountProfitableLanes(ctx context.Context, playerID int, systems []string) (census LaneCensus, readable bool, err error)
}

// UnservedLaneReader surfaces the trade solver's lane demand in the two dimensions the wave judges:
// the profitable-but-unflown lane COUNT ranked BEYOND the current trade-hull pool, and whether the
// DEPTH of that surface has been saturated by the pool's own hold. The surplus IS the capacity-short
// signal; the depth comparison is the switch-back cue that ends it.
//
// ONE INSTANCE, TWO CONSUMERS: the growth coordinator's heavy demand and the sensing drain's wave
// gate, constructed once at the composition root precisely so a second one is conspicuous — two
// readers of one quantity is how a spender and a withholder disagree. They tick CONCURRENTLY and at
// different rates, hence a wall-clock, mutex-guarded window rather than a per-consumer streak.
//
// READ-ONLY: it consumes the same pure ranking off the market cache that the trade circuit uses.
type UnservedLaneReader struct {
	ships navigation.ShipRepository
	lanes ProfitableLaneCounter

	// mu guards the window and its knobs: both consumers read through ONE reader from their own
	// goroutines, so the latch is genuinely shared state rather than per-caller scratch.
	mu        sync.Mutex
	clock     shared.Clock
	marginPct int
	dwell     time.Duration
	latch     map[int]*saturationLatch
}

// saturationLatch is one player's published verdict and the anti-thrash window behind it.
//
// steadySince is the last instant the RAW comparison AGREED with what is published, not the first
// instant it disagreed. That anchor makes the window independent of who polled it — "the first tick
// that saw the change" is a fact about the poller, "the last tick that confirmed the old verdict" a
// fact about the surface — and is the only anchor a slow consumer ever closes the window under.
type saturationLatch struct {
	published   bool
	steadySince time.Time
}

// NewUnservedLaneReader wires the capacity-short signal over its two read sources. The saturation
// knobs are left unset and resolve to their defaults on use, so a composition root that forgets
// SetSaturation runs the fitted term rather than the 0%-margin, no-window pair that disables it.
func NewUnservedLaneReader(ships navigation.ShipRepository, lanes ProfitableLaneCounter) *UnservedLaneReader {
	return &UnservedLaneReader{
		ships: ships,
		lanes: lanes,
		clock: shared.NewRealClock(),
		latch: map[int]*saturationLatch{},
	}
}

// SetClock replaces the clock the window is measured on; nil keeps the real one.
func (r *UnservedLaneReader) SetClock(clock shared.Clock) {
	if clock == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clock = clock
}

// SetSaturation wires the config-resolved switch-back knobs (RULINGS #5), the same way the census
// takes its freshness table through SetRankerAgeCaps.
//
// A NON-POSITIVE VALUE IS AN UNSET KNOB, NOT A SETTING: `tune <key> 0` deletes a key, so zero
// resolves to the default here as well as in the config resolver. A 0% margin saturates only an
// empty surface and a zero dwell makes the debounce a no-op — the two ways an operator meaning to
// reset this term would instead switch it off.
func (r *UnservedLaneReader) SetSaturation(marginPct int, dwell time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.marginPct, r.dwell = marginPct, dwell
}

// UnservedLaneCount reports profitable lanes beyond the trade pool, floored at 0. It is
// LaneDemand's count, not a second read: an entry point that skipped the demand read would leave
// the two wave consumers advancing two windows at two rates.
func (r *UnservedLaneReader) UnservedLaneCount(ctx context.Context, playerID int) (int, bool, error) {
	demand, readable, err := r.LaneDemand(ctx, playerID)
	return demand.UnservedLanes, readable, err
}

// LaneDemand reports the tick's lane surface in both dimensions: the unserved COUNT and the DEPTH
// verdict that answers it.
//
// It fails CLOSED (readable=false) on a genuine ship or market read failure: acting on a capacity
// signal nobody could read is the runaway the guard stacks exist to prevent. A READABLE zero — an
// empty cache, or no lane clearing the floor — is a genuine zero and reported as such, or a fleet
// with nothing to fly is indistinguishable from one whose sensors are down. A BLIND TICK DOES NOT
// OBSERVE "UNSATURATED": it leaves the verdict alone and restarts the window, so an outage cannot
// debounce a saturated fleet back into heavy buying, which is the spend direction.
func (r *UnservedLaneReader) LaneDemand(ctx context.Context, playerID int) (fleetgrowth.LaneDemand, bool, error) {
	label := strconv.Itoa(playerID)
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return fleetgrowth.LaneDemand{}, false, nil
	}
	ships, err := r.ships.FindAllByPlayer(ctx, pid)
	if err != nil {
		r.holdWindow(playerID)
		metrics.RecordGrowthLaneSurface(label, metrics.LaneSurface{})
		return fleetgrowth.LaneDemand{}, false, err
	}
	systems := distinctShipSystems(ships)
	// THE CAPACITY SIDE IS THE TRADE POOL'S HOLD, NOT THE FLEET'S, counted off the same walk as the
	// pool it divides: a hull flying contracts or standing as a probe will never lift one of these
	// lanes, so counting its hold would declare a surface saturated on capacity no lane can reach.
	pool, hold := 0, 0
	for _, sh := range ships {
		if sh.DedicatedFleet() != tradeFleetTag {
			continue
		}
		pool++
		hold += sh.CargoCapacity()
	}
	census, readable, err := r.lanes.CountProfitableLanes(ctx, playerID, systems)
	if err != nil || !readable {
		r.holdWindow(playerID)
		metrics.RecordGrowthLaneSurface(label, metrics.LaneSurface{TradePool: pool, SystemsScanned: len(systems)})
		return fleetgrowth.LaneDemand{}, false, err
	}
	unserved := census.Profitable - pool
	if unserved < 0 {
		unserved = 0
	}

	marginPct, dwell := r.knobs()
	demand := fleetgrowth.LaneDemand{
		UnservedLanes:       unserved,
		AbsorbableUnits:     census.AbsorbableUnits,
		TradeHoldCapacity:   hold,
		SaturationThreshold: fleetgrowth.SaturationThreshold(hold, marginPct),
		Saturated: r.publishSaturation(
			playerID,
			fleetgrowth.TradeSaturated(census.AbsorbableUnits, hold, marginPct),
			dwell,
		),
	}

	// The census terms travel UNCHANGED: they explain the count taken on this tick, or nothing. Both
	// sides of the depth comparison travel with them, so a switch-back is explicable from the series.
	metrics.RecordGrowthLaneSurface(label, metrics.LaneSurface{
		Unserved:            unserved,
		Profitable:          census.Profitable,
		TradePool:           pool,
		SystemsScanned:      len(systems),
		CrossSystem:         census.CrossSystem,
		AbsorbableUnits:     census.AbsorbableUnits,
		TradeHoldCapacity:   hold,
		SaturationThreshold: demand.SaturationThreshold,
		RejectedUnreachable: census.RejectedUnreachable,
		RejectedJumpCost:    census.RejectedJumpCost,
		Readable:            true,
	})
	return demand, true, nil
}

// publishSaturation debounces a CHANGED verdict across the dwell and returns what is published.
//
// THE FIRST READING IS ADOPTED WHOLE. The latch is in-memory and re-derived from durable facts
// (RULINGS #2), so a restart begins with no history — and one that spent its first dwell reporting
// "not saturated" would resume heavy buying against a surface it had already outgrown. THE WINDOW
// IS SYMMETRIC: one deep scan must not release a fleet into heavy buying, and one thin scan must
// not pause a fleet that has work.
func (r *UnservedLaneReader) publishSaturation(playerID int, raw bool, dwell time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock.Now()
	latch, seen := r.latch[playerID]
	if !seen {
		r.latch[playerID] = &saturationLatch{published: raw, steadySince: now}
		return raw
	}
	if raw == latch.published {
		latch.steadySince = now
		return latch.published
	}
	if now.Sub(latch.steadySince) >= dwell {
		latch.published = raw
		latch.steadySince = now
	}
	return latch.published
}

// holdWindow restarts an unreadable player's window without touching the verdict it publishes. A
// blind stretch is NOT evidence and must not accumulate toward a flip in either direction; it
// restarts rather than freezes, so a change needs a full dwell of readable, agreeing observations —
// the conservative choice on the un-latching side, which is the side that resumes spending.
func (r *UnservedLaneReader) holdWindow(playerID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if latch, seen := r.latch[playerID]; seen {
		latch.steadySince = r.clock.Now()
	}
}

// knobs snapshots the saturation settings for one tick. The margin's default is applied by the
// domain primitives themselves; only the dwell has no other resolver below this line.
func (r *UnservedLaneReader) knobs() (marginPct int, dwell time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dwell = r.dwell
	if dwell <= 0 {
		dwell = fleetgrowth.DefaultSaturationDwell
	}
	return r.marginPct, dwell
}

// distinctShipSystems returns the distinct systems the player's hulls stand in — the trading
// grounds the lane count scans.
func distinctShipSystems(ships []*navigation.Ship) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ships))
	for _, sh := range ships {
		loc := sh.CurrentLocation()
		if loc == nil {
			continue
		}
		system := shared.ExtractSystemSymbol(loc.Symbol)
		if _, ok := seen[system]; ok {
			continue
		}
		seen[system] = struct{}{}
		out = append(out, system)
	}
	return out
}
