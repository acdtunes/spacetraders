package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

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

	buckets, deliveriesByContract := bucketContractsByEra(rows, playerToEra, good)

	eraIDs := sortedMapKeys(buckets)
	out := make([]ContractsEraStat, 0, len(eraIDs))
	for _, e := range eraIDs {
		out = append(out, contractsEraStat(e, names[e], buckets[e], deliveriesByContract))
	}
	return out, nil
}

func bucketContractsByEra(
	rows []ContractModel,
	playerToEra map[int]int,
	good *string,
) (map[int][]ContractModel, map[string][]contractDelivery) {
	buckets := map[int][]ContractModel{}
	deliveriesByContract := map[string][]contractDelivery{}
	for _, row := range rows {
		var deliveries []contractDelivery
		_ = json.Unmarshal([]byte(row.DeliveriesJSON), &deliveries)
		if good != nil && !deliversGood(deliveries, *good) {
			continue
		}
		deliveriesByContract[row.ID] = deliveries
		era := playerToEra[row.PlayerID]
		buckets[era] = append(buckets[era], row)
	}
	return buckets, deliveriesByContract
}

func deliversGood(deliveries []contractDelivery, good string) bool {
	for _, d := range deliveries {
		if d.TradeSymbol == good {
			return true
		}
	}
	return false
}

func contractsEraStat(
	eraID int,
	eraName string,
	bucket []ContractModel,
	deliveriesByContract map[string][]contractDelivery,
) ContractsEraStat {
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
		if slack, ok := acceptSlackHours(row); ok {
			slackHours = append(slackHours, slack)
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
	return ContractsEraStat{
		EraID:                  eraID,
		EraName:                eraName,
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
}

// ok is false when either timestamp is unparseable, so the contract skews no average.
func acceptSlackHours(row ContractModel) (float64, bool) {
	acceptBy, err1 := time.Parse(time.RFC3339, row.DeadlineToAccept)
	deadline, err2 := time.Parse(time.RFC3339, row.Deadline)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return deadline.Sub(acceptBy).Hours(), true
}

// ContractGoodDemand aggregates per-good contract demand across the eras selected
// by eraID (nil = all eras), optionally scoped to deliveries whose destination is in
// deliverySystem (nil = all systems). It is the units-aware companion to
// ContractsStats: the demand miner home-scopes it and joins the
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

	byGood := map[string]*demandAgg{}
	for _, row := range rows {
		accumulateContractDemand(byGood, row, deliverySystem)
	}

	goods := sortedMapKeys(byGood)
	out := make([]ContractGoodDemand, 0, len(goods))
	for _, g := range goods {
		out = append(out, byGood[g].toDemandRow(g))
	}
	return out, nil
}

type demandAgg struct {
	unitsByContract map[string]int // per-contract summed units (len => ContractCount; max => MaxContractUnits)
	unitsRequired   int
	rewardSum       float64 // Σ contract payment attributed to the good; ÷ unitsRequired => RewardPerUnit
	firstSeen       time.Time
	lastSeen        time.Time
}

func accumulateContractDemand(byGood map[string]*demandAgg, row ContractModel, deliverySystem *string) {
	var deliveries []contractDelivery
	_ = json.Unmarshal([]byte(row.DeliveriesJSON), &deliveries)

	observed := time.Time{}
	tsOK := false
	if t, perr := time.Parse(time.RFC3339, row.LastUpdated); perr == nil {
		observed, tsOK = t, true
	}

	// Each good is credited its unit-proportional share of the contract's whole reward — full
	// payment for a single-good contract, split by units for a multi-good one.
	payment := float64(row.PaymentOnAccepted + row.PaymentOnFulfilled)
	contractScopedUnits := scopedDeliveryUnits(deliveries, deliverySystem)

	for _, d := range deliveries {
		if !inDeliveryScope(d, deliverySystem) {
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
			a.widenWindow(observed)
		}
	}
}

func (a *demandAgg) widenWindow(observed time.Time) {
	if a.firstSeen.IsZero() || observed.Before(a.firstSeen) {
		a.firstSeen = observed
	}
	if observed.After(a.lastSeen) {
		a.lastSeen = observed
	}
}

func (a *demandAgg) toDemandRow(good string) ContractGoodDemand {
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
	return ContractGoodDemand{
		Good:             good,
		ContractCount:    len(a.unitsByContract),
		UnitsRequired:    a.unitsRequired,
		MaxContractUnits: maxUnits,
		RewardPerUnit:    rewardPerUnit,
		FirstSeen:        a.firstSeen,
		LastSeen:         a.lastSeen,
	}
}

func inDeliveryScope(d contractDelivery, deliverySystem *string) bool {
	return deliverySystem == nil || shared.ExtractSystemSymbol(d.DestinationSymbol) == *deliverySystem
}

func scopedDeliveryUnits(deliveries []contractDelivery, deliverySystem *string) int {
	units := 0
	for _, d := range deliveries {
		if inDeliveryScope(d, deliverySystem) {
			units += d.UnitsRequired
		}
	}
	return units
}

// ContractGoodCountsForDeliveryWaypoint returns, per good, how many DISTINCT contracts delivered
// that good specifically to deliveryWaypoint — an EXACT DestinationSymbol match, NOT the whole
// system. It is the hub-contract-membership signal: the demand miner scopes demand to
// the destination SYSTEM (a good contracted to ANY waypoint in the system becomes a candidate), so
// the buffer selector needs this finer per-HUB membership to exclude a good that is contracted
// elsewhere in the system but never to this hub. A good absent from the
// result is not contracted TO this hub. It reuses ContractGoodDemand's proven load-and-unmarshal
// path; eraID scopes the same way (nil = all eras), so the caller passes the current era to confine
// membership to the current universe (a system/waypoint symbol is reused across weekly resets).
func (r *HistoryRepository) ContractGoodCountsForDeliveryWaypoint(ctx context.Context, eraID *int, deliveryWaypoint string) (map[string]int, error) {
	counts := map[string]int{}
	if deliveryWaypoint == "" {
		return counts, nil
	}
	playerIDs, _, err := r.eraPlayerIDs(ctx, eraID)
	if err != nil {
		return nil, err
	}
	if len(playerIDs) == 0 {
		return counts, nil
	}

	var rows []ContractModel
	if err := r.db.WithContext(ctx).Where("player_id IN ?", playerIDs).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to query contracts: %w", err)
	}

	for _, row := range rows {
		var deliveries []contractDelivery
		_ = json.Unmarshal([]byte(row.DeliveriesJSON), &deliveries)
		// A good is counted ONCE per contract even if a contract lists it in several deliveries to
		// the hub — matching ContractGoodDemand's per-contract dedup.
		seen := map[string]bool{}
		for _, d := range deliveries {
			if d.DestinationSymbol != deliveryWaypoint {
				continue
			}
			good := strings.ToUpper(strings.TrimSpace(d.TradeSymbol))
			if good == "" || seen[good] {
				continue
			}
			seen[good] = true
			counts[good]++
		}
	}
	return counts, nil
}
