package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

type fakeShipAssignmentLister struct {
	infos []persistence.ShipAssignmentInfo
	err   error
	calls int
	gotID int
}

func (f *fakeShipAssignmentLister) ListActive(ctx context.Context, playerID int) ([]persistence.ShipAssignmentInfo, error) {
	f.calls++
	f.gotID = playerID
	return f.infos, f.err
}

func TestHumanizeDurationFormatsByMagnitude(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"seconds", 45 * time.Second, "45s"},
		{"minutes", 2 * time.Minute, "2m"},
		{"hours and minutes", 64 * time.Minute, "1h4m"},
		{"exact hours", 2 * time.Hour, "2h"},
		{"negative clamps to zero", -5 * time.Second, "0s"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, humanizeDuration(tc.d))
		})
	}
}

func TestBuildShipRowsMergesRoleAssignmentAndCacheAge(t *testing.T) {
	now := time.Now()
	ships := []*pb.ShipInfo{
		{Symbol: "SHIP-1", Location: "X1-A1", NavStatus: "DOCKED"},
		{Symbol: "SHIP-2", Location: "X1-A2", NavStatus: "IN_TRANSIT"},
	}
	infos := map[string]persistence.ShipAssignmentInfo{
		"SHIP-1": {ShipSymbol: "SHIP-1", Role: "HAULER", ContainerID: "CTR-1", SyncedAt: now.Add(-2 * time.Minute)},
	}

	rows := buildShipRows(ships, infos, now)

	require.Len(t, rows, 2)
	require.Equal(t, "HAULER", rows[0].Role)
	require.Equal(t, "CTR-1", rows[0].Assignment)
	require.Equal(t, "2m", rows[0].CacheAge)

	require.Equal(t, "-", rows[1].Role)
	require.Equal(t, "-", rows[1].Assignment)
	require.Equal(t, "-", rows[1].CacheAge)
}

// A captain reservation has no ContainerID (sp-i1ku: it was never a container
// claim), so without dedicated rendering it would fall back to "-" and be
// indistinguishable from a genuinely idle, unassigned ship. The ASSIGNMENT
// column must show the reservation itself, plus the reason when one was given.
func TestBuildShipRowsShowsCaptainReservationWithReason(t *testing.T) {
	now := time.Now()
	ships := []*pb.ShipInfo{
		{Symbol: "SHIP-1", Location: "X1-A1", NavStatus: "IN_ORBIT"},
	}
	infos := map[string]persistence.ShipAssignmentInfo{
		"SHIP-1": {
			ShipSymbol:       "SHIP-1",
			Role:             "HAULER",
			AssignmentOwner:  string(navigation.AssignmentOwnerCaptain),
			AssignmentReason: "manual gate-supply errand",
			SyncedAt:         now.Add(-90 * time.Second),
		},
	}

	rows := buildShipRows(ships, infos, now)

	require.Len(t, rows, 1)
	require.Equal(t, "captain (manual gate-supply errand)", rows[0].Assignment)
}

// A reservation taken with no explicit reason must still render as a
// reservation, not "-" — omitting the reason must never make it look idle.
func TestBuildShipRowsShowsCaptainReservationWithoutReason(t *testing.T) {
	now := time.Now()
	ships := []*pb.ShipInfo{
		{Symbol: "SHIP-1", Location: "X1-A1", NavStatus: "IN_ORBIT"},
	}
	infos := map[string]persistence.ShipAssignmentInfo{
		"SHIP-1": {
			ShipSymbol:      "SHIP-1",
			Role:            "HAULER",
			AssignmentOwner: string(navigation.AssignmentOwnerCaptain),
			SyncedAt:        now.Add(-90 * time.Second),
		},
	}

	rows := buildShipRows(ships, infos, now)

	require.Len(t, rows, 1)
	require.Equal(t, "captain", rows[0].Assignment)
}

// TestBuildShipRowsPopulatesFleetAndSortsNatural is the sp-ioqt fixture test:
// a probe, a hauler, a container-claimed ship, and a dedicated-fleet ship,
// supplied out of symbol order (including a lexicographic trap — "TORWIND-3"
// and "TORWIND-10" sort backwards under plain string comparison). It asserts
// both the new FLEET column content (the sp-lybx-prevention payload) and
// that buildShipRows itself — not caller order — produces natural-sorted
// output, since a scrambled fleet roster was part of what let sp-lybx slip
// past a visual check in the first place.
func TestBuildShipRowsPopulatesFleetAndSortsNatural(t *testing.T) {
	now := time.Now()
	ships := []*pb.ShipInfo{
		{Symbol: "TORWIND-10", Location: "X1-A1", NavStatus: "DOCKED"},
		{Symbol: "TORWIND-3", Location: "X1-A1", NavStatus: "DOCKED"},
		{Symbol: "TORWIND-1", Location: "X1-A1", NavStatus: "DOCKED"},
		{Symbol: "TORWIND-2", Location: "X1-A1", NavStatus: "DOCKED"},
	}
	infos := map[string]persistence.ShipAssignmentInfo{
		"TORWIND-1":  {ShipSymbol: "TORWIND-1", Role: "PROBE"},
		"TORWIND-2":  {ShipSymbol: "TORWIND-2", Role: "HAULER"},
		"TORWIND-3":  {ShipSymbol: "TORWIND-3", Role: "HAULER", ContainerID: "navigate-TORWIND-3-a3f8e2b1"},
		"TORWIND-10": {ShipSymbol: "TORWIND-10", Role: "PROBE", DedicatedFleet: "contract"},
	}

	rows := buildShipRows(ships, infos, now)

	require.Len(t, rows, 4)

	gotOrder := make([]string, len(rows))
	for i, r := range rows {
		gotOrder[i] = r.Symbol
	}
	require.Equal(t, []string{"TORWIND-1", "TORWIND-2", "TORWIND-3", "TORWIND-10"}, gotOrder,
		"rows must be in natural symbol order, not lexicographic (TORWIND-3 must not sort after TORWIND-10)")

	// probe, unclaimed, undedicated
	require.Equal(t, "PROBE", rows[0].Role)
	require.Equal(t, "-", rows[0].Fleet)
	require.Equal(t, "-", rows[0].Assignment)

	// hauler, unclaimed, undedicated
	require.Equal(t, "HAULER", rows[1].Role)
	require.Equal(t, "-", rows[1].Fleet)
	require.Equal(t, "-", rows[1].Assignment)

	// hauler, claimed by a container
	require.Equal(t, "HAULER", rows[2].Role)
	require.Equal(t, "-", rows[2].Fleet)
	require.Equal(t, "navigate-TORWIND-3-a3f8e2b1", rows[2].Assignment)

	// dedicated to the "contract" fleet, otherwise idle — this is the
	// sp-lybx scenario: a PROBE pinned to contract must be visible at a
	// glance via FLEET even though it carries no container claim.
	require.Equal(t, "PROBE", rows[3].Role)
	require.Equal(t, "contract", rows[3].Fleet)
	require.Equal(t, "-", rows[3].Assignment)
}

func TestRunShipListPropagatesListerError(t *testing.T) {
	lister := &fakeShipAssignmentLister{err: errors.New("db down")}
	ships := []*pb.ShipInfo{{Symbol: "SHIP-1"}}

	err := runShipList(context.Background(), ships, lister, 7, time.Now(), false)

	require.Error(t, err)
	require.Equal(t, 7, lister.gotID)
}

func TestRunShipListSkipsAssignmentLookupWhenNoShips(t *testing.T) {
	lister := &fakeShipAssignmentLister{}

	err := runShipList(context.Background(), nil, lister, 7, time.Now(), false)

	require.NoError(t, err)
	require.Equal(t, 0, lister.calls)
}
