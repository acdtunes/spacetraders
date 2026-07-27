package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// container_ops_sensing_rescreen.go is the daemon side of the operator rescreen
// verb (sp-j2efq): the supported response to editing config.yaml's [sensing]
// goods_whitelist mid-era.
//
// WHY THE VERB HAS TO EXIST. Two things in the sensing ledger are stamped with
// the whitelist in force when they were written, and the design is sound only
// while that list never changes:
//
//   - A system's VERDICT. NO_WHITELIST is durable — only PENDING systems are ever
//     re-screened — so a system judged worthless under the old list is never
//     reconsidered under the new one.
//   - A slot's whitelist_goods PROJECTION, the intersection of that market's
//     goods with the list at slot time.
//
// An edit therefore breaks the model in whichever direction it went. Widening
// leaves systems the new list would accept written off, and nothing re-opens them
// on its own. Rotation
// re-verdicts systems NO_WHITELIST while their PARKED hulls, already paid for,
// sit there. Before this verb the only fix was editing the database by hand.
//
// THIS VERB RE-OPENS THE VERDICTS ONLY, and the omission is deliberate. The
// projection half cannot be done from here: recordSlots skips any waypoint that
// already holds a slot, so a re-screen never rewrites an existing projection (a
// clear would be PERMANENT), and screenMarkets' cache branch keys on the slot
// EXISTING rather than on its projection being populated, so a cleared projection
// reads as an authoritative "nothing wanted here" and suppresses the very refetch
// that would repopulate it — while also stopping the scan rotation observing
// spread there at all (Scanner.observe). Empty-means-authoritative is a REVIEWED
// decision, pinned by TestScreenSystemTreatsEmptyProjectionAsAuthoritative, so
// re-opening the never-scanned-market case is a design change rather than a fix:
// tracked as sp-ysg8h. DO NOT "fix" it by widening this write.
//
// What this DOES fix is the case that matters most. For any market a probe has
// actually scanned, market_data answers GoodsAt with the FULL goods list, so the
// projection is never consulted and the re-screen re-matches against the current
// whitelist correctly — that is every parked market, exactly where hulls stand.
//
// WHAT IT IS NOT ALLOWED TO TOUCH is the half that carries the risk. A rescreen
// re-evaluates JUDGEMENT, never OWNERSHIP: the hulls are bought and standing. The
// write below is column-scoped (sp-wgjb7) and confined to sensing_systems, so
// state, assigned_ship, slot_kind, the scan stamps and the seed fields cannot be
// reached from here — blanking a hull would drop it out of CountOwnedProbes and
// authorise buying a replacement for a probe already on station (RULINGS #4).

// SensingRescreenResult reports what one rescreen invalidated.
type SensingRescreenResult struct {
	// SystemsReopened counts verdicts returned to PENDING for the steady-state
	// sweep to re-screen under the current whitelist.
	SystemsReopened int64
}

// RescreenSensing returns every one of the player's system verdicts to PENDING so
// the steady-state sweep re-judges them under the CURRENT whitelist. It is
// idempotent and safe to run at any time — the cost of a needless run is one
// re-screen sweep, which the existing five-systems-per-tick batch already bounds.
//
// The write is a single column-scoped UPDATE on sensing_systems. It cannot reach
// sensing_slots at all, which is what makes running it against a fleet of parked
// probes safe: the probe cap counts slot rows, and a verb that disturbed one would
// make the fleet read smaller than it is and authorise buying a replacement for a
// hull already on station (RULINGS #4).
func (s *DaemonServer) RescreenSensing(ctx context.Context, playerID int) (*SensingRescreenResult, error) {
	repo := persistence.NewSensingLedgerRepository(s.db)

	systems, err := repo.ResetVerdictsToPending(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("sensing rescreen made no change: %w", err)
	}
	return &SensingRescreenResult{SystemsReopened: systems}, nil
}
