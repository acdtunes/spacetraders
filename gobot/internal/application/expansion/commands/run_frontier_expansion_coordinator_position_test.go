package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- sp-255rz: probe-unpriceable stall breaker (position a buyer hull) ----------------------
//
// When QuoteProbe fails closed ("no idle undedicated ship is stationed at a probe-selling
// shipyard"), the coordinator can never price a probe and fleet growth halts forever. The fix
// wires a ProbeBuyerPositioner that, on that stall, relays an eligible idle undedicated hull to a
// reachable probe-yard so the NEXT tick's live price reads. These tests exercise the coordinator's
// side of the seam through its driving port (ReconcileOnce) with a double at the positioner
// boundary; the positioning MECHANISM (locate a yard + pick a poach-free hull + navigate) is tested
// against the adapter in the expansion adapters package.

// fakePositioner records positioning attempts and returns a scripted outcome. It stands in for the
// real probe-buyer positioner at the coordinator's driven-port boundary (port-to-port test).
type fakePositioner struct {
	dispatched bool
	err        error
	calls      int
}

func (f *fakePositioner) PositionProbeBuyer(_ context.Context, _ shared.PlayerID) (bool, error) {
	f.calls++
	return f.dispatched, f.err
}

// unpriceableStallSetup builds the minimal cycle that REACHES the QuoteProbe call and makes it fail
// closed: one standing post with an unmanned slot (demand), no idle probes (fleet short), a fat
// readable treasury and no cooldown (every prior guard passes), and a purchaser whose QuoteProbe
// errors exactly as the live "probe unpriceable" stall does.
func unpriceableStallSetup(t *testing.T) (*RunFrontierExpansionCoordinatorHandler, *fakePurchaser) {
	t.Helper()
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}}
	fr := &fakeFleetRepo{} // no idle probes → fleet short of the open slot
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quoteErr: errors.New("no idle undedicated ship is stationed at a probe-selling shipyard")}
	h.SetProbePurchaser(buyer)
	return h, buyer
}

// AC (the stall fix): a fleet short of open manning demand whose probe quote fails closed positions a
// buyer hull — the coordinator consults the positioner exactly once and buys nothing THIS cycle
// (pricing resumes next tick once the hull is at the yard). Mutation guard: delete the on-stall
// positioning call and positioner.calls==0 → the test fails. The buy still never fires (RULINGS #4 —
// positioning only makes the price READABLE, it does not bypass the guard).
func TestFrontier_ProbeUnpriceable_PositionsBuyerHullToResumePricing(t *testing.T) {
	h, buyer := unpriceableStallSetup(t)
	positioner := &fakePositioner{dispatched: true}
	h.SetProbeBuyerPositioner(positioner)

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))
	require.Equal(t, 1, buyer.quoteCalls, "the quote is attempted (and fails closed)")
	require.Equal(t, 1, positioner.calls, "the unpriceable stall triggers positioning a buyer hull")
	require.Zero(t, buyer.buyCalls, "no buy this cycle — the price is still unreadable; it resumes next tick")
}

// Nil-safety / byte-identical: with NO positioner wired the unpriceable stall behaves exactly as
// before sp-255rz — the quote fails closed, nothing is bought, and the cycle completes without a
// crash. This is the merge-safety guarantee: an unset positioner is previous behavior.
func TestFrontier_ProbeUnpriceable_NoPositioner_StaysFailClosedByteIdentical(t *testing.T) {
	h, buyer := unpriceableStallSetup(t)
	// No positioner wired.

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))
	require.Equal(t, 1, buyer.quoteCalls, "the quote is still attempted")
	require.Zero(t, buyer.buyCalls, "unpriceable → no buy, exactly as before")
}

// Dry-run observes but never moves a hull: a dry-run cycle that hits the unpriceable stall does NOT
// position a buyer (positioning is a real ship action, suppressed under dry-run like the buy).
func TestFrontier_ProbeUnpriceable_DryRun_DoesNotPositionAHull(t *testing.T) {
	h, buyer := unpriceableStallSetup(t)
	positioner := &fakePositioner{dispatched: true}
	h.SetProbeBuyerPositioner(positioner)

	cmd := testCmd()
	cmd.DryRun = true
	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))
	require.Zero(t, positioner.calls, "dry-run reports the stall but never relays a hull")
	require.Zero(t, buyer.buyCalls, "dry-run buys nothing")
}

// The positioner is a STALL breaker only: when the quote SUCCEEDS and every guard passes, the buy
// proceeds and the positioner is never consulted (no needless hull movement on the happy path).
func TestFrontier_ProbeQuoteSucceeds_PositionerNotConsulted_BuyProceeds(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding}}}
	fr := &fakeFleetRepo{}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 20000, quoteYard: "X1-HOME-SY", buySymbol: "PROBE-NEW", buyPrice: 20000}
	h.SetProbePurchaser(buyer)
	positioner := &fakePositioner{dispatched: true}
	h.SetProbeBuyerPositioner(positioner)

	require.NoError(t, h.ReconcileOnce(context.Background(), testCmd()))
	require.Equal(t, 1, buyer.buyCalls, "the priceable quote buys as today")
	require.Zero(t, positioner.calls, "no stall → the positioner is never consulted")
}
