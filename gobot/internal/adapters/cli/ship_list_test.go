package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
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
