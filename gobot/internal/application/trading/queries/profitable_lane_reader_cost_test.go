package queries

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// pooledSurface builds systems × marketsPerSystem waypoints, each quoting goods goods, alternating
// exporter and importer roles so most (good, source, dest) pairs genuinely clear the floor. Lanes
// grow with the SQUARE of the pooled market count while listings grow linearly, which is the whole
// point of the surface.
func pooledSurface(t *testing.T, systems, marketsPerSystem, goods int) (*fakeLaneMarketReader, []string) {
	t.Helper()
	surface := &fakeLaneMarketReader{
		systems: map[string][]string{},
		markets: map[string]*market.Market{},
	}
	scanned := make([]string, 0, systems)
	now := time.Now()
	for s := 0; s < systems; s++ {
		system := fmt.Sprintf("X1-S%02d", s)
		scanned = append(scanned, system)
		waypoints := make([]string, 0, marketsPerSystem)
		for w := 0; w < marketsPerSystem; w++ {
			wp := fmt.Sprintf("%s-W%02d", system, w)
			waypoints = append(waypoints, wp)
			tradeGoods := make([]market.TradeGood, 0, goods)
			for g := 0; g < goods; g++ {
				symbol := fmt.Sprintf("GOOD_%02d", g)
				var tg *market.TradeGood
				var err error
				if w%2 == 0 {
					tg, err = market.NewTradeGood(symbol, nil, nil, 100, 50, 40, market.TradeTypeExport)
				} else {
					tg, err = market.NewTradeGood(symbol, nil, nil, 3000, 2000, 40, market.TradeTypeImport)
				}
				if err != nil {
					t.Fatalf("building fixture good: %v", err)
				}
				tradeGoods = append(tradeGoods, *tg)
			}
			m, err := market.NewMarket(wp, tradeGoods, now)
			if err != nil {
				t.Fatalf("building fixture market: %v", err)
			}
			surface.markets[wp] = m
		}
		surface.systems[system] = waypoints
	}
	return surface, scanned
}

// allocBytesPerRun is the mean heap allocated by one call to f. Bytes rather than allocation COUNT
// because a slice of every lane is a handful of large allocations, which an allocation count cannot
// see at all.
func allocBytesPerRun(runs int, f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		f()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(runs)
}

// THE CENSUS MUST NOT HOLD ITS LANES. Three coordinator read paths call it every tick, none of them
// cached, over a pooled surface whose market count is every system ANY hull stands in — parked
// sensing probes included. Lanes are QUADRATIC in that market count, so a census that materialises
// its candidate slice and ranks it (only to discard the ordering it paid for) carries a per-tick
// cost that grows with the sensing rotation and has nothing bounding it.
//
// BOTH BOUNDS ARE DERIVED, NOT FITTED. One lane costs at least sizeof(ArbitrageLane) to keep, so a
// census whose whole run allocates less than that per counted lane demonstrably is not keeping
// them; and a census that touches lanes one at a time cannot need an allocation for a meaningful
// fraction of them. Each catches a regression the other misses — a per-lane string key moves the
// count, a materialised slice moves only the bytes.
func TestCountProfitableLanes_DoesNotHoldItsLanes(t *testing.T) {
	const systems, marketsPerSystem, goods = 20, 8, 4
	surface, scanned := pooledSurface(t, systems, marketsPerSystem, goods)
	reader := NewProfitableLaneReader(surface, reachAllWithin(1))
	ctx := context.Background()

	counted, readable, err := reader.countProfitable(ctx, 1, scanned)
	if err != nil || !readable {
		t.Fatalf("census failed: readable=%v err=%v", readable, err)
	}
	// The fixture has to actually be the quadratic shape, or both bounds are vacuous: lanes must
	// dominate the listings the census reads in, not merely exceed them.
	listings := systems * marketsPerSystem * goods
	if counted <= listings*20 {
		t.Fatalf("fixture counted %d lanes over %d listings — too flat to distinguish a per-lane cost", counted, listings)
	}

	census := func() {
		if _, _, err := reader.countProfitable(ctx, 1, scanned); err != nil {
			t.Fatalf("census failed mid-measurement: %v", err)
		}
	}

	perLane := unsafe.Sizeof(trading.ArbitrageLane{})
	if bytes := allocBytesPerRun(3, census); bytes/uint64(counted) >= uint64(perLane) {
		t.Fatalf("census allocated %d bytes for %d counted lanes (%d/lane) — at %d bytes per lane it is holding them",
			bytes, counted, bytes/uint64(counted), perLane)
	}
	if allocs := testing.AllocsPerRun(3, census); allocs > float64(counted)/4 {
		t.Fatalf("census allocated %.0f times for %d counted lanes over %d listings — the cost scales with LANES, not with the surface read in",
			allocs, counted, listings)
	}
}
