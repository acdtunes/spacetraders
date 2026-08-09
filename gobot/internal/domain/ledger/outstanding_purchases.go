package ledger

import (
	"context"
	"sort"
)

// outstanding_purchases.go — how many units of each good a hull bought under one operation and
// has not since discharged, reconstructed from the ledger alone. Every fact is already on the
// PURCHASE_CARGO / SELL_CARGO rows, so the count is DERIVED rather than stored a second time
// where it could drift from the trades that made it.

// OutstandingPurchaseReader reports each hull's undischarged obligation under one operation
// type, as hull -> good -> units. Deliberately separate from TransactionRepository, though the
// same GORM type satisfies both — the narrow-port reasoning GateFeeAggregator documents.
type OutstandingPurchaseReader interface {
	OutstandingPurchases(ctx context.Context, playerID int, operationType string) (map[string]map[string]int, error)
}

// OutstandingPurchases replays cargo legs per (hull, good) and reports what each hull still
// owes against purchases made under operationType.
//
// It REPLAYS rather than position-matches (MatchPositions): the question is how many UNITS are
// aboard unaccounted for, not which credits realised against which. A buy under operationType
// accrues, ANY later sale discharges — a tour purchase is routinely liquidated under "manual",
// and scoping the discharge would leave an emptied hold owing forever — and the count floors at
// zero AT EACH STEP, so selling cargo the operation never bought banks no credit against a
// LATER purchase. That is the arithmetic a live in-memory ledger runs, so the reconstruction
// and the running total cannot disagree about the same history.
//
// legs may arrive in any order. Hulls and goods with nothing outstanding are ABSENT, never
// present at zero: a settled obligation must never linger.
func OutstandingPurchases(legs []CargoLeg, operationType string) map[string]map[string]int {
	type key struct{ hull, good string }

	grouped := make(map[key][]CargoLeg)
	for _, leg := range legs {
		if leg.Hull == "" || leg.Good == "" || leg.Units <= 0 {
			continue // unattributable: no stream to place it in, and inventing one fabricates an obligation
		}
		if leg.IsBuy && leg.OperationType != operationType {
			continue // another operation's purchase; its SALES still count below
		}
		k := key{leg.Hull, leg.Good}
		grouped[k] = append(grouped[k], leg)
	}

	outstanding := make(map[string]map[string]int, len(grouped))
	for k, stream := range grouped {
		sort.SliceStable(stream, func(i, j int) bool {
			if !stream[i].At.Equal(stream[j].At) {
				return stream[i].At.Before(stream[j].At)
			}
			return stream[i].TxID < stream[j].TxID
		})

		units := 0
		for _, leg := range stream {
			if leg.IsBuy {
				units += leg.Units
				continue
			}
			units -= leg.Units
			if units < 0 {
				units = 0
			}
		}
		if units <= 0 {
			continue
		}
		if outstanding[k.hull] == nil {
			outstanding[k.hull] = make(map[string]int)
		}
		outstanding[k.hull][k.good] = units
	}
	return outstanding
}
