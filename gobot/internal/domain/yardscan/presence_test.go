package yardscan

import "testing"

// presence_test.go pins the two decisions this half of the package makes: which
// yards are worth a HULL (as opposed to another read), and in what order.

func TestWantsPresence_OnlyAConfirmedSellerAtAnUnknownPrice(t *testing.T) {
	cases := []struct {
		name  string
		facts Facts
		want  bool
	}{
		{
			name:  "confirmed seller, never priced — the only case worth a hull",
			facts: Facts{SellsWanted: true},
			want:  true,
		},
		{
			name: "already priced — a read answered it, presence buys nothing",
			// Presence buys exactly one thing: the listings array. A yard that has
			// already handed it over would cost a hull to learn nothing.
			facts: Facts{SellsWanted: true, Priced: true},
			want:  false,
		},
		{
			name: "unopened catalogue — the FREE pass answers this for one request",
			// Unknown outranks a dull yard for a READ precisely because it might
			// hold a heavy. It must never justify a hull: the presence-less
			// catalogue sweep resolves it at no cost, and admitting it here would
			// fly hulls at counters selling nothing we want.
			facts: Facts{Unknown: true},
			want:  false,
		},
		{
			name:  "unopened catalogue that somehow also claims to sell wanted",
			facts: Facts{Unknown: true, SellsWanted: true},
			want:  false,
		},
		{
			name:  "scanned, sells nothing we want",
			facts: Facts{},
			want:  false,
		},
		{
			name: "targeted but priced — a money guard is buying here already",
			// Targeted raises a yard's READ weight so the rotation keeps it warm.
			// It must not conjure a presence request for a yard we can already see.
			facts: Facts{SellsWanted: true, Priced: true, Targeted: true},
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WantsPresence(tc.facts); got != tc.want {
				t.Fatalf("WantsPresence(%+v) = %v, want %v", tc.facts, got, tc.want)
			}
		})
	}
}

// TestRankPresence_HeavyOutranksEverything. One unpriced heavy counter is worth
// more than every probe yard on the map: the incident behind this work had the
// fleet paying up to 2,288,156 against a visible cheapest of 1,918,293.
//
// The fixture gives the probe yards the HIGHER read weight, so a ranking that
// merely sorted on weight would put them first and this test would catch it.
func TestRankPresence_HeavyOutranksEverything(t *testing.T) {
	got := RankPresence([]PresenceRequest{
		{Waypoint: "X1-A-Y1", Weight: 8},
		{Waypoint: "X1-B-Y1", Weight: 8},
		{Waypoint: "X1-C-Y1", Weight: 2, Heavy: true},
		{Waypoint: "X1-D-Y1", Weight: 8},
	})
	if len(got) != 4 {
		t.Fatalf("expected four requests, got %d", len(got))
	}
	if !got[0].Heavy {
		t.Fatalf("a heavy seller must head the queue even at a lower read weight, got %+v", got[0])
	}
	if got[0].Waypoint != "X1-C-Y1" {
		t.Fatalf("expected the heavy yard first, got %s", got[0].Waypoint)
	}
}

// TestRankPresence_WeightBreaksTiesInsideAClass, so presence inherits the read
// budget's own ordering — including Targeted — rather than inventing a second one.
func TestRankPresence_WeightBreaksTiesInsideAClass(t *testing.T) {
	got := RankPresence([]PresenceRequest{
		{Waypoint: "X1-A-Y1", Weight: 2},
		{Waypoint: "X1-B-Y1", Weight: 8},
		{Waypoint: "X1-C-Y1", Weight: 4},
	})
	want := []string{"X1-B-Y1", "X1-C-Y1", "X1-A-Y1"}
	for i, w := range want {
		if got[i].Waypoint != w {
			t.Fatalf("position %d: got %s, want %s (full order %+v)", i, got[i].Waypoint, w, got)
		}
	}
}

// TestRankPresence_IsTotalAndReproducible. Equal-ranked yards are the COMMON case
// — every non-heavy unpriced seller sits at the same clamp — and sort.Slice is not
// stable, so without a symbol tie-break the head of the queue would vary between
// two runs over the same rows and the bounded pick would depend on store order.
func TestRankPresence_IsTotalAndReproducible(t *testing.T) {
	in := []PresenceRequest{
		{Waypoint: "X1-D-Y1", Weight: 8},
		{Waypoint: "X1-A-Y1", Weight: 8},
		{Waypoint: "X1-C-Y1", Weight: 8},
		{Waypoint: "X1-B-Y1", Weight: 8},
	}
	first := RankPresence(in)
	for i := 0; i < 20; i++ {
		again := RankPresence(in)
		for j := range first {
			if first[j].Waypoint != again[j].Waypoint {
				t.Fatalf("ranking is not reproducible: run %d position %d gave %s, first run gave %s",
					i, j, again[j].Waypoint, first[j].Waypoint)
			}
		}
	}
	want := []string{"X1-A-Y1", "X1-B-Y1", "X1-C-Y1", "X1-D-Y1"}
	for i, w := range want {
		if first[i].Waypoint != w {
			t.Fatalf("position %d: got %s, want %s", i, first[i].Waypoint, w)
		}
	}
}

// TestRankPresence_DoesNotMutateItsInput. The caller holds the budget's own slice
// and must not find it reordered underneath it.
func TestRankPresence_DoesNotMutateItsInput(t *testing.T) {
	in := []PresenceRequest{
		{Waypoint: "X1-D-Y1", Weight: 2},
		{Waypoint: "X1-A-Y1", Weight: 8, Heavy: true},
	}
	RankPresence(in)
	if in[0].Waypoint != "X1-D-Y1" || in[1].Waypoint != "X1-A-Y1" {
		t.Fatalf("input was reordered: %+v", in)
	}
}

// TestRankPresence_DropsEmptyAndDuplicateWaypoints. A waypoint listed twice would
// claim two of the dispatcher's bounded slots for one counter.
func TestRankPresence_DropsEmptyAndDuplicateWaypoints(t *testing.T) {
	got := RankPresence([]PresenceRequest{
		{Waypoint: "X1-A-Y1", Weight: 8},
		{Waypoint: "", Weight: 8},
		{Waypoint: "X1-A-Y1", Weight: 8},
		{Waypoint: "X1-B-Y1", Weight: 8},
	})
	if len(got) != 2 {
		t.Fatalf("expected two distinct requests, got %d: %+v", len(got), got)
	}
}

// TestRankPresence_EmptyInput is the boot case: nothing known, nothing requested.
func TestRankPresence_EmptyInput(t *testing.T) {
	if got := RankPresence(nil); len(got) != 0 {
		t.Fatalf("expected no requests, got %+v", got)
	}
}
