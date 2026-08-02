package parkedsensing

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// MarketGoodsPort reads the local market cache: what a market deals in, how deep
// it is, and what it is currently quoting.
//
// All three reads hit market_data and none of them touches the API. The cache is
// exactly what a parked probe's scans populate, so these reads are how the model
// closes its own loop.
type MarketGoodsPort struct{ db *gorm.DB }

// NewMarketGoodsPort wires the market cache reads.
func NewMarketGoodsPort(db *gorm.DB) *MarketGoodsPort {
	return &MarketGoodsPort{db: db}
}

// marketRow is one persisted (waypoint, good) quote.
type marketRow struct {
	GoodSymbol    string
	PurchasePrice int
	SellPrice     int
	TradeVolume   int
}

// rowsAt reads one waypoint's cached quotes.
func (p *MarketGoodsPort) rowsAt(ctx context.Context, playerID int, waypoint string) ([]marketRow, error) {
	var rows []marketRow
	err := p.db.WithContext(ctx).
		Table("market_data").
		Select("good_symbol, purchase_price, sell_price, trade_volume").
		Where("player_id = ? AND waypoint_symbol = ?", playerID, waypoint).
		Order("good_symbol ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to read the market cache at %q: %w", waypoint, err)
	}
	return rows, nil
}

// GoodsAt returns the goods a market is known to deal in, and whether we know
// anything about it at all.
//
// The bool is the whole point: a market known to trade nothing returns
// (empty, true), which is an ANSWER, while a market never scanned returns
// (nil, false), which is a gap the screen fills remotely. Collapsing the two
// would either spend an API call on every barren market forever, or record a
// never-visited one as barren.
func (p *MarketGoodsPort) GoodsAt(ctx context.Context, playerID int, waypoint string) ([]string, bool, error) {
	rows, err := p.rowsAt(ctx, playerID, waypoint)
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	goods := make([]string, 0, len(rows))
	for _, row := range rows {
		goods = append(goods, row.GoodSymbol)
	}
	return goods, true, nil
}

// DepthRowsAt returns the priced rows behind a market's goods, with the
// side-neutral integer mid-price — matching MarketDepthRows, the codebase's one
// depth convention, so a market sized here and a market sized by the census can
// never disagree.
func (p *MarketGoodsPort) DepthRowsAt(ctx context.Context, playerID int, waypoint string) ([]scouting.MarketDepthRow, error) {
	rows, err := p.rowsAt(ctx, playerID, waypoint)
	if err != nil {
		return nil, err
	}
	out := make([]scouting.MarketDepthRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, scouting.MarketDepthRow{
			System:      shared.ExtractSystemSymbol(waypoint),
			Waypoint:    waypoint,
			Good:        row.GoodSymbol,
			TradeVolume: row.TradeVolume,
			MidPrice:    (row.PurchasePrice + row.SellPrice) / 2,
		})
	}
	return out, nil
}

// MarketPrices returns the two-sided quotes a scan just persisted, for the
// spread weighting.
//
// The column names and the field names come from opposite sides of the trade, so
// the mapping is a RENAME rather than a pass-through:
//
//	Bid ← market_data.sell_price      (what the market PAYS us — its bid)
//	Ask ← market_data.purchase_price  (what the market CHARGES us — its ask)
//
// The persisted columns are named from OUR side of the trade; GoodPrice is named
// from the MARKET's. Wiring them by name inverts every quote, and the failure is
// SILENT rather than loud: RelativeSpread's guard skips each inverted good, every
// market observes a spread of zero, the fleet median collapses to its cold-start
// fallback, and every slot lands on the same prior weight. The rotation keeps
// running and reports no error — it just stops preferring the markets worth
// watching.
//
// This mapping is CORRECT and was correct all along. It is also what DETECTED
// sp-en5h7: the scanner had been persisting the two prices transposed since the
// project's first era, so this port computed ask<bid at nearly every market and
// RelativeSpread skipped the goods. An earlier version of this comment claimed
// the resulting warning was "a symptom" and that this mapping was "the fix" —
// that sent the next reader to the wrong file. The warning was the true signal
// and the bug was in the WRITER (application/ship/market_scanner.go). Do not
// re-explain a warning from here; check what the scanner persisted.
func (p *MarketGoodsPort) MarketPrices(ctx context.Context, playerID int, waypoint string) ([]appSensing.GoodPrice, error) {
	rows, err := p.rowsAt(ctx, playerID, waypoint)
	if err != nil {
		return nil, err
	}
	out := make([]appSensing.GoodPrice, 0, len(rows))
	for _, row := range rows {
		out = append(out, appSensing.GoodPrice{
			Good: row.GoodSymbol,
			Bid:  row.SellPrice,     // renamed, not swapped — see the doc above
			Ask:  row.PurchasePrice, // renamed, not swapped — see the doc above
		})
	}
	return out, nil
}

// marketFetchAPI is the one API verb the remote gap fill needs.
// *api.SpaceTradersClient satisfies it.
type marketFetchAPI interface {
	GetMarket(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.MarketData, error)
}

// scanDebiter charges a market read to the fleet's one market-scan budget.
//
// It has no "may I" counterpart on purpose — see FetchGoods for why this read is
// metered but never deniable. *ship.ScanBudget satisfies it.
type scanDebiter interface {
	Debit(playerID int, waypoint string)
}

// RemoteMarketPort fills a market-cache gap from the API — the screen's only
// genuine spend, and the reason a charted-but-never-visited market can be judged
// without sending a hull to it.
type RemoteMarketPort struct {
	client marketFetchAPI
	tokens playerTokenReader
	budget scanDebiter
}

// NewRemoteMarketPort wires the remote gap fill.
func NewRemoteMarketPort(client marketFetchAPI, tokens playerTokenReader) *RemoteMarketPort {
	return &RemoteMarketPort{client: client, tokens: tokens}
}

// SetScanBudget wires the fleet market-scan budget this port charges its reads to
// (sp-ntgfj). Nil leaves the read unmetered, which is a test-only shape: the
// composition root always wires it.
func (p *RemoteMarketPort) SetScanBudget(b scanDebiter) {
	if b == nil {
		return
	}
	p.budget = b
}

// FetchGoods returns what a market DEALS IN, read remotely.
//
// It reads the goods CATALOGUE, not the priced rows. A market GET made with no
// ship at the waypoint returns the imports/exports/exchange symbol arrays but an
// EMPTY tradeGoods list, so a caller reading prices would see every unvisited
// market as trading nothing — and the screen would record that as a durable
// rejection. The catalogue survives a presence-less GET, which is exactly the
// call being made here.
func (p *RemoteMarketPort) FetchGoods(ctx context.Context, playerID int, system, waypoint string) ([]string, error) {
	token, err := playerToken(ctx, p.tokens, playerID)
	if err != nil {
		return nil, err
	}
	// Charged to the fleet market-scan budget, but never gated by it.
	// There is no store to serve this from — filling the gap is the point — and a
	// declined catalogue read makes the screen record a durable rejection of a
	// market it never managed to look at, which costs more than the request saves.
	// The spend is bounded by the rate of CHARTING rather than by the size of the
	// map, so admitting it does not put the fixed-budget invariant at risk; metering
	// it keeps the budget the honest total, since the allowance it consumes is
	// allowance discretionary scanning then cannot.
	if p.budget != nil {
		p.budget.Debit(playerID, waypoint)
	}

	market, err := p.client.GetMarket(sensingCtx(ctx), system, waypoint, token)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch the market at %q: %w", waypoint, err)
	}
	if market == nil {
		return nil, fmt.Errorf("the market at %q returned no data", waypoint)
	}
	return market.TradedGoodSymbols, nil
}
