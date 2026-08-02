package persistence

import "time"

type EraOverview struct {
	EraID             int     `json:"era_id"`
	Name              string  `json:"name"`
	AgentSymbol       string  `json:"agent_symbol"`
	Faction           string  `json:"faction,omitempty"`
	UniverseResetDate string  `json:"universe_reset_date,omitempty"`
	RegisteredAt      string  `json:"registered_at,omitempty"`
	ClosedAt          string  `json:"closed_at,omitempty"`
	DurationDays      float64 `json:"duration_days"`
	FinalCredits      int64   `json:"final_credits"`
}

type GoodsEraStat struct {
	EraID               int            `json:"era_id"`
	EraName             string         `json:"era_name"`
	MarketCount         int            `json:"market_count"`
	SampleCount         int            `json:"sample_count"`
	MedianBuyPrice      float64        `json:"median_buy_price"`
	MedianSellPrice     float64        `json:"median_sell_price"`
	SupplyDistribution  map[string]int `json:"supply_distribution"`
	AvgTradeVolume      float64        `json:"avg_trade_volume"`
	SellPriceVolatility float64        `json:"sell_price_volatility"`
}

type ContractsEraStat struct {
	EraID                  int            `json:"era_id"`
	EraName                string         `json:"era_name"`
	TotalCount             int            `json:"total_count"`
	ByType                 map[string]int `json:"by_type"`
	ByFaction              map[string]int `json:"by_faction"`
	ByGood                 map[string]int `json:"by_good"`
	AvgTotalPayout         float64        `json:"avg_total_payout"`
	PayoutVariance         float64        `json:"payout_variance"`
	FulfillmentRate        float64        `json:"fulfillment_rate"`
	AvgAcceptSlackHours    float64        `json:"avg_accept_slack_hours"`
	PayoutPerDeliveredUnit float64        `json:"payout_per_delivered_unit"`
}

// ContractGoodDemand is the units-aware, recurrence-windowed demand for a single
// good aggregated across an era's contracts, optionally scoped to deliveries bound
// for one system (home pre-positioning). Unlike ContractsEraStat.ByGood —
// a per-era frequency count — this carries the total UNITS the contracts required
// (the quantity signal the economics guard needs) plus the observation window that
// makes "recurrence" measurable rather than a raw count.
type ContractGoodDemand struct {
	Good             string `json:"good"`
	ContractCount    int    `json:"contract_count"`     // distinct contracts requiring the good
	UnitsRequired    int    `json:"units_required"`     // summed UnitsRequired across matching deliveries
	MaxContractUnits int    `json:"max_contract_units"` // largest SINGLE-contract units (the s_G the warehouse buffers fully)
	// RewardPerUnit is the per-unit CONTRACT REWARD for the good, scoped to the delivery
	// system: Σ (contract payment attributed to this good, proportional to its units) ÷ Σ
	// units, across the matching contracts. It is the TRUE value the destination's
	// contracts PAY for the good — the ranking signal a destination-side depot buffer needs,
	// distinct from a market ask (what the good RESELLS for). 0 when no payment is known.
	RewardPerUnit float64   `json:"reward_per_unit"`
	FirstSeen     time.Time `json:"first_seen"` // earliest contributing contract observation
	LastSeen      time.Time `json:"last_seen"`  // latest contributing contract observation
}

type PnLBucket struct {
	Key   string `json:"key"`
	Net   int    `json:"net"`
	Count int    `json:"count"`
}

type PnLDailyPoint struct {
	Date string `json:"date"`
	Net  int    `json:"net"`
}

type PnLReport struct {
	Breakdown []PnLBucket     `json:"breakdown"`
	Daily     []PnLDailyPoint `json:"daily,omitempty"`
	NetTotal  int             `json:"net_total"`
}

type ManufacturingGoodStat struct {
	Good         string  `json:"good"`
	Count        int     `json:"count"`
	SuccessRate  float64 `json:"success_rate"`
	AvgCost      float64 `json:"avg_cost"`
	AvgNetProfit float64 `json:"avg_net_profit"`
}

type EventTypeStat struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

type EventWeeklyPoint struct {
	WeekStart string `json:"week_start"`
	Count     int    `json:"count"`
}

type EventReport struct {
	ByType []EventTypeStat    `json:"by_type"`
	Weekly []EventWeeklyPoint `json:"weekly"`
	Total  int                `json:"total"`
}

type GoodProfit struct {
	Good      string `json:"good"`
	NetProfit int    `json:"net_profit"`
}

type SummaryReport struct {
	EraID                   int                `json:"era_id"`
	EraName                 string             `json:"era_name"`
	DurationDays            float64            `json:"duration_days"`
	FinalCredits            int64              `json:"final_credits"`
	IncomeMixPct            map[string]float64 `json:"income_mix_pct"`
	TopGoodsByTradingProfit []GoodProfit       `json:"top_goods_by_trading_profit"`
	ContractCount           int                `json:"contract_count"`
	ContractFulfillmentRate float64            `json:"contract_fulfillment_rate"`
	ThinGoods               []string           `json:"thin_goods"`
	FuelPriceMin            int                `json:"fuel_price_min"`
	FuelPriceMax            int                `json:"fuel_price_max"`
	EventHighlights         []EventTypeStat    `json:"event_highlights"`
}
