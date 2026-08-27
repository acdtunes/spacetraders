package trading

import "testing"

func TestSaturationPermilleStaysSilentWhileTheLimiterHasHeadroom(t *testing.T) {
	// Complementary slackness: an unbound constraint has a shadow price of zero, so below
	// the floor the estimator says nothing rather than "a little".
	p := DefaultAPISaturationParams()
	for _, util := range []float64{0, 12.5, 50, 74.9, APISaturationHeadroomFloorPct} {
		if got, ok := SaturationPermille(util, requests(p), p); ok || got != 0 {
			t.Fatalf("util %.1f%%: got (%d, %v), want (0, false)", util, got, ok)
		}
	}
}

func TestSaturationPermilleRisesLinearlyFromTheFloorToTheCeiling(t *testing.T) {
	// The reading is the proportion of headroom consumed, so weight shifts continuously.
	p := DefaultAPISaturationParams()
	floor := APISaturationHeadroomFloorPct
	cases := []struct {
		util float64
		want int
	}{
		{floor + (100-floor)*0.25, 250},
		{floor + (100-floor)*0.50, 500},
		{floor + (100-floor)*0.75, 750},
		{100, 1000},
	}
	for _, c := range cases {
		got, ok := SaturationPermille(c.util, requests(p), p)
		if !ok || got != c.want {
			t.Fatalf("util %.2f%%: got (%d, %v), want (%d, true)", c.util, got, ok, c.want)
		}
	}
}

func TestSaturationPermilleClampsAnOverCeilingReading(t *testing.T) {
	// Past the ceiling the limiter is fully bound, not worth more than everything else.
	p := DefaultAPISaturationParams()
	got, ok := SaturationPermille(140, requests(p), p)
	if !ok || got != 1000 {
		t.Fatalf("got (%d, %v), want (1000, true)", got, ok)
	}
}

func TestSaturationPermilleIsSilentOnThinOrDegenerateEvidence(t *testing.T) {
	// THE FAIL-OPEN SURFACE. A thin window, a negative reading, or a params shape that
	// describes nothing all yield no opinion, and selection ranks on credits/hour.
	p := DefaultAPISaturationParams()
	if got, ok := SaturationPermille(100, p.MinRequests-1, p); ok || got != 0 {
		t.Fatalf("thin window: got (%d, %v), want (0, false)", got, ok)
	}
	if got, ok := SaturationPermille(-5, requests(p), p); ok || got != 0 {
		t.Fatalf("negative util: got (%d, %v), want (0, false)", got, ok)
	}
	for _, bad := range []APISaturationParams{
		{},
		{HeadroomFloorPct: 100, MinRequests: 10},
		{HeadroomFloorPct: 120, MinRequests: 10},
		{HeadroomFloorPct: -1, MinRequests: 10},
		{HeadroomFloorPct: 75, MinRequests: 0},
	} {
		if got, ok := SaturationPermille(100, 10_000, bad); ok || got != 0 {
			t.Fatalf("params %+v: got (%d, %v), want (0, false)", bad, got, ok)
		}
	}
}

// requests returns a request count comfortably above the params' evidence floor.
func requests(p APISaturationParams) int { return p.MinRequests * 10 }
