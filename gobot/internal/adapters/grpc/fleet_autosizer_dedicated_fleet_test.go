package grpc

import (
	"testing"

	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
)

// The class→dedicated-fleet mapping is the exclusivity contract: a bought hull is stamped for its
// fleet in the same breath so no other coordinator poaches it. HullClassContractDelivery MUST map to
// "contract" so the dedicated contract-scaler fleet is exclusive — before this fix the class fell
// through to "" (no tag), which is SAFE only because the class is opt-in default-off.
func TestAutosizerDedicatedFleet_MapsEveryClassToItsExclusiveFleet(t *testing.T) {
	cases := []struct {
		class fleetCmd.HullClass
		want  string
	}{
		{fleetCmd.HullClassLight, ""}, // a light IS a HAULER worker the moment it is bought — no tag
		{fleetCmd.HullClassHeavy, "trade"},
		{fleetCmd.HullClassExplorer, "explorer"},
		{fleetCmd.HullClassContractDelivery, "contract"}, // the contract-scaler exclusivity fix (was "")
	}
	for _, tc := range cases {
		if got := autosizerDedicatedFleet(tc.class); got != tc.want {
			t.Errorf("autosizerDedicatedFleet(%q) = %q, want %q", tc.class, got, tc.want)
		}
	}
}
