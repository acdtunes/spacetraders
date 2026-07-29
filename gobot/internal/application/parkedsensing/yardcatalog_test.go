package parkedsensing

// yardcatalog_test.go pins the free shipyard-catalogue sweep — the pass that
// stopped treating "what does this yard sell" as a question only a hull standing
// at the counter could answer.
//
// The live numbers this was written against: 76 waypoints carried a SHIPYARD
// trait, 23 had ever been recorded, 44 had never been read at all — and a manual
// presence-less pass over 61 of them found SHIP_HEAVY_FREIGHTER at three yards
// the fleet was actively hunting one at.

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// --- fakes -------------------------------------------------------------------

// yardWorld is the STORE the sweep re-derives from, not a recording of the sweep.
//
// It models the two facts an adapter computes the outstanding set from — which
// shipyards are charted, and which of them already carry a stored reading — so a
// recorded catalogue genuinely removes its yard from the work list, exactly as
// the SQL difference does. Asserting against a call log instead would prove the
// pass called something; asserting against this proves the blind spot closed.
type yardWorld struct {
	// charted is every known shipyard and the frontier rank the adapter gave it.
	charted []OutstandingYard
	// catalogue is what we have recorded: waypoint → the ship types it sells.
	// A waypoint present here is one the sweep must never read again.
	catalogue map[string][]string
	// sells is the WORLD's truth — what each yard actually sells — revealed only
	// by a read. Absent means the read succeeds and finds an empty counter.
	sells map[string][]string
	// unreadable are the yards the API refuses.
	unreadable map[string]bool
	// listErr fails the enumeration itself.
	listErr error
	// reads records every read ATTEMPT in order, failures included: the per-tick
	// bound is spent on attempts, so the order and the count both matter.
	reads []string
}

func newYardWorld() *yardWorld {
	return &yardWorld{
		catalogue:  map[string][]string{},
		sells:      map[string][]string{},
		unreadable: map[string]bool{},
	}
}

func (w *yardWorld) OutstandingYards(context.Context, int) ([]OutstandingYard, error) {
	if w.listErr != nil {
		return nil, w.listErr
	}
	out := make([]OutstandingYard, 0, len(w.charted))
	for _, yard := range w.charted {
		if _, held := w.catalogue[yard.Waypoint]; held {
			continue
		}
		out = append(out, yard)
	}
	return out, nil
}

func (w *yardWorld) ReadCatalog(_ context.Context, _ int, waypoint string) error {
	w.reads = append(w.reads, waypoint)
	if w.unreadable[waypoint] {
		return errors.New("shipyard read refused")
	}
	// A presence-less read learns WHAT the yard sells. It learns no prices — that
	// needs a hull at the counter — which is why this records types alone.
	w.catalogue[waypoint] = append([]string(nil), w.sells[waypoint]...)
	return nil
}

// yard adds a charted shipyard at the given frontier rank, selling `sells`.
func (w *yardWorld) yard(waypoint string, frontier int, sells ...string) *yardWorld {
	w.charted = append(w.charted, OutstandingYard{
		Waypoint: waypoint,
		System:   waypoint[:len(waypoint)-3],
		Frontier: frontier,
	})
	w.sells[waypoint] = sells
	return w
}

func (w *yardWorld) sweep(t *testing.T) YardCatalogReport {
	t.Helper()
	rep, err := ReadYardCatalogues(context.Background(),
		YardCatalogPorts{Frontier: w, Catalog: w}, testPlayerID)
	if err != nil {
		t.Fatalf("ReadYardCatalogues returned error: %v", err)
	}
	return rep
}

// --- the blind spot ----------------------------------------------------------

// THE HEADLINE, and the whole reason this pass exists: a shipyard with NO HULL
// ANYWHERE — not ours, not at that waypoint, not in that system — has its
// catalogue recorded, and a heavy freighter it sells becomes visible.
//
// This is X1-QR78-FE8C, the second heavy yard in a system the engine had already
// screened. Nothing was ever going to stand there: it sells no probe, so no
// probe placement targeted it, and the engine had no other route to the fact that
// it sells the exact hull the fleet was hunting.
func TestReadYardCatalogues_ReadsAYardWithNoHullPresentAnywhere(t *testing.T) {
	world := newYardWorld().yard("X1-QR78-FE8C", 1, "SHIP_HEAVY_FREIGHTER", "SHIP_EXPLORER")

	rep := world.sweep(t)

	if got := world.catalogue["X1-QR78-FE8C"]; len(got) == 0 {
		t.Fatal("the yard's catalogue was never recorded — presence is not a prerequisite for shipTypes")
	}
	if !sellsType(world.catalogue["X1-QR78-FE8C"], "SHIP_HEAVY_FREIGHTER") {
		t.Fatalf("the recorded catalogue must name the heavy hull, got %v", world.catalogue["X1-QR78-FE8C"])
	}
	if rep.Read != 1 || rep.Outstanding != 1 || rep.Failed != 0 {
		t.Fatalf("report = %+v, want 1 read of 1 outstanding with no failures", rep)
	}
}

// The pass is SELF-QUIESCING: a yard whose catalogue we already hold is never
// read again, so the standing cost of this feature falls to one enumeration per
// tick once the backlog drains.
//
// This is also what stops the sweep from stomping a PRICED reading with an
// unpriced one — the priced rows a parked hull recorded are a catalogue we hold.
func TestReadYardCatalogues_DoesNotRereadAYardWeAlreadyHold(t *testing.T) {
	world := newYardWorld().yard("X1-AA11-Y1", 1, "SHIP_PROBE")

	first := world.sweep(t)
	second := world.sweep(t)

	if first.Read != 1 {
		t.Fatalf("the first pass must read the outstanding yard, got %+v", first)
	}
	if second.Read != 0 || second.Outstanding != 0 {
		t.Fatalf("second pass = %+v, want nothing outstanding and nothing read", second)
	}
	if len(world.reads) != 1 {
		t.Fatalf("the yard was read %d times (%v), want exactly once", len(world.reads), world.reads)
	}
}

// --- the per-tick bound ------------------------------------------------------

// The bound is real, and the fixture supplies STRICTLY MORE outstanding yards
// than it: with the queue shorter than the cap the loop simply runs out of work
// and the cap is never consulted at all, so the test would pass against a cap of
// any size — including none.
func TestReadYardCatalogues_StopsAtThePerTickBound(t *testing.T) {
	world := newYardWorld()
	const surplus = 3
	for i := 0; i < MaxYardCatalogReads+surplus; i++ {
		world.yard(fmt.Sprintf("X1-AA11-Y%02d", i), 1, "SHIP_PROBE")
	}

	rep := world.sweep(t)

	if rep.Read != MaxYardCatalogReads {
		t.Fatalf("read %d yards, want exactly the bound %d", rep.Read, MaxYardCatalogReads)
	}
	if len(world.reads) != MaxYardCatalogReads {
		t.Fatalf("attempted %d reads (%v), want exactly the bound %d", len(world.reads), world.reads, MaxYardCatalogReads)
	}
	if rep.Outstanding != MaxYardCatalogReads+surplus {
		t.Fatalf("Outstanding = %d, want the whole backlog %d — the heartbeat reports what is LEFT to do, not what fit",
			rep.Outstanding, MaxYardCatalogReads+surplus)
	}

	// RECURSIVE BY CONSTRUCTION: the surplus is not lost, it is the next tick's
	// head. Nothing is scheduled, retried or remembered between the two.
	next := world.sweep(t)
	if next.Read != surplus {
		t.Fatalf("the next tick read %d, want the %d yards the bound held back", next.Read, surplus)
	}
}

// A FAILED read still spends its call, so it must still spend budget. Counting
// only successes would let a frontier the API refuses turn one tick into an
// unbounded retry storm against an API that is already saying no.
func TestReadYardCatalogues_AFailedReadIsChargedAgainstTheBound(t *testing.T) {
	world := newYardWorld()
	for i := 0; i < MaxYardCatalogReads+3; i++ {
		waypoint := fmt.Sprintf("X1-AA11-Y%02d", i)
		world.yard(waypoint, 1, "SHIP_PROBE")
		world.unreadable[waypoint] = true
	}

	rep := world.sweep(t)

	if len(world.reads) != MaxYardCatalogReads {
		t.Fatalf("attempted %d reads against an API refusing every one, want the bound %d",
			len(world.reads), MaxYardCatalogReads)
	}
	if rep.Failed != MaxYardCatalogReads || rep.Read != 0 {
		t.Fatalf("report = %+v, want %d failures and no reads", rep, MaxYardCatalogReads)
	}
}

// --- ordering ----------------------------------------------------------------

// FRONTIER-FIRST, SYMBOL TIE-BREAK, and the same answer tick after tick.
//
// Stability is what makes the bounded pick reproducible from the store alone. The
// store hands its rows back in an arbitrary order, so the sweep re-derives the
// order rather than inheriting it — the second pass below is handed the SAME
// yards shuffled and must still take them in the same sequence.
func TestReadYardCatalogues_OrdersFrontierFirstAndIsStableAcrossTicks(t *testing.T) {
	// Two frontier tiers, deliberately interleaved alphabetically so neither key
	// alone can produce the expected order.
	build := func(order []int) *yardWorld {
		world := newYardWorld()
		yards := []struct {
			waypoint string
			frontier int
		}{
			{"X1-AA11-A1", 0},
			{"X1-AA11-B2", 1},
			{"X1-AA11-C3", 0},
			{"X1-AA11-D4", 1},
		}
		for _, i := range order {
			world.yard(yards[i].waypoint, yards[i].frontier)
		}
		return world
	}

	want := []string{"X1-AA11-B2", "X1-AA11-D4", "X1-AA11-A1", "X1-AA11-C3"}

	forward := build([]int{0, 1, 2, 3})
	forward.sweep(t)
	if !sameOrder(forward.reads, want) {
		t.Fatalf("read order = %v, want frontier-first then symbol %v", forward.reads, want)
	}

	// The same yards, handed back by the store in the opposite order.
	shuffled := build([]int{3, 2, 1, 0})
	shuffled.sweep(t)
	if !sameOrder(shuffled.reads, want) {
		t.Fatalf("read order = %v with the store's rows reversed, want the same %v — "+
			"the order must come from the yards, not from the row order", shuffled.reads, want)
	}
}

// --- failure isolation -------------------------------------------------------

// One yard the API will not answer for costs THAT yard and nothing else: not the
// tick, and not the yards queued behind it in the same tick. A read failure that
// aborted the pass would let a single permanently-broken waypoint hold the whole
// blind spot open forever, because it sorts to the same place every tick.
func TestReadYardCatalogues_AnUnreadableYardDoesNotBlockTheOthers(t *testing.T) {
	world := newYardWorld().
		yard("X1-AA11-A1", 1, "SHIP_PROBE").
		yard("X1-AA11-B2", 1, "SHIP_HEAVY_FREIGHTER").
		yard("X1-AA11-C3", 1, "SHIP_EXPLORER")
	world.unreadable["X1-AA11-A1"] = true

	rep := world.sweep(t)

	if rep.Failed != 1 || rep.Read != 2 {
		t.Fatalf("report = %+v, want 1 failure and the other 2 read", rep)
	}
	for _, waypoint := range []string{"X1-AA11-B2", "X1-AA11-C3"} {
		if _, held := world.catalogue[waypoint]; !held {
			t.Fatalf("%s was queued behind the unreadable yard and never read: %v", waypoint, world.reads)
		}
	}
	if _, held := world.catalogue["X1-AA11-A1"]; held {
		t.Fatal("a refused read must record nothing — a blank catalogue would suppress every retry")
	}
}

// The one failure that IS fatal to the pass. An enumeration that cannot be read
// is not a finding about any yard, it is the pass unable to see its own work —
// and proceeding from it would report an empty backlog, which is the exact
// reading an operator uses to conclude the blind spot has drained.
func TestReadYardCatalogues_AnUnreadableWorkListFailsThePass(t *testing.T) {
	world := newYardWorld().yard("X1-AA11-A1", 1, "SHIP_PROBE")
	world.listErr = errors.New("store down")

	rep, err := ReadYardCatalogues(context.Background(),
		YardCatalogPorts{Frontier: world, Catalog: world}, testPlayerID)
	if err == nil {
		t.Fatal("expected the enumeration failure to fail the pass")
	}
	if rep.Outstanding != 0 || rep.Read != 0 {
		t.Fatalf("a failed pass must report nothing, got %+v", rep)
	}
	if len(world.reads) != 0 {
		t.Fatalf("nothing may be read against an unreadable work list, got %v", world.reads)
	}
}

// --- helpers -----------------------------------------------------------------

func sellsType(catalogue []string, shipType string) bool {
	for _, held := range catalogue {
		if held == shipType {
			return true
		}
	}
	return false
}

func sameOrder(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
