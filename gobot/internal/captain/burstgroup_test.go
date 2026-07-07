package watchkeeper

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// recordingBurstStore is a deterministic in-memory stand-in for the narrow
// cooldown store the burst grouper wraps. It stamps CreatedAt from an injected
// clock so window math is fully controllable (no sleeps), and HasSince scans
// only the events that were actually recorded — which is the whole point: a
// suppressed emission leaves no row, so it cannot extend the cooldown.
type recordingBurstStore struct {
	now         func() time.Time
	events      []*captain.Event
	hasSinceErr error
}

func (s *recordingBurstStore) Record(_ context.Context, e *captain.Event) error {
	cp := *e
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = s.now()
	}
	s.events = append(s.events, &cp)
	return nil
}

func (s *recordingBurstStore) HasSince(_ context.Context, playerID int, t captain.EventType, ship string, since time.Time) (bool, error) {
	if s.hasSinceErr != nil {
		return false, s.hasSinceErr
	}
	for _, e := range s.events {
		if e.PlayerID == playerID && e.Type == t && e.Ship == ship && e.CreatedAt.After(since) {
			return true, nil
		}
	}
	return false, nil
}

func wfFailed(playerID int, ship string) *captain.Event {
	return &captain.Event{Type: captain.EventWorkflowFailed, Ship: ship, PlayerID: playerID}
}

// TestBurstGroupingSuppressesRepeatedWorkflowFailedWithinWindow is the core
// requirement (sp-kb61): a retry burst of the same failure for the same entity
// inside the window collapses to ONE event in the captain's attention budget.
func TestBurstGroupingSuppressesRepeatedWorkflowFailedWithinWindow(t *testing.T) {
	cur := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return cur }
	store := &recordingBurstStore{now: clock}
	g := NewBurstGroupingRecorder(store, 15*time.Minute, captain.EventWorkflowFailed)
	g.now = clock

	require.NoError(t, g.Record(context.Background(), wfFailed(1, "SHIP-A")))
	cur = cur.Add(2 * time.Minute)
	require.NoError(t, g.Record(context.Background(), wfFailed(1, "SHIP-A")))
	cur = cur.Add(3 * time.Minute)
	require.NoError(t, g.Record(context.Background(), wfFailed(1, "SHIP-A")))

	require.Len(t, store.events, 1, "three failures of one ship inside the window must yield one event")
}

// TestBurstGroupingResurfacesWorkflowFailedAfterWindow proves the cooldown is a
// window, not a permanent mute: a failure after the window is a fresh incident.
func TestBurstGroupingResurfacesWorkflowFailedAfterWindow(t *testing.T) {
	cur := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return cur }
	store := &recordingBurstStore{now: clock}
	g := NewBurstGroupingRecorder(store, 15*time.Minute, captain.EventWorkflowFailed)
	g.now = clock

	require.NoError(t, g.Record(context.Background(), wfFailed(1, "SHIP-A")))
	cur = cur.Add(16 * time.Minute)
	require.NoError(t, g.Record(context.Background(), wfFailed(1, "SHIP-A")))

	require.Len(t, store.events, 2, "a failure past the window is a new incident and must surface")
}

// TestBurstGroupingLeavesContainerCrashedUngrouped is the reconciliation with
// sp-no9i: container.crashed is NOT in the burst set, so its one-row-per-true-
// death granularity (sp-okwk) is preserved for detectCrashLoops to count into a
// crash-LOOP interrupt. Grouping it here would starve that detector.
func TestBurstGroupingLeavesContainerCrashedUngrouped(t *testing.T) {
	cur := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return cur }
	store := &recordingBurstStore{now: clock}
	g := NewBurstGroupingRecorder(store, 15*time.Minute, captain.EventWorkflowFailed)
	g.now = clock

	crash := func() *captain.Event {
		return &captain.Event{Type: captain.EventContainerCrashed, Ship: "c-1", PlayerID: 1}
	}
	require.NoError(t, g.Record(context.Background(), crash()))
	cur = cur.Add(2 * time.Minute)
	require.NoError(t, g.Record(context.Background(), crash()))

	require.Len(t, store.events, 2, "container.crashed must stay one-row-per-death for detectCrashLoops")
}

// TestBurstGroupingKeysOnShipSoDistinctEntitiesBothSurface proves the group key
// includes the entity: a burst for one ship never masks a real failure of a
// different ship in the same window.
func TestBurstGroupingKeysOnShipSoDistinctEntitiesBothSurface(t *testing.T) {
	cur := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return cur }
	store := &recordingBurstStore{now: clock}
	g := NewBurstGroupingRecorder(store, 15*time.Minute, captain.EventWorkflowFailed)
	g.now = clock

	require.NoError(t, g.Record(context.Background(), wfFailed(1, "SHIP-A")))
	cur = cur.Add(1 * time.Minute)
	require.NoError(t, g.Record(context.Background(), wfFailed(1, "SHIP-B")))

	require.Len(t, store.events, 2, "distinct ships are distinct incidents")
}

// TestBurstGroupingFailsOpenWhenCooldownCheckErrors: unlike a detector (which
// re-checks next poll), an emission is one-shot. A transient cooldown-check
// failure must never silently drop a strategic event, so the grouper fails OPEN
// (records) when HasSince errors.
func TestBurstGroupingFailsOpenWhenCooldownCheckErrors(t *testing.T) {
	cur := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return cur }
	store := &recordingBurstStore{now: clock, hasSinceErr: errors.New("db down")}
	g := NewBurstGroupingRecorder(store, 15*time.Minute, captain.EventWorkflowFailed)
	g.now = clock

	require.NoError(t, g.Record(context.Background(), wfFailed(1, "SHIP-A")))
	cur = cur.Add(1 * time.Minute)
	require.NoError(t, g.Record(context.Background(), wfFailed(1, "SHIP-A")))

	require.Len(t, store.events, 2, "cooldown-check errors must not lose strategic events")
}
