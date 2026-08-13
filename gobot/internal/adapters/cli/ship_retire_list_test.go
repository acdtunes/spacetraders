package cli

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// `ship list` is where an operator reads a retirement: the FLEET column names the mark and
// whether the hull has DRAINED, alongside the CARGO column that decides it. Drained is the
// scrap-ready signal — the guard on the scrap verb refuses anything still laden. An
// unmarked hull renders exactly as before, so the roster is unchanged until one is marked.
func TestBuildShipRows_FleetColumnReportsRetirement(t *testing.T) {
	cases := []struct {
		name       string
		retiring   bool
		fleet      string
		cargoUnits int32
		want       string
	}{
		{name: "unmarked hull is unchanged", fleet: "trade", want: "trade"},
		{name: "unmarked undedicated hull is unchanged", want: "-"},
		{name: "retiring but still laden", retiring: true, fleet: "trade", cargoUnits: 20, want: "trade (retiring)"},
		{name: "retiring and drained", retiring: true, fleet: "trade", want: "trade (retiring, drained)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			ships := []*pb.ShipInfo{{
				Symbol: "SHIP-1", Location: "X1-A1", NavStatus: "IN_ORBIT",
				CargoUnits: tc.cargoUnits, CargoCapacity: 40,
			}}
			infos := map[string]persistence.ShipAssignmentInfo{
				"SHIP-1": {ShipSymbol: "SHIP-1", DedicatedFleet: tc.fleet, Retiring: tc.retiring},
			}

			rows := buildShipRows(ships, infos, now)

			require.Equal(t, tc.want, rows[0].Fleet)
		})
	}
}
