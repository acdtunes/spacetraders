package main

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	domainTrading "github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// LaneDebtStore is the durable full-lane compression store.
// *persistence.LaneCooldownDebtRepositoryGORM satisfies it.
type LaneDebtStore interface {
	Save(ctx context.Context, key domainTrading.LaneKey, debt float64, at time.Time) error
	LoadSince(ctx context.Context, since time.Time) ([]persistence.LaneDebt, error)
}

// armLaneCooldownPersistence reloads the trade engine's FULL-LANE compression debt and arms the
// write-through that keeps it durable, reporting how many lanes it restored.
//
// THIS IS THE HALF THE PURCHASE REPLAY CANNOT DO. That replay rebuilds the ledger's source-drain
// keys from the purchase rows, which record every drain durably. The trade engine keys on the whole
// lane instead, and no transaction row carries a lane: a purchase names the market it bought at, a
// sale names the market it sold at, and nothing joins the pair without guessing. So the full-lane
// debt is persisted as it is accrued rather than reconstructed afterwards — which also makes the
// reload EXACT, where a reconstruction could only ever approximate the trade volume in force at
// the time and would have to bias itself to under-accrue to stay safe.
//
// IT CANNOT FAIL THE BOOT, the same standing rule the purchase replay follows: every outcome here —
// no resolvable player, an unreadable store — logs and returns, leaving the ledger exactly as empty
// as it was before this existed, and the daemon starts.
//
// ORDER MATTERS IN BOTH DIRECTIONS. It must run after the ledger exists and BEFORE any coordinator
// is handed the ledger to accrue into: Restore refuses a key already carrying debt, so a reload
// running after live accrual would silently restore nothing.
func armLaneCooldownPersistence(
	ctx context.Context,
	ledger *domainTrading.LaneCooldownLedger,
	store LaneDebtStore,
	tau time.Duration,
	now time.Time,
) int {
	if ledger == nil || store == nil {
		fmt.Printf("Lane cooldown persistence: no durable store wired - trade lane compression will not survive a restart\n")
		return 0
	}

	restored := 0
	debts, err := store.LoadSince(ctx, now.Add(-cooldownReplayWindow(tau)))
	if err != nil {
		fmt.Printf("Lane cooldown persistence: could not read stored lane debt, starting with no trade lane memory: %v\n", err)
	} else {
		for _, d := range debts {
			if ledger.Restore(d.Key, d.Debt, d.AccruedAt) {
				restored++
			}
		}
	}

	// Armed even when the reload found nothing: persisting is forward-looking, and a first boot
	// with an empty table is exactly the case that most needs the write-through armed.
	//
	// Best-effort by construction. This runs on the trade circuit's sell path, so a store error
	// must never propagate into a completed leg — the debt is still live in memory, and the only
	// thing lost is its survival of the NEXT restart. Its own context, because the ledger's Accrue
	// carries none and a request-scoped one would cancel the write under it.
	ledger.SetDebtPersister(func(key domainTrading.LaneKey, debt float64, at time.Time) {
		if err := store.Save(context.Background(), key, debt, at); err != nil {
			fmt.Printf("Lane cooldown persistence: could not record lane %s->%s %s: %v\n", key.Source, key.Dest, key.Good, err)
		}
	})

	// ALWAYS SAY SO, including the restored-nothing case, because on a deployment whose trade
	// circuits are idle that IS the expected outcome and a silent success is indistinguishable
	// from the code never having run — which is exactly how sp-hxqao shipped inert and was read as
	// working. One line per boot, and it is falsifiable: no line at all means this never executed.
	fmt.Printf("Lane cooldown persistence: armed, restored %d trade lane(s) of compression debt\n", restored)
	return restored
}
