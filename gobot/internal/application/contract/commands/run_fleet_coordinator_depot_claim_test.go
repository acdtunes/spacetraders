package commands

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
)

// sp-3l64 behavior 2 (the claim half of the exclusion): a depot delivery hull carries the
// DISTINCT depot.DeliveryHullFleet tag so the coordinator's discovery can never re-grab it, and
// it is dispatched ONLY via routeContractViaDepot. That depot-routed claim must run under the
// hull's OWN dedication, or ClaimShip's guard (DedicatedFleet != "" && DedicatedFleet !=
// operation) would REJECT the very hull the route selected — breaking depot delivery entirely.
// Every other hull still claims under the coordinator's "contract" identity, so a foreign-pinned
// hull is still rejected, never poached. One parametrized test covers the claim-identity decision
// (Mandate 5: input variations of one behavior).
func TestContractClaimFleet_DepotDeliveryHullClaimsUnderOwnDedication(t *testing.T) {
	cases := []struct {
		name           string
		dedicatedFleet string
		want           string
	}{
		{"unpinned hull claims under the coordinator's contract identity", "", dedicatedFleetContract},
		{"contract-pinned hull claims under contract (unchanged)", dedicatedFleetContract, dedicatedFleetContract},
		{
			"depot delivery hull claims under its OWN depot-delivery dedication so routeContractViaDepot passes the ClaimShip guard",
			depot.DeliveryHullFleet, depot.DeliveryHullFleet,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, contractClaimFleet(tc.dedicatedFleet))
		})
	}
}
