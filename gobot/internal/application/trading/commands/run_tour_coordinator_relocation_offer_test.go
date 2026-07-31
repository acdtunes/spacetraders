package commands

import (
	"testing"
	"time"
)

// run_tour_coordinator_relocation_offer_test.go — the TOUR half of first refusal (sp-e8d92).
//
// The tour writes the offer at its boundary and waits for it. Two properties dominate everything else
// here, and both are about not making things worse than the problem being solved:
//
//  1. THE OFFER MUST EXPIRE. A hull offered and never taken — relocator down, no ground clears NPV,
//     nothing reads the key — must go back to touring. An unexpiring offer is a STRANDED TRADE HULL,
//     which is strictly worse than a hull that merely never spread.
//  2. AN OFFERED HULL IS NOT TRADING. 40 hulls each paying a window every tour cycle is real revenue,
//     so the offer is gated to hulls whose relocation would plausibly help.

var offerNow = time.Date(2026, 7, 30, 20, 0, 0, 0, time.UTC)

// THE HERD GATE. A hull is offered only when its system already holds other trade hulls.
//
// This is the answer to "offer only when there is plausibly somewhere better to go". A hull ALONE in its
// system is already doing the thing the objective asks for — occupying a distinct system — so stalling
// it buys no spread and costs a window of earning. A hull in a stack of 7 is the population whose
// relocation actually raises "distinct systems occupied", which is the success metric.
//
// It also self-limits: as the fleet spreads, fewer systems hold a stack, so fewer hulls are offered and
// the earning cost falls automatically as the feature succeeds.
func TestRelocationOfferShould_OfferOnlyAHullThatIsSharingItsSystem(t *testing.T) {
	for name, tc := range map[string]struct {
		hullsInSystem int
		want          bool
	}{
		"alone in its system — already spreading, stalling it buys nothing": {1, false},
		"sharing with one other hull":                                       {2, true},
		"one of a seven-hull stack":                                         {7, true},
		"system count unreadable (0) — fail closed, keep touring":           {0, false},
	} {
		t.Run(name, func(t *testing.T) {
			_, got := shouldOfferForRelocation(tc.hullsInSystem, defaultRelocationOfferMinHullsInSystem, offerNow, time.Time{})
			if got != tc.want {
				t.Fatalf("%s: offered=%v, want %v", name, got, tc.want)
			}
		})
	}
}

// THE BACKOFF. A herded hull the relocator cannot move — no ground clears the NPV floor — would
// otherwise be offered at EVERY tour boundary forever, paying a window each time for a relocation that
// never comes. After an offer lapses unclaimed the hull is not re-offered until the backoff passes.
func TestRelocationOfferShould_NotReOfferAHullWhoseLastOfferLapsedUnclaimed(t *testing.T) {
	backoffUntil := offerNow.Add(20 * time.Minute)

	if _, offered := shouldOfferForRelocation(5, defaultRelocationOfferMinHullsInSystem, offerNow, backoffUntil); offered {
		t.Fatal("a hull inside its post-lapse backoff was offered again; a hull the relocator cannot move would pay a window every tour cycle forever")
	}
	// Past the backoff the SAME hull is offered again — it is a timer, not a permanent exclusion, because
	// the ground around it changes as the sensing surge prices new systems.
	if _, offered := shouldOfferForRelocation(5, defaultRelocationOfferMinHullsInSystem, backoffUntil.Add(time.Second), backoffUntil); !offered {
		t.Fatal("the hull was still refused past its backoff; the ground changes, so the refusal must lapse")
	}
}

// THE EXPIRY, which is the constraint that can hurt. An offer in the past does NOT stand, so the tour
// stops waiting and re-plans exactly as it does today.
func TestRelocationOfferShould_StandOnlyUntilItsDeadline(t *testing.T) {
	deadline := offerNow.Add(150 * time.Second)

	if !relocationOfferStands(deadline, offerNow) {
		t.Fatal("a live offer did not stand, so the tour would never wait and the feature is inert")
	}
	if relocationOfferStands(deadline, deadline.Add(time.Millisecond)) {
		t.Fatal("an EXPIRED offer still stands — the hull would be held out of touring forever, which is a stranded trade hull and strictly worse than one that never spread")
	}
	if relocationOfferStands(time.Time{}, offerNow) {
		t.Fatal("an absent offer stands; an unwritten key must degrade to exactly today's behaviour")
	}
}

// The window must be long enough for the relocator to actually SEE the offer: it polls on a tick, and an
// offer shorter than that tick can expire between two observations and never be taken. The documented
// default is sized above the relocator's cadence and below the measured 224s median inter-tour gap, so
// the hold costs less than the planning the hull was already going to do.
func TestRelocationOfferShould_ResolveAWindowLongerThanTheRelocatorsOwnTick(t *testing.T) {
	if got := resolveRelocationOfferWindow(0); got != time.Duration(defaultRelocationOfferWindowSeconds)*time.Second {
		t.Fatalf("0/absent resolved to %s, want the documented default", got)
	}
	relocatorTick := time.Duration(defaultRelocatorTickSeconds) * time.Second
	if window := resolveRelocationOfferWindow(0); window >= relocatorTick {
		return // a window at least as long as the relocator's own cadence: it will be seen
	}
	t.Fatalf("the default offer window (%s) is shorter than the relocator's default tick (%s), so an offer can expire between two observations and never be seen — the feature would be inert by construction",
		resolveRelocationOfferWindow(0), relocatorTick)
}

// EVERY REFUSAL MUST NAME ITSELF. sp-e8d92 shipped offering silently: the success path logged, the
// persist-failure path logged, and a hull refused by the herd gate or the backoff logged NOTHING. So
// "why wasn't this hull offered?" was unanswerable without a debugger — and the first time the feature
// looked under-firing in production, answering it took a database investigation instead of a grep.
//
// That is the same absence-of-signal defect as sp-j1i49, in code written hours after fixing it there.
// The reasons are a SMALL STABLE VOCABULARY so they can be grepped and counted rather than read.
func TestRelocationOfferShould_NameTheReasonItRefusedToOffer(t *testing.T) {
	for name, tc := range map[string]struct {
		hullsInSystem int
		backoffUntil  time.Time
		wantReason    string
		wantOffer     bool
	}{
		"sharing its system, no backoff — offered": {
			hullsInSystem: 3, wantReason: "", wantOffer: true,
		},
		"alone in its system": {
			hullsInSystem: 1, wantReason: offerRefusedAloneInSystem,
		},
		"the fleet snapshot read as zero": {
			hullsInSystem: 0, wantReason: offerRefusedAloneInSystem,
		},
		"inside its post-lapse backoff": {
			hullsInSystem: 5, backoffUntil: offerNow.Add(20 * time.Minute), wantReason: offerRefusedWithinBackoff,
		},
	} {
		t.Run(name, func(t *testing.T) {
			reason, offer := shouldOfferForRelocation(tc.hullsInSystem, defaultRelocationOfferMinHullsInSystem, offerNow, tc.backoffUntil)

			if offer != tc.wantOffer {
				t.Fatalf("%s: offered=%v, want %v", name, offer, tc.wantOffer)
			}
			if reason != tc.wantReason {
				t.Fatalf("%s: reason %q, want %q — a refusal that names nothing is why the first production question needed a database investigation instead of a grep", name, reason, tc.wantReason)
			}
			if tc.wantOffer && reason != "" {
				t.Fatalf("%s: an OFFERED hull carries refusal reason %q", name, reason)
			}
		})
	}
}
