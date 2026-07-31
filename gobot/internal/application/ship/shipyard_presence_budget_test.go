package ship

// Unit tests for the shipyard budget's PRESENCE half (sp-fox5u): the request set
// it publishes for yards no read can price, and the allowance that paces the hull
// moves those requests cause.
//
// The read budget beside this one is proven in shipyard_scan_budget_test.go and
// nothing here re-proves it. What is proven here is the four properties the bead
// turns on: the set is exactly the unpriceable tier, heavies outrank everything,
// a yard leaves the set the moment it is priced, and repositions are METERED
// rather than issued as fast as the fleet can find hulls.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// yardRow is one persisted inventory row, the form the demand picture is rebuilt
// from after a restart.
func yardRow(waypoint, shipType string, price int) shipyard.ShipTypeAvailability {
	return shipyard.ShipTypeAvailability{
		SystemSymbol:   waypoint[:len(waypoint)-len("-Y1")],
		WaypointSymbol: waypoint,
		ShipType:       shipType,
		PurchasePrice:  price,
	}
}

// TestPresenceRequests_AreExactlyTheYardsNoReadCanPrice is the core claim. A yard
// we hold a price for is not requested (a read already answered it); an unopened
// catalogue is not requested (the FREE presence-less pass answers that, for one
// request and no hull); only a CONFIRMED seller of a wanted hull at an unknown
// price is worth moving a hull for.
func TestPresenceRequests_AreExactlyTheYardsNoReadCanPrice(t *testing.T) {
	b, _ := newTestYardBudget(t, 10)
	b.SetYardCatalogReader(&stubYardCatalog{rows: []shipyard.ShipTypeAvailability{
		yardRow("X1-DARK-Y1", "SHIP_HEAVY_FREIGHTER", 0),
		yardRow("X1-LIT-Y1", "SHIP_HEAVY_FREIGHTER", 1_918_293),
	}})

	got := b.PresenceRequests(context.Background(), testPlayerID, 16)
	require.Len(t, got, 1, "expected only the unpriced seller to request presence, got %+v", got)
	require.Equal(t, "X1-DARK-Y1", got[0].Waypoint)
	require.Equal(t, "X1-DARK", got[0].System, "the system must be derivable for the mover's in-system search")
	require.True(t, got[0].Heavy, "a SHIP_HEAVY_FREIGHTER seller must be marked heavy")
}

// TestPresenceRequests_NeverAsksForAnUnopenedCatalogue. An Unknown yard MIGHT
// hold a heavy, and the read budget ranks it above a dull yard for exactly that
// reason — but what it needs is one request, not a hull. Admitting it here would
// spend the scarce remedy on the cheap problem.
func TestPresenceRequests_NeverAsksForAnUnopenedCatalogue(t *testing.T) {
	b, _ := newTestYardBudget(t, 10)
	// No catalogue reader and no observation: every yard is Unknown.
	require.Empty(t, b.PresenceRequests(context.Background(), testPlayerID, 16))
}

// TestPresenceRequests_HeavySellersOutrankEverything. The incident this feature
// exists for had 24 heavies bought at up to 2,288,156 against a visible cheapest
// of 1,918,293. A ranking that let cheap yards crowd out a heavy counter would
// reproduce it.
func TestPresenceRequests_HeavySellersOutrankEverything(t *testing.T) {
	b, _ := newTestYardBudget(t, 40)
	rows := []shipyard.ShipTypeAvailability{}
	// Twenty probe yards ahead of the heavy one in symbol order, so a pass that
	// ranked by anything but demand would bury it.
	for i := 0; i < 20; i++ {
		rows = append(rows, yardRow("X1-AAA"+string(rune('A'+i))+"-Y1", "SHIP_PROBE", 0))
	}
	rows = append(rows, yardRow("X1-ZZZ-Y1", "SHIP_HEAVY_FREIGHTER", 0))
	b.SetYardCatalogReader(&stubYardCatalog{rows: rows})
	// SHIP_PROBE must be actively shopped for, or it is not wanted at all and the
	// heavy yard would win by default rather than by rank.
	b.NoteDemand("SHIP_PROBE")

	got := b.PresenceRequests(context.Background(), testPlayerID, 32)
	require.Greater(t, len(got), 1, "the fixture must offer competition, or the ranking is untested")
	require.Equal(t, "X1-ZZZ-Y1", got[0].Waypoint, "the heavy seller must head the queue, got %+v", got[0])
}

// TestPresenceRequests_HonourTheCallersLimit. The caller's bound is the last line
// of defence against a burst of hull moves.
func TestPresenceRequests_HonourTheCallersLimit(t *testing.T) {
	b, _ := newTestYardBudget(t, 40)
	rows := []shipyard.ShipTypeAvailability{}
	for i := 0; i < 12; i++ {
		rows = append(rows, yardRow("X1-D"+string(rune('A'+i))+"-Y1", "SHIP_HEAVY_FREIGHTER", 0))
	}
	b.SetYardCatalogReader(&stubYardCatalog{rows: rows})

	require.Len(t, b.PresenceRequests(context.Background(), testPlayerID, 5), 5)
	// A missing bound must yield NOTHING rather than everything: the reading that
	// turns an absent cap into "unbounded" is the one that would hurt.
	require.Empty(t, b.PresenceRequests(context.Background(), testPlayerID, 0))
	require.Empty(t, b.PresenceRequests(context.Background(), testPlayerID, -1))
}

// TestPresenceRequests_AYardStopsAskingOnceItIsPriced is the RETRACTION property,
// and the reason this is a pull rather than a push. A stored request list would
// keep sending hulls at a counter that no longer needs one.
func TestPresenceRequests_AYardStopsAskingOnceItIsPriced(t *testing.T) {
	b, _ := newTestYardBudget(t, 10)
	b.SetYardCatalogReader(&stubYardCatalog{rows: []shipyard.ShipTypeAvailability{
		yardRow("X1-DARK-Y1", "SHIP_HEAVY_FREIGHTER", 0),
	}})
	require.Len(t, b.PresenceRequests(context.Background(), testPlayerID, 16), 1)

	// The hull arrived and the next scan read a price. Observe is fed the SAME
	// availabilities the store is written from (shipyard_scanner.go), so this is
	// the real coupling and not a test convenience.
	b.Observe("X1-DARK-Y1", []shipyard.ShipTypeAvailability{
		yardRow("X1-DARK-Y1", "SHIP_HEAVY_FREIGHTER", 1_918_293),
	})
	require.Empty(t, b.PresenceRequests(context.Background(), testPlayerID, 16),
		"a priced yard must leave the request set on its own")
}

// TestPresenceRequests_AYardAsksAgainWhenItsPriceIsLost is the other direction,
// and it is what makes a dead quote impossible. ReplaceScan deletes a waypoint's
// rows and re-inserts from the reading, so a presence-less rescan of a yard whose
// hull has left writes purchase_price 0 — and the budget, fed the same reading,
// must let it back into the queue rather than latching Priced forever.
func TestPresenceRequests_AYardAsksAgainWhenItsPriceIsLost(t *testing.T) {
	b, _ := newTestYardBudget(t, 10)
	b.SetYardCatalogReader(&stubYardCatalog{rows: []shipyard.ShipTypeAvailability{
		yardRow("X1-DARK-Y1", "SHIP_HEAVY_FREIGHTER", 1_918_293),
	}})
	require.Empty(t, b.PresenceRequests(context.Background(), testPlayerID, 16))

	// The hull left; the next rotation read finds a catalogue and no listings.
	b.Observe("X1-DARK-Y1", []shipyard.ShipTypeAvailability{
		yardRow("X1-DARK-Y1", "SHIP_HEAVY_FREIGHTER", 0),
	})
	got := b.PresenceRequests(context.Background(), testPlayerID, 16)
	require.Len(t, got, 1, "a yard that lost its price must ask for presence again")
	require.Equal(t, "X1-DARK-Y1", got[0].Waypoint)
}

// TestPresenceRequests_SurviveARestart. A daemon that has just come up has
// observed nothing, so an in-memory-only request set would report that no yard
// needs presence on a fleet with 81 dark heavy counters in the DATABASE.
func TestPresenceRequests_SurviveARestart(t *testing.T) {
	b, _ := newTestYardBudget(t, 10)
	catalog := &stubYardCatalog{rows: []shipyard.ShipTypeAvailability{
		yardRow("X1-DARK-Y1", "SHIP_HEAVY_FREIGHTER", 0),
	}}
	b.SetYardCatalogReader(catalog)

	// No Admit, no Observe — nothing has happened on this process at all.
	require.Len(t, b.PresenceRequests(context.Background(), testPlayerID, 16), 1)
	require.Positive(t, catalog.calls, "the store must be read, or the set is memory-only")
}

// --- the allowance --------------------------------------------------------------

// TestAdmitPresence_RefusesWhenTheAllowanceIsSpent is the metering property. The
// bucket is drained by its own burst depth and the next request must be refused —
// a reposition is never forced, unlike a money guard's read.
func TestAdmitPresence_RefusesWhenTheAllowanceIsSpent(t *testing.T) {
	b, _ := newTestYardBudget(t, 10)

	granted := 0
	// One past the burst depth, so the bound is actually reached rather than
	// merely approached.
	for i := 0; i < yardPresenceBurst+1; i++ {
		if b.AdmitPresence() {
			granted++
		}
	}
	require.Equal(t, yardPresenceBurst, granted,
		"the reposition bucket must hand out exactly its depth against a frozen clock")

	snap := b.Snapshot()
	require.Equal(t, uint64(yardPresenceBurst), snap.PresenceIssued)
	require.Equal(t, uint64(1), snap.PresenceDeclined,
		"a refusal must be counted, or a pass losing every decision looks idle")
}

// TestAdmitPresence_RefillsAtTheConfiguredRate. The allowance is a rate, not a
// per-process quota: it must recover.
func TestAdmitPresence_RefillsAtTheConfiguredRate(t *testing.T) {
	b, now := newTestYardBudget(t, 10)
	for i := 0; i < yardPresenceBurst+1; i++ {
		b.AdmitPresence()
	}
	require.False(t, b.AdmitPresence(), "the fixture must start from an empty bucket")

	// One full interval at the configured rate.
	*now = now.Add(time.Duration(float64(time.Second) / defaultYardPresenceReqPerSec))
	require.True(t, b.AdmitPresence(), "the allowance must refill at its configured rate")
}

// TestAdmitPresence_IsNotTiedToTheReadAllowance. Deciding to scan the map harder
// must not silently authorise pulling hulls off their markets faster: the two
// allowances are sized against different things.
func TestAdmitPresence_IsNotTiedToTheReadAllowance(t *testing.T) {
	fast := NewYardScanBudget(50.0, testYardClamp, testHeavy)
	now := time.Now()
	fast.setClock(func() time.Time { return now })

	granted := 0
	for i := 0; i < yardPresenceBurst+8; i++ {
		if fast.AdmitPresence() {
			granted++
		}
	}
	require.Equal(t, yardPresenceBurst, granted,
		"a 50 req/s READ budget must still meter repositions at the reposition rate, got %d", granted)
}

// TestSnapshot_ReportsTheUnpriceableBacklog. A coordinator losing every decision
// must not look identical to an idle one, so the backlog is published beside the
// two counters that say why it is not moving.
func TestSnapshot_ReportsTheUnpriceableBacklog(t *testing.T) {
	b, _ := newTestYardBudget(t, 10)
	b.SetYardCatalogReader(&stubYardCatalog{rows: []shipyard.ShipTypeAvailability{
		yardRow("X1-DARKA-Y1", "SHIP_HEAVY_FREIGHTER", 0),
		yardRow("X1-DARKB-Y1", "SHIP_HEAVY_FREIGHTER", 0),
		yardRow("X1-LIT-Y1", "SHIP_HEAVY_FREIGHTER", 1_918_293),
	}})
	b.PresenceRequests(context.Background(), testPlayerID, 16)

	snap := b.Snapshot()
	require.Equal(t, 2, snap.YardsNeedingPresence,
		"the backlog must count only the yards a read cannot fix, got %+v", snap)
}

// TestPresenceRequests_OnANilBudgetAreEmpty. The port is optional wiring; a nil
// budget must be inert rather than a panic.
func TestPresenceRequests_OnANilBudgetAreEmpty(t *testing.T) {
	var b *YardScanBudget
	require.Empty(t, b.PresenceRequests(context.Background(), testPlayerID, 16))
	require.False(t, b.AdmitPresence())
}
