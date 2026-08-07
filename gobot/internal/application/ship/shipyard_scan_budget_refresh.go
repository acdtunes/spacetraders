package ship

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// yardCountTTL is how long a charted-shipyard count is reused. The count moves
// only when charting finds new yards, so minutes of staleness cost nothing and
// re-counting per admission would put a query on the scan path.
const yardCountTTL = 5 * time.Minute

// yardFactsTTL is how long the demand picture read from the shipyard inventory is
// reused.
//
// It exists for RESTARTS, and that is the case that makes it load-bearing rather
// than an optimisation. Live scans keep the in-memory picture current through
// Observe, but a daemon that has just restarted has observed nothing — and the 80
// starved yards of the incident were known to sell a heavy only in the DATABASE.
// Without this refresh the budget would wake up believing no yard sells anything
// wanted, weight the entire map at the prior, and rediscover the demand ordering
// only as scans happened to land: the exact blindness it exists to prevent.
const yardFactsTTL = 5 * time.Minute

// ChartedYardCounter reports how many shipyard waypoints the player has charted —
// the denominator of "budget ÷ yards known".
//
// A narrow optional port rather than a widening of the scanner's waypoint
// interface, which dozens of test fakes implement and which has no business
// knowing the map size. The production waypoint repository satisfies it and the
// budget discovers it by type assertion; a store that does not falls back to
// counting the yards the budget has been asked about, which is a lower bound on
// the map and so paces slightly LOOSER but never leaves the budget unenforced.
type ChartedYardCounter interface {
	ChartedShipyardCount(ctx context.Context) (int, error)
}

// YardCatalogReader reports every persisted yard row whose ship type is in the
// given set — the budget's view of which counters sell what the fleet wants, and
// which of those have ever been priced. shipyard.InventoryRepository satisfies it.
type YardCatalogReader interface {
	ListByTypes(ctx context.Context, playerID int, shipTypes []string) ([]shipyard.ShipTypeAvailability, error)
}

// refreshChartedCount re-reads the map size when the cached reading has aged out.
// It runs OUTSIDE the mutex so a slow query cannot block every other container's
// admission, and a failed read leaves the previous count in place — a counter
// hiccup must widen nothing and must not reset the denominator to zero, which
// would collapse every interval.
func (b *YardScanBudget) refreshChartedCount(ctx context.Context) {
	b.mu.Lock()
	counter := b.counter
	due := b.chartedAt.IsZero() || b.now().Sub(b.chartedAt) >= yardCountTTL
	b.mu.Unlock()

	if counter == nil || !due {
		return
	}

	total, err := counter.ChartedShipyardCount(ctx)

	b.mu.Lock()
	defer b.mu.Unlock()
	// Stamp the attempt either way, so a persistently failing counter is retried on
	// the TTL rather than on every single admission.
	b.chartedAt = b.now()
	// A ZERO IS A FAILED READING HERE, NOT A MAP SIZE. The count reaches this seam
	// through an era scope that can degrade to a predicate no row matches, so an
	// unreadable era arrives as a confident zero rather than as an error — the
	// source refuses that case now, and this is the second half of the same guard,
	// because any other route to an empty count would collapse the denominator just
	// as completely. Accepting one drops the map size to the handful of yards this
	// process happens to have been asked about, which shortens every interval and
	// the anti-starvation bound with it; that bound admits UNCONDITIONALLY, above
	// the token cap and above the value bar, so collapsing it forces every read
	// through and leaves the allowance unenforced. Refusing costs only that an era
	// with genuinely no charted yard paces against the previous count until the
	// first one is charted — the direction that spends less. A real, SMALLER count
	// is still adopted: this rejects an unreadable map, not a shrinking one.
	if err != nil || total <= 0 {
		return
	}
	if total != b.charted {
		b.charted = total
		b.aggregateStale = true
	}
}

// refreshFacts rebuilds the demand picture from the persisted inventory when the
// cached one has aged out.
//
// A failed read leaves the previous picture in place. That direction is the safe
// one: the picture only ever RAISES a yard's weight above the baseline, so losing
// it degrades the budget to an unprioritised rotation rather than to an unpaced
// one, and re-reading on the TTL recovers it.
func (b *YardScanBudget) refreshFacts(ctx context.Context, playerID int) {
	b.mu.Lock()
	catalog := b.catalog
	due := b.catalogAt.IsZero() || b.now().Sub(b.catalogAt) >= yardFactsTTL || playerID != b.lastPlayer && b.catalogHeld
	wanted := b.wantedTypesLocked()
	b.mu.Unlock()

	if catalog == nil || !due || len(wanted) == 0 {
		return
	}

	rows, err := catalog.ListByTypes(ctx, playerID, wanted)

	b.mu.Lock()
	defer b.mu.Unlock()
	b.catalogAt = b.now()
	if err != nil {
		return
	}
	b.catalogHeld = true
	// Stamped here rather than only in Admit, because the dueness test above reads
	// it: a caller that refreshes WITHOUT admitting (PresenceRequests) would
	// otherwise never match lastPlayer, find itself perpetually due, and put a store
	// query on every single tick. Set only on a SUCCESSFUL read, so a failing
	// catalogue still forces the re-read a player change is supposed to force.
	b.lastPlayer = playerID

	// PRICED IS REBUILT PER WAYPOINT, NOT ACCUMULATED, and that is a correctness
	// fix rather than a tidy-up. The rows here are the whole of what the store holds
	// for these yards — ReplaceScan deletes a waypoint's rows and re-inserts from
	// each reading, so a presence-less rescan of a yard whose hull has left writes
	// purchase_price 0 and nothing else survives. A flag that could only ever be
	// SET would latch the old price forever: the yard would read as priced, drop out
	// of the presence queue, and never get another hull — while the number the buy
	// loop is reasoning about is gone from the database. Aggregating first is what
	// lets a yard go dark again.
	priced := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.WaypointSymbol == "" {
			continue
		}
		if row.PurchasePrice > 0 {
			priced[row.WaypointSymbol] = true
		} else if _, seen := priced[row.WaypointSymbol]; !seen {
			priced[row.WaypointSymbol] = false
		}
	}

	for _, row := range rows {
		if row.WaypointSymbol == "" {
			continue
		}
		f := b.facts[row.WaypointSymbol]
		f.Unknown = false
		f.SellsWanted = true
		f.Priced = priced[row.WaypointSymbol]
		b.facts[row.WaypointSymbol] = f
		// The HEAVY flag, unlike Priced, is accumulated and never cleared here.
		// These rows are the wanted-type SUBSET of the inventory rather than a
		// yard's whole listing, so an absence of heavy rows is "the query did not
		// ask about that" and not "the counter stopped stocking them". Observe holds
		// the full listing and is the one place that may demote a heavy yard.
		if b.heavy.Contains(row.ShipType) {
			b.heavySeller[row.WaypointSymbol] = true
		}
		if _, ok := b.seen[row.WaypointSymbol]; !ok {
			b.seen[row.WaypointSymbol] = struct{}{}
		}
	}
	b.aggregateStale = true
}
