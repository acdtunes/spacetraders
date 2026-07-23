package grpc

import (
	"testing"

	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
)

// sp-0ms61 wiring: the optional --fleet role MUST round-trip through the persisted
// container launch config into the built BatchPurchaseShipsCommand, or the container's
// atomic buy+dedicate never fires and the hull lands undedicated (reclaimable) — the exact
// operator-intent-drift orphan this bead closes. A missing/misnamed config key would
// silently default DedicateFleet to "" and go build cannot catch it, so this pins the key
// name. A present key reaches the command; an absent one leaves it "" (byte-identical plain
// purchase; reloadable on restart, RULINGS #2).
func TestBuildBatchPurchaseShipsCommand_RoundTripsDedicateFleet(t *testing.T) {
	base := map[string]interface{}{
		"ship_symbol": "TORWIND-1",
		"ship_type":   "SHIP_HEAVY_FREIGHTER",
		"quantity":    5,
		"max_budget":  0,
	}
	cases := []struct {
		name        string
		setFleet    bool
		fleetValue  string
		expectFleet string
	}{
		{"fleet_reaches_the_command", true, "trade", "trade"},
		{"absent_leaves_it_empty", false, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := map[string]interface{}{}
			for key, value := range base {
				cfg[key] = value
			}
			if tc.setFleet {
				cfg["dedicate_fleet"] = tc.fleetValue
			}

			built := buildBatchPurchaseShipsCommand(newConfigReader(cfg), 4, "batch-0ms61")
			cmd, ok := built.(*shipyardCmd.BatchPurchaseShipsCommand)
			if !ok {
				t.Fatalf("expected *BatchPurchaseShipsCommand, got %T", built)
			}
			if cmd.DedicateFleet != tc.expectFleet {
				t.Fatalf("expected DedicateFleet %q to round-trip from config, got %q", tc.expectFleet, cmd.DedicateFleet)
			}
		})
	}
}
