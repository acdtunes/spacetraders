package api

import (
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// symbolRef is the API's bare {"symbol": "..."} array element.
type symbolRef struct {
	Symbol string `json:"symbol"`
}

type tradeGoodDTO struct {
	Symbol        string `json:"symbol"`
	Supply        string `json:"supply"`
	Activity      string `json:"activity"`
	SellPrice     int    `json:"sellPrice"`
	PurchasePrice int    `json:"purchasePrice"`
	TradeVolume   int    `json:"tradeVolume"`
}

type marketDTO struct {
	Symbol     string         `json:"symbol"`
	Exports    []symbolRef    `json:"exports"`
	Imports    []symbolRef    `json:"imports"`
	Exchange   []symbolRef    `json:"exchange"`
	TradeGoods []tradeGoodDTO `json:"tradeGoods"`
}

func symbolSet(refs []symbolRef) map[string]bool {
	set := make(map[string]bool, len(refs))
	for _, ref := range refs {
		set[ref.Symbol] = true
	}
	return set
}

// catalogue is every good the market handles, deduped in imports → exports → exchange
// order. With no ship at the waypoint tradeGoods is empty but these three are not.
func (m marketDTO) catalogue() []string {
	size := len(m.Imports) + len(m.Exports) + len(m.Exchange)
	seen := make(map[string]bool, size)
	symbols := make([]string, 0, size)
	for _, group := range [][]symbolRef{m.Imports, m.Exports, m.Exchange} {
		for _, ref := range group {
			if ref.Symbol == "" || seen[ref.Symbol] {
				continue
			}
			seen[ref.Symbol] = true
			symbols = append(symbols, ref.Symbol)
		}
	}
	return symbols
}

func (m marketDTO) tradeGoods() []domainPorts.TradeGoodData {
	exports, imports, exchange := symbolSet(m.Exports), symbolSet(m.Imports), symbolSet(m.Exchange)
	goods := make([]domainPorts.TradeGoodData, len(m.TradeGoods))
	for i, good := range m.TradeGoods {
		goods[i] = domainPorts.TradeGoodData{
			Symbol:        good.Symbol,
			Supply:        good.Supply,
			Activity:      good.Activity,
			SellPrice:     good.SellPrice,
			PurchasePrice: good.PurchasePrice,
			TradeVolume:   good.TradeVolume,
			TradeType:     classifyTradeType(good.Symbol, exports, imports, exchange),
		}
	}
	return goods
}

func (m marketDTO) toMarketData() *domainPorts.MarketData {
	return &domainPorts.MarketData{
		Symbol:            m.Symbol,
		TradeGoods:        m.tradeGoods(),
		TradedGoodSymbols: m.catalogue(),
	}
}
