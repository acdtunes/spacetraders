package commands

import (
	"context"
	"testing"
)

// --- sp-k9klz: probe buying is SEQUENCED AHEAD of the frigate's trade dedication.
//
// A hull the trade-fleet coordinator owns is CLAIMED for the whole multi-minute tour leg, so it
// reads IsIdle=false at every 45s bootstrap tick. Dedicate the frigate at tick 1 and the fleet has
// no idle hull left at all — the touring probe is claimed too — so acquireProbesToTarget blocks on
// no_purchaser before it can reach any yard-warming path, and probes never leave 1/3 (observed live
// on staging for 15+ minutes). The frigate therefore stays undedicated until probes reach target. ---

// probeSeqWorld is that fleet: probe #1 out on its own scan tour, the command frigate the only
// other hull, and a presence-gated home yard that prices nothing until something stands at it.
type probeSeqWorld struct {
	probeCount        int
	frigateFleet      string // "" = undedicated and free; tradeFleetTag = owned by the trade coordinator
	frigateIdle       bool
	enRouteToYard     bool
	yardReadable      bool
	graduated         bool
	dedicatedAtProbes int // probe count when the trade dedication was written (-1 = never)
}

func (w *probeSeqWorld) snapshot() Observation {
	return Observation{
		HomeSystem:     "X1-HQ",
		ProbeCount:     w.probeCount,
		ProbesScouting: w.probeCount,
		// The touring probe is claimed by its tour container, so the frigate is the fleet's only
		// possible idle hull — and only while nothing owns it.
		HasIdlePurchaser:      w.frigateIdle,
		Treasury:              175_000, // the live staging figure: under the 500k contract-start bar
		MarketsTotal:          10,
		CommandFrigateID:      "FRIGATE-1",
		CommandFrigateOnTrade: w.frigateFleet == tradeFleetTag,
		CommandFrigateIdle:    w.frigateIdle,
		FrigateCargoEmpty:     true,
		ContractGraduated:     w.graduated,
		Readable:              true,
	}
}

// advance is the ~45s between bootstrap ticks. A trade-dedicated frigate is picked up by the standing
// trade-fleet coordinator and spends it on a tour leg, so it is claimed (never idle at a tick) and any
// errand it was on is abandoned. An undedicated frigate simply completes its yard trip.
func (w *probeSeqWorld) advance() {
	if w.frigateFleet == tradeFleetTag {
		w.frigateIdle = false
		w.enRouteToYard = false
		return
	}
	if w.enRouteToYard {
		w.enRouteToYard = false
		w.yardReadable = true
	}
}

type probeSeqObserver struct{ world *probeSeqWorld }

func (o *probeSeqObserver) Observe(ctx context.Context, playerID int) (Observation, error) {
	return o.world.snapshot(), nil
}

type probeSeqAcquirer struct {
	world *probeSeqWorld
	buys  int
}

func (a *probeSeqAcquirer) PriceCheck(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	if !a.world.yardReadable {
		return 0, "", false, nil
	}
	return 40_000, "X1-HQ-YARD", true, nil
}

func (a *probeSeqAcquirer) Buy(ctx context.Context, playerID int, shipType, yard string) (BuyResult, error) {
	a.buys++
	a.world.probeCount++
	return BuyResult{ShipSymbol: "PROBE-NEW", Price: 40_000}, nil
}

// probeSeqScanner models the real hullToSend rule: the free-hull search takes an idle UNDEDICATED
// hull, and `borrow` is the last-resort lend of a tagged-but-free one (never one back on tour).
type probeSeqScanner struct {
	world      *probeSeqWorld
	dispatches int
	sent       []string
}

func (s *probeSeqScanner) EnsureShipyardReadable(ctx context.Context, playerID int, homeSystem, purchaser, borrow string) (bool, error) {
	send := ""
	switch {
	case s.world.frigateIdle && s.world.frigateFleet == "":
		send = "FRIGATE-1"
	case borrow != "" && s.world.frigateIdle:
		send = borrow
	}
	if send == "" {
		return false, nil
	}
	s.dispatches++
	s.sent = append(s.sent, send)
	s.world.enRouteToYard = true
	return true, nil
}

type probeSeqRetirer struct {
	world *probeSeqWorld
	trade []string
}

func (r *probeSeqRetirer) RetireFromContract(ctx context.Context, playerID int, shipSymbol string) error {
	return nil
}

func (r *probeSeqRetirer) DedicateAsPurchaser(ctx context.Context, playerID int, shipSymbol string) error {
	return nil
}

func (r *probeSeqRetirer) DedicateAsTrade(ctx context.Context, playerID int, shipSymbol string) error {
	r.trade = append(r.trade, shipSymbol)
	r.world.frigateFleet = tradeFleetTag
	r.world.dedicatedAtProbes = r.world.probeCount
	return nil
}

func newProbeSeqHandler(world *probeSeqWorld) (*RunBootstrapCoordinatorHandler, *probeSeqAcquirer, *probeSeqScanner, *probeSeqRetirer, *fakeHandoff) {
	world.dedicatedAtProbes = -1
	acq := &probeSeqAcquirer{world: world}
	scanner := &probeSeqScanner{world: world}
	ret := &probeSeqRetirer{world: world}
	ho := &fakeHandoff{}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&probeSeqObserver{world: world})
	h.SetProbeAcquirer(acq)
	h.SetShipyardScanner(scanner)
	h.SetFrigateRetirer(ret)
	h.SetHandoffLauncher(ho)
	return h, acq, scanner, ret, ho
}

// Acceptance 1: a fresh cold start one probe in reaches 3/3. Nothing owns the frigate yet, so the
// yard-warming errand has a hull and the ramp completes — the deadlock this bead exists to cure.
func TestBootstrap_ProbeSequencing_ColdStartReachesProbeTarget(t *testing.T) {
	world := &probeSeqWorld{probeCount: 1, frigateIdle: true}
	h, acq, scanner, ret, _ := newProbeSeqHandler(world)

	lastBlocker := ""
	for i := 0; i < 8 && world.probeCount < probeTarget; i++ {
		res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
		if err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		lastBlocker = res.Blocker
		world.advance()
	}

	if world.probeCount != probeTarget {
		t.Fatalf("DEADLOCK: probes stuck at %d/%d — the frigate was dedicated to trade at probes=%d (%v), the trade coordinator has owned it ever since, and no hull was ever idle to warm the yard (blocker=%q, yard dispatches=%d, sent=%v)",
			world.probeCount, probeTarget, world.dedicatedAtProbes, ret.trade, lastBlocker, scanner.dispatches, scanner.sent)
	}
	if acq.buys != probeTarget-1 {
		t.Fatalf("expected exactly %d buys to fill 1/3 → 3/3, got %d", probeTarget-1, acq.buys)
	}
}

// Acceptance 2 (a): below target the frigate is left genuinely undedicated — and that is precisely
// what makes it available to the scanning workstream's yard errand on the very same tick.
func TestBootstrap_ProbeSequencing_BelowTarget_FrigateLeftFreeForTheYardErrand(t *testing.T) {
	world := &probeSeqWorld{probeCount: 1, frigateIdle: true}
	h, _, scanner, ret, ho := newProbeSeqHandler(world)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(ret.trade) != 0 || res.FrigateTrading || world.frigateFleet != "" {
		t.Fatalf("below the probe target the frigate must NOT be dedicated to trade; trade=%v FrigateTrading=%v fleet=%q", ret.trade, res.FrigateTrading, world.frigateFleet)
	}
	if scanner.dispatches != 1 || scanner.sent[0] != "FRIGATE-1" {
		t.Fatalf("the undedicated frigate must be the hull the probe buy sends to warm the cold yard, got dispatches=%d sent=%v", scanner.dispatches, scanner.sent)
	}
	if ho.tradeCoord < 1 {
		t.Fatalf("the trade-fleet coordinator is still ensured early so the frigate tours the moment it is dedicated, got %d", ho.tradeCoord)
	}
}

// Acceptance 3 (b): at target the frigate takes up trade exactly as sp-tt3j4 intended.
func TestBootstrap_ProbeSequencing_AtTarget_FrigateIsDedicatedAndToured(t *testing.T) {
	world := &probeSeqWorld{probeCount: probeTarget, frigateIdle: true}
	h, acq, _, ret, ho := newProbeSeqHandler(world)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(ret.trade) != 1 || ret.trade[0] != "FRIGATE-1" || !res.FrigateTrading {
		t.Fatalf("with the probe seed complete the frigate must be dedicated TRADE, by symbol; trade=%v FrigateTrading=%v (blocker=%q)", ret.trade, res.FrigateTrading, res.Blocker)
	}
	if ho.tradeCoord < 1 {
		t.Fatalf("the trade-fleet coordinator must be ensured so the frigate actually tours, got %d", ho.tradeCoord)
	}
	if acq.buys != 0 {
		t.Fatalf("the probe seed is complete — nothing more may be bought, got %d", acq.buys)
	}
}

// Acceptance 4 (c): a contract-graduated player follows the SAME order. Trade dedication sits ahead
// of the graduation stop, so without the probe gate a graduated fleet — frigate plus probes and
// nothing else — deadlocks exactly the same way, with no contract workstream to fall back on.
func TestBootstrap_ProbeSequencing_GraduatedPlayerFollowsTheSameOrder(t *testing.T) {
	below := &probeSeqWorld{probeCount: 1, frigateIdle: true, graduated: true}
	h, _, _, ret, _ := newProbeSeqHandler(below)
	if _, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("graduated below target: %v", err)
	}
	if len(ret.trade) != 0 {
		t.Fatalf("a graduated player below the probe target must not have its frigate claimed either, got %v", ret.trade)
	}

	at := &probeSeqWorld{probeCount: probeTarget, frigateIdle: true, graduated: true}
	h2, _, _, ret2, _ := newProbeSeqHandler(at)
	if _, err := h2.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd()); err != nil {
		t.Fatalf("graduated at target: %v", err)
	}
	if len(ret2.trade) != 1 || ret2.trade[0] != "FRIGATE-1" {
		t.Fatalf("a graduated player at the probe target still trades — trade is not contract income; got %v", ret2.trade)
	}
}

// RULINGS #1: the gate defers NEW dedication only. A frigate already trading — a restart, or a probe
// lost after the seed completed — is never withdrawn back out of the trade fleet.
func TestBootstrap_ProbeSequencing_NeverWithdrawsAnAlreadyTradingFrigate(t *testing.T) {
	world := &probeSeqWorld{probeCount: 1, frigateFleet: tradeFleetTag, frigateIdle: true}
	h, _, _, ret, _ := newProbeSeqHandler(world)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if len(ret.trade) != 0 || res.FrigateTrading {
		t.Fatalf("an already-trading frigate must be left alone, not re-tagged; trade=%v FrigateTrading=%v", ret.trade, res.FrigateTrading)
	}
	if world.frigateFleet != tradeFleetTag {
		t.Fatalf("running trade work is never withdrawn, got fleet=%q", world.frigateFleet)
	}
}
