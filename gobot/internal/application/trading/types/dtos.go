package types

// OpportunityDTO is a data transfer object for arbitrage opportunities
type OpportunityDTO struct {
	Good            string  `json:"good"`
	BuyMarket       string  `json:"buy_market"`
	SellMarket      string  `json:"sell_market"`
	BuyPrice        int     `json:"buy_price"`
	SellPrice       int     `json:"sell_price"`
	ProfitPerUnit   int     `json:"profit_per_unit"`
	ProfitMargin    float64 `json:"profit_margin"`
	EstimatedProfit int     `json:"estimated_profit"`
	Distance        float64 `json:"distance"`
	BuySupply       string  `json:"buy_supply"`
	SellActivity    string  `json:"sell_activity"`
	Score           float64 `json:"score"`
}
