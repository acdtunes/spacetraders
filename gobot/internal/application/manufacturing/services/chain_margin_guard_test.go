package services

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// sp-2dv4 pre-spend chain-margin + absorption guard. These tests drive the four
// acceptance scenarios directly against ChainMarginGuard.Evaluate: crushed feed
// import bids (Guard 1 parks on negative margin), healthy margins (proceeds),
// a tiny final sink (Guard 2 caps feed spend below what the sink can absorb),
// and a market-read error during projection (fail closed). Evaluate itself does
// no logging — the coordinator logs proj.ParkMessage() — so the tests assert on
// the returned projection and its message text, which carries every number
// because the container-log renderer drops metadata (sp-iqyq).

const (
	guardSystem   = "X1-GUARD"
	guardProduct  = "SHIP_PARTS"  // fabricated good; confirmed NOT a ship type (regular market path)
	guardFeed     = "ELECTRONICS" // raw BUY input, delivered into the fab's import bid
	guardFabWP    = "X1-GUARD-FAB"
	guardSinkWP   = "X1-GUARD-SINK"
	guardSourceWP = "X1-GUARD-SRC"
)

// guardQuote is one market's price+volume for a good.
type guardQuote struct {
	waypoint string
	price    int
	volume   int
}

// guardMarketRepo is a data-driven fake implementing only the three repo methods
// the MarketLocator + guard touch. It embeds market.MarketRepository so any
// unexpected call panics loudly rather than returning a silent zero value.
type guardMarketRepo struct {
	market.MarketRepository
	sell    map[string]guardQuote            // good -> cheapest EXPORT market (source/fab ask)
	buy     map[string]guardQuote            // good -> best IMPORT market (final sink bid)
	buyErr  map[string]error                 // good -> FindBestMarketBuying error
	imports map[string]map[string]guardQuote // fab waypoint -> input good -> import bid quote
	dataErr map[string]error                 // waypoint -> GetMarketData error
}

func (r *guardMarketRepo) FindCheapestMarketSelling(_ context.Context, good, _ string, _ int) (*market.CheapestMarketResult, error) {
	q, ok := r.sell[good]
	if !ok {
		return nil, nil
	}
	return &market.CheapestMarketResult{WaypointSymbol: q.waypoint, TradeSymbol: good, SellPrice: q.price, Supply: "MODERATE"}, nil
}

func (r *guardMarketRepo) FindBestMarketBuying(_ context.Context, good, _ string, _ int) (*market.BestMarketBuyingResult, error) {
	if err := r.buyErr[good]; err != nil {
		return nil, err
	}
	q, ok := r.buy[good]
	if !ok {
		return nil, nil
	}
	return &market.BestMarketBuyingResult{WaypointSymbol: q.waypoint, TradeSymbol: good, PurchasePrice: q.price, Supply: "HIGH"}, nil
}

func (r *guardMarketRepo) GetMarketData(_ context.Context, wp string, _ int) (*market.Market, error) {
	if err := r.dataErr[wp]; err != nil {
		return nil, err
	}
	bySym := map[string]market.TradeGood{}
	add := func(sym string, purchase, sell, vol int, tt market.TradeType) {
		supply := "MODERATE"
		activity := "STRONG"
		if g, err := market.NewTradeGood(sym, &supply, &activity, purchase, sell, vol, tt); err == nil {
			bySym[sym] = *g
		}
	}
	for good, q := range r.sell {
		if q.waypoint == wp {
			purchase := q.price - 1
			if purchase < 0 {
				purchase = 0
			}
			add(good, purchase, q.price, q.volume, market.TradeTypeExport)
		}
	}
	for good, q := range r.buy {
		if q.waypoint == wp {
			add(good, q.price, q.price+1, q.volume, market.TradeTypeImport)
		}
	}
	for good, q := range r.imports[wp] {
		add(good, q.price, q.price+1, q.volume, market.TradeTypeImport)
	}
	goodsList := make([]market.TradeGood, 0, len(bySym))
	for _, g := range bySym {
		goodsList = append(goodsList, g)
	}
	return market.NewMarket(wp, goodsList, time.Now())
}

// FindAllMarketsInSystem returns every waypoint this fake knows a market for, so the
// trade-type-aware FindExportMarket (sp-9mkf) can iterate them. The GetMarketData
// above is consistent with the sell/buy maps, so iterating yields the same source the
// old FindCheapestMarketSelling path did.
func (r *guardMarketRepo) FindAllMarketsInSystem(_ context.Context, _ string, _ int) ([]string, error) {
	seen := map[string]bool{}
	var wps []string
	add := func(wp string) {
		if wp != "" && !seen[wp] {
			seen[wp] = true
			wps = append(wps, wp)
		}
	}
	for _, q := range r.sell {
		add(q.waypoint)
	}
	for _, q := range r.buy {
		add(q.waypoint)
	}
	for wp := range r.imports {
		add(wp)
	}
	return wps, nil
}

// singleFeedChain is the incident's shape: a fabricated product with one raw BUY feed.
func singleFeedChain() *goods.SupplyChainNode {
	root := goods.NewSupplyChainNode(guardProduct, goods.AcquisitionFabricate)
	root.AddChild(goods.NewSupplyChainNode(guardFeed, goods.AcquisitionBuy))
	return root
}

func newGuard(repo *guardMarketRepo) *ChainMarginGuard {
	return NewChainMarginGuard(NewMarketLocator(repo, nil, nil, nil), repo)
}

// Crushed feed import bids (fab pays 1000 for a feed that costs 2000 at source),
// and a tiny recovery sink whose product leg cannot offset the feed loss: the
// whole-chain P&L projects negative, so Guard 1 refuses to spend.
func TestChainMarginGuard_CrushedFeedBids_ParksPreSpend(t *testing.T) {
	repo := &guardMarketRepo{
		sell: map[string]guardQuote{
			guardProduct: {waypoint: guardFabWP, price: 5000, volume: 60}, // fab/harvest ask
			guardFeed:    {waypoint: guardSourceWP, price: 2000, volume: 60},
		},
		buy: map[string]guardQuote{
			guardProduct: {waypoint: guardSinkWP, price: 8000, volume: 6}, // vol-6 recovery sink
		},
		imports: map[string]map[string]guardQuote{
			guardFabWP: {guardFeed: {price: 1000, volume: 60}}, // crushed import bid, below source ask
		},
	}

	proj := newGuard(repo).Evaluate(context.Background(), singleFeedChain(), guardSystem, 1)

	if proj.Proceed {
		t.Fatalf("crushed feed bids must PARK pre-spend, got proceed=true: %+v", proj)
	}
	if proj.Reason != chainGuardNegativeMargin {
		t.Fatalf("expected reason %q, got %q (%+v)", chainGuardNegativeMargin, proj.Reason, proj)
	}
	if proj.ProjectedPL >= 0 {
		t.Fatalf("expected a negative projected chain P&L, got %d", proj.ProjectedPL)
	}
	msg := proj.ParkMessage()
	if !strings.Contains(msg, guardProduct) || !strings.Contains(msg, fmt.Sprintf("%d", proj.ProjectedPL)) {
		t.Fatalf("park message must name the good and the projected P&L, got: %s", msg)
	}
}

// Healthy margins on both legs and an ample sink: the chain projects a clear
// profit within the absorption cap, so the factory proceeds (no false-positive).
func TestChainMarginGuard_HealthyMargins_Proceeds(t *testing.T) {
	repo := &guardMarketRepo{
		sell: map[string]guardQuote{
			guardProduct: {waypoint: guardFabWP, price: 5000, volume: 60},
			guardFeed:    {waypoint: guardSourceWP, price: 1000, volume: 60},
		},
		buy: map[string]guardQuote{
			guardProduct: {waypoint: guardSinkWP, price: 8000, volume: 60},
		},
		imports: map[string]map[string]guardQuote{
			guardFabWP: {guardFeed: {price: 3000, volume: 60}}, // healthy import bid, above source ask
		},
	}

	proj := newGuard(repo).Evaluate(context.Background(), singleFeedChain(), guardSystem, 1)

	if !proj.Proceed {
		t.Fatalf("healthy margins must proceed, got park: %s", proj.ParkMessage())
	}
	if proj.Reason != chainGuardProceed {
		t.Fatalf("expected reason %q, got %q", chainGuardProceed, proj.Reason)
	}
	if proj.ProjectedPL <= proj.RequiredPL {
		t.Fatalf("projected P&L %d must exceed required %d for a healthy chain", proj.ProjectedPL, proj.RequiredPL)
	}
	if proj.FeedSpend > proj.AbsorptionCap {
		t.Fatalf("feed spend %d must be within absorption cap %d", proj.FeedSpend, proj.AbsorptionCap)
	}
}

// Per-unit margins are positive (so Guard 1 passes), but the final sink's trade
// volume is only 6: the absorption cap (sink_bid × 6 × tranches) sits below one
// pass's feed spend, so Guard 2 caps the spend and parks.
func TestChainMarginGuard_TinySinkVolume_CapsFeedSpend(t *testing.T) {
	repo := &guardMarketRepo{
		sell: map[string]guardQuote{
			guardProduct: {waypoint: guardFabWP, price: 5000, volume: 60},
			guardFeed:    {waypoint: guardSourceWP, price: 2000, volume: 100},
		},
		buy: map[string]guardQuote{
			guardProduct: {waypoint: guardSinkWP, price: 8000, volume: 6}, // cap = 8000*6*4 = 192,000
		},
		imports: map[string]map[string]guardQuote{
			guardFabWP: {guardFeed: {price: 2100, volume: 100}}, // +100/unit feed margin (Guard 1 passes)
		},
	}

	proj := newGuard(repo).Evaluate(context.Background(), singleFeedChain(), guardSystem, 1)

	if proj.Proceed {
		t.Fatalf("a sink too small to absorb the feed spend must PARK, got proceed=true: %+v", proj)
	}
	if proj.Reason != chainGuardSinkAbsorption {
		t.Fatalf("expected reason %q, got %q (%+v)", chainGuardSinkAbsorption, proj.Reason, proj)
	}
	if proj.ProjectedPL <= proj.RequiredPL {
		t.Fatalf("Guard 1 must PASS here (P&L %d > required %d) so Guard 2 is the one that trips", proj.ProjectedPL, proj.RequiredPL)
	}
	if proj.FeedSpend <= proj.AbsorptionCap {
		t.Fatalf("feed spend %d must exceed absorption cap %d to trip Guard 2", proj.FeedSpend, proj.AbsorptionCap)
	}
	if msg := proj.ParkMessage(); !strings.Contains(msg, fmt.Sprintf("%d", proj.AbsorptionCap)) {
		t.Fatalf("absorption park message must state the cap, got: %s", msg)
	}
}

// A market-read error during projection must fail CLOSED (park), never spend
// blind. Exercised at both entry points: the sink lookup and a feed source read.
func TestChainMarginGuard_MarketReadError_FailsClosed(t *testing.T) {
	base := func() *guardMarketRepo {
		return &guardMarketRepo{
			sell: map[string]guardQuote{
				guardProduct: {waypoint: guardFabWP, price: 5000, volume: 60},
				guardFeed:    {waypoint: guardSourceWP, price: 1000, volume: 60},
			},
			buy: map[string]guardQuote{
				guardProduct: {waypoint: guardSinkWP, price: 8000, volume: 60},
			},
			imports: map[string]map[string]guardQuote{
				guardFabWP: {guardFeed: {price: 3000, volume: 60}},
			},
		}
	}

	t.Run("sink unpriceable", func(t *testing.T) {
		repo := base()
		repo.buyErr = map[string]error{guardProduct: fmt.Errorf("db timeout reading sink")}
		proj := newGuard(repo).Evaluate(context.Background(), singleFeedChain(), guardSystem, 1)
		if proj.Proceed {
			t.Fatalf("an unpriceable sink must fail CLOSED, got proceed=true")
		}
		if proj.Reason != chainGuardUnpriceable {
			t.Fatalf("expected reason %q, got %q", chainGuardUnpriceable, proj.Reason)
		}
	})

	t.Run("feed source unpriceable during projection", func(t *testing.T) {
		repo := base()
		repo.dataErr = map[string]error{guardSourceWP: fmt.Errorf("market not scanned")}
		proj := newGuard(repo).Evaluate(context.Background(), singleFeedChain(), guardSystem, 1)
		if proj.Proceed {
			t.Fatalf("an unpriceable feed source must fail CLOSED, got proceed=true")
		}
		if proj.Reason != chainGuardUnpriceable {
			t.Fatalf("expected reason %q, got %q", chainGuardUnpriceable, proj.Reason)
		}
	})
}

// An inputs-only / non-fabrication root has no resale sink to bound and no feed
// bleed to prevent: the guard must let it through untouched (construction supply
// is governed by the pipeline's own economics + the bp6f #3 harvest guard).
func TestChainMarginGuard_NonFabricationRoot_ProceedsUntouched(t *testing.T) {
	repo := &guardMarketRepo{}
	leaf := goods.NewSupplyChainNode(guardFeed, goods.AcquisitionBuy)

	proj := newGuard(repo).Evaluate(context.Background(), leaf, guardSystem, 1)

	if !proj.Proceed {
		t.Fatalf("a non-fabrication root must proceed untouched, got park: %s", proj.ParkMessage())
	}
	if proj.Reason != chainGuardNoFabrication {
		t.Fatalf("expected reason %q, got %q", chainGuardNoFabrication, proj.Reason)
	}
}
