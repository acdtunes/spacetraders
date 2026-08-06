package common

import (
	"sort"
	"testing"
)

func TestHeavyReserve(t *testing.T) {
	base := HeavyReserveInputs{CapabilityOpen: true, HeaviesOwned: 0, HeavyCap: 5, TargetYardPrice: 1_200_000}
	cases := []struct {
		name string
		in   HeavyReserveInputs
		want HeavyReserveTarget
	}{
		{"open with room reserves exactly one heavy", base, 1_200_000},
		{"closed capability reserves nothing", func() HeavyReserveInputs { c := base; c.CapabilityOpen = false; return c }(), 0},
		{"at cap reserves nothing", func() HeavyReserveInputs { c := base; c.HeaviesOwned = 5; return c }(), 0},
		{"over cap reserves nothing", func() HeavyReserveInputs { c := base; c.HeaviesOwned = 9; return c }(), 0},
		{"zero cap is a legitimate hold", func() HeavyReserveInputs { c := base; c.HeavyCap = 0; return c }(), 0},
		{"unpriced yard reserves nothing", func() HeavyReserveInputs { c := base; c.TargetYardPrice = 0; return c }(), 0},
		{"negative price reserves nothing", func() HeavyReserveInputs { c := base; c.TargetYardPrice = -5; return c }(), 0},
		{"room for four still reserves only one", func() HeavyReserveInputs { c := base; c.HeaviesOwned = 1; return c }(), 1_200_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HeavyReserve(tc.in); got != tc.want {
				t.Fatalf("HeavyReserve = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestHeavyReserveLockstep is the LOCKSTEP PIN on the reservation arithmetic —
// the design's single most important invariant. HeavyReserve must have exactly
// ONE definition, and every spender that respects the heavy reservation must call
// it rather than re-deriving the arithmetic. Go cannot forbid a second copy of a
// function, so this pins the properties a divergent copy would violate: any
// second implementation wired in behind this contract fails the suite rather than
// quietly drifting the economics apart (one caller holding a stale price while
// the other spends against the fresh one, so the heavy never accumulates).
func TestHeavyReserveLockstep(t *testing.T) {
	// SINGLE SLOT: the reserve is one heavy's price, never cap−owned multiples,
	// at every level of remaining headroom. This is what creates the interleaving
	// that lets expansion spend between heavy purchases.
	t.Run("always exactly one heavy regardless of headroom", func(t *testing.T) {
		const price = int64(1_200_000)
		for owned := 0; owned < 5; owned++ {
			in := HeavyReserveInputs{CapabilityOpen: true, HeaviesOwned: owned, HeavyCap: 5, TargetYardPrice: price}
			if got := HeavyReserve(in); got != HeavyReserveTarget(price) {
				t.Fatalf("owned=%d (headroom %d): reserve = %d, want exactly one heavy at %d", owned, 5-owned, got, price)
			}
		}
	})

	// The reserve TRACKS the TARGET yard's ask exactly, rather than any stored or
	// remembered figure: the target is recomputed every tick, so a newly-discovered
	// nearer yard immediately re-prices the reservation and hands any difference
	// back to expansion. It is deliberately the target's ask and not the cheapest
	// on the map — the purchase path buys NEAREST, so reserving the cheapest would
	// under-reserve and the heavy would never clear its guard.
	t.Run("tracks the target yard ask exactly", func(t *testing.T) {
		for _, price := range []int64{1, 23_500, 900_000, 1_200_000, 4_000_000} {
			in := HeavyReserveInputs{CapabilityOpen: true, HeaviesOwned: 0, HeavyCap: 5, TargetYardPrice: price}
			if got := HeavyReserve(in); got != HeavyReserveTarget(price) {
				t.Fatalf("price %d: reserve = %d, want the price itself", price, got)
			}
		}
	})

	// Every "cannot" answer RELEASES treasury (reserves zero) rather than holding
	// it. A reserve that held treasury on a blind input would starve expansion
	// indefinitely; the heavy buy itself stays gated by the autosizer's own
	// fail-closed guard stack, so releasing here weakens no money guard.
	t.Run("every cannot-buy condition releases the treasury", func(t *testing.T) {
		open := HeavyReserveInputs{CapabilityOpen: true, HeaviesOwned: 0, HeavyCap: 5, TargetYardPrice: 1_200_000}
		blocked := map[string]HeavyReserveInputs{
			"capability closed": func() HeavyReserveInputs { c := open; c.CapabilityOpen = false; return c }(),
			"at cap":            func() HeavyReserveInputs { c := open; c.HeaviesOwned = 5; return c }(),
			"over cap":          func() HeavyReserveInputs { c := open; c.HeaviesOwned = 500; return c }(),
			"zero cap":          func() HeavyReserveInputs { c := open; c.HeavyCap = 0; return c }(),
			"negative cap":      func() HeavyReserveInputs { c := open; c.HeavyCap = -3; return c }(),
			"unpriced yard":     func() HeavyReserveInputs { c := open; c.TargetYardPrice = 0; return c }(),
			"negative price":    func() HeavyReserveInputs { c := open; c.TargetYardPrice = -1; return c }(),
		}
		for name, in := range blocked {
			if got := HeavyReserve(in); got != 0 {
				t.Fatalf("%s: reserve = %d, want 0 (treasury released)", name, got)
			}
		}
	})

	// PURE: same inputs, same answer, no clock and no hidden state. A predicate
	// two containers evaluate independently must agree between them.
	t.Run("pure and repeatable", func(t *testing.T) {
		in := HeavyReserveInputs{CapabilityOpen: true, HeaviesOwned: 2, HeavyCap: 5, TargetYardPrice: 987_654}
		first := HeavyReserve(in)
		for i := 0; i < 100; i++ {
			if got := HeavyReserve(in); got != first {
				t.Fatalf("call %d returned %d, first call returned %d — not pure", i, got, first)
			}
		}
	})
}

// ---- HoldAt: the treasury bound (sp-zg71k) ----------------------------------
//
// era5HeavyAsk and era5Treasury are the numbers the defect was found on: a heavy freighter
// asking 1,510,645 against a treasury of about 372,000. Withholding the ask verbatim put the
// probe-buy floor at 1,560,645 — above the balance — so probe buying stood down completely and
// every non-heavy autosizer class went with it. They are named rather than inlined because every
// test below is an assertion about THAT fleet, not about a round number.
const (
	era5HeavyAsk = int64(1_510_645)
	era5Treasury = int64(372_000)
)

// TestHeavyReserveHoldAt pins the bound's arithmetic at every rung.
func TestHeavyReserveHoldAt(t *testing.T) {
	const ask = int64(1_000_000)
	entry := ask * HeavyReserveEntrySharePct / 100 // 500_000: half the ask must already be in hand

	cases := []struct {
		name     string
		target   HeavyReserveTarget
		treasury int64
		want     int64
	}{
		{"nothing to save for holds nothing", 0, 10_000_000, 0},
		{"negative target holds nothing", -1, 10_000_000, 0},
		{"treasury at the immutable floor holds nothing", HeavyReserveTarget(ask), ImmutableReserveFloor, 0},
		{"treasury below the immutable floor holds nothing", HeavyReserveTarget(ask), 1_000, 0},
		{"an unreadable treasury reads as zero and holds nothing", HeavyReserveTarget(ask), 0, 0},
		{"one credit short of the entry threshold holds nothing", HeavyReserveTarget(ask), ImmutableReserveFloor + entry, 0},
		{"the first credit past the entry threshold is held", HeavyReserveTarget(ask), ImmutableReserveFloor + entry + 1, 1},
		{"mid-climb holds the surplus above the entry threshold", HeavyReserveTarget(ask), ImmutableReserveFloor + entry + 250_000, 250_000},
		{"one credit short of saturation holds one credit short of the ask", HeavyReserveTarget(ask), ImmutableReserveFloor + entry + ask - 1, ask - 1},
		{"saturated holds exactly one ask", HeavyReserveTarget(ask), ImmutableReserveFloor + entry + ask, ask},
		{"far past saturation still holds exactly one ask", HeavyReserveTarget(ask), 500_000_000, ask},
		{"the era-5 fleet holds NOTHING toward an out-of-reach heavy", HeavyReserveTarget(era5HeavyAsk), era5Treasury, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.target.HoldAt(tc.treasury); got != tc.want {
				t.Fatalf("HoldAt(%d) on target %d = %d, want %d", tc.treasury, tc.target, got, tc.want)
			}
		})
	}
}

// TestHeavyReserveHoldLeavesProbeBuyingAlive is the bead's first acceptance criterion, stated as
// the property rather than as one example: a priced heavy whose ask exceeds plausible reach must
// not zero out probe buying, and the immutable 50k floor must still bind underneath.
//
// The floor arithmetic is reproduced here rather than imported because domain/parkedsensing
// depends on this package and not the other way round; TestProbeBuyFloorNeverBelowImmutable pins
// the real ProbeBuyFloor on the same property from the other side.
func TestHeavyReserveHoldLeavesProbeBuyingAlive(t *testing.T) {
	target := HeavyReserveTarget(era5HeavyAsk)

	hold := target.HoldAt(era5Treasury)
	floor := ImmutableReserveFloor + hold

	if floor >= era5Treasury {
		t.Fatalf("floor %d at treasury %d — the reservation priced the fleet out of buying, which is the sp-zg71k freeze", floor, era5Treasury)
	}
	if spendable := era5Treasury - floor; spendable != era5Treasury-ImmutableReserveFloor {
		t.Fatalf("spendable %d, want the whole %d surplus — an ask this era cannot reach must cost expansion nothing", spendable, era5Treasury-ImmutableReserveFloor)
	}
	// The money guard is untouched: the fleet still may not spend below 50k.
	if floor != ImmutableReserveFloor {
		t.Fatalf("floor %d, want exactly the immutable %d — the guard neither moved nor was waived", floor, ImmutableReserveFloor)
	}
}

// TestHeavyReserveHoldStillAccumulatesAReachableHeavy is the bead's second acceptance criterion
// and the sp-fwk8z no-regression pin: a target the fleet is genuinely in range of is still held in
// FULL, and the hold is treasury-INDEPENDENT from there up — exactly the behaviour sp-fwk8z
// shipped, which is what lets the purchase it exists for actually complete.
func TestHeavyReserveHoldStillAccumulatesAReachableHeavy(t *testing.T) {
	const ask = int64(240_000)
	target := HeavyReserveTarget(ask)
	inRange := ImmutableReserveFloor + ask*HeavyReserveEntrySharePct/100 + ask

	for _, treasury := range []int64{inRange, inRange + 1, inRange * 2, inRange * 10} {
		if got := target.HoldAt(treasury); got != ask {
			t.Fatalf("treasury %d: held %d, want the FULL ask %d — under-reserving a reachable heavy is the sp-fwk8z stall", treasury, got, ask)
		}
	}

	// And the buy it is saving for still clears. The autosizer waives the reserve for the heavy's
	// own class, so the heavy buy is judged on treasury − immutable floor >= price + margin; the
	// reserve stands down entirely the moment the hull lands (HeaviesOwned reaches the cap).
	const margin = int64(25_000)
	if spendableForTheHeavy := inRange - ImmutableReserveFloor; spendableForTheHeavy < ask+margin {
		t.Fatalf("at full reserve the heavy itself could not be afforded (%d < %d+%d) — the reservation would have stalled its own purchase", spendableForTheHeavy, ask, margin)
	}
	landed := HeavyReserve(HeavyReserveInputs{CapabilityOpen: true, HeaviesOwned: 1, HeavyCap: 1, TargetYardPrice: ask})
	if got := landed.HoldAt(inRange); got != 0 {
		t.Fatalf("held %d after the heavy landed, want 0 — the treasury must be released to expansion", got)
	}
}

// naiveStandDownHold is THE TEMPTING ONE-LINER, implemented here so the suite can prove it wrong
// rather than merely warn about it: "stand the reserve down when it would push the floor above
// treasury". It is what a reader of the bug report reaches for first, it satisfies the two
// acceptance criteria above, and it is broken.
//
// It is deliberately NOT wired into anything. If it is ever deleted, delete
// TestHeavyReserveHoldCannotThrash with it — the test's whole claim is comparative.
func naiveStandDownHold(t HeavyReserveTarget, liveTreasury int64) int64 {
	ask := int64(t)
	if ask <= 0 {
		return 0
	}
	if ImmutableReserveFloor+ask > liveTreasury {
		return 0 // it would bind — stand down
	}
	return ask
}

// cliffViolations walks a treasury trajectory and reports every place a hold rule breaks one of
// the two anti-thrash properties:
//
//	SLOPE:     a one-credit change in treasury may move the hold by at most one credit. A rule
//	           that jumps by a whole hull's ask on one credit has an arm/disarm edge, and a fleet
//	           whose treasury wanders across that edge alternates between hoarding everything and
//	           releasing everything.
//	SPENDABLE: treasury − immutable floor − hold must never DECREASE as treasury rises. When it
//	           does, earning a credit costs a spender headroom and spending one buys headroom
//	           back — a positive feedback loop, which is the thrash itself.
func cliffViolations(hold func(HeavyReserveTarget, int64) int64, target HeavyReserveTarget, points []int64) []string {
	var out []string
	for i := 1; i < len(points); i++ {
		lo, hi := points[i-1], points[i]
		step := hi - lo
		dHold := hold(target, hi) - hold(target, lo)
		if dHold > step || -dHold > step {
			out = append(out, "slope: treasury "+itoa(lo)+"→"+itoa(hi)+" (Δ"+itoa(step)+") moved the hold by "+itoa(dHold))
		}
		spendLo := lo - ImmutableReserveFloor - hold(target, lo)
		spendHi := hi - ImmutableReserveFloor - hold(target, hi)
		if spendHi < spendLo {
			out = append(out, "spendable: treasury "+itoa(lo)+"→"+itoa(hi)+" DROPPED spendable "+itoa(spendLo)+"→"+itoa(spendHi))
		}
	}
	return out
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// treasuryTrajectory is a rising treasury path with EVERY candidate rule's corner visited at
// one-credit granularity. A coarse sweep alone would step straight over a cliff and report a
// clean bill of health, so each rule's arm/disarm point is bracketed explicitly — including the
// naive rule's, so the comparison below cannot be accused of sampling to taste.
func treasuryTrajectory(ask int64) []int64 {
	entry := ask * HeavyReserveEntrySharePct / 100
	points := []int64{0, 1, ImmutableReserveFloor - 1, ImmutableReserveFloor, ImmutableReserveFloor + 1}
	for _, corner := range []int64{
		ImmutableReserveFloor + entry,       // ours: the hold opens
		ImmutableReserveFloor + entry + ask, // ours: the hold saturates at one ask
		ImmutableReserveFloor + ask,         // the naive rule's arm/disarm edge
	} {
		points = append(points, corner-1, corner, corner+1)
	}
	// A coarse sweep of the whole range, at a stride that never aligns with a corner. Clamped to
	// at least one credit: a stride of zero on a trivially cheap ask would spin forever.
	top := ImmutableReserveFloor + 3*ask
	stride := top / 37
	if stride < 1 {
		stride = 1
	}
	for treasury := int64(0); treasury <= top; treasury += stride {
		points = append(points, treasury)
	}
	sort.Slice(points, func(i, j int) bool { return points[i] < points[j] })
	return points
}

// TestHeavyReserveHoldCannotThrash is THE test for this bead. The two acceptance criteria can both
// be satisfied by a rule that oscillates; this is the one that cannot.
//
// It runs the SAME rising treasury trajectory through the naive stand-down rule and through
// HoldAt, and asserts the naive rule VIOLATES the anti-thrash properties while HoldAt holds them
// everywhere. Asserting the naive rule fails is not decoration: without it a future edit could
// replace HoldAt with the one-liner and this test would still be green if it only checked one
// side. With it, the test states exactly which alternative was rejected and why.
//
// What the naive rule actually does to a fleet: treasury climbs to 50k + ask, the reserve arms,
// the floor lands exactly on the balance and buying stops; one credit of drift later — a single
// probe, a refuel — it disarms, and the ENTIRE accumulation is released for probe spending. The
// fleet neither accumulates a heavy nor buys probes reliably; it alternates, and the operator sees
// intermittent stalling rather than a clean failure.
func TestHeavyReserveHoldCannotThrash(t *testing.T) {
	target := HeavyReserveTarget(era5HeavyAsk)
	points := treasuryTrajectory(era5HeavyAsk)

	naive := cliffViolations(naiveStandDownHold, target, points)
	if len(naive) == 0 {
		t.Fatalf("the naive stand-down rule showed NO cliff over %d treasury points — the trajectory is not exercising its arm/disarm edge, so this test proves nothing about ours", len(points))
	}

	ours := cliffViolations(HeavyReserveTarget.HoldAt, target, points)
	if len(ours) != 0 {
		t.Fatalf("HoldAt thrashes over a rising treasury — %d violation(s), first: %s", len(ours), ours[0])
	}
}

// TestHeavyReserveHoldNeverOutrunsTheTreasury is the anti-freeze invariant, swept rather than
// sampled: whatever the ask and whatever the balance, the hold never exceeds the surplus above the
// immutable floor. That is what makes "the reservation alone can never push a spend floor above
// the live treasury" a property of the code rather than a claim about the eras we have seen.
func TestHeavyReserveHoldNeverOutrunsTheTreasury(t *testing.T) {
	for _, ask := range []int64{1, 2, 99, 100, 101, 23_500, 819_000, era5HeavyAsk, 4_000_000, 250_000_000} {
		target := HeavyReserveTarget(ask)
		for _, treasury := range treasuryTrajectory(ask) {
			hold := target.HoldAt(treasury)
			surplus := treasury - ImmutableReserveFloor
			if surplus < 0 {
				surplus = 0
			}
			switch {
			case hold < 0:
				t.Fatalf("ask %d treasury %d: held %d — a negative hold is a phantom windfall", ask, treasury, hold)
			case hold > surplus:
				t.Fatalf("ask %d treasury %d: held %d against a surplus of %d — the floor now exceeds the balance, which is the sp-zg71k freeze", ask, treasury, hold, surplus)
			case hold > ask:
				t.Fatalf("ask %d treasury %d: held %d — never more than ONE heavy's ask", ask, treasury, hold)
			}
		}
	}
}

// TestHeavyReserveHoldIsPureAndMonotone pins the structural properties the rest of the design
// leans on:
//
//   - PURE. A function of its two inputs alone — no clock, no stored progress, nothing that can
//     outlive its evidence. RULINGS #2 is satisfied trivially because nothing is remembered, so a
//     restart mid-accumulation re-derives the same hold from the same durable facts.
//   - MONOTONE IN TREASURY. Earning never lowers the hold, so a fleet closing on a heavy is never
//     handed its own savings back by a tick that went well.
//   - 1-LIPSCHITZ IN THE ASK. A re-priced target — a nearer yard discovered, a market moving —
//     moves the hold by at most what the price moved. The reservation tracks a re-price smoothly
//     instead of stepping. It is deliberately NOT monotone in the ask: a target that gets more
//     expensive gets FURTHER out of reach, and holding less toward it is the point of the bound.
func TestHeavyReserveHoldIsPureAndMonotone(t *testing.T) {
	target := HeavyReserveTarget(era5HeavyAsk)
	treasury := ImmutableReserveFloor + era5HeavyAsk

	first := target.HoldAt(treasury)
	for i := 0; i < 100; i++ {
		if got := target.HoldAt(treasury); got != first {
			t.Fatalf("call %d returned %d, first returned %d — not pure", i, got, first)
		}
	}

	points := treasuryTrajectory(era5HeavyAsk)
	for i := 1; i < len(points); i++ {
		if lo, hi := target.HoldAt(points[i-1]), target.HoldAt(points[i]); hi < lo {
			t.Fatalf("treasury %d→%d: hold FELL %d→%d as treasury rose", points[i-1], points[i], lo, hi)
		}
	}

	const treasuryFixed = ImmutableReserveFloor + 900_000
	for ask := int64(1); ask < 2_000_000; ask++ {
		lo := HeavyReserveTarget(ask).HoldAt(treasuryFixed)
		hi := HeavyReserveTarget(ask + 1).HoldAt(treasuryFixed)
		if delta := hi - lo; delta > 1 || delta < -1 {
			t.Fatalf("ask %d→%d at treasury %d: hold jumped %d→%d — a one-credit re-price must not move the reservation by more than a credit", ask, ask+1, treasuryFixed, lo, hi)
		}
	}
}
