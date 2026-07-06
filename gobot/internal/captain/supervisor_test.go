package captainsup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

type captainStores struct {
	store    captain.EventStore
	playerID int
	dir      string
}

func TestTickRespectsHourlyCap(t *testing.T) {
	sup, s, gw := newBridgeSupervisor(t)
	now := time.Now()
	for i := 0; i < 6; i++ {
		sup.sessionStarts = append(sup.sessionStarts, now.Add(-time.Duration(i)*time.Minute))
	}
	recordEvent(t, s, captain.EventShipIdle)

	ran, err := sup.Tick(context.Background(), now)
	require.NoError(t, err)
	require.False(t, ran, "cap reached: events queue, no session")
	require.Empty(t, gw.nudges, "capped tick emits no wake signals")
	require.Empty(t, gw.mails)
}
