package commands

// THE ONLY SIGNAL, LAST TIME, WAS A HUMAN ASKING "why are we buying probes again?" — and the fleet
// spent 1,585,996 credits in the hour before that question was asked. Nobody watches this loop run,
// so the cycle line is the standing sensor, and a refusal it cannot name is a refusal nobody will
// find. These cases hold the line to naming this one.

import (
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// cappedHold is the live frame as the heartbeat receives it: a PROBE wave carrying the borrowed
// "unreachable" reason, spending paused, and the hold naming the actual cause.
func cappedHold() parkedsensing.BuyReport {
	return parkedsensing.BuyReport{
		Wave:            common.WaveProbe,
		WaveProbeReason: common.WaveProbeReasonUnreachable,
		ProbeSpendHold:  common.ProbeSpendHoldHeavyCapped,
		SpendingPaused:  true,
	}
}

// THE LINE NAMES BOTH HALVES OF THE CAUSE, because either half alone is an ordinary healthy state
// and neither on its own tells an operator which knob resumes the spending.
func TestHeartbeat_NoConsumerHoldIsNamedInTheCycleLine(t *testing.T) {
	msg, fields := cycleLine(t, cappedHold())

	if !strings.Contains(msg, "heavy cap") {
		t.Fatalf("cycle line does not name the cap that stopped the buying: %q", msg)
	}
	if !strings.Contains(msg, "more than the fleet can lift") {
		t.Fatalf("cycle line names the cap but not the ample depth beside it — a capped fleet with work it CAN lift is a different, correct state: %q", msg)
	}
	if got, _ := fields["buy_probe_spend_hold"].(string); got != string(common.ProbeSpendHoldHeavyCapped) {
		t.Fatalf("payload does not carry buy_probe_spend_hold, got %v", fields["buy_probe_spend_hold"])
	}
}

// THE HOLD OUTRANKS THE SWITCH ARM, and this is the assertion that ordering exists for. The hold
// sets SpendingPaused, so without its own arm the line would tell an operator whose expansion
// switch is ON that the switch is what stopped the buying — sending them to a knob nobody touched
// while the credits kept moving. It is the same failure sp-suzfh filed against the wave's enum,
// one layer down, and it is why this test asserts on the ABSENCE of the switch text too.
func TestHeartbeat_TheHoldOutranksTheSwitchInTheHeldReason(t *testing.T) {
	msg, _ := cycleLine(t, cappedHold())

	if strings.Contains(msg, "expansion_enabled") {
		t.Fatalf("cycle line blamed the operator's switch for a refusal the fleet made on its own: %q", msg)
	}
}

// THE ORDINARY TICK IS UNTOUCHED. The line an operator has learned to read must not grow a clause
// for the regime this fix is invisible in.
func TestHeartbeat_TheOrdinaryCycleLineNeverMentionsTheHold(t *testing.T) {
	msg, fields := cycleLine(t, parkedsensing.BuyReport{
		Wave: common.WaveProbe, WaveProbeReason: common.WaveProbeReasonLanesServed, Bought: 6, Queued: 5, Attempts: 6,
	})

	if strings.Contains(msg, "heavy cap") {
		t.Fatalf("a spending tick's line mentions the hold: %q", msg)
	}
	if !strings.Contains(msg, "bought 6") {
		t.Fatalf("cycle line lost its bought count: %q", msg)
	}
	if got, _ := fields["buy_probe_spend_hold"].(string); got != "" {
		t.Fatalf("payload claims a hold on a tick that bought: %v", fields["buy_probe_spend_hold"])
	}
}

// EVERY REASON IS PUBLISHED ON EVERY TICK, held or not. A gauge that set only the LIVE reason would
// leave a lifted hold reading 1 forever — a series claiming the fleet is refusing to spend long
// after it resumed, which is the worst lie this particular metric could tell.
func TestHeartbeat_PublishesEveryHoldReasonEveryTick(t *testing.T) {
	for _, tc := range []struct {
		name string
		rep  parkedsensing.BuyReport
		want map[string]bool
	}{
		{
			"held",
			cappedHold(),
			map[string]bool{string(common.ProbeSpendHoldHeavyCapped): true},
		},
		{
			"not held — the reason must still be written, as a 0",
			parkedsensing.BuyReport{Wave: common.WaveProbe, WaveProbeReason: common.WaveProbeReasonLanesServed},
			map[string]bool{string(common.ProbeSpendHoldHeavyCapped): false},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newFakeRecorder()
			h := &RunProbeSensingCoordinatorHandler{}
			h.SetMetricsRecorder(rec)
			h.heartbeat(common.WithLogger(t.Context(), &messageLogger{}),
				&RunProbeSensingCoordinatorCommand{ContainerID: "c1"}, sensingConfig{ProbeCap: 800},
				heartbeat{buy: tc.rep})

			got, writes := rec.recordedSpendHolds()
			if writes != len(common.ProbeSpendHolds()) {
				t.Fatalf("wrote %d reason series, want %d — a reason missing from the tick never falls back to 0", writes, len(common.ProbeSpendHolds()))
			}
			for reason, want := range tc.want {
				if got[reason] != want {
					t.Fatalf("reason %q published %v, want %v", reason, got[reason], want)
				}
			}
		})
	}
}

// A TICK THAT DERIVED NO REGIME PUBLISHES NO HOLD EITHER — the same rule the wave gauge follows,
// and for the same reason. A row of zeros would claim a refusal was evaluated and declined, when
// the tick aborted before anything was evaluated at all.
func TestHeartbeat_PublishesNoHoldWhenNoRegimeWasDerived(t *testing.T) {
	rec := newFakeRecorder()
	h := &RunProbeSensingCoordinatorHandler{}
	h.SetMetricsRecorder(rec)

	h.heartbeat(common.WithLogger(t.Context(), &messageLogger{}),
		&RunProbeSensingCoordinatorCommand{ContainerID: "c1"}, sensingConfig{ProbeCap: 800},
		heartbeat{buy: parkedsensing.BuyReport{}})

	if _, writes := rec.recordedSpendHolds(); writes != 0 {
		t.Fatalf("wrote %d reason series for a tick that derived no regime, want 0", writes)
	}
}
