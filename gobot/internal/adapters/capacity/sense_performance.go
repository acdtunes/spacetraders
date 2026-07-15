package capacity

import (
	"context"
	"sort"
	"time"

	domainCapacity "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
)

// sensePerformance measures the per-hub mean accept→fulfill cycle time from
// the ledger's CONTRACT_ACCEPTED / CONTRACT_FULFILLED event pairs (the ledger
// rows carry the wall-clock timestamps; the contracts table does not). A
// contract's cycle is attributed to every hub it delivered to. StallEvents
// stays 0: no persisted stall source exists yet (documented gap — the family
// degrades to "no stalls observed" rather than blocking the tick).
func (s *Sensor) sensePerformance(ctx context.Context, playerID int, contracts []playerContract) domainCapacity.PerformanceSignals {
	accepted, fulfilled, err := s.loadContractCycleEvents(ctx, playerID)
	if err != nil {
		s.note("performance", err)
		return domainCapacity.PerformanceSignals{}
	}

	hubsByContract := make(map[string][]string, len(contracts))
	for _, c := range contracts {
		hubsByContract[c.id] = c.hubs()
	}

	type cycleAgg struct {
		sumSeconds float64
		count      int
	}
	byHub := make(map[string]*cycleAgg)
	for contractID, acceptedAt := range accepted {
		fulfilledAt, done := fulfilled[contractID]
		if !done || !fulfilledAt.After(acceptedAt) {
			continue
		}
		cycleSeconds := fulfilledAt.Sub(acceptedAt).Seconds()
		for _, hub := range hubsByContract[contractID] {
			agg, ok := byHub[hub]
			if !ok {
				agg = &cycleAgg{}
				byHub[hub] = agg
			}
			agg.sumSeconds += cycleSeconds
			agg.count++
		}
	}

	hubs := make([]domainCapacity.HubPerformance, 0, len(byHub))
	for hub, agg := range byHub {
		hubs = append(hubs, domainCapacity.HubPerformance{
			HubSymbol:        hub,
			CycleTimeSeconds: agg.sumSeconds / float64(agg.count),
		})
	}
	sort.Slice(hubs, func(i, j int) bool { return hubs[i].HubSymbol < hubs[j].HubSymbol })
	return domainCapacity.PerformanceSignals{Hubs: hubs}
}

// loadContractCycleEvents reads the accept/fulfill ledger events keyed by
// contract ID: earliest accept, latest fulfill (re-accepts/re-fulfills collapse
// to the widest observed cycle).
func (s *Sensor) loadContractCycleEvents(ctx context.Context, playerID int) (accepted, fulfilled map[string]time.Time, err error) {
	var rows []struct {
		RelatedEntityID string
		TransactionType string
		Timestamp       time.Time
	}
	err = s.db.WithContext(ctx).
		Table("transactions").
		Select("related_entity_id, transaction_type, timestamp").
		Where("player_id = ? AND related_entity_type = ? AND transaction_type IN ?",
			playerID, "contract",
			[]string{ledger.TransactionTypeContractAccepted.String(), ledger.TransactionTypeContractFulfilled.String()}).
		Scan(&rows).Error
	if err != nil {
		return nil, nil, err
	}

	accepted = make(map[string]time.Time)
	fulfilled = make(map[string]time.Time)
	for _, row := range rows {
		if row.RelatedEntityID == "" {
			continue
		}
		switch row.TransactionType {
		case ledger.TransactionTypeContractAccepted.String():
			if current, ok := accepted[row.RelatedEntityID]; !ok || row.Timestamp.Before(current) {
				accepted[row.RelatedEntityID] = row.Timestamp
			}
		case ledger.TransactionTypeContractFulfilled.String():
			if current, ok := fulfilled[row.RelatedEntityID]; !ok || row.Timestamp.After(current) {
				fulfilled[row.RelatedEntityID] = row.Timestamp
			}
		}
	}
	return accepted, fulfilled, nil
}
