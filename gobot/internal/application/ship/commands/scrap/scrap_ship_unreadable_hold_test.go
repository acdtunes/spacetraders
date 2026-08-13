package scrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// scrapStubShipRepo answers the live read with exactly what a test names, so the
// guard can be shown a read that produced no hull.
type scrapStubShipRepo struct {
	navigation.ShipRepository
	ship *navigation.Ship
}

func (s *scrapStubShipRepo) FindBySymbol(context.Context, string, shared.PlayerID) (*navigation.Ship, error) {
	return s.ship, nil
}

func (s *scrapStubShipRepo) ClaimShip(context.Context, string, string, shared.PlayerID, string) error {
	return nil
}

func (s *scrapStubShipRepo) SyncShipFromAPI(context.Context, string, shared.PlayerID) (*navigation.Ship, error) {
	return s.ship, nil
}

func (s *scrapStubShipRepo) SaveWithRetry(context.Context, string, shared.PlayerID, navigation.ShipMutation) (*navigation.Ship, bool, error) {
	return nil, false, nil
}

type scrapStubContainerRepo struct{}

func (scrapStubContainerRepo) Add(context.Context, *domainContainer.Container, string) error {
	return nil
}
func (scrapStubContainerRepo) Remove(context.Context, string, int) error { return nil }

// A live read that errors is refused elsewhere; this is the quieter shape, where
// the read succeeds and simply produces no hull. It must refuse, not panic.
func TestScrapShipRefusesWhenTheLiveReadProducesNoHull(t *testing.T) {
	playerID := shared.MustNewPlayerID(7)
	fake := &scrapFakeAPIClient{}

	handler := NewScrapShipHandler(
		&scrapStubShipRepo{ship: nil},
		&scrapFakePlayerRepo{p: &player.Player{ID: playerID, AgentSymbol: "TORWIND", Token: "tok"}},
		fake, scrapStubContainerRepo{}, nil, nil,
	)

	pid := playerID.Value()
	_, err := handler.Handle(context.Background(), &ScrapShipCommand{ShipSymbol: scrapHull, PlayerID: &pid})

	require.Error(t, err, "an unreadable hold must refuse, not scrap on a silent zero")
	require.Zero(t, fake.scrapCalls, "the irreversible scrap must never be reached")
}
