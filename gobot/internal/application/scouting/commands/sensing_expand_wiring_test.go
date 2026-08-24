package commands

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/yardscan"
)

// sensing_expand_wiring_test.go pins that the expansion engine is actually HANDED the collaborators
// it needs — the class of defect where a correct engine ships dormant because one line of wiring is
// missing and no test notices.
//
// It is a real failure mode in this codebase rather than a hypothetical: an entire off-gate
// expansion chain sat complete and unreachable because its only driver was a retired coordinator.
// A mutation deleting the ListingMemo wiring below failed no test until this file existed.

type wiringMemo struct{}

func (wiringMemo) LastListingScan(context.Context, int, string) (bool, time.Time, bool, error) {
	return false, time.Time{}, false, nil
}

// The expansion engine receives the stored-listing memo, which is what lets seed staging prefer a
// yard we have EVIDENCE sells probes over one the shipyard-trait fallback merely guessed at. Without
// it staging has no evidence to rank on and silently reverts to the previous behaviour.
func TestSensingEnginePorts_ExpandPortsCarriesTheListingMemo(t *testing.T) {
	memo := wiringMemo{}
	ports := SensingEnginePorts{ListingMemo: memo}

	if got := ports.expandPorts(1, map[string]bool{"FUEL": true}).ListingMemo; got == nil {
		t.Fatalf("expandPorts dropped the ListingMemo — seed staging would have no evidence to rank " +
			"yards on and would quietly stage onto shipyard-trait guesses again, which is the exact " +
			"defect this port was added to fix")
	}
}

type wiringPartitioner struct{}

func (wiringPartitioner) PartitionFleet(
	context.Context, *domainRouting.VRPRequest,
) (*domainRouting.VRPResponse, error) {
	return &domainRouting.VRPResponse{}, nil
}

// …and the fleet partitioner. Dropped, charting runs on its angular fallback.
func TestSensingEnginePorts_ExpandPortsCarriesTheFleetPartitioner(t *testing.T) {
	ports := SensingEnginePorts{Partitioner: wiringPartitioner{}}

	if got := ports.expandPorts(1, map[string]bool{"FUEL": true}).Partitioner; got == nil {
		t.Fatalf("expandPorts dropped the Partitioner — every charting crew would fall back to " +
			"angular sectors, with nothing reporting that the solver was never asked")
	}
}

// …and the buy queue keeps it too, so arming one consumer cannot silently disarm the other.
func TestSensingEnginePorts_BuyPortsStillCarriesTheListingMemo(t *testing.T) {
	ports := SensingEnginePorts{ListingMemo: wiringMemo{}}

	if got := ports.buyPorts("container-1", nil).ListingMemo; got == nil {
		t.Fatalf("buyPorts dropped the ListingMemo — the drain would pay to re-learn a standing fact")
	}
}

// …and the buy queue is handed the operator's expansion switch, which is the
// wiring line whose absence cost 907,545 credits.
//
// `expansion_enabled=2` correctly stopped the expansion pass asking other engines
// to buy, and the drain that actually pays for a coverage probe never saw the
// switch at all: same cycle line, same tick, `bought 6 reused 0 queued 5` for over
// an hour while the knob read off (sp-com1h). The gate now lives in
// parkedsensing.DrainBuyQueue and is tested there; what THIS test defends is that
// the coordinator still hands it the value — a gate nobody passes an argument to is
// a gate that ships dormant, which is the exact failure mode this file exists for.
func TestSensingBuyKnobs_CarriesTheExpansionSpendSwitch(t *testing.T) {
	if buyKnobs(sensingConfig{ProbeSpend: false, ProbeCap: 100}).SpendEnabled {
		t.Fatalf("the buy queue was told spending is ENABLED while expansion_enabled reads off — " +
			"the drain would buy probes against a switch the operator has turned off (sp-com1h)")
	}
	if !buyKnobs(sensingConfig{ProbeSpend: true, ProbeCap: 100}).SpendEnabled {
		t.Fatalf("the buy queue was told spending is DISABLED with the switch on — sensing would " +
			"never buy a probe again")
	}
}

// …and the expansion pass is handed the OTHER half of the same switch, through its own named
// function for the same reason. The two are separately settable, so a wiring that fed one value to
// both would silently make the probes-only state unreachable.
func TestSensingExpandKnobs_CarriesTheSeedDispatchSwitch(t *testing.T) {
	probesOnly := sensingConfig{ProbeSpend: true, SeedDispatch: false}

	if expandKnobs(probesOnly).SeedsEnabled {
		t.Fatalf("the expansion pass was told to dispatch seeds while the operator asked for probes " +
			"only — hulls would go back onto charting errands instead of pricing markets")
	}
	if !buyKnobs(probesOnly).SpendEnabled {
		t.Fatalf("the buy queue was told spending is off in the probes-only state — the state would " +
			"be indistinguishable from a full stop")
	}
	if !expandKnobs(sensingConfig{ProbeSpend: true, SeedDispatch: true}).SeedsEnabled {
		t.Fatalf("the expansion pass was told seeds are off with the switch fully on — charting " +
			"would never be dispatched again")
	}
}

// …and the buy queue is handed the operator's coverage-reserve share, the tune
// key that reaches a never-entered system inside an already-held backlog. Same
// failure shape as the expansion switch above: a gate nobody passes an argument
// to is a gate that ships dormant.
func TestSensingBuyKnobs_CarriesTheCoverageReserve(t *testing.T) {
	if got := buyKnobs(sensingConfig{ProbeSpend: true, ProbeCap: 100, CoverageReserve: 3}).CoverageReserve; got != 3 {
		t.Fatalf("buy queue coverage reserve = %d, want 3 — an armed tune key would reach nothing", got)
	}
	if got := buyKnobs(sensingConfig{ProbeSpend: true, ProbeCap: 100}).CoverageReserve; got != 0 {
		t.Fatalf("buy queue coverage reserve = %d, want 0 when unset — it must ship off by default", got)
	}
}

// …and the same for the probe-procurement pair, plus the freshness window it is
// DERIVED from rather than set beside. All three reach a decision that can refuse a
// purchase, so a knob stopping at the struct would be a guard nobody consulted.
func TestSensingBuyKnobs_CarryTheProcurementPairAndDerivedFreshness(t *testing.T) {
	knobs := buyKnobs(resolveSensingConfig(context.Background(), &RunProbeSensingCoordinatorCommand{}, nil))

	if knobs.WalkAwayMult != defaultWalkAwayMult {
		t.Fatalf("walk-away multiple with no config = %d, want the documented default %d — the pair "+
			"ships ARMED (RULINGS #22), never dormant", knobs.WalkAwayMult, defaultWalkAwayMult)
	}
	if knobs.JumpPenaltyCredits != int64(defaultJumpPenaltyCredits) {
		t.Fatalf("jump penalty with no config = %d, want %d", knobs.JumpPenaltyCredits, defaultJumpPenaltyCredits)
	}
	if want := defaultQuartermasterCadenceSecs * askFreshnessCadences * time.Second; knobs.AskFreshness != want {
		t.Fatalf("ask freshness = %v, want %v — it TRACKS quartermaster_cadence_secs, so tightening "+
			"the re-read interval tightens the comparison window with it", knobs.AskFreshness, want)
	}

	got := buyKnobs(resolveSensingConfig(context.Background(), &RunProbeSensingCoordinatorCommand{
		WalkAwayMult: 5, JumpPenaltyCredits: 9_000, QuartermasterCadence: 600,
	}, nil))
	if got.WalkAwayMult != 5 || got.JumpPenaltyCredits != 9_000 {
		t.Fatalf("operator values did not reach the buy queue: %+v", got)
	}
	if want := 600 * askFreshnessCadences * time.Second; got.AskFreshness != want {
		t.Fatalf("ask freshness = %v, want %v after tightening the cadence", got.AskFreshness, want)
	}
}

// wiringYardDemand is a stand-in for the shipyard-read budget, identifiable by
// pointer so a test can assert the drain and the presence pass got the SAME one.
type wiringYardDemand struct{}

func (*wiringYardDemand) PresenceRequests(context.Context, int, int) []yardscan.PresenceRequest {
	return nil
}
func (*wiringYardDemand) AdmitPresence() bool { return false }

// …and the buy queue is handed the shipyard-read budget, which is what makes the
// yard-aware ordering fire at all (sp-7qhum).
//
// This is the wiring line whose absence would ship the whole feature inert, and it
// is a quiet inertness rather than a loud one: an unwired port FAILS OPEN by
// design, so the drain would order its queue exactly as it did before, report zero
// dark yards queued, and look identical to a fleet that simply has none. A
// mutation setting this field to nil failed no test until this existed — the same
// finding that put the ListingMemo tests above in this file.
func TestSensingEnginePorts_BuyPortsCarriesTheYardDemandReader(t *testing.T) {
	budget := &wiringYardDemand{}
	ports := SensingEnginePorts{YardPresence: budget}

	got := ports.buyPorts("container-1", nil).YardDemand
	if got == nil {
		t.Fatalf("buyPorts dropped the YardDemand reader — the buy queue would go back to ordering " +
			"8,934 placements on coverage, depth and arrival with nothing marking the 78 heavy counters " +
			"among them, and would report zero dark yards while doing it (sp-7qhum)")
	}
	// The SAME instance the presence pass gets. Two budgets would rank dark yards
	// from two different fact sets, so the pass that MOVES a hull and the queue that
	// BUYS one would aim at different counters.
	if got != parkedsensing.YardDemandReader(budget) {
		t.Fatalf("buyPorts was handed a different yard budget than the presence pass — the mover and the "+
			"buyer would rank shipyards from separate fact sets. got=%#v want=%#v", got, budget)
	}
	if presence := ports.yardPresencePorts(nil).Demand; presence != parkedsensing.YardPresenceDemand(budget) {
		t.Fatalf("the presence pass no longer holds the budget this test compares against: %#v", presence)
	}
}

var (
	_ parkedsensing.ProbeListingMemo   = wiringMemo{}
	_ parkedsensing.YardPresenceDemand = (*wiringYardDemand)(nil)
	// The read half the drain is handed is a strict subset of the presence
	// interface, so one budget satisfies both and the drain cannot reach the
	// reposition allowance.
	_ parkedsensing.YardDemandReader = (*wiringYardDemand)(nil)
)
