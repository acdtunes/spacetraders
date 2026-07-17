package api

import (
	"context"
	"testing"
)

// Endpoint-name -> tier classification. Trade-critical writes are HIGH; the named
// deferrable status/discovery reads are LOW; everything else, INCLUDING unknown
// endpoints, is NORMAL (never accidentally LOW/starved, never wrongly HIGH).
func TestPriorityForEndpoint_ClassifiesByTier(t *testing.T) {
	cases := []struct {
		endpoint string
		want     Priority
	}{
		// HIGH: the ~8% of calls that actually move credits.
		{"Buy Cargo", PriorityHigh},
		{"Sell Cargo", PriorityHigh},
		// LOW: deferrable status/discovery polls.
		{"Get Agent", PriorityLow},
		{"Get Construction", PriorityLow},
		{"Get Ship", PriorityLow},
		{"Get Jump Gate", PriorityLow},
		{"Get Shipyard", PriorityLow},
		{"Get Waypoint", PriorityLow},
		{"List Ships", PriorityLow},
		{"List Systems", PriorityLow},
		{"List Waypoints", PriorityLow},
		// NORMAL: necessary overhead that is NOT a deferrable poll.
		{"Navigate", PriorityNormal},
		{"Dock", PriorityNormal},
		{"Orbit", PriorityNormal},
		{"Refuel", PriorityNormal},
		{"Get Market", PriorityNormal}, // trade-enabling read stays NORMAL, never deprioritised
		{"Get Cooldown", PriorityNormal},
		// NORMAL: unknown / unclassified endpoints default here.
		{"Totally Unknown Endpoint", PriorityNormal},
		{"/some/unmapped/pattern", PriorityNormal},
		{"", PriorityNormal},
	}
	for _, tc := range cases {
		if got := priorityForEndpoint(tc.endpoint); got != tc.want {
			t.Errorf("priorityForEndpoint(%q) = %v, want %v", tc.endpoint, got, tc.want)
		}
	}
}

// The tiers must line up with the ACTUAL human-readable names the real endpoint
// classifier emits for real SpaceTraders paths — guards against a name drift
// (e.g. "Purchase Cargo" vs "Buy Cargo") silently mis-tiering trade calls.
func TestPriorityForRealApiPaths(t *testing.T) {
	cases := []struct {
		path string
		want Priority
	}{
		{"/my/ships/TORWIND-1/purchase", PriorityHigh},                     // Buy Cargo
		{"/my/ships/TORWIND-1/sell", PriorityHigh},                         // Sell Cargo
		{"/my/agent", PriorityLow},                                         // Get Agent
		{"/systems/X1-FB5/waypoints/X1-FB5-I61/construction", PriorityLow}, // Get Construction
		{"/my/ships/TORWIND-1", PriorityLow},                               // Get Ship
		{"/systems/X1-FB5/waypoints/X1-FB5-I61/jump-gate", PriorityLow},    // Get Jump Gate
		{"/my/ships/TORWIND-1/navigate", PriorityNormal},                   // Navigate
		{"/my/ships/TORWIND-1/dock", PriorityNormal},                       // Dock
		{"/systems/X1-FB5/waypoints/X1-FB5-I61/market", PriorityNormal},    // Get Market
	}
	for _, tc := range cases {
		endpoint := apiEndpointClassifier.classify(tc.path)
		if got := priorityForEndpoint(endpoint); got != tc.want {
			t.Errorf("path %q classified as %q -> %v, want %v", tc.path, endpoint, got, tc.want)
		}
	}
}

// An explicit WithPriority tag OVERRIDES endpoint classification in both
// directions. This is how a trade tour marks its enabling dock/navigate steps
// HIGH, and how a spend-gate marks a Get Agent read it is blocked on HIGH.
func TestPriorityForRequest_ContextOverrideWins(t *testing.T) {
	base := context.Background()

	// Explicit HIGH promotes a call whose endpoint would classify LOW.
	if got := priorityForRequest(WithPriority(base, PriorityHigh), "Get Agent"); got != PriorityHigh {
		t.Fatalf("explicit HIGH override on Get Agent = %v, want HIGH", got)
	}
	// Override is unconditional, so it also works downward.
	if got := priorityForRequest(WithPriority(base, PriorityLow), "Buy Cargo"); got != PriorityLow {
		t.Fatalf("explicit LOW override on Buy Cargo = %v, want LOW", got)
	}
	// With no override, classification falls back to the endpoint tier.
	if got := priorityForRequest(base, "Buy Cargo"); got != PriorityHigh {
		t.Fatalf("no override on Buy Cargo = %v, want HIGH", got)
	}
	if got := priorityForRequest(base, "Get Agent"); got != PriorityLow {
		t.Fatalf("no override on Get Agent = %v, want LOW", got)
	}
	if got := priorityForRequest(base, "Totally Unknown"); got != PriorityNormal {
		t.Fatalf("no override on unknown endpoint = %v, want NORMAL", got)
	}
}
