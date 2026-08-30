package commands

import (
	"strings"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// THE FEEDER SPLIT (sp-p11ce). Every factory hull planned off the same bill, in the same
// neediest-first order, and returned the same first feedable step — so a second feeder bought a
// second hull's worth of the SAME chain. In era torwind-2026-08-30 that meant every feed plan ever
// logged was FAB_MATS: ADVANCED_CIRCUITRY got no feedstock, its factory never restocked, and with
// its supply stuck below the buy floor the construction pipeline could not purchase it either. The
// gate could not complete on any amount of FAB_MATS.
//
// These tests drive the real leg (feedGateLeg) rather than gate.FeedRootOrder, which has its own
// pure tests: the defect was in which chain a HULL ends up on, not in the arithmetic.

// bothChainsFeedable gives the SECONDARY gate material a recipe of its own, so the bill has TWO
// chains a feeder could lead on.
//
// The default fixture deliberately leaves it absent from the recipe map (IsRaw calls it raw, the
// walk yields no steps), which is the right shape for the single-chain tests but makes every
// split assertion vacuous: with one feedable chain, spreading feeders and serializing them produce
// the identical answer. Its two inputs are RAW here, mirroring the live shape where the circuitry
// factory imports ELECTRONICS and MICROPROCESSORS and both are bought at in-system exporters.
func bothChainsFeedable(f *gateFeedFixture) {
	f.topo.mu.Lock()
	defer f.topo.mu.Unlock()
	f.topo.recipes[gateMaterialSecondary] = []string{"ELECTRONICS", "MICROPROCESSORS"}
	f.topo.mineable["ELECTRONICS"] = true
	f.topo.mineable["MICROPROCESSORS"] = true
}

// factoryRoster registers the FACTORY-tagged hulls the feeder census reads. The census is the
// fleet, not this leg's own hull, so a test that does not stage it is testing a lone feeder.
func factoryRoster(t *testing.T, f *gateFeedFixture, symbols ...string) {
	t.Helper()
	ships := make([]*navigation.Ship, 0, len(symbols))
	for _, symbol := range symbols {
		ships = append(ships, gateTestHull(t, symbol, gate.FactoryFleetTag))
	}
	f.shipRepo.mu.Lock()
	defer f.shipRepo.mu.Unlock()
	f.shipRepo.ships = append(f.shipRepo.ships, ships...)
}

// fedFactories is the factory waypoints this fixture's legs actually fed, in order.
func fedFactories(f *gateFeedFixture) []string {
	feeds := f.feeder.feeds()
	out := make([]string, 0, len(feeds))
	for _, feed := range feeds {
		out = append(out, feed.waypoint)
	}
	return out
}

// THE DEFECT. Two feeders on one bill must land on TWO chains.
func TestFeedGateLeg_ASecondFeederLeadsOnTheChainNobodyElseIsFeeding(t *testing.T) {
	f := newGateFactoryHandler(t)
	bothChainsFeedable(f)
	factoryRoster(t, f, "GF-1", "GF-2")

	f.runFeed(t, "GF-1")
	f.runFeed(t, "GF-2")

	fed := fedFactories(f)
	if len(fed) != 2 {
		t.Fatalf("two feeding legs fed %v, want one factory each", fed)
	}
	if fed[0] != gateMaterialPrimary+"-EXPORTER" {
		t.Fatalf("the FIRST feeder fed %s, want the %s factory — feeder 1 still leads on the neediest chain", fed[0], gateMaterialPrimary)
	}
	if fed[1] != gateMaterialSecondary+"-EXPORTER" {
		t.Fatalf("the SECOND feeder fed %s, want the %s factory. Both feeders on one chain is sp-p11ce exactly: the other material then has no production path, its factory never leaves LIMITED, and the construction buy stays paused behind its floor", fed[1], gateMaterialSecondary)
	}
	if !strings.Contains(f.logLines(), "Gate factory feed plan for "+gateMaterialSecondary) {
		t.Fatalf("no feed plan was ever logged for %s; the plan line is the operator's only evidence a chain is being worked at all:\n%s", gateMaterialSecondary, f.logLines())
	}
}

// A LONE FEEDER IS UNCHANGED. Same fixture, same hull, one feeder in the roster: it takes the
// NEEDIEST chain, exactly as before. Splitting one hull across chains finishes neither.
//
// This is the contrast that makes the test above mean something: GF-2 leads on the secondary chain
// only BECAUSE a second feeder exists, never because of anything about GF-2.
func TestFeedGateLeg_ALoneFeederStillTakesTheNeediestChain(t *testing.T) {
	f := newGateFactoryHandler(t)
	bothChainsFeedable(f)
	factoryRoster(t, f, "GF-2")

	f.runFeed(t, "GF-2")

	fed := fedFactories(f)
	if len(fed) != 1 || fed[0] != gateMaterialPrimary+"-EXPORTER" {
		t.Fatalf("the only feeder fed %v, want the %s factory — with nobody to spread to, the walk stays neediest-first", fed, gateMaterialPrimary)
	}
	if strings.Contains(f.logLines(), "LEADS on") {
		t.Fatalf("a single feeder announced a chain split, which decides nothing and is pure noise:\n%s", f.logLines())
	}
}

// THE SPLIT CHOOSES THE CHAIN, THE SCARCITY RANKING STILL CHOOSES THE STEP. Within its own chain
// the second feeder must still buy the input its factory is SHORTEST of, not the one the recipe
// happens to list first — the sp-q9um6 ordering, unchanged by the rotation.
func TestFeedGateLeg_TheSecondFeederStillOrdersItsOwnChainByScarcity(t *testing.T) {
	f := newGateFactoryHandler(t)
	bothChainsFeedable(f)
	factoryRoster(t, f, "GF-1", "GF-2")
	// The circuitry factory is SHORTEST of MICROPROCESSORS, which its recipe lists SECOND — so
	// recipe order and scarcity order disagree and the assertion can tell them apart.
	f.topo.importSupply = map[string]string{
		importSupplyKey(gateMaterialSecondary+"-EXPORTER", "ELECTRONICS"):     "MODERATE",
		importSupplyKey(gateMaterialSecondary+"-EXPORTER", "MICROPROCESSORS"): "SCARCE",
	}

	f.runFeed(t, "GF-2")

	feeds := f.feeder.feeds()
	if len(feeds) != 1 || len(feeds[0].inputs) != 1 {
		t.Fatalf("feeds = %+v, want one input on a one-step leg", feeds)
	}
	if feeds[0].inputs[0] != "MICROPROCESSORS" {
		t.Fatalf("the second feeder bought %s; its factory is SCARCE of MICROPROCESSORS and MODERATE of ELECTRONICS, so rotating a hull onto this chain must not cost it the scarcity ordering", feeds[0].inputs[0])
	}
	if !strings.Contains(f.logLines(), "feeding the "+gateMaterialSecondary+" chain by SCARCITY") {
		t.Fatalf("the scarcity reordering went unannounced on the rotated chain:\n%s", f.logLines())
	}
}

// LEADING IS A PREFERENCE, NOT AN ASSIGNMENT. A chain with no viable feed path costs the fleet no
// capacity: the feeder falls through to the chain behind it rather than standing down.
func TestFeedGateLeg_ASecondFeederFallsBackWhenItsOwnChainHasNothingFeedable(t *testing.T) {
	f := newGateFactoryHandler(t)
	factoryRoster(t, f, "GF-1", "GF-2")

	// Non-vacuity: the default fixture leaves the secondary material out of the recipe map, so its
	// walk yields no steps at all. Give it one and this test proves nothing about the fallback.
	if steps := gate.PlanFeed(gateMaterialSecondary, f.topo, gate.DefaultFeedDepthCap).Steps; len(steps) != 0 {
		t.Fatalf("fixture is inert: the %s chain plans %d step(s), so the second feeder has real work there and never needs to fall back", gateMaterialSecondary, len(steps))
	}

	f.runFeed(t, "GF-2")

	fed := fedFactories(f)
	if len(fed) != 1 || fed[0] != gateMaterialPrimary+"-EXPORTER" {
		t.Fatalf("the second feeder fed %v; its own chain has no feedable step, so it must fall back onto the %s chain rather than idle in front of work", fed, gateMaterialPrimary)
	}
}

// A MET BILL IS NOT A CHAIN. A material whose bill is closed never becomes a leader — it is
// dropped from the feedable roots before the split ever sees it — so the second feeder joins the
// chain that is still open instead of leading on a finished one.
func TestFeedGateLeg_NoFeederLeadsOnAMaterialWhoseBillIsMet(t *testing.T) {
	f := newGateFactoryHandler(t)
	bothChainsFeedable(f)
	f.pipeline.delivered[gateMaterialSecondary] = f.pipeline.target[gateMaterialSecondary]
	factoryRoster(t, f, "GF-1", "GF-2")

	f.runFeed(t, "GF-2")

	fed := fedFactories(f)
	if len(fed) != 1 || fed[0] != gateMaterialPrimary+"-EXPORTER" {
		t.Fatalf("the second feeder fed %v while the %s bill is already met; feeding a closed chain buys the gate nothing", fed, gateMaterialSecondary)
	}
	if strings.Contains(f.logLines(), "Gate factory feed plan for "+gateMaterialSecondary) {
		t.Fatalf("a feed plan was walked for %s, whose bill is met:\n%s", gateMaterialSecondary, f.logLines())
	}
}

// THE ROSTER IS THE FACTORY TAG, IN THIS SYSTEM. A delivery hull, a legacy-tagged one and a
// factory hull parked in another system are all excluded — counting any of them would shift the
// real feeders' positions and collide two of them back onto one chain.
func TestFactoryFeederSlot_CountsOnlyInSystemFactoryHulls(t *testing.T) {
	f := newGateFactoryHandler(t)
	factoryRoster(t, f, "GF-1", "GF-2")
	f.shipRepo.mu.Lock()
	f.shipRepo.ships = append(f.shipRepo.ships,
		gateTestHull(t, "GD-1", gate.DeliveryFleetTag),
		gateTestHull(t, "GL-1", gate.LegacyFleetTag),
		newTestHaulerInFleet(t, "GF-FAR", gate.FactoryFleetTag),
	)
	f.shipRepo.mu.Unlock()

	slot := f.handler.factoryFeederSlot(f.ctx(), gateTestSystem, gateTestHull(t, "GF-2", gate.FactoryFleetTag), shared.MustNewPlayerID(1))

	if slot.feeders != 2 {
		t.Fatalf("the roster counted %d feeder(s); only GF-1 and GF-2 are in-system factory hulls, and any other tag or system inflating the count moves a real feeder onto a chain another one already leads", slot.feeders)
	}
	if slot.index != 1 {
		t.Fatalf("GF-2 placed at position %d of a symbol-sorted roster of GF-1, GF-2, want 1", slot.index)
	}
}

// MEMBERSHIP, NOT LIVENESS. A feeder mid-haul still counts. Feeding legs are long, so the OTHER
// feeder is almost always busy when this one plans: a census that dropped busy hulls would report
// every planning feeder as the only one and put every one of them back on the neediest chain —
// the defect, rebuilt out of liveness.
func TestFactoryFeederSlot_CountsAFeederThatIsAlreadyOutOnALeg(t *testing.T) {
	f := newGateFactoryHandler(t)
	factoryRoster(t, f, "GF-1", "GF-2")
	if _, admitted := f.handler.supplies.admit("GF-1", "container-1", gateMaterialPrimary, 40, f.handler.clock.Now().Add(time.Hour)); !admitted {
		t.Fatal("could not stage GF-1 as busy, so this test cannot tell a liveness filter from a membership one")
	}

	slot := f.handler.factoryFeederSlot(f.ctx(), gateTestSystem, gateTestHull(t, "GF-2", gate.FactoryFleetTag), shared.MustNewPlayerID(1))

	if slot.feeders != 2 || slot.index != 1 {
		t.Fatalf("with GF-1 out on a leg the roster reported feeder %d of %d; a busy feeder is still a feeder, and dropping it hands GF-2 the neediest chain GF-1 is already on", slot.index+1, slot.feeders)
	}
}

// FAIL CLOSED TO TODAY'S BEHAVIOUR. An unreadable or empty fleet narrows the leg to a lone
// feeder — the neediest-first walk it already had — and never widens it. The hull is always in
// its own roster: it is here, feeding.
func TestFactoryFeederSlot_AnEmptyFleetLeavesTheHullASoleFeeder(t *testing.T) {
	f := newGateFactoryHandler(t)

	slot := f.handler.factoryFeederSlot(f.ctx(), gateTestSystem, gateTestHull(t, "GF-2", gate.FactoryFleetTag), shared.MustNewPlayerID(1))

	if slot.feeders != 1 || slot.index != 0 {
		t.Fatalf("a hull absent from the fleet read placed at feeder %d of %d, want the sole-feeder slot 1 of 1", slot.index+1, slot.feeders)
	}
}
