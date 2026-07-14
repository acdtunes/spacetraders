package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"gorm.io/gorm"
)

type HistoryRepository struct {
	db *gorm.DB
}

func NewHistoryRepository(db *gorm.DB) *HistoryRepository {
	return &HistoryRepository{db: db}
}

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
// for one system (home pre-positioning, sp-dchv). Unlike ContractsEraStat.ByGood —
// a per-era frequency count — this carries the total UNITS the contracts required
// (the quantity signal the economics guard needs) plus the observation window that
// makes "recurrence" measurable rather than a raw count.
type ContractGoodDemand struct {
	Good             string `json:"good"`
	ContractCount    int    `json:"contract_count"`     // distinct contracts requiring the good
	UnitsRequired    int    `json:"units_required"`     // summed UnitsRequired across matching deliveries
	MaxContractUnits int    `json:"max_contract_units"` // largest SINGLE-contract units (the s_G the warehouse buffers fully, sp-5n7v)
	// RewardPerUnit is the per-unit CONTRACT REWARD for the good, scoped to the delivery
	// system: Σ (contract payment attributed to this good, proportional to its units) ÷ Σ
	// units, across the matching contracts (sp-64se). It is the TRUE value the destination's
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

func (r *HistoryRepository) eraPlayerIDs(ctx context.Context, eraID *int) ([]int, map[int]int, error) {
	var eras []EraModel
	q := r.db.WithContext(ctx)
	if eraID != nil {
		q = q.Where("era_id = ?", *eraID)
	}
	if err := q.Find(&eras).Error; err != nil {
		return nil, nil, fmt.Errorf("failed to load eras: %w", err)
	}
	ids := make([]int, 0, len(eras))
	playerToEra := make(map[int]int, len(eras))
	for _, e := range eras {
		ids = append(ids, e.PlayerID)
		playerToEra[e.PlayerID] = e.EraID
	}
	return ids, playerToEra, nil
}

// CurrentEraID resolves the era a player belongs to — the CURRENT universe when playerID is the
// running agent. It is how a RUNTIME demand mine confines a delivery-system scope to the current
// universe (sp-fo0d): SpaceTraders regenerates the universe on each weekly reset and REUSES
// system symbols, so a system symbol alone does NOT identify a universe — a nil (all-eras) scope
// joined to a system filter aggregates every past universe that reused that symbol. Returns nil
// when the player has no era row (fail-open: the caller then scopes to all eras, the prior
// behavior). Each era row carries a single player_id, so a player maps to exactly one era.
func (r *HistoryRepository) CurrentEraID(ctx context.Context, playerID int) (*int, error) {
	var eras []EraModel
	if err := r.db.WithContext(ctx).Where("player_id = ?", playerID).Limit(1).Find(&eras).Error; err != nil {
		return nil, fmt.Errorf("failed to resolve current era for player %d: %w", playerID, err)
	}
	if len(eras) == 0 {
		return nil, nil
	}
	return &eras[0].EraID, nil
}

func (r *HistoryRepository) eraNames(ctx context.Context) (map[int]string, error) {
	var eras []EraModel
	if err := r.db.WithContext(ctx).Find(&eras).Error; err != nil {
		return nil, fmt.Errorf("failed to load eras: %w", err)
	}
	names := make(map[int]string, len(eras))
	for _, e := range eras {
		names[e.EraID] = e.Name
	}
	return names, nil
}

func (r *HistoryRepository) ListEras(ctx context.Context) ([]EraOverview, error) {
	var eras []EraModel
	if err := r.db.WithContext(ctx).Order("era_id ASC").Find(&eras).Error; err != nil {
		return nil, fmt.Errorf("failed to list eras: %w", err)
	}

	out := make([]EraOverview, 0, len(eras))
	for _, e := range eras {
		o := EraOverview{
			EraID:       e.EraID,
			Name:        e.Name,
			AgentSymbol: e.AgentSymbol,
		}
		if e.Faction != nil {
			o.Faction = *e.Faction
		}
		if e.UniverseResetDate != nil {
			o.UniverseResetDate = e.UniverseResetDate.Format("2006-01-02")
		}
		if e.RegisteredAt != nil {
			o.RegisteredAt = e.RegisteredAt.Format(time.RFC3339)
		}
		if e.ClosedAt != nil {
			o.ClosedAt = e.ClosedAt.Format(time.RFC3339)
		}
		if e.FinalCredits != nil {
			o.FinalCredits = *e.FinalCredits
		}
		if e.RegisteredAt != nil {
			end := time.Now()
			if e.ClosedAt != nil {
				end = *e.ClosedAt
			}
			o.DurationDays = end.Sub(*e.RegisteredAt).Hours() / 24
		}
		out = append(out, o)
	}
	return out, nil
}

func (r *HistoryRepository) GoodsStats(ctx context.Context, good string, eraID *int) ([]GoodsEraStat, error) {
	playerIDs, playerToEra, err := r.eraPlayerIDs(ctx, eraID)
	if err != nil {
		return nil, err
	}
	names, err := r.eraNames(ctx)
	if err != nil {
		return nil, err
	}
	if len(playerIDs) == 0 {
		return nil, nil
	}

	var rows []MarketPriceHistoryModel
	if err := r.db.WithContext(ctx).
		Where("good_symbol = ? AND player_id IN ?", good, playerIDs).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to query market price history: %w", err)
	}

	buckets := map[int][]MarketPriceHistoryModel{}
	for _, row := range rows {
		era := playerToEra[row.PlayerID]
		buckets[era] = append(buckets[era], row)
	}

	eraIDs := make([]int, 0, len(buckets))
	for e := range buckets {
		eraIDs = append(eraIDs, e)
	}
	sort.Ints(eraIDs)

	out := make([]GoodsEraStat, 0, len(eraIDs))
	for _, e := range eraIDs {
		bucket := buckets[e]
		markets := map[string]bool{}
		buys := make([]float64, 0, len(bucket))
		sells := make([]float64, 0, len(bucket))
		supplyDist := map[string]int{}
		totalVolume := 0
		for _, row := range bucket {
			markets[row.WaypointSymbol] = true
			buys = append(buys, float64(row.PurchasePrice))
			sells = append(sells, float64(row.SellPrice))
			if row.Supply != nil {
				supplyDist[*row.Supply]++
			}
			totalVolume += row.TradeVolume
		}
		stat := GoodsEraStat{
			EraID:               e,
			EraName:             names[e],
			MarketCount:         len(markets),
			SampleCount:         len(bucket),
			MedianBuyPrice:      median(buys),
			MedianSellPrice:     median(sells),
			SupplyDistribution:  supplyDist,
			AvgTradeVolume:      avgInt(totalVolume, len(bucket)),
			SellPriceVolatility: stddev(sells),
		}
		out = append(out, stat)
	}
	return out, nil
}

type contractDelivery struct {
	TradeSymbol       string `json:"TradeSymbol"`
	DestinationSymbol string `json:"DestinationSymbol"`
	UnitsRequired     int    `json:"UnitsRequired"`
	UnitsFulfilled    int    `json:"UnitsFulfilled"`
}

func (r *HistoryRepository) ContractsStats(ctx context.Context, eraID *int, good *string) ([]ContractsEraStat, error) {
	playerIDs, playerToEra, err := r.eraPlayerIDs(ctx, eraID)
	if err != nil {
		return nil, err
	}
	names, err := r.eraNames(ctx)
	if err != nil {
		return nil, err
	}
	if len(playerIDs) == 0 {
		return nil, nil
	}

	var rows []ContractModel
	if err := r.db.WithContext(ctx).Where("player_id IN ?", playerIDs).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to query contracts: %w", err)
	}

	buckets := map[int][]ContractModel{}
	deliveriesByContract := map[string][]contractDelivery{}
	for _, row := range rows {
		var deliveries []contractDelivery
		_ = json.Unmarshal([]byte(row.DeliveriesJSON), &deliveries)
		if good != nil {
			matched := false
			for _, d := range deliveries {
				if d.TradeSymbol == *good {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		deliveriesByContract[row.ID] = deliveries
		era := playerToEra[row.PlayerID]
		buckets[era] = append(buckets[era], row)
	}

	eraIDs := make([]int, 0, len(buckets))
	for e := range buckets {
		eraIDs = append(eraIDs, e)
	}
	sort.Ints(eraIDs)

	out := make([]ContractsEraStat, 0, len(eraIDs))
	for _, e := range eraIDs {
		bucket := buckets[e]
		byType := map[string]int{}
		byFaction := map[string]int{}
		byGood := map[string]int{}
		payouts := make([]float64, 0, len(bucket))
		fulfilled := 0
		slackHours := make([]float64, 0, len(bucket))
		totalPayout := 0.0
		totalDeliveredUnits := 0
		for _, row := range bucket {
			byType[row.Type]++
			byFaction[row.FactionSymbol]++
			payout := float64(row.PaymentOnAccepted + row.PaymentOnFulfilled)
			payouts = append(payouts, payout)
			if row.Fulfilled {
				fulfilled++
			}
			acceptBy, err1 := time.Parse(time.RFC3339, row.DeadlineToAccept)
			deadline, err2 := time.Parse(time.RFC3339, row.Deadline)
			if err1 == nil && err2 == nil {
				slackHours = append(slackHours, deadline.Sub(acceptBy).Hours())
			}
			goodsSeen := map[string]bool{}
			for _, d := range deliveriesByContract[row.ID] {
				if !goodsSeen[d.TradeSymbol] {
					byGood[d.TradeSymbol]++
					goodsSeen[d.TradeSymbol] = true
				}
				totalDeliveredUnits += d.UnitsFulfilled
			}
			totalPayout += payout
		}
		stat := ContractsEraStat{
			EraID:                  e,
			EraName:                names[e],
			TotalCount:             len(bucket),
			ByType:                 byType,
			ByFaction:              byFaction,
			ByGood:                 byGood,
			AvgTotalPayout:         mean(payouts),
			PayoutVariance:         variance(payouts),
			FulfillmentRate:        avgInt(fulfilled, len(bucket)),
			AvgAcceptSlackHours:    mean(slackHours),
			PayoutPerDeliveredUnit: divOrZero(totalPayout, totalDeliveredUnits),
		}
		out = append(out, stat)
	}
	return out, nil
}

// ContractGoodDemand aggregates per-good contract demand across the eras selected
// by eraID (nil = all eras), optionally scoped to deliveries whose destination is in
// deliverySystem (nil = all systems). It is the units-aware companion to
// ContractsStats: the demand miner (sp-dchv Lane A) home-scopes it and joins the
// result against market asks to rank pre-positioning candidates.
//
// UNITS AGGREGATION PATH: load-and-aggregate in Go, not SQL JSON extraction. Units
// live inside DeliveriesJSON with no SQL column, and ContractsStats already loads
// every era-scoped contract and unmarshals that JSON in Go; the contract row count is
// bounded (one era's contracts — a few hundred), so a second dialect-specific
// json_extract path (fragile across the sqlite test dialect and the prod dialect)
// would buy nothing. This reuses the identical load-and-unmarshal already proven here.
//
// A good is counted ONCE per contract (matching ByGood's per-contract dedup) but its
// UnitsRequired is summed across every matching delivery. The observation window comes
// from each contract's LastUpdated (RFC3339); a contract whose timestamp does not
// parse still contributes to the count and units but not to the window.
func (r *HistoryRepository) ContractGoodDemand(ctx context.Context, eraID *int, deliverySystem *string) ([]ContractGoodDemand, error) {
	playerIDs, _, err := r.eraPlayerIDs(ctx, eraID)
	if err != nil {
		return nil, err
	}
	if len(playerIDs) == 0 {
		return nil, nil
	}

	var rows []ContractModel
	if err := r.db.WithContext(ctx).Where("player_id IN ?", playerIDs).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to query contracts: %w", err)
	}

	type demandAgg struct {
		unitsByContract map[string]int // per-contract summed units (len => ContractCount; max => MaxContractUnits)
		unitsRequired   int
		rewardSum       float64 // Σ contract payment attributed to the good (sp-64se); ÷ unitsRequired => RewardPerUnit
		firstSeen       time.Time
		lastSeen        time.Time
	}
	byGood := map[string]*demandAgg{}

	for _, row := range rows {
		var deliveries []contractDelivery
		_ = json.Unmarshal([]byte(row.DeliveriesJSON), &deliveries)

		observed := time.Time{}
		tsOK := false
		if t, perr := time.Parse(time.RFC3339, row.LastUpdated); perr == nil {
			observed, tsOK = t, true
		}

		// The contract's whole reward is spread across the units it delivers into scope, so a
		// good's per-unit reward is the payment-per-delivered-unit (sp-64se). Sum the in-scope
		// units first (the attribution denominator), then credit each good its unit-proportional
		// share — full payment for a single-good contract, split by units for a multi-good one.
		payment := float64(row.PaymentOnAccepted + row.PaymentOnFulfilled)
		contractScopedUnits := 0
		for _, d := range deliveries {
			if deliverySystem != nil && shared.ExtractSystemSymbol(d.DestinationSymbol) != *deliverySystem {
				continue
			}
			contractScopedUnits += d.UnitsRequired
		}

		for _, d := range deliveries {
			if deliverySystem != nil && shared.ExtractSystemSymbol(d.DestinationSymbol) != *deliverySystem {
				continue
			}
			a := byGood[d.TradeSymbol]
			if a == nil {
				a = &demandAgg{unitsByContract: map[string]int{}}
				byGood[d.TradeSymbol] = a
			}
			a.unitsByContract[row.ID] += d.UnitsRequired
			a.unitsRequired += d.UnitsRequired
			if contractScopedUnits > 0 {
				a.rewardSum += payment * float64(d.UnitsRequired) / float64(contractScopedUnits)
			}
			if tsOK {
				if a.firstSeen.IsZero() || observed.Before(a.firstSeen) {
					a.firstSeen = observed
				}
				if observed.After(a.lastSeen) {
					a.lastSeen = observed
				}
			}
		}
	}

	goods := make([]string, 0, len(byGood))
	for g := range byGood {
		goods = append(goods, g)
	}
	sort.Strings(goods)

	out := make([]ContractGoodDemand, 0, len(goods))
	for _, g := range goods {
		a := byGood[g]
		maxUnits := 0
		for _, u := range a.unitsByContract {
			if u > maxUnits {
				maxUnits = u
			}
		}
		rewardPerUnit := 0.0
		if a.unitsRequired > 0 {
			rewardPerUnit = a.rewardSum / float64(a.unitsRequired)
		}
		out = append(out, ContractGoodDemand{
			Good:             g,
			ContractCount:    len(a.unitsByContract),
			UnitsRequired:    a.unitsRequired,
			MaxContractUnits: maxUnits,
			RewardPerUnit:    rewardPerUnit,
			FirstSeen:        a.firstSeen,
			LastSeen:         a.lastSeen,
		})
	}
	return out, nil
}

func (r *HistoryRepository) PnL(ctx context.Context, eraID *int, byOperation bool) (*PnLReport, error) {
	playerIDs, _, err := r.eraPlayerIDs(ctx, eraID)
	if err != nil {
		return nil, err
	}
	if len(playerIDs) == 0 {
		return &PnLReport{}, nil
	}

	var rows []TransactionModel
	if err := r.db.WithContext(ctx).Where("player_id IN ?", playerIDs).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to query transactions: %w", err)
	}

	breakdown := map[string]*PnLBucket{}
	daily := map[string]int{}
	total := 0
	for _, row := range rows {
		key := row.Category
		if byOperation {
			key = row.OperationType
			if key == "" {
				key = "UNSPECIFIED"
			}
		}
		b, ok := breakdown[key]
		if !ok {
			b = &PnLBucket{Key: key}
			breakdown[key] = b
		}
		b.Net += row.Amount
		b.Count++
		total += row.Amount

		if eraID != nil {
			day := row.Timestamp.Format("2006-01-02")
			daily[day] += row.Amount
		}
	}

	keys := make([]string, 0, len(breakdown))
	for k := range breakdown {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	report := &PnLReport{NetTotal: total}
	for _, k := range keys {
		report.Breakdown = append(report.Breakdown, *breakdown[k])
	}

	if eraID != nil {
		days := make([]string, 0, len(daily))
		for d := range daily {
			days = append(days, d)
		}
		sort.Strings(days)
		for _, d := range days {
			report.Daily = append(report.Daily, PnLDailyPoint{Date: d, Net: daily[d]})
		}
	}

	return report, nil
}

func (r *HistoryRepository) ManufacturingStats(ctx context.Context, eraID *int, good *string) ([]ManufacturingGoodStat, error) {
	playerIDs, _, err := r.eraPlayerIDs(ctx, eraID)
	if err != nil {
		return nil, err
	}
	if len(playerIDs) == 0 {
		return nil, nil
	}

	q := r.db.WithContext(ctx).Where("player_id IN ?", playerIDs)
	if good != nil {
		q = q.Where("product_good = ?", *good)
	}
	var rows []ManufacturingPipelineModel
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to query manufacturing pipelines: %w", err)
	}

	buckets := map[string][]ManufacturingPipelineModel{}
	for _, row := range rows {
		buckets[row.ProductGood] = append(buckets[row.ProductGood], row)
	}

	goods := make([]string, 0, len(buckets))
	for g := range buckets {
		goods = append(goods, g)
	}
	sort.Strings(goods)

	out := make([]ManufacturingGoodStat, 0, len(goods))
	for _, g := range goods {
		bucket := buckets[g]
		completed := 0
		costs := make([]float64, 0, len(bucket))
		profits := make([]float64, 0, len(bucket))
		for _, row := range bucket {
			if row.Status == "COMPLETED" {
				completed++
			}
			costs = append(costs, float64(row.TotalCost))
			profits = append(profits, float64(row.NetProfit))
		}
		out = append(out, ManufacturingGoodStat{
			Good:         g,
			Count:        len(bucket),
			SuccessRate:  avgInt(completed, len(bucket)),
			AvgCost:      mean(costs),
			AvgNetProfit: mean(profits),
		})
	}
	return out, nil
}

func (r *HistoryRepository) EventStats(ctx context.Context, eraID *int, eventType *string) (*EventReport, error) {
	playerIDs, _, err := r.eraPlayerIDs(ctx, eraID)
	if err != nil {
		return nil, err
	}
	if len(playerIDs) == 0 {
		return &EventReport{}, nil
	}

	q := r.db.WithContext(ctx).Where("player_id IN ?", playerIDs)
	if eventType != nil {
		q = q.Where("type = ?", *eventType)
	}
	var rows []CaptainEventModel
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to query captain events: %w", err)
	}

	byType := map[string]int{}
	weekly := map[string]int{}
	for _, row := range rows {
		byType[row.Type]++
		weekStart := row.CreatedAt.Truncate(7 * 24 * time.Hour)
		weekly[weekStart.Format("2006-01-02")]++
	}

	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)
	report := &EventReport{Total: len(rows)}
	for _, t := range types {
		report.ByType = append(report.ByType, EventTypeStat{Type: t, Count: byType[t]})
	}

	weeks := make([]string, 0, len(weekly))
	for w := range weekly {
		weeks = append(weeks, w)
	}
	sort.Strings(weeks)
	for _, w := range weeks {
		report.Weekly = append(report.Weekly, EventWeeklyPoint{WeekStart: w, Count: weekly[w]})
	}

	return report, nil
}

func (r *HistoryRepository) LatestClosedEraID(ctx context.Context) (*int, error) {
	var era EraModel
	err := r.db.WithContext(ctx).
		Where("closed_at IS NOT NULL").
		Order("era_id DESC").
		First(&era).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find latest closed era: %w", err)
	}
	return &era.EraID, nil
}

func (r *HistoryRepository) Summary(ctx context.Context, eraID *int) (*SummaryReport, error) {
	resolvedEra := eraID
	if resolvedEra == nil {
		latest, err := r.LatestClosedEraID(ctx)
		if err != nil {
			return nil, err
		}
		resolvedEra = latest
	}
	if resolvedEra == nil {
		return &SummaryReport{}, nil
	}

	var era EraModel
	if err := r.db.WithContext(ctx).Where("era_id = ?", *resolvedEra).First(&era).Error; err != nil {
		return nil, fmt.Errorf("failed to load era: %w", err)
	}

	overview, err := r.ListEras(ctx)
	if err != nil {
		return nil, err
	}
	var eraOverview *EraOverview
	for i := range overview {
		if overview[i].EraID == *resolvedEra {
			eraOverview = &overview[i]
			break
		}
	}

	report := &SummaryReport{EraID: *resolvedEra, EraName: era.Name}
	if eraOverview != nil {
		report.DurationDays = eraOverview.DurationDays
		report.FinalCredits = eraOverview.FinalCredits
	}

	pnl, err := r.PnL(ctx, resolvedEra, false)
	if err != nil {
		return nil, err
	}
	report.IncomeMixPct = map[string]float64{}
	incomeTotal := 0
	for _, b := range pnl.Breakdown {
		if b.Net > 0 {
			incomeTotal += b.Net
		}
	}
	for _, b := range pnl.Breakdown {
		if b.Net > 0 && incomeTotal > 0 {
			report.IncomeMixPct[b.Key] = (float64(b.Net) / float64(incomeTotal)) * 100
		}
	}

	var txRows []TransactionModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND category IN ?", era.PlayerID, []string{"TRADING_REVENUE", "TRADING_COSTS"}).
		Find(&txRows).Error; err != nil {
		return nil, fmt.Errorf("failed to query trading transactions: %w", err)
	}
	goodProfit := map[string]int{}
	for _, row := range txRows {
		var meta map[string]interface{}
		if row.Metadata == "" {
			continue
		}
		if err := json.Unmarshal([]byte(row.Metadata), &meta); err != nil {
			continue
		}
		good, ok := meta["good_symbol"].(string)
		if !ok || good == "" {
			continue
		}
		goodProfit[good] += row.Amount
	}
	goods := make([]string, 0, len(goodProfit))
	for g := range goodProfit {
		goods = append(goods, g)
	}
	sort.Slice(goods, func(i, j int) bool {
		return goodProfit[goods[i]] > goodProfit[goods[j]]
	})
	if len(goods) > 5 {
		goods = goods[:5]
	}
	for _, g := range goods {
		report.TopGoodsByTradingProfit = append(report.TopGoodsByTradingProfit, GoodProfit{Good: g, NetProfit: goodProfit[g]})
	}

	contractStats, err := r.ContractsStats(ctx, resolvedEra, nil)
	if err != nil {
		return nil, err
	}
	if len(contractStats) > 0 {
		report.ContractCount = contractStats[0].TotalCount
		report.ContractFulfillmentRate = contractStats[0].FulfillmentRate
	}

	var mphRows []MarketPriceHistoryModel
	if err := r.db.WithContext(ctx).Where("player_id = ?", era.PlayerID).Find(&mphRows).Error; err != nil {
		return nil, fmt.Errorf("failed to query market price history: %w", err)
	}
	goodSamples := map[string][]MarketPriceHistoryModel{}
	for _, row := range mphRows {
		goodSamples[row.GoodSymbol] = append(goodSamples[row.GoodSymbol], row)
	}
	var thin []string
	fuelMin, fuelMax := -1, -1
	for good, samples := range goodSamples {
		scarce := 0
		volume := 0
		for _, s := range samples {
			if s.Supply != nil && (*s.Supply == "SCARCE" || *s.Supply == "LIMITED") {
				scarce++
			}
			volume += s.TradeVolume
			if good == "FUEL" {
				if fuelMin == -1 || s.SellPrice < fuelMin {
					fuelMin = s.SellPrice
				}
				if s.SellPrice > fuelMax {
					fuelMax = s.SellPrice
				}
			}
		}
		if len(samples) > 0 && float64(scarce)/float64(len(samples)) >= 0.5 && avgInt(volume, len(samples)) < 20 {
			thin = append(thin, good)
		}
	}
	sort.Strings(thin)
	report.ThinGoods = thin
	if fuelMin >= 0 {
		report.FuelPriceMin = fuelMin
		report.FuelPriceMax = fuelMax
	}

	events, err := r.EventStats(ctx, resolvedEra, nil)
	if err != nil {
		return nil, err
	}
	sort.Slice(events.ByType, func(i, j int) bool {
		return events.ByType[i].Count > events.ByType[j].Count
	})
	if len(events.ByType) > 5 {
		report.EventHighlights = events.ByType[:5]
	} else {
		report.EventHighlights = events.ByType
	}

	return report, nil
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

func variance(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	m := mean(values)
	sum := 0.0
	for _, v := range values {
		sum += (v - m) * (v - m)
	}
	return sum / float64(len(values))
}

func stddev(values []float64) float64 {
	return math.Sqrt(variance(values))
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

func avgInt(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func divOrZero(numerator float64, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return numerator / float64(denominator)
}
