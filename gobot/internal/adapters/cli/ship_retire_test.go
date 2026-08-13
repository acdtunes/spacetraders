package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// The line an operator reads after marking a hull decides what they do next, and the next
// step is scrapping — which destroys whatever is still in the hold. So it may call a hull
// ready only when the hold is actually empty, and must say what is still aboard when it is
// not.
func TestRetireOutcomeLines_NeverCallALadenHullReady(t *testing.T) {
	cases := []struct {
		name       string
		resp       *pb.RetireShipResponse
		wantSub    string
		wantNoSubs []string
	}{
		{
			name:       "laden hull is retiring but not ready",
			resp:       &pb.RetireShipResponse{ShipSymbol: "TORWIND-1E", Retiring: true, CargoUnits: 20},
			wantSub:    "20 unit",
			wantNoSubs: []string{"ready to scrap", "drained"},
		},
		{
			name:    "empty hull is drained and ready",
			resp:    &pb.RetireShipResponse{ShipSymbol: "TORWIND-1E", Retiring: true, Drained: true},
			wantSub: "ready to scrap",
		},
		{
			name:       "cancelled retirement returns it to service",
			resp:       &pb.RetireShipResponse{ShipSymbol: "TORWIND-1E", CargoUnits: 20},
			wantSub:    "normal service",
			wantNoSubs: []string{"ready to scrap"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := strings.Join(retireOutcomeLines(tc.resp), "\n")

			require.Contains(t, out, tc.wantSub)
			require.Contains(t, out, "TORWIND-1E")
			for _, no := range tc.wantNoSubs {
				require.NotContains(t, out, no)
			}
		})
	}
}
