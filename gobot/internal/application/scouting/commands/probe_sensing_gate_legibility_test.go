package commands

// probe_sensing_gate_legibility_test.go pins what the cycle line has to SAY about
// the two things that can quietly stop the sensing loop doing work: the expansion
// budget gate, and the fleet-wide market-scan budget declining the scans the pacer
// issues.
//
// Both were readable only from Go source. "expansion skipped (budget)" names a gate
// and no numbers, so a gate that is genuinely holding is indistinguishable from a
// loop still running on a threshold that was changed minutes ago. And the pacer's
// own rate is an ALLOWANCE, not a throughput: every scan it issues is separately
// admitted by the fleet market-scan budget, so a rotation can run at its full paced
// rate and land almost nothing with no line saying which of the two is binding.

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// gateLine renders one heartbeat from a whole tick's outcome, and returns the
// message and its structured payload.
func gateLine(t *testing.T, cfg sensingConfig, hb heartbeat) (string, map[string]interface{}) {
	t.Helper()
	log := &messageLogger{}
	h := &RunProbeSensingCoordinatorHandler{}
	h.heartbeat(common.WithLogger(context.Background(), log),
		&RunProbeSensingCoordinatorCommand{ContainerID: "probe_sensing_coordinator-player-1-24f32043"},
		cfg, hb)

	if len(log.messages) != 1 {
		t.Fatalf("want exactly one cycle line, got %d", len(log.messages))
	}
	return log.messages[0], log.fields[0]
}

// THE REGRESSION PROOF for the skip line: it names the gate AND the two numbers
// the gate compared, so an operator can check the arithmetic without reading Go.
func TestHeartbeat_BudgetSkipNamesTheGateAndItsNumbers(t *testing.T) {
	msg, fields := gateLine(t,
		sensingConfig{MinScanRateMilli: 450, ExpansionMinBudgetMilli: 20},
		heartbeat{
			sensingRate: 0.009,
			expand: parkedsensing.ExpandReport{
				Skipped: parkedsensing.SkippedBudget, BudgetRate: 0.009, MinBudgetRate: 0.020,
			},
		})

	for _, want := range []string{"budget", "0.009", "0.020"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("cycle line does not carry %q: %q\n"+
				"a gate named without its numbers cannot be told apart from a stale threshold", want, msg)
		}
	}
	if got := fields["expansion_budget_rate"]; got != 0.009 {
		t.Errorf("payload expansion_budget_rate = %v, want 0.009", got)
	}
	if got := fields["expansion_min_budget_rate"]; got != 0.020 {
		t.Errorf("payload expansion_min_budget_rate = %v, want 0.020", got)
	}
}

// The EFFECTIVE knob values ride every line, skipped or not. That is what turns
// "the gate held" and "this loop has not picked up my tune yet" into one query
// rather than an inference: the tick prints the thresholds it ran on, so a value
// that disagrees with the config is propagation lag and nothing else.
func TestHeartbeat_PayloadCarriesTheEffectiveThresholds(t *testing.T) {
	_, fields := gateLine(t,
		sensingConfig{MinScanRateMilli: 50, ExpansionMinBudgetMilli: 20},
		heartbeat{expand: parkedsensing.ExpandReport{BudgetRate: 0.081, MinBudgetRate: 0.020}})

	if got := fields["min_scan_rate_milli"]; got != 50 {
		t.Errorf("payload min_scan_rate_milli = %v, want the 50 this tick resolved", got)
	}
	if got := fields["expansion_min_budget_milli"]; got != 20 {
		t.Errorf("payload expansion_min_budget_milli = %v, want the 20 this tick resolved", got)
	}
	if got := fields["expansion_min_budget_rate"]; got != 0.020 {
		t.Errorf("payload expansion_min_budget_rate = %v on a RUNNING tick, want 0.020 — the "+
			"threshold has to be readable before something goes wrong, not only after", got)
	}
}

// THE SCAN CEILING, made legible. A gap between turns issued and data landed is
// the fleet-wide market-scan budget serving waypoints from the store, and the line
// has to say so — the pacer's rate cannot show it, and neither can a rotation
// count.
func TestHeartbeat_DeclinedScansNameTheFleetMarketBudget(t *testing.T) {
	msg, fields := gateLine(t, sensingConfig{},
		heartbeat{scans: parkedsensing.ScanOutcomes{Scanned: 3, Declined: 47, Failed: 1}})

	if !strings.Contains(msg, "market-scan budget") {
		t.Fatalf("cycle line does not name what declined the scans: %q\n"+
			"a rotation running at its full paced rate and landing nothing is otherwise "+
			"indistinguishable from a rotation that is not running", msg)
	}
	for _, want := range []string{"3", "51", "47"} {
		if !strings.Contains(msg, want) {
			t.Errorf("cycle line does not carry %q (landed / turns / declined): %q", want, msg)
		}
	}
	if got := fields["scans_landed"]; got != 3 {
		t.Errorf("payload scans_landed = %v, want 3", got)
	}
	if got := fields["scans_declined"]; got != 47 {
		t.Errorf("payload scans_declined = %v, want 47", got)
	}
	if got := fields["scans_failed"]; got != 1 {
		t.Errorf("payload scans_failed = %v, want 1", got)
	}
}

// A clean stretch says so briefly: the ordinary line an operator has learned to
// read does not grow a clause explaining a budget that declined nothing.
func TestHeartbeat_CleanScanStretchStaysQuiet(t *testing.T) {
	msg, _ := gateLine(t, sensingConfig{},
		heartbeat{scans: parkedsensing.ScanOutcomes{Scanned: 12}})

	if strings.Contains(msg, "market-scan budget") {
		t.Fatalf("a stretch that declined nothing still explains the budget: %q", msg)
	}
	if !strings.Contains(msg, "12") {
		t.Fatalf("cycle line lost its landed count: %q", msg)
	}
}
