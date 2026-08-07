package commands

// The cycle line has to SAY the fleet is saving for a heavy.
//
// A HEAVY wave and a dead drain look identical from outside: bought 0, tick after tick, for as long
// as the treasury takes to climb. The line is the standing sensor — nobody watches this loop run —
// so it must name the regime in words, and it must name it AHEAD of the operator's switch, which is
// also true whenever both are shut. An operator told "expansion switch off" when the real reason is
// the wave goes looking for a knob nobody touched.

import (
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// THE HEAVY WAVE IS NAMED IN WORDS, and the payload carries it for querying beside buy_bought.
func TestHeartbeat_HeavyWaveIsNamedInTheCycleLine(t *testing.T) {
	msg, fields := cycleLine(t, parkedsensing.BuyReport{
		Wave: common.WaveHeavy, SpendingPaused: true, HeavyReserveTarget: 1_916_613, Reused: 2,
	})

	if !strings.Contains(msg, "heavy wave") {
		t.Fatalf("cycle line does not name the regime that stopped the buying: %q\n"+
			"bought 0 on its own is equally a probe cap, a buy floor, an empty queue or a dead engine", msg)
	}
	if got, _ := fields["buy_wave"].(string); got != string(common.WaveHeavy) {
		t.Fatalf("payload does not carry buy_wave=heavy, got %v", fields["buy_wave"])
	}
	if got, _ := fields["buy_heavy_reserve_target"].(int64); got != 1_916_613 {
		t.Fatalf("payload does not carry what the pause is FOR, got %v", fields["buy_heavy_reserve_target"])
	}
	// The free half still ran, and the line still shows it: a paused wave is not a stall.
	if !strings.Contains(msg, "reused 2") {
		t.Fatalf("cycle line hid the free work the paused tick did: %q", msg)
	}
}

// THE WAVE OUTRANKS THE SWITCH in the one-reason slot, and that ordering is the point rather than a
// tidy-up: both are true whenever an operator has also switched expansion off, and the wave is the
// more specific answer.
func TestHeartbeat_HeavyWaveOutranksTheSwitchInTheHeldReason(t *testing.T) {
	msg, _ := cycleLine(t, parkedsensing.BuyReport{Wave: common.WaveHeavy, SpendingPaused: true})

	if !strings.Contains(msg, "heavy wave") {
		t.Fatalf("cycle line reported the switch over the more specific regime: %q", msg)
	}
	if strings.Contains(msg, "expansion_enabled") {
		t.Fatalf("cycle line named a knob nobody touched: %q", msg)
	}
}

// THE SWITCH STILL WINS ON THE PROBE WAVE. Narrowing the switch's arm must not silence it: a tick
// paused by the operator on a released regime is still the operator's own decision, and the line
// has to say so in those words.
func TestHeartbeat_ProbeWaveStillNamesTheSwitch(t *testing.T) {
	msg, fields := cycleLine(t, parkedsensing.BuyReport{
		Wave: common.WaveProbe, WaveProbeReason: common.WaveProbeReasonUnreachable, SpendingPaused: true,
	})

	if !strings.Contains(msg, "expansion_enabled") {
		t.Fatalf("cycle line does not name the switch that stopped the buying: %q", msg)
	}
	if strings.Contains(msg, "heavy wave") {
		t.Fatalf("cycle line blamed a regime that was released: %q", msg)
	}
	if got, _ := fields["buy_wave_probe_reason"].(string); got != string(common.WaveProbeReasonUnreachable) {
		t.Fatalf("payload does not carry which clause forced PROBE, got %v — PROBE otherwise has one name and several meanings", fields["buy_wave_probe_reason"])
	}
}

// A SPENDING TICK'S LINE IS UNTOUCHED: the wave text appears only on the HEAVY wave, so the
// ordinary cycle line an operator has learned to read is the same one it was.
func TestHeartbeat_ProbeWaveCycleLineNeverMentionsTheWave(t *testing.T) {
	msg, _ := cycleLine(t, parkedsensing.BuyReport{Wave: common.WaveProbe, Bought: 6, Queued: 5, Attempts: 6})

	if strings.Contains(msg, "heavy wave") {
		t.Fatalf("a spending tick's line mentions the wave: %q", msg)
	}
	if !strings.Contains(msg, "bought 6") {
		t.Fatalf("cycle line lost its bought count: %q", msg)
	}
}

// BOTH REGIMES ARE PUBLISHED TO THE GAUGE, every tick, under the drain's own reader label. The
// growth coordinator publishes its copy of the same series, and the failure the pair exists to
// catch is the two DISAGREEING — which neither reader can see alone, and which a drain that only
// published its HEAVY ticks would hide exactly half of.
func TestHeartbeat_PublishesTheDrainsWaveOnBothRegimes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rep    parkedsensing.BuyReport
		wave   common.Wave
		reason common.WaveProbeReason
	}{
		{"heavy", parkedsensing.BuyReport{Wave: common.WaveHeavy}, common.WaveHeavy, common.WaveProbeReasonNone},
		{"probe", parkedsensing.BuyReport{Wave: common.WaveProbe, WaveProbeReason: common.WaveProbeReasonLanesServed}, common.WaveProbe, common.WaveProbeReasonLanesServed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newFakeRecorder()
			h := &RunProbeSensingCoordinatorHandler{}
			h.SetMetricsRecorder(rec)
			h.heartbeat(common.WithLogger(t.Context(), &messageLogger{}),
				&RunProbeSensingCoordinatorCommand{ContainerID: "c1"}, sensingConfig{ProbeCap: 800},
				heartbeat{buy: tc.rep})

			got := rec.recordedWaves()
			if len(got) != 1 {
				t.Fatalf("the drain published %d waves this tick, want exactly 1 — a gap leaves the gauge standing at its last value and reads as a regime nobody chose", len(got))
			}
			if got[0].wave != tc.wave || got[0].reason != tc.reason {
				t.Fatalf("published %q/%q, want %q/%q", got[0].wave, got[0].reason, tc.wave, tc.reason)
			}
		})
	}
}

// A TICK THAT DERIVED NO REGIME PUBLISHES NOTHING. The gauge carries two values and both are
// claims; there is no third value meaning "we could not tell", so an invented PROBE would report a
// release the drain did not make — it aborted the tick and bought nothing.
func TestHeartbeat_PublishesNoWaveWhenNoneWasDerived(t *testing.T) {
	rec := newFakeRecorder()
	h := &RunProbeSensingCoordinatorHandler{}
	h.SetMetricsRecorder(rec)

	h.heartbeat(common.WithLogger(t.Context(), &messageLogger{}),
		&RunProbeSensingCoordinatorCommand{ContainerID: "c1"}, sensingConfig{ProbeCap: 800},
		heartbeat{buy: parkedsensing.BuyReport{}})

	if got := rec.recordedWaves(); len(got) != 0 {
		t.Fatalf("published %v for a tick that derived no regime, want nothing", got)
	}
}
