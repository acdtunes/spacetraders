package parkedsensing

import "testing"

// TestFleetMedianSpread pins the yardstick the weights normalise against. Only
// slots with a real observation count; a rotation of nothing but unmeasured
// slots falls back to 1.0, which hands every one of them the optimistic prior
// rather than a zero it never earned.
func TestFleetMedianSpread(t *testing.T) {
	tests := []struct {
		name    string
		spreads []float64
		want    float64
	}{
		{"odd count takes the middle", []float64{0.1, 0.5, 0.3}, 0.3},
		{"even count averages the two middles", []float64{0.1, 0.2, 0.4, 0.5}, 0.3},
		{"unmeasured slots are excluded", []float64{0, 0, 0.7, 0.3}, 0.5},
		{"nothing measured falls back to 1.0", []float64{0, 0}, 1.0},
		{"empty rotation falls back to 1.0", nil, 1.0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			slots := make([]SensingSlotView, 0, len(tc.spreads))
			for i, s := range tc.spreads {
				slots = append(slots, parkedMarket(waypointN(i), s, scanEpoch))
			}
			if got := fleetMedianSpread(slots); !nearly(got, tc.want) {
				t.Errorf("fleetMedianSpread = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRelativeSpread pins the observation the EWMA is fed: the mean relative
// bid-ask gap over the goods a slot exists to watch.
func TestRelativeSpread(t *testing.T) {
	tests := []struct {
		name         string
		prices       []GoodPrice
		whitelist    []string
		want         float64
		wantInverted int
	}{
		{
			name:      "single good is its own relative spread",
			prices:    []GoodPrice{{Good: "FUEL", Bid: 90, Ask: 110}},
			whitelist: []string{"FUEL"},
			want:      0.2, // 20/100
		},
		{
			name: "whitelist goods are averaged",
			prices: []GoodPrice{
				{Good: "FUEL", Bid: 90, Ask: 110},
				{Good: "GOLD", Bid: 100, Ask: 100},
			},
			whitelist: []string{"FUEL", "GOLD"},
			want:      0.1, // (0.2 + 0.0) / 2
		},
		{
			name: "goods outside the whitelist are ignored",
			prices: []GoodPrice{
				{Good: "FUEL", Bid: 90, Ask: 110},
				{Good: "JUNK", Bid: 1, Ask: 1000},
			},
			whitelist: []string{"FUEL"},
			want:      0.2,
		},
		{
			name: "non-positive prices disqualify a good",
			prices: []GoodPrice{
				{Good: "FUEL", Bid: 90, Ask: 110},
				{Good: "GOLD", Bid: 0, Ask: 500},
			},
			whitelist: []string{"FUEL", "GOLD"},
			want:      0.2, // GOLD contributes nothing, and does not dilute
		},
		{
			name:      "a zero-margin market is a real reading, not bad data",
			prices:    []GoodPrice{{Good: "FUEL", Bid: 100, Ask: 100}},
			whitelist: []string{"FUEL"},
			want:      0,
		},
		{
			name:      "a whitelist good the market stopped dealing in observes zero",
			prices:    []GoodPrice{{Good: "OTHER", Bid: 10, Ask: 20}},
			whitelist: []string{"FUEL"},
			want:      0,
		},
		{
			name:      "no rows at all observes zero",
			prices:    nil,
			whitelist: []string{"FUEL"},
			want:      0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, inverted := RelativeSpread(tc.prices, tc.whitelist)
			if !nearly(got, tc.want) {
				t.Errorf("RelativeSpread = %v, want %v", got, tc.want)
			}
			if inverted != tc.wantInverted {
				t.Errorf("inverted = %d, want %d", inverted, tc.wantInverted)
			}
		})
	}
}

// TestRelativeSpread_InvertedQuotesAreRejectedNotAveraged is the guard on the
// one fault this model cannot otherwise detect. A GoodPrice wired straight from
// the persisted columns without crossing them inverts every quote, and a
// negative spread would then look merely uninteresting: the EWMA adopts it,
// ScanWeight reads it as no history, and the whole rotation silently flattens.
// Impossible data must be rejected and COUNTED, so the scanner can say so.
func TestRelativeSpread_InvertedQuotesAreRejectedNotAveraged(t *testing.T) {
	// What market_data holds for a normal market, wired uncrossed: every good
	// comes back with its ask below its bid.
	miswired := []GoodPrice{
		{Good: "FUEL", Bid: 110, Ask: 90},
		{Good: "GOLD", Bid: 220, Ask: 180},
	}

	got, inverted := RelativeSpread(miswired, []string{"FUEL", "GOLD"})
	if got < 0 {
		t.Errorf("RelativeSpread = %v; a negative spread is what makes a miswire invisible", got)
	}
	if !nearly(got, 0) {
		t.Errorf("RelativeSpread = %v, want 0 with every good rejected", got)
	}
	if inverted != 2 {
		t.Fatalf("inverted = %d, want both goods reported so the miswire can be logged", inverted)
	}

	// A single bad good must not poison the goods that ARE quoted sanely.
	got, inverted = RelativeSpread([]GoodPrice{
		{Good: "FUEL", Bid: 90, Ask: 110},
		{Good: "GOLD", Bid: 220, Ask: 180},
	}, []string{"FUEL", "GOLD"})
	if !nearly(got, 0.2) {
		t.Errorf("RelativeSpread = %v, want the sane good's 0.2 undiluted", got)
	}
	if inverted != 1 {
		t.Errorf("inverted = %d, want 1", inverted)
	}
}

func waypointN(i int) string {
	return "X1-AA-M" + string(rune('A'+i))
}

func nearly(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}
