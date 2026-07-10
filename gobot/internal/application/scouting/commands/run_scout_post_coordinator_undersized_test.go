package commands

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// countingMarketProvider returns a configurable number of marketplace waypoints per
// system, so a test can build a market-rich system the single default fake cannot.
type countingMarketProvider struct {
	counts map[string]int
}

func (m *countingMarketProvider) ListBySystemWithTrait(_ context.Context, systemSymbol, _ string) ([]*shared.Waypoint, error) {
	n := m.counts[systemSymbol]
	wps := make([]*shared.Waypoint, 0, n)
	for i := 0; i < n; i++ {
		wp, err := shared.NewWaypoint(fmt.Sprintf("%s-M%02d", systemSymbol, i), 0, 0)
		if err != nil {
			return nil, err
		}
		wps = append(wps, wp)
	}
	return wps, nil
}

// fakeScoutEventStore captures Record calls and answers HasSince from what it has
// recorded (ignoring the window — a match ever recorded reads as recent), enough to
// assert both the warning payload and its per-system dedup.
type fakeScoutEventStore struct {
	recorded []*captain.Event
}

func (s *fakeScoutEventStore) Record(_ context.Context, e *captain.Event) error {
	s.recorded = append(s.recorded, e)
	return nil
}

func (s *fakeScoutEventStore) HasSince(_ context.Context, _ int, t captain.EventType, ship string, _ time.Time) (bool, error) {
	for _, e := range s.recorded {
		if e.Type == t && e.Ship == ship {
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeScoutEventStore) FindUnprocessed(context.Context, int, int) ([]*captain.Event, error) {
	return nil, nil
}
func (s *fakeScoutEventStore) MarkProcessed(context.Context, []int64, time.Time) error { return nil }
func (s *fakeScoutEventStore) HasUnprocessed(context.Context, int, captain.EventType, string) (bool, error) {
	return false, nil
}
func (s *fakeScoutEventStore) LatestByType(context.Context, int, captain.EventType) (*captain.Event, error) {
	return nil, nil
}

func undersizedTestHandler(postRepo *fakeScoutPostRepo, mp *countingMarketProvider, store captain.EventStore, clock shared.Clock) *RunScoutPostCoordinatorHandler {
	return &RunScoutPostCoordinatorHandler{
		postRepo:       postRepo,
		shipRepo:       &fakeScoutShipRepo{clock: clock},
		daemonClient:   &fakeScoutDaemonClient{},
		containerQuery: &fakeContainerStatusQuery{},
		marketProvider: mp,
		clock:          clock,
		eventStore:     store,
	}
}

// TestScoutPost_Undersized_WarnsNamingRequiredHulls is the layer-1 acceptance case: a
// market-rich single-probe standing post whose circuit math (22 markets × 3min = 66min)
// exceeds its 60min freshness target fires ONE scout.post_undersized naming the two hulls
// it needs — the XT71/UQ87 warning that never existed. Wired through reconcileOnce (with a
// healthy running tour so no other pass mutates the post) to prove the seam, not just the
// helper.
func TestScoutPost_Undersized_WarnsNamingRequiredHulls(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-XT71", Kind: domainScouting.PostKindStanding,
		FreshnessTarget: 60 * time.Minute, Hulls: 1, AssignedHull: "SAT-1", TourContainerID: "tour-1",
	}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	mp := &countingMarketProvider{counts: map[string]int{"X1-XT71": 22}}
	store := &fakeScoutEventStore{}
	handler := undersizedTestHandler(postRepo, mp, store, clock)
	// A RUNNING tour keeps pass 1 from disturbing the manned post, so the warning is the
	// only observable effect this tick.
	handler.containerQuery = &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{
		"RUNNING": {{ID: "tour-1"}},
	}}

	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))

	require.Len(t, store.recorded, 1, "an undersized standing post must warn exactly once")
	ev := store.recorded[0]
	require.Equal(t, captain.EventScoutPostUndersized, ev.Type)
	require.Equal(t, "X1-XT71", ev.Ship)
	require.Contains(t, ev.Payload, `"markets":22`)
	require.Contains(t, ev.Payload, `"hulls":1`)
	require.Contains(t, ev.Payload, `"required_hulls":2`)

	// Re-run within the cooldown: the HasSince dedup suppresses a duplicate warning.
	require.NoError(t, handler.reconcileOnce(context.Background(), scoutPostTestCmd()))
	require.Len(t, store.recorded, 1, "a persistently-undersized post must not re-warn every tick")
}

// TestScoutPost_AdequatelySized_Silent: the same 22-market system correctly budgeted at 2
// hulls is NOT undersized, so no warning fires.
func TestScoutPost_AdequatelySized_Silent(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-XT71", Kind: domainScouting.PostKindStanding,
		FreshnessTarget: 60 * time.Minute, Hulls: 2,
	}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	mp := &countingMarketProvider{counts: map[string]int{"X1-XT71": 22}}
	store := &fakeScoutEventStore{}
	handler := undersizedTestHandler(postRepo, mp, store, clock)

	handler.warnUndersizedPosts(context.Background(), scoutPostTestCmd(), postRepo.posts)

	require.Empty(t, store.recorded, "an adequately-sized post must stay silent")
}

// TestScoutPost_SweepOnce_NotWarned: a sweep-once post has no standing freshness contract,
// so it is never assessed for undersizing even when it would compute as too small.
func TestScoutPost_SweepOnce_NotWarned(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-XT71", Kind: domainScouting.PostKindSweepOnce,
		FreshnessTarget: 60 * time.Minute, Hulls: 1,
	}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	mp := &countingMarketProvider{counts: map[string]int{"X1-XT71": 22}}
	store := &fakeScoutEventStore{}
	handler := undersizedTestHandler(postRepo, mp, store, clock)

	handler.warnUndersizedPosts(context.Background(), scoutPostTestCmd(), postRepo.posts)

	require.Empty(t, store.recorded, "sweep-once posts carry no standing freshness contract")
}

// TestScoutPost_Undersized_NoEventStore_NoOp proves the optional-injection guard: with no
// event store wired the warning path is a no-op and never panics.
func TestScoutPost_Undersized_NoEventStore_NoOp(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-XT71", Kind: domainScouting.PostKindStanding,
		FreshnessTarget: 60 * time.Minute, Hulls: 1,
	}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	mp := &countingMarketProvider{counts: map[string]int{"X1-XT71": 22}}
	handler := undersizedTestHandler(postRepo, mp, nil, clock)
	handler.eventStore = nil

	require.NotPanics(t, func() {
		handler.warnUndersizedPosts(context.Background(), scoutPostTestCmd(), postRepo.posts)
	})
}
