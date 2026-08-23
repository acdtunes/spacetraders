package commands

import (
	"context"
	"testing"
)

// --- sp-k9klz: the probe fleet must still reach target once the command frigate holds its standing
// "trade" dedication. A yard prices its hulls only while one stands at it, and the free-hull search
// takes only UNDEDICATED idle hulls — so once the frigate trades and probe #1 tours, nothing qualifies
// and the probe price can never be read again (observed live: probes stuck at 1/3 for 30+ minutes). ---

// probeYardWorld is that fleet: probe #1 bought and out on its own tour, the command frigate carrying
// the trade tag, nothing else. Buys land probes; the yard prices only once a hull has been sent to it.
type probeYardWorld struct {
	probeCount  int
	frigateIdle bool
}

func (w *probeYardWorld) snapshot() Observation {
	return Observation{
		HomeSystem:     "X1-HQ",
		ProbeCount:     w.probeCount,
		ProbesScouting: w.probeCount,
		// True in both variants on purpose: an incidentally-idle hull can execute the BUY (the buy path
		// takes any idle hull), so the only thing under test is which hull may WARM the yard.
		HasIdlePurchaser:      true,
		Treasury:              300_000,
		MarketsTotal:          10,
		CommandFrigateID:      "FRIGATE-1",
		CommandFrigateOnTrade: true,
		CommandFrigateIdle:    w.frigateIdle,
		Readable:              true,
	}
}

type probeYardObserver struct{ world *probeYardWorld }

func (o *probeYardObserver) Observe(ctx context.Context, playerID int) (Observation, error) {
	return o.world.snapshot(), nil
}

// probeYardAcquirer is the presence-gated yard: unreadable until a hull has been stood at it.
type probeYardAcquirer struct {
	readable bool
	buys     int
	world    *probeYardWorld
}

func (a *probeYardAcquirer) PriceCheck(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	if !a.readable {
		return 0, "", false, nil
	}
	return 40_000, "X1-HQ-YARD", true, nil
}

func (a *probeYardAcquirer) Buy(ctx context.Context, playerID int, shipType, yard string) (BuyResult, error) {
	a.buys++
	a.world.probeCount++
	return BuyResult{ShipSymbol: "PROBE-NEW", Price: 40_000}, nil
}

// tradeFleetScanner models the real hullToSend rule against that fleet: the free-hull search (idle AND
// undedicated) finds nothing, so the only hull that can warm the yard is one the caller hands it. It
// re-reads live ship state before flying anything, so a frigate the trade coordinator has since put
// back on tour is refused (PLAYBOOK §9).
type tradeFleetScanner struct {
	acq        *probeYardAcquirer
	world      *probeYardWorld
	offered    []string // the borrow candidate each call was given ("" = none)
	dispatches int
}

func (s *tradeFleetScanner) EnsureShipyardReadable(ctx context.Context, playerID int, homeSystem, shipType, purchaser, borrow string) (bool, bool, error) {
	s.offered = append(s.offered, borrow)
	send := purchaser
	if send == "" {
		send = borrow
	}
	if send == "" || (send == "FRIGATE-1" && !s.world.frigateIdle) {
		return false, false, nil
	}
	s.dispatches++
	s.acq.readable = true // the hull now stands at the yard → the next tick's live price reads
	return true, false, nil
}

func newProbeYardHandler(world *probeYardWorld) (*RunBootstrapCoordinatorHandler, *probeYardAcquirer, *tradeFleetScanner) {
	acq := &probeYardAcquirer{world: world}
	scanner := &tradeFleetScanner{acq: acq, world: world}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&probeYardObserver{world: world})
	h.SetProbeAcquirer(acq)
	h.SetShipyardScanner(scanner)
	return h, acq, scanner
}

// Acceptance 1: a cold start whose frigate already trades still reaches 3/3. The frigate stands idle
// between tours, so it can be lent to the yard for the read — without ever being re-dedicated.
func TestBootstrap_ProbeBuying_IdleTradeFrigateWarmsTheColdYard(t *testing.T) {
	world := &probeYardWorld{probeCount: 1, frigateIdle: true}
	h, acq, scanner := newProbeYardHandler(world)

	for i := 0; i < 6 && world.probeCount < probeTarget; i++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		if res.FrigatePivoted {
			t.Fatalf("tick %d: warming the yard must BORROW the idle frigate, never re-dedicate it", i)
		}
	}

	if world.probeCount != probeTarget {
		t.Fatalf("DEADLOCK: probes stuck at %d/%d — the trade-dedicated frigate stood idle every tick and was never lent to the cold yard (dispatches=%d, borrow candidates offered=%v)",
			world.probeCount, probeTarget, scanner.dispatches, scanner.offered)
	}
	if acq.buys != probeTarget-1 {
		t.Fatalf("expected exactly %d buys to fill 1/3 → 3/3, got %d", probeTarget-1, acq.buys)
	}
}

// Acceptance 2: mid-tour is untouchable. While the frigate is out on a tour it is never offered and
// never flown — the tick waits instead, however long the yard stays cold (PLAYBOOK §9). The wait ends
// on its own once the tour does.
func TestBootstrap_ProbeBuying_MidTourFrigateIsNeverRedirected(t *testing.T) {
	world := &probeYardWorld{probeCount: 1, frigateIdle: false} // a tour is in flight
	h, _, scanner := newProbeYardHandler(world)

	for i := 0; i < 3; i++ {
		if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
			t.Fatalf("mid-tour tick %d: %v", i, err)
		}
	}
	if scanner.dispatches != 0 {
		t.Fatalf("a frigate mid-tour must never be pulled off it to warm the yard, got %d dispatches", scanner.dispatches)
	}
	for _, cand := range scanner.offered {
		if cand != "" {
			t.Fatalf("a frigate mid-tour must not even be offered as a candidate, got %q", cand)
		}
	}
	if world.probeCount != 1 {
		t.Fatalf("no price was ever read, so nothing may be bought; got %d probes", world.probeCount)
	}

	// The tour ends — the same fleet now clears itself with no intervention.
	world.frigateIdle = true
	for i := 0; i < 3 && world.probeCount < probeTarget; i++ {
		if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
			t.Fatalf("idle tick %d: %v", i, err)
		}
	}
	if world.probeCount != probeTarget {
		t.Fatalf("once the tour ended the errand must proceed: probes %d/%d", world.probeCount, probeTarget)
	}
}
