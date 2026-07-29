package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// The reservation raises the autosizer's OWN spend floor so that treasury being accumulated for the
// next heavy is not spent on something else first. Without this, the continuous small spender wins
// every tick and the heavy never accumulates — the whole reason the reserve exists.

// A light buy that fits comfortably WITHOUT the reserve must be refused WITH it.
func TestGuard_TreasuryFloor_HeavyReserveRaisesTheFloor(t *testing.T) {
	r := passingRequest()
	// treasury 5,000,000 − floor 50,000 = 4,950,000 spendable vs price 437,000 + margin 200,000.
	// A 4,500,000 reserve leaves 450,000 — no longer enough for the 637,000 needed.
	r.HeavyReserve = 4_500_000
	assertBlockedBy(t, r, GuardAffordability)
}

// ...and when the reserve clears (heavy bought, or capability closed), the same buy proceeds.
func TestGuard_TreasuryFloor_ClearedReserveReleasesTheBuy(t *testing.T) {
	r := passingRequest()
	r.HeavyReserve = 0
	d := EvaluateGuards(r)
	if !d.Approved {
		t.Fatalf("with no reserve outstanding the buy must proceed, got blocked by %q; arithmetic: %s", d.BlockedBy, d.Arithmetic())
	}
}

// The floor rises by EXACTLY the reserve — not a rounded or scaled amount. Pinned at the boundary:
// one credit of headroom passes, one credit short blocks.
func TestGuard_TreasuryFloor_HeavyReserveIsExact(t *testing.T) {
	base := passingRequest()
	need := base.Price + base.MarginOverFloor // 637,000
	spendable := base.LiveTreasury - int64(common.ImmutableReserveFloor)

	exactlyEnough := base
	exactlyEnough.HeavyReserve = spendable - need // leaves exactly `need`
	if d := EvaluateGuards(exactlyEnough); !d.Approved {
		t.Fatalf("a reserve leaving EXACTLY price+margin must pass, got %q; arithmetic: %s", d.BlockedBy, d.Arithmetic())
	}

	oneShort := base
	oneShort.HeavyReserve = spendable - need + 1 // one credit too much reserved
	assertBlockedBy(t, oneShort, GuardAffordability)
}

// THE ANTI-CIRCULARITY PIN. The reserve is held for the NEXT heavy; the heavy purchase it is
// saving FOR must not be blocked by it. Applying it to the heavy buy would demand roughly twice the
// hull's price (price + a reserve equal to that same price) — the buyer reserving against itself,
// which spec §4 calls out explicitly as circular.
func TestGuard_TreasuryFloor_HeavyBuyDoesNotReserveAgainstItself(t *testing.T) {
	r := passingHeavyRequest()
	// Isolate the FLOOR guard: the 25%-treasury rule is a separate, independently-tested
	// bound and at these numbers it dominates (a 1,565,500 hull needs ~6,262,000 treasury
	// to clear 25%), which would mask what this test is pinning. 0 = rule not applied.
	r.TreasuryPctPerBuy = 0
	// Treasury covers ONE heavy (1,565,500 + 200,000 margin over the 50,000 floor) but is
	// far short of covering it twice — so if the reserve were charged against this buy,
	// the floor guard would block it.
	r.LiveTreasury = 2_000_000
	r.HeavyReserve = 1_565_500 // the reserve for this very hull
	d := EvaluateGuards(r)
	if !d.Approved {
		t.Fatalf("a heavy buy must not be blocked by its own reserve (circular), got blocked by %q; arithmetic: %s", d.BlockedBy, d.Arithmetic())
	}
}

// The reserve must still be VISIBLE in the heavy's arithmetic line, so an operator reading the
// decision log can tell "not reserving against myself" from "reserve silently dropped".
func TestGuard_TreasuryFloor_HeavyArithmeticNamesTheWaivedReserve(t *testing.T) {
	r := passingHeavyRequest()
	r.HeavyReserve = 1_565_500
	d := EvaluateGuards(r)
	var floorDetail string
	for _, v := range d.Verdicts {
		if v.Guard == GuardAffordability {
			floorDetail = v.Detail
		}
	}
	if !strings.Contains(floorDetail, "own reserve waived") {
		t.Fatalf("the heavy's floor arithmetic must say its reserve was waived, got %q", floorDetail)
	}
}

// A non-heavy class's arithmetic must show the reserve it is actually holding back.
func TestGuard_TreasuryFloor_ArithmeticNamesTheHeldReserve(t *testing.T) {
	r := passingRequest()
	r.HeavyReserve = 1_000_000
	d := EvaluateGuards(r)
	var floorDetail string
	for _, v := range d.Verdicts {
		if v.Guard == GuardAffordability {
			floorDetail = v.Detail
		}
	}
	if !strings.Contains(floorDetail, "1000000") {
		t.Fatalf("the floor arithmetic must name the heavy reserve it held back, got %q", floorDetail)
	}
}

// THE PRODUCTION-PLUMBING PIN. The guard tests above prove guardTreasuryFloor honours a reserve
// handed to it; this proves the coordinator actually HANDS it one.
//
// Without this, deleting either `HeavyReserve: in.heavyReserve` in buildPurchaseRequest or the whole
// common.HeavyReserve block in readTickInputs leaves the entire suite green — because no other test
// drives a NON-heavy class with a heavy yard wired: the heavy drills exercise HullClassHeavy (where
// the waiver zeroes the reserve, so its value cannot change a verdict), armedHandler leaves
// h.heavyYard nil, and the pure-guard tests set PurchaseRequest.HeavyReserve directly, bypassing
// buildPurchaseRequest entirely.
//
// The production regression would be silent and total: the reserve reads 0 for every class, light
// and explorer buying never stands down, treasury never accumulates, and the heavy never lands —
// presenting as "the buyer looks fine, it just never buys".
func TestReconcile_HeavyReserveStandsDownLightBuying(t *testing.T) {
	// Treasury covers the light hull comfortably on its own, but NOT once a heavy is being saved
	// for: 2,000,000 − 50,000 floor − 1,565,500 reserve = 384,500, short of 437,000 + 200,000.
	newHandler := func(owned int) (*RunFleetAutosizerCoordinatorHandler, *recordingPurchaser) {
		h, purchaser, _, _ := armedHandler(lightShortfall())
		h.SetTreasuryReader(&fakeTreasury{credits: 2_000_000, ok: true})
		h.SetHeavyCensusReader(&fakeHeavyCensus{owned: owned})
		h.SetHeavyYardReader(&fakeHeavyYard{price: 1_565_500, found: true})
		return h, purchaser
	}
	cmd := func() *RunFleetAutosizerCoordinatorCommand {
		return &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyCap: intPtr(5)}
	}

	// Reserve OUTSTANDING (0 owned, cap 5, a priced yard known) ⇒ the light buy stands down.
	held, heldPurchaser := newHandler(0)
	if _, err := held.reconcileOnce(context.Background(), cmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(heldPurchaser.orders) != 0 {
		t.Fatalf("an outstanding heavy reserve must stand down light buying, got %d buys — the reservation is not reaching the guard stack", len(heldPurchaser.orders))
	}

	// Reserve CLEARED (5 owned == cap ⇒ HeavyReserve returns 0) ⇒ the very same buy proceeds,
	// proving the treasury itself was never the blocker.
	released, releasedPurchaser := newHandler(5)
	if _, err := released.reconcileOnce(context.Background(), cmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(releasedPurchaser.orders) != 1 {
		t.Fatalf("with the reserve cleared the light buy must proceed, got %d buys", len(releasedPurchaser.orders))
	}
	if releasedPurchaser.orders[0].Class != HullClassLight {
		t.Fatalf("expected a light buy, got class %s", releasedPurchaser.orders[0].Class)
	}
}

// The reserve gauge is emitted EVERY tick, whatever the outcome. A series recorded only when
// something happens cannot answer the question it exists for — "is the fleet saving for a heavy, or
// is the buyer stuck?" — because both states look like an absent sample.
func TestReconcile_HeavyReserveIsRecordedEveryTick(t *testing.T) {
	h, _, metrics, _ := armedHandler(lightShortfall())
	h.SetHeavyCensusReader(&fakeHeavyCensus{owned: 2})
	h.SetHeavyYardReader(&fakeHeavyYard{price: 1_565_500, found: true})
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyCap: intPtr(5)}

	for tick := 1; tick <= 3; tick++ {
		if _, err := h.reconcileOnce(context.Background(), cmd); err != nil {
			t.Fatalf("reconcileOnce error: %v", err)
		}
	}
	if metrics.heavyReserveCalls != 3 {
		t.Fatalf("heavy reserve recorded %d times over 3 ticks, want 3 — a gauge that skips ticks cannot tell saving from stuck", metrics.heavyReserveCalls)
	}
	if metrics.lastReserve != 1_565_500 || metrics.lastOwned != 2 || metrics.lastCap != 5 {
		t.Fatalf("recorded reserve=%d owned=%d cap=%d, want 1565500/2/5", metrics.lastReserve, metrics.lastOwned, metrics.lastCap)
	}
}

// heavyExecutionSlippage separates the QUOTE from the EXECUTED price in the drill below. It must
// stay non-zero: the shared recordingPurchaser echoes ExpectedPrice straight back as BuyResult.Price,
// so at zero slippage res.Price and req.Price are the SAME number and no assertion can tell which
// one the premium bound on. The drill asserts the gap is real before it relies on it.
const heavyExecutionSlippage = 75_000

// slippingPurchaser executes at a price DIFFERENT from the quote it was handed — the yard moved
// between the price read and the buy, which is the only condition under which "premium binds on the
// result, not the quote" is an observable property at all.
type slippingPurchaser struct {
	orders   []BuyOrder
	slippage int64
}

func (f *slippingPurchaser) BuyAndDedicate(_ context.Context, order BuyOrder) (BuyResult, error) {
	f.orders = append(f.orders, order)
	return BuyResult{ShipSymbol: "SHIP-" + order.ShipType, Price: order.ExpectedPrice + f.slippage, Dedicated: true}, nil
}

// The premium is observed on a HEAVY purchase and binds on the EXECUTED price, not the quote: it is
// only an honest measure of presence-lag cost if it reflects what actually left the treasury.
//
// The quote and the execution are deliberately DIFFERENT numbers here. With the shared
// recordingPurchaser (which echoes the quote back) this assertion held for `res.Price` and
// `req.Price` alike — it could not fail, and its comment claimed a property nothing tested.
func TestReconcile_HeavyPremiumBindsOnTheExecutedPriceNotTheQuote(t *testing.T) {
	h, _, metrics, _ := armedForHeavy(heavyProvider(6, 2))
	purchaser := &slippingPurchaser{slippage: heavyExecutionSlippage}
	h.SetPurchaser(purchaser)

	if _, err := h.reconcileOnce(context.Background(), heavyCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(purchaser.orders) != 1 {
		t.Fatalf("expected one heavy buy, got %d", len(purchaser.orders))
	}
	if len(metrics.pricePremiums) != 1 {
		t.Fatalf("expected one price-premium observation for a heavy buy, got %d", len(metrics.pricePremiums))
	}

	quoted := purchaser.orders[0].ExpectedPrice
	executed := quoted + heavyExecutionSlippage
	// The fixture must DISCRIMINATE. If these ever coincide the assertion below passes on the quote
	// and the execution alike, and this test silently stops pinning anything — the exact defect it
	// replaced.
	if executed == quoted {
		t.Fatalf("fixture is not discriminating: quote and execution are both %d, so this test cannot tell them apart", quoted)
	}

	paid, cheapest := metrics.pricePremiums[0][0], metrics.pricePremiums[0][1]
	if paid != executed {
		t.Fatalf("premium observed against %d, want the EXECUTED price %d and NOT the %d quote — the premium must measure what left the treasury", paid, executed, quoted)
	}
	if cheapest != 1_400_000 {
		t.Fatalf("premium basis %d, want the cheapest known ask 1400000", cheapest)
	}
}

// A LIGHT purchase must not emit a heavy premium — the series would otherwise measure the wrong
// hull class and the presence-lag reading would be meaningless.
func TestReconcile_LightPurchaseObservesNoHeavyPremium(t *testing.T) {
	h, purchaser, metrics, _ := armedHandler(lightShortfall())
	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1"}); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(purchaser.orders) != 1 {
		t.Fatalf("expected one light buy, got %d", len(purchaser.orders))
	}
	if len(metrics.pricePremiums) != 0 {
		t.Fatalf("a light buy must not emit a heavy price premium, got %d", len(metrics.pricePremiums))
	}
}

// THE TARGET'S ASK, NOT THE CHEAPEST ON THE MAP (sp-fwk8z).
//
// The purchase path buys at the NEAREST reachable yard. Reserving the cheapest ask anywhere
// under-reserves whenever the two differ: treasury tops out at floor + cheapest, the nearer yard
// asks more, the guard never clears, and the heavy is never bought — the exact stall the
// reservation exists to prevent. The coordinator must hold back what the TARGET asks.
//
// The shared query resolves which yard that is (TestHeavyTarget_PicksTheNearestPricedYardWithNoPresenceAndNotTheCheapest);
// this pins that the coordinator reserves the number it is handed rather than re-deriving one.
func TestReconcile_ReserveIsTheTargetYardAsk(t *testing.T) {
	h, _, metrics, _ := armedHandler()
	h.SetHeavyCensusReader(&fakeHeavyCensus{owned: 0})
	// The target is the NEAREST yard at 2,400,000; a cheaper 1,565,500 yard exists further out
	// and is deliberately not what the shared query returns.
	h.SetHeavyYardReader(&fakeHeavyYard{price: 2_400_000, found: true})

	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyCap: intPtr(5)}); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if metrics.lastReserve != 2_400_000 {
		t.Fatalf("reserve = %d, want the target yard's ask 2400000 — reserving less than we will be asked can never complete a purchase", metrics.lastReserve)
	}
}

// A KNOWN heavy yard whose ask nobody has ever read reserves NOTHING, while still reporting the
// capability as open. This is the live production state: a shipyard prices its hulls only while a
// ship stands there, so a yard discovered without presence is known and unpriceable at once.
//
// Both halves matter. Reserving against a price we cannot see would stall expansion indefinitely
// on a number we invented; and closing the capability instead would hide the yard from the pricing
// errand that is supposed to go read its ask.
func TestReconcile_KnownButUnpricedHeavyYardReservesNothing(t *testing.T) {
	h, _, metrics, _ := armedHandler()
	h.SetHeavyCensusReader(&fakeHeavyCensus{owned: 0})
	h.SetHeavyYardReader(&fakeHeavyYard{found: false, capabilityOpenUnpriced: true})

	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyCap: intPtr(5)}); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if metrics.lastReserve != 0 {
		t.Fatalf("reserve = %d, want 0 — a yard whose ask has never been read is not a number to hold treasury against", metrics.lastReserve)
	}
}

// A BLIND RESERVE INPUT RESERVES NOTHING — the same direction the sensing buy-floor takes, and the
// claim its own test already makes about THIS side.
//
// TestHeavyReservePort_CensusErrorReservesNothingAndWarns cites fleet_autosizer_act.go by name
// ("the reserve is computed only when heaviesOwnedOK, so a census error leaves it at 0 and light
// buying continues") — and nothing here pinned it. No test set either reader to fail, so deleting
// `in.heaviesOwnedOK` or the `err == nil` on the heavy-target read left the entire suite green
// while the coordinator held back a full heavy against a signal it could not read. A reserve held
// on a blind input starves expansion on nothing, which is the failure direction the shared
// predicate's own rationale rules out.
//
// Both fixtures are ADVERSARIAL: 0 owned under a cap of 5 against a 1,565,500 target reserves
// 1,565,500 if the failure is swallowed, so a swallowed failure cannot pass as the required 0.
func TestReconcile_BlindReserveInputsReserveNothing(t *testing.T) {
	cases := map[string]struct {
		census *fakeHeavyCensus
		yard   *fakeHeavyYard
	}{
		"owned-heavy census unreadable": {
			census: &fakeHeavyCensus{owned: 0, err: errors.New("ships table unreadable")},
			yard:   &fakeHeavyYard{price: 1_565_500, found: true},
		},
		"heavy target unreadable": {
			census: &fakeHeavyCensus{owned: 0},
			yard:   &fakeHeavyYard{price: 1_565_500, found: true, err: errors.New("shipyard inventory unreadable")},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, metrics, _ := armedHandler()
			h.SetHeavyCensusReader(tc.census)
			h.SetHeavyYardReader(tc.yard)

			if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyCap: intPtr(5)}); err != nil {
				t.Fatalf("a blind reserve input must not fail the tick: %v", err)
			}
			if metrics.lastReserve != 0 {
				t.Fatalf("reserve = %d, want 0 — treasury must not be held back against an input we could not read", metrics.lastReserve)
			}
		})
	}
}

// SINGLE SLOT AT THE TICK, not just in the predicate.
//
// The predicate's own lockstep test pins that HeavyReserve returns one heavy's ask at every level
// of headroom; this pins that the COORDINATOR publishes that number rather than scaling it. A
// cap−owned reservation would hold back five heavies' worth from the first tick and stop expansion
// until the whole heavy fleet was bought, which is "protecting" heavies by starving expansion
// outright — the asymmetry the design explicitly rejects (§3).
//
// The walk is what makes it falsifiable: with a fixed ask, the published reserve must be a FLAT
// LINE across every level of headroom. A cap−owned reservation starts at 5× and tapers; a
// per-purchase one grows. Only single-slot is flat.
func TestReconcile_ReserveIsOneHeavyAtEveryLevelOfHeadroom(t *testing.T) {
	const ask = int64(1_565_500)
	for owned := 0; owned < 5; owned++ {
		h, _, metrics, _ := armedHandler()
		h.SetHeavyCensusReader(&fakeHeavyCensus{owned: owned})
		h.SetHeavyYardReader(&fakeHeavyYard{price: ask, found: true})

		if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyCap: intPtr(5)}); err != nil {
			t.Fatalf("reconcileOnce error: %v", err)
		}
		if metrics.lastReserve != ask {
			t.Fatalf("owned=%d (headroom %d): reserve = %d, want exactly one heavy at %d — expansion needs a spending window between purchases",
				owned, 5-owned, metrics.lastReserve, ask)
		}
	}
}
