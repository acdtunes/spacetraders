package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// marketFixtureServer serves one canned GetMarket body on any path.
func marketFixtureServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func newMarketTestClient(serverURL string) *SpaceTradersClient {
	clock := &shared.MockClock{CurrentTime: time.Unix(0, 0).UTC()}
	return NewSpaceTradersClientWithConfig(serverURL, 1, time.Millisecond, clock)
}

// A market GET made without a ship present at the waypoint comes back with the
// imports/exports/exchange symbol lists populated but tradeGoods EMPTY — prices
// are presence-gated, the goods catalogue is not. The mapped result must still
// carry the catalogue, otherwise a remote screen of an unvisited market reads as
// "trades nothing" and the system is wrongly rejected.
func TestGetMarketMapsGoodsListsWithoutShipPresence(t *testing.T) {
	body := `{"data":{
		"symbol":"X1-AA11-A1",
		"imports":[{"symbol":"FOOD"}],
		"exports":[{"symbol":"IRON"}],
		"exchange":[{"symbol":"FUEL"}],
		"tradeGoods":[]
	}}`
	server := marketFixtureServer(t, body)
	client := newMarketTestClient(server.URL)

	market, err := client.GetMarket(context.Background(), "X1-AA11", "X1-AA11-A1", "token")
	if err != nil {
		t.Fatalf("GetMarket returned error: %v", err)
	}

	want := []string{"FOOD", "IRON", "FUEL"}
	if !reflect.DeepEqual(market.TradedGoodSymbols, want) {
		t.Fatalf("TradedGoodSymbols = %v, want %v", market.TradedGoodSymbols, want)
	}
	if len(market.TradeGoods) != 0 {
		t.Fatalf("expected no priced trade-good rows without presence, got %d", len(market.TradeGoods))
	}
}

// A good listed in more than one catalogue array must appear once, and the order
// is imports, then exports, then exchange — so the symbol list is deterministic
// across calls and safe to compare or persist.
func TestGetMarketDedupesGoodsListsInDeclarationOrder(t *testing.T) {
	body := `{"data":{
		"symbol":"X1-AA11-A1",
		"imports":[{"symbol":"FOOD"},{"symbol":"FUEL"}],
		"exports":[{"symbol":"IRON"},{"symbol":"FOOD"}],
		"exchange":[{"symbol":"FUEL"},{"symbol":"ANTIMATTER"}],
		"tradeGoods":[]
	}}`
	server := marketFixtureServer(t, body)
	client := newMarketTestClient(server.URL)

	market, err := client.GetMarket(context.Background(), "X1-AA11", "X1-AA11-A1", "token")
	if err != nil {
		t.Fatalf("GetMarket returned error: %v", err)
	}

	want := []string{"FOOD", "FUEL", "IRON", "ANTIMATTER"}
	if !reflect.DeepEqual(market.TradedGoodSymbols, want) {
		t.Fatalf("TradedGoodSymbols = %v, want %v", market.TradedGoodSymbols, want)
	}
}

// The catalogue addition is purely additive: when tradeGoods IS present, every
// mapped priced row — including its EXPORT/IMPORT/EXCHANGE classification — must
// be exactly what it was before, since the whole trading stack reads these rows.
func TestGetMarketPricedRowsUnchangedWhenTradeGoodsPresent(t *testing.T) {
	body := `{"data":{
		"symbol":"X1-AA11-A1",
		"imports":[{"symbol":"FOOD"}],
		"exports":[{"symbol":"IRON"}],
		"exchange":[{"symbol":"FUEL"}],
		"tradeGoods":[
			{"symbol":"FOOD","supply":"ABUNDANT","activity":"STRONG","sellPrice":10,"purchasePrice":12,"tradeVolume":100},
			{"symbol":"IRON","supply":"MODERATE","activity":"WEAK","sellPrice":30,"purchasePrice":35,"tradeVolume":20},
			{"symbol":"FUEL","supply":"HIGH","activity":"GROWING","sellPrice":2,"purchasePrice":3,"tradeVolume":500}
		]
	}}`
	server := marketFixtureServer(t, body)
	client := newMarketTestClient(server.URL)

	market, err := client.GetMarket(context.Background(), "X1-AA11", "X1-AA11-A1", "token")
	if err != nil {
		t.Fatalf("GetMarket returned error: %v", err)
	}

	wantRows := []domainPorts.TradeGoodData{
		{Symbol: "FOOD", Supply: "ABUNDANT", Activity: "STRONG", SellPrice: 10, PurchasePrice: 12, TradeVolume: 100, TradeType: "IMPORT"},
		{Symbol: "IRON", Supply: "MODERATE", Activity: "WEAK", SellPrice: 30, PurchasePrice: 35, TradeVolume: 20, TradeType: "EXPORT"},
		{Symbol: "FUEL", Supply: "HIGH", Activity: "GROWING", SellPrice: 2, PurchasePrice: 3, TradeVolume: 500, TradeType: "EXCHANGE"},
	}
	if !reflect.DeepEqual(market.TradeGoods, wantRows) {
		t.Fatalf("mapped trade goods changed:\n got %+v\nwant %+v", market.TradeGoods, wantRows)
	}
	if market.Symbol != "X1-AA11-A1" {
		t.Fatalf("Symbol = %q, want %q", market.Symbol, "X1-AA11-A1")
	}

	// The catalogue is populated alongside the priced rows, not instead of them.
	wantSymbols := []string{"FOOD", "IRON", "FUEL"}
	if !reflect.DeepEqual(market.TradedGoodSymbols, wantSymbols) {
		t.Fatalf("TradedGoodSymbols = %v, want %v", market.TradedGoodSymbols, wantSymbols)
	}
}
