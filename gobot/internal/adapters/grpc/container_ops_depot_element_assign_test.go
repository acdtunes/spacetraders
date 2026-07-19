package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-udgc NEVER-POACH (RULINGS #7, generalized to depot-launch) — SUPERSEDES the earlier sp-3l64
// behavior of adopting a foreign-fleet ("contract"/"manufacturing") hull into a depot role.
// positionDepotElementHull no longer poaches a hull already dedicated to a DIFFERENT fleet: an
// operator's/existing dedication (e.g. the Admiral moved a former depot-crew light to "trade") wins
// over the depot topology's naming, so a daemon restart never overrides an existing assignment (the
// Admiral's invariant). Only an UNDEDICATED hull (the cold-start bootstrap/reconciler provisioning
// norm) is crewed. These tests assert both halves against the REAL ship repository (an ADAPTER,
// integration-tested — no mocked persistence), reusing the delivery harness.

// A warehouse/stocker hull already dedicated to a FOREIGN fleet — "trade" (the Admiral's explicit
// dedication, the durability case), or a coordinator pool like "contract"/"manufacturing" — is LEFT
// ALONE: not re-dedicated, its work-claim not severed, not repositioned. The element goes uncrewed
// (crewed=false) rather than steal an already-assigned hull. This is what makes the freed VB74 lights
// durable across the boot re-crew.
func TestPositionDepotElementHull_DoesNotPoachForeignDedicatedHull(t *testing.T) {
	const waypoint = "X1-VB74-J58"
	cases := []struct {
		name      string
		roleFleet string
		priorTag  string
		active    bool // true => a live foreign work-claim (the "mid-task at assign time" shape)
	}{
		{"warehouse must not poach a trade-dedicated hull (the Admiral's VB74 decision)", operationWarehouse, operationTrade, false},
		{"stocker must not poach a trade-dedicated hull", operationStocker, operationTrade, true},
		{"warehouse must not poach an idle contract-tagged hull", operationWarehouse, "contract", false},
		{"stocker must not poach a busy manufacturing hull", operationStocker, "manufacturing", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s, db, playerID, navCalls := newDepotDeliveryTestServer(t)
			const hull = "HULL-1"
			insertDepotDeliveryHull(t, db, hull, playerID, tc.priorTag, "X1-VB74-I55", tc.active)

			ship, crewed, err := s.positionDepotElementHull(context.Background(), hull, waypoint, tc.roleFleet, false, playerID)
			require.NoError(t, err)
			require.False(t, crewed, "a foreign-dedicated hull is not crewed — the element goes uncrewed")
			require.NotNil(t, ship)

			var got persistence.ShipModel
			require.NoError(t, db.First(&got, "ship_symbol = ? AND player_id = ?", hull, playerID).Error)
			require.Equal(t, tc.priorTag, got.DedicatedFleet,
				"the hull keeps its existing dedication — depot-launch does not override it (never-poach)")
			if tc.active {
				require.Equal(t, "active", got.AssignmentStatus, "its live work-claim is untouched — never yanked")
				require.NotNil(t, got.ContainerID, "the hull still belongs to its prior container")
			}
			require.Empty(t, *navCalls, "a hull left dedicated elsewhere is not repositioned")
		})
	}
}

// An UNDEDICATED warehouse/stocker hull (the cold-start bootstrap/reconciler provisioning norm) IS
// crewed byte-identically to before: re-dedicated to its role's own coordinator fleet so the SAME tag
// both excludes it from the contract grab AND satisfies the coordinator's operation-checked claim.
// This proves the never-poach guard does NOT break legitimate cold-start crewing.
func TestPositionDepotElementHull_CrewsUndedicatedHullToRoleFleet(t *testing.T) {
	const waypoint = "X1-VB74-J58"
	for _, roleFleet := range []string{operationWarehouse, operationStocker} {
		roleFleet := roleFleet
		t.Run(roleFleet, func(t *testing.T) {
			s, db, playerID, navCalls := newDepotDeliveryTestServer(t)
			const hull = "HULL-FRESH"
			insertDepotDeliveryHull(t, db, hull, playerID, "", "X1-VB74-I55", false) // UNDEDICATED, idle
			pid := shared.MustNewPlayerID(playerID)

			ship, crewed, err := s.positionDepotElementHull(context.Background(), hull, waypoint, roleFleet, false, playerID)
			require.NoError(t, err)
			require.True(t, crewed, "an undedicated hull is crewed for the depot role")
			require.NotNil(t, ship)

			var got persistence.ShipModel
			require.NoError(t, db.First(&got, "ship_symbol = ? AND player_id = ?", hull, playerID).Error)
			require.Equal(t, roleFleet, got.DedicatedFleet,
				"an undedicated hull is re-dedicated to its role's own coordinator fleet")

			_, contractPool, err := appContract.FindIdleShipsByFleet(context.Background(), pid, s.shipRepo, "contract")
			require.NoError(t, err)
			require.NotContains(t, contractPool, hull, "a crewed depot hull is excluded from the contract coordinator's pool")
			_, generalPool, err := appContract.FindIdleLightHaulers(context.Background(), pid, s.shipRepo, "")
			require.NoError(t, err)
			require.NotContains(t, generalPool, hull, "a crewed depot hull is excluded from the general idle-hauler pool")

			require.Empty(t, *navCalls,
				"a warehouse/stocker assignment fires no navigate here — its own coordinator parks the hull")
		})
	}
}

// Idempotency (never yank a hull mid-role): a warehouse/stocker hull ALREADY carrying its role's
// coordinator fleet tag and mid-task (its coordinator running) is left completely undisturbed on a
// reload/re-apply — its live claim is kept, its dedication unchanged, no reposition. This guards
// against the free/reposition firing every pass, which would strand a running buffer hull.
func TestPositionDepotElementHull_LeavesAlreadyRoleDedicatedBusyHullUndisturbed(t *testing.T) {
	const waypoint = "X1-VB74-K83"
	for _, roleFleet := range []string{operationWarehouse, operationStocker} {
		roleFleet := roleFleet
		t.Run(roleFleet, func(t *testing.T) {
			s, db, playerID, navCalls := newDepotDeliveryTestServer(t)
			const hull = "HULL-BUSY"
			insertDepotDeliveryHull(t, db, hull, playerID, roleFleet, "X1-VB74-E44", true) // already dedicated, busy, off-waypoint

			ship, crewed, err := s.positionDepotElementHull(context.Background(), hull, waypoint, roleFleet, false, playerID)
			require.NoError(t, err)
			require.True(t, crewed, "a hull already on this role is crewed (idempotent — never-poach applies only to a DIFFERENT fleet)")
			require.NotNil(t, ship)

			var got persistence.ShipModel
			require.NoError(t, db.First(&got, "ship_symbol = ? AND player_id = ?", hull, playerID).Error)
			require.Equal(t, "active", got.AssignmentStatus, "a hull mid-role keeps its live claim — never yanked on reload")
			require.NotNil(t, got.ContainerID)
			require.Equal(t, "worker-"+hull, *got.ContainerID, "the SAME worker still holds the hull")
			require.Equal(t, roleFleet, got.DedicatedFleet, "its dedication is unchanged")
			require.Empty(t, *navCalls, "a busy already-dedicated hull is not repositioned")
		})
	}
}

// A SOURCE-HUB hull (no standing coordinator, like a delivery hull) crewed from an UNDEDICATED hull
// is re-dedicated to the DISTINCT depot-source-hub fleet AND — because nothing else will park it —
// navigated to its market waypoint on assign. This is the navigate-on-assign path for a non-delivery
// role, and it proves a crewed source hub no longer drifts off its configured anchor. (A source-hub
// hull already dedicated to a FOREIGN fleet is left alone — covered by the never-poach test.)
func TestPositionDepotElementHull_NavigatesSourceHubHullToItsWaypoint(t *testing.T) {
	s, db, playerID, navCalls := newDepotDeliveryTestServer(t)
	const hull = "SRCH-1"
	const waypoint = "X1-VB74-SRC7"
	insertDepotDeliveryHull(t, db, hull, playerID, "", "X1-VB74-I55", false) // idle, UNDEDICATED, off its market
	pid := shared.MustNewPlayerID(playerID)

	ship, crewed, err := s.positionDepotElementHull(context.Background(), hull, waypoint, depotSourceHubFleet, true, playerID)
	require.NoError(t, err)
	require.True(t, crewed, "an undedicated source-hub hull is crewed and positioned")
	require.NotNil(t, ship)

	var got persistence.ShipModel
	require.NoError(t, db.First(&got, "ship_symbol = ? AND player_id = ?", hull, playerID).Error)
	require.Equal(t, depotSourceHubFleet, got.DedicatedFleet, "re-dedicated to the distinct depot-source-hub fleet")

	require.Len(t, *navCalls, 1, "a crewed source hub off its market waypoint is navigated to it")
	require.Equal(t, hull, (*navCalls)[0].ship)
	require.Equal(t, waypoint, (*navCalls)[0].dest)

	_, generalPool, err := appContract.FindIdleLightHaulers(context.Background(), pid, s.shipRepo, "")
	require.NoError(t, err)
	require.NotContains(t, generalPool, hull, "a source-hub hull is excluded from the contract grab (its tag != \"\")")
}

// planDepotLaunches positions a crewed source hub at its OWN market waypoint (sp-3l64) — the
// role-agnostic extension of the delivery-hull placement. Before this, source hubs were config-only
// (planDepotLaunches emitted nothing for them), so a declared source-hub hull never deployed.
func TestPlanDepotLaunches_PositionsCrewedSourceHubAtItsWaypoint(t *testing.T) {
	c, err := depot.NewContractDepot(
		"j58",
		[]depot.Element{{Waypoint: "X1-J58-WH", ShipSymbol: "WH-1"}},
		nil,
		nil,
		[]depot.Element{{Waypoint: "X1-J58-SRC", ShipSymbol: "SRCH-1"}}, // a crewed source hub
	)
	require.NoError(t, err)
	reg := depot.NewRegistry([]*depot.ContractDepot{c})

	var sourceHubIntents []depotLaunchIntent
	for _, intent := range planDepotLaunches(reg) {
		if intent.role == depot.RoleSourceHub {
			sourceHubIntents = append(sourceHubIntents, intent)
		}
	}

	require.Len(t, sourceHubIntents, 1, "a crewed source-hub element must produce a positioning intent")
	require.Equal(t, "SRCH-1", sourceHubIntents[0].shipSymbol)
	require.Equal(t, "X1-J58-SRC", sourceHubIntents[0].targetWaypoint, "a source hub is positioned at its own market waypoint")
}

// The reopened bug's load-bearing fix: a granular `element add` for ANY role must POSITION the
// crewing hull, not only persist config. AddDepotElement now dispatches the added element's per-role
// launch (through the injectable depot sink) — a warehouse/stocker/delivery/source-hub launch
// carrying the element's waypoint — where before it returned after persisting and the hull sat
// docked. Proven against a spy sink so the wiring is verified without spawning coordinator goroutines.
func TestAddDepotElement_PositionsAddedHullForEveryRole(t *testing.T) {
	const depotID = "d1"
	const anchor = "X1-A-WH0" // the depot's (uncrewed) destination-warehouse anchor
	cases := []struct {
		name         string
		role         string
		addedHull    string
		addWaypoint  string
		pick         func(*spyDepotSink) []depotLaunchRecord
		wantWaypoint string
	}{
		{"warehouse", "warehouse", "WH-ADD", "X1-A-WH1", func(s *spyDepotSink) []depotLaunchRecord { return s.warehouses }, "X1-A-WH1"},
		// a stocker deposits into the depot's destination-warehouse anchor, so its launch carries the anchor
		{"stocker", "stocker", "ST-ADD", "X1-A-SRC", func(s *spyDepotSink) []depotLaunchRecord { return s.stockers }, anchor},
		{"delivery-hull", "delivery-hull", "DLV-ADD", "X1-A-H52", func(s *spyDepotSink) []depotLaunchRecord { return s.deliveries }, "X1-A-H52"},
		{"source-hub", "source-hub", "SRCH-ADD", "X1-A-SRC7", func(s *spyDepotSink) []depotLaunchRecord { return s.sourceHubs }, "X1-A-SRC7"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s, _, playerID, _ := newDepotDeliveryTestServer(t)
			spy := &spyDepotSink{}
			s.depotSinkOverride = spy
			// A depot with an (uncrewed) warehouse anchor satisfies the >=1-warehouse invariant
			// without emitting a launch of its own, so the spy records ONLY the added element.
			require.NoError(t, s.AddDepot(context.Background(), playerID, DepotSpec{
				ID:         depotID,
				Warehouses: []ElementSpec{{Waypoint: anchor}},
			}))

			require.NoError(t, s.AddDepotElement(context.Background(), playerID, depotID, tc.role, tc.addWaypoint, tc.addedHull))

			launches := tc.pick(spy)
			require.Len(t, launches, 1, "element add dispatches exactly the added element's per-role launch")
			require.Equal(t, tc.addedHull, launches[0].ship)
			require.Equal(t, tc.wantWaypoint, launches[0].waypoint, "the launch carries the element's positioning waypoint")
			require.Equal(t, playerID, launches[0].playerID)
		})
	}
}

// An uncrewed slot add positions nothing (no hull to fly) — the fail-open counterpart of the
// per-role positioning, matching planDepotLaunches' uncrewed-skip discipline.
func TestAddDepotElement_UncrewedSlotPositionsNothing(t *testing.T) {
	s, _, playerID, _ := newDepotDeliveryTestServer(t)
	spy := &spyDepotSink{}
	s.depotSinkOverride = spy
	require.NoError(t, s.AddDepot(context.Background(), playerID, DepotSpec{
		ID:         "d1",
		Warehouses: []ElementSpec{{Waypoint: "X1-A-WH0"}},
	}))

	require.NoError(t, s.AddDepotElement(context.Background(), playerID, "d1", "delivery-hull", "X1-A-H52", "")) // uncrewed

	require.Empty(t, spy.warehouses)
	require.Empty(t, spy.stockers)
	require.Empty(t, spy.deliveries)
	require.Empty(t, spy.sourceHubs)
}
