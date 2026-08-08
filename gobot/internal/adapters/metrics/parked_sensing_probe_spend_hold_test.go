package metrics

import "testing"

// A HELD REASON PUBLISHES 1 UNDER ITS OWN LABEL SET, per player.
func TestRecordProbeSpendHold_HeldReasonPublishesOne(t *testing.T) {
	c := NewParkedSensingMetricsCollector()

	c.RecordProbeSpendHold(1, "heavy_capped_ample_depth", true)

	got := gaugeSamples(t, c.probeSpendHold)
	if v := got["player_id=1;reason=heavy_capped_ample_depth;"]; v != 1 {
		t.Fatalf("held reason published %v, want 1 (%v)", v, got)
	}
}

// A LIFTED HOLD FALLS TO 0 ON THE SAME SERIES, and this is the property the whole
// write-every-reason-every-tick contract exists for. A gauge left standing at 1 after the cap is
// raised claims the fleet is still refusing to spend — the one lie this series must never tell,
// because an operator would then raise a cap that is already raised.
func TestRecordProbeSpendHold_LiftedHoldFallsBackToZero(t *testing.T) {
	c := NewParkedSensingMetricsCollector()

	c.RecordProbeSpendHold(1, "heavy_capped_ample_depth", true)
	c.RecordProbeSpendHold(1, "heavy_capped_ample_depth", false)

	got := gaugeSamples(t, c.probeSpendHold)
	if v := got["player_id=1;reason=heavy_capped_ample_depth;"]; v != 0 {
		t.Fatalf("lifted hold still reads %v, want 0 (%v)", v, got)
	}
}

// PLAYERS DO NOT SHARE A SERIES. One player's cap says nothing about another's, and a shared series
// would have each tick overwrite the other's verdict.
func TestRecordProbeSpendHold_ScopedPerPlayer(t *testing.T) {
	c := NewParkedSensingMetricsCollector()

	c.RecordProbeSpendHold(1, "heavy_capped_ample_depth", true)
	c.RecordProbeSpendHold(2, "heavy_capped_ample_depth", false)

	got := gaugeSamples(t, c.probeSpendHold)
	if got["player_id=1;reason=heavy_capped_ample_depth;"] != 1 || got["player_id=2;reason=heavy_capped_ample_depth;"] != 0 {
		t.Fatalf("players are sharing a series: %v", got)
	}
}

// A NIL COLLECTOR RECORDS NOTHING AND PANICS AT NOTHING — the package's nil-safe contract, and
// RULINGS #4: observation must never be able to fail a reconcile.
func TestRecordProbeSpendHold_NilCollectorIsSafe(t *testing.T) {
	var c *ParkedSensingMetricsCollector
	c.RecordProbeSpendHold(1, "heavy_capped_ample_depth", true)
}
