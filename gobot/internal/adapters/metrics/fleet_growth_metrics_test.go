package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// gaugeSamples collects one GaugeVec into label→value pairs, so a test can assert both the value
// and the LABEL SET a series was published under.
func gaugeSamples(t *testing.T, vec *prometheus.GaugeVec) map[string]float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 32)
	vec.Collect(ch)
	close(ch)

	out := map[string]float64{}
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatalf("write metric: %v", err)
		}
		key := ""
		for _, lp := range pb.GetLabel() {
			key += lp.GetName() + "=" + lp.GetValue() + ";"
		}
		out[key] = pb.GetGauge().GetValue()
	}
	return out
}

// THE HEAVY PATH MUST CARRY A NON-EMPTY REASON LABEL. In Go the "no reason" constant is the empty
// string, which is correct there — but a PromQL selector reason="" also matches every series that
// lacks the label entirely, so an empty label value conflates "the wave was HEAVY" with "this
// series was never labelled". That is precisely the discrimination the reason series exists to
// provide, so the empty reason is mapped at this boundary.
func TestRecordWave_HeavyPublishesTheNoneReasonLabelNotAnEmptyOne(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()

	c.RecordWave("1", WaveReaderGrowth, true, "")

	got := gaugeSamples(t, c.waveProbeReason)
	if v, ok := got["player_id=1;reader=growth;reason=none;"]; !ok || v != 1 {
		t.Fatalf("expected reason=none set to 1, got %v", got)
	}
	if _, ok := got["player_id=1;reader=growth;reason=;"]; ok {
		t.Fatalf("an EMPTY reason label was published — PromQL cannot tell it from a missing label: %v", got)
	}
}

// A named PROBE reason is published verbatim: the mapping touches ONLY the empty value.
func TestRecordWave_NamedProbeReasonIsPublishedVerbatim(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()

	c.RecordWave("1", WaveReaderGrowth, false, "unreachable")

	got := gaugeSamples(t, c.waveProbeReason)
	if v, ok := got["player_id=1;reader=growth;reason=unreachable;"]; !ok || v != 1 {
		t.Fatalf("expected reason=unreachable set to 1, got %v", got)
	}
}

// A SUPERSEDED REASON MUST BE DRIVEN TO 0, not left standing. A gauge that only ever sets the
// current reason leaves the previous one reading 1 forever, and an operator would then see two
// reasons claiming the same tick.
func TestRecordWave_SupersededReasonIsZeroedNotLeftStanding(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()

	c.RecordWave("1", WaveReaderGrowth, false, "lanes_served")
	c.RecordWave("1", WaveReaderGrowth, false, "unreachable")

	got := gaugeSamples(t, c.waveProbeReason)
	if v, ok := got["player_id=1;reader=growth;reason=lanes_served;"]; !ok || v != 0 {
		t.Fatalf("the superseded reason must read 0, got %v", got)
	}
	if v := got["player_id=1;reader=growth;reason=unreachable;"]; v != 1 {
		t.Fatalf("the current reason must read 1, got %v", got)
	}
}

// THE TWO READERS ARE SEPARATE SERIES ON ONE GAUGE. That is the whole point of the reader label:
// a divergence between the coordinator and the drain is visible directly rather than inferred.
func TestRecordWave_BothReadersPublishIndependentSeries(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()

	c.RecordWave("1", WaveReaderGrowth, true, "")
	c.RecordWave("1", WaveReaderDrain, false, "")

	got := gaugeSamples(t, c.wave)
	if v, ok := got["player_id=1;reader=growth;"]; !ok || v != 1 {
		t.Fatalf("expected the growth reader at HEAVY(1), got %v", got)
	}
	if v, ok := got["player_id=1;reader=drain;"]; !ok || v != 0 {
		t.Fatalf("expected the drain reader at PROBE(0), got %v", got)
	}
}

// Reasons are scoped per player: one player's PROBE reason must never zero another's.
func TestRecordWave_ReasonsAreScopedPerPlayer(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()

	c.RecordWave("1", WaveReaderGrowth, false, "lanes_served")
	c.RecordWave("2", WaveReaderGrowth, false, "unreachable")

	got := gaugeSamples(t, c.waveProbeReason)
	if got["player_id=1;reader=growth;reason=lanes_served;"] != 1 {
		t.Fatalf("player 1's reason was disturbed by player 2, got %v", got)
	}
	if got["player_id=2;reader=growth;reason=unreachable;"] != 1 {
		t.Fatalf("player 2's reason is missing, got %v", got)
	}
}

// THE REASON SERIES IS PER READER, exactly like the wave beside it, and this is the assertion that
// makes the second reader safe to add. Two consumers writing one {player,reason} series each drive
// the other's live reason to 0 on their own tick: the drain would report "no reason" for a
// coordinator that had just published one, and an operator reading either would see a reason
// flapping at the beat frequency of two tick periods.
//
// It also buys the diagnosis the wave gauge alone cannot give. fleet_growth_wave says THAT the two
// disagree; a per-reader reason says WHICH input they saw differently.
func TestRecordWave_EachReaderKeepsItsOwnProbeReason(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()

	c.RecordWave("1", WaveReaderGrowth, false, "lanes_served")
	c.RecordWave("1", WaveReaderDrain, false, "unreachable")

	got := gaugeSamples(t, c.waveProbeReason)
	if got["player_id=1;reader=growth;reason=lanes_served;"] != 1 {
		t.Fatalf("the growth reader's reason was zeroed by the drain, got %v", got)
	}
	if got["player_id=1;reader=drain;reason=unreachable;"] != 1 {
		t.Fatalf("the drain reader's reason is missing, got %v", got)
	}
}

// A reader's OWN superseded reason is still driven to 0 — the per-reader scoping narrows the
// zeroing, it does not switch it off. Without this the fix above would trade one lingering series
// for two.
func TestRecordWave_SupersededReasonIsZeroedWithinOneReader(t *testing.T) {
	c := NewFleetGrowthMetricsCollector()

	c.RecordWave("1", WaveReaderDrain, false, "lanes_served")
	c.RecordWave("1", WaveReaderDrain, true, "")

	got := gaugeSamples(t, c.waveProbeReason)
	if got["player_id=1;reader=drain;reason=lanes_served;"] != 0 {
		t.Fatalf("the drain's superseded reason must read 0, got %v", got)
	}
	if got["player_id=1;reader=drain;reason=none;"] != 1 {
		t.Fatalf("the drain's current reason must read 1, got %v", got)
	}
}
