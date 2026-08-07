package queries

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// benchSurface builds a pooled market surface of systems × marketsPerSystem waypoints, each
// quoting goods goods. Waypoints alternate exporter/importer roles so a large share of the
// (good, source, dest) pairs are genuinely floor-clearing — the census's expensive case, not its
// easy one.
func benchSurface(b *testing.B, systems, marketsPerSystem, goods int) (*fakeLaneMarketReader, []string) {
	b.Helper()
	surface := &fakeLaneMarketReader{
		systems: map[string][]string{},
		markets: map[string]*market.Market{},
	}
	scanned := make([]string, 0, systems)
	now := time.Now()
	for s := 0; s < systems; s++ {
		system := fmt.Sprintf("X1-S%03d", s)
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
					b.Fatalf("building bench good: %v", err)
				}
				tradeGoods = append(tradeGoods, *tg)
			}
			m, err := market.NewMarket(wp, tradeGoods, now)
			if err != nil {
				b.Fatalf("building bench market: %v", err)
			}
			surface.markets[wp] = m
		}
		surface.systems[system] = waypoints
	}
	return surface, scanned
}

// BenchmarkCountProfitableLanes measures one census over a pooled surface at three fleet spreads.
// The census walks every (good, source, dest) pair across the pooled markets, so its cost is
// quadratic in the POOLED market count — which grows with the number of systems any hull stands
// in, parked sensing probes included.
func BenchmarkCountProfitableLanes(b *testing.B) {
	cases := []struct{ systems, markets, goods int }{
		{9, 4, 10},
		{20, 8, 30},
		{40, 8, 40},
	}
	for _, c := range cases {
		b.Run(fmt.Sprintf("systems%d_markets%d_goods%d", c.systems, c.markets, c.goods), func(b *testing.B) {
			surface, scanned := benchSurface(b, c.systems, c.markets, c.goods)
			reader := NewProfitableLaneReader(surface, reachAllWithin(1))
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, readable, err := reader.countProfitable(ctx, 1, scanned); err != nil || !readable {
					b.Fatalf("census failed: readable=%v err=%v", readable, err)
				}
			}
		})
	}
}
