package commands

import (
	"context"
	"testing"
)

// WHERE THE PRICING ERRAND SITS IN THE TICK, and which hull flies it.
//
// The dispatch verb waits out the flight, so an errand taken ahead of the buy holds the whole tick
// — and every decision behind it — open for the length of a journey.
//
// TEST BUDGET (behaviour-first): 3 distinct behaviours x 2 = 6 unit tests maximum.
//
//	B1 a tick that BOUGHT its heavy stands the errand down — that ask is already readable
//	B2 the buy decision is taken BEFORE any hull is dispatched
//	B3 the fastest eligible carrier flies, not the lowest-symbol one

// tickTrace records the ORDER of a tick's outward acts across the ports it drives, so a test can
// pin which act the coordinator reaches first without reading its log prose.
type tickTrace struct{ acts []string }

func (t *tickTrace) record(act string) { t.acts = append(t.acts, act) }

func (t *tickTrace) indexOf(act string) int {
	for i, a := range t.acts {
		if a == act {
			return i
		}
	}
	return -1
}

const (
	actDecisionBlocked = "heavy_decision_blocked"
	actErrandSent      = "errand_sent"
)

// tracingGrowthSink times-stamps the act that proves the buy DECISION was taken: a named guard
// refusing one.
type tracingGrowthSink struct {
	noopGrowthSink
	trace *tickTrace
}

func (s *tracingGrowthSink) RecordBlocked(HullClass, GuardName) { s.trace.record(actDecisionBlocked) }

// tracingErrandPort is the recording errand port with the dispatch timestamped into the same trace.
type tracingErrandPort struct {
	*recordingErrandPort
	trace *tickTrace
}

func (p *tracingErrandPort) SendToYard(ctx context.Context, playerID int, shipSymbol, waypointSymbol string) error {
	p.trace.record(actErrandSent)
	return p.recordingErrandPort.SendToYard(ctx, playerID, shipSymbol, waypointSymbol)
}

// heavyWaveWithAnUnpricedYardToo is the live shape: a HEAVY wave, one priced and affordable heavy,
// and a FOURTH yard in the catalogue that nobody has read an ask at, with a spare probe free to go.
func heavyWaveWithAnUnpricedYardToo(t *testing.T, maxPrice int64) (*RunFleetGrowthCoordinatorHandler, *RunFleetGrowthCoordinatorCommand, *growthPurchaseRecorder, *tracingErrandPort, *tickTrace) {
	t.Helper()
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes:         &fakeLanes{count: 9, readable: true},
		treasury:      12_000_000,
		yardAsk:       1_831_018,
		shortfallHeld: growthSettledWindow,
	})
	h.SetPurchaser(buyer)

	trace := &tickTrace{}
	h.SetMetricsSink(&tracingGrowthSink{trace: trace})
	h.SetHeavyYardCatalogReader(&fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-VV53-XA3F", 1)}})
	errand := &tracingErrandPort{
		recordingErrandPort: &recordingErrandPort{hulls: []PricingErrandHull{spareParkedProbe("PROBE-A", "X1-VV53-Z22C")}},
		trace:               trace,
	}
	h.SetHeavyPricingErrandPort(errand)

	cmd := growthCmd()
	cmd.MaxPriceHeavies = maxPrice
	return h, cmd, buyer, errand, trace
}

// B1 — THE PRODUCTION REGRESSION. A heavy was priced and affordable, and the coordinator spent the
// tick pricing a fourth yard instead of buying it.
//
// A yard whose ask we are about to spend against is already readable, so the trip buys information
// the decision did not want — and it takes a probe off station to buy it.
func TestGrowthReconcile_ATickThatBoughtItsHeavyStandsThePricingErrandDown(t *testing.T) {
	h, cmd, buyer, errand, _ := heavyWaveWithAnUnpricedYardToo(t, 0)

	res, err := h.reconcileOnce(context.Background(), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Purchased != 1 || buyer.calls != 1 {
		t.Fatalf("the affordable priced heavy must be bought: purchased=%d calls=%d", res.Purchased, buyer.calls)
	}
	// The calibration that the PURCHASE stands the errand down, and not an empty fixture, is B2
	// below: the same catalogue and the same probe, with the buy refused, dispatches one hull.
	if len(errand.sent) != 0 {
		t.Fatalf("a tick that bought its heavy must send no pricing errand, got %+v", errand.sent)
	}
}

// B2 — the dispatch waits out the flight, so it must never run ahead of the decision it would
// otherwise hold open. With the buy refused the errand still flies, and it flies second.
func TestGrowthReconcile_TheBuyDecisionIsTakenBeforeAnyHullIsDispatched(t *testing.T) {
	h, cmd, buyer, errand, trace := heavyWaveWithAnUnpricedYardToo(t, 1_000_000)

	if _, err := h.reconcileOnce(context.Background(), cmd); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if buyer.calls != 0 {
		t.Fatalf("the price ceiling must refuse this buy, got %d purchases", buyer.calls)
	}
	if len(errand.sent) != 1 {
		t.Fatalf("a refused buy leaves the unpriced yard worth a trip: want one dispatch, got %+v", errand.sent)
	}
	decided, dispatched := trace.indexOf(actDecisionBlocked), trace.indexOf(actErrandSent)
	if decided < 0 || dispatched < 0 {
		t.Fatalf("both acts must be observed, got %v", trace.acts)
	}
	if decided > dispatched {
		t.Fatalf("the buy decision must be taken before a hull is flown, got %v", trace.acts)
	}
}

// B3 — the carrier is chosen for SPEED, not for its symbol. A satellite is the slowest hull class we
// own, and an errand flown on one costs multiples of the wall clock the same trip costs a hauler.
//
// The slow hull deliberately sorts FIRST by symbol, so the old lowest-symbol tiebreak picks it.
func TestReconcile_TheFasterSpareCarrierFliesTheErrand(t *testing.T) {
	catalog := &fakeHeavyYardCatalog{yards: []KnownHeavyYard{unpricedYard("X1-QR78-AE4F", 3)}}
	errand := &recordingErrandPort{hulls: []PricingErrandHull{
		spareCarrierAtSpeed("AAA-SATELLITE", "X1-HOME-A1", 9),
		spareCarrierAtSpeed("ZZZ-HAULER", "X1-HOME-A1", 15),
	}}
	h, _ := errandHandler(catalog, errand)
	h.SetHeavyYardReader(&fakeHeavyYard{found: false})

	if _, err := h.reconcileOnce(context.Background(), errandCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}

	if len(errand.sent) != 1 {
		t.Fatalf("want exactly one dispatch, got %+v", errand.sent)
	}
	if errand.sent[0].Ship != "ZZZ-HAULER" {
		t.Fatalf("the faster carrier must fly the errand, got %q", errand.sent[0].Ship)
	}
}

// spareCarrierAtSpeed is the eligible carrier with its engine speed named — the fact that separates
// a satellite from a hauler standing in the same pool.
func spareCarrierAtSpeed(symbol, at string, speed int) PricingErrandHull {
	h := spareParkedProbe(symbol, at)
	h.EngineSpeed = speed
	return h
}
