package grpc

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// reanchorEventRecorder captures what reached the captain outbox.
type reanchorEventRecorder struct {
	mu     sync.Mutex
	events []*captain.Event
}

func (r *reanchorEventRecorder) Record(_ context.Context, e *captain.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *reanchorEventRecorder) recorded() []*captain.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*captain.Event(nil), r.events...)
}

// TestShipPositionReanchor_ReachesTheCaptainOutboxWithBothSystemsNamed is the durability
// assertion for the whole signal. A WARN log is lost the moment the daemon rotates it —
// which is precisely why the TORWIND-41 hunt could not identify the lost write from the
// retained logs. The re-anchor must land in captain_events, keyed to the hull and naming
// BOTH systems, so the next occurrence is diagnosable from stored state alone.
func TestShipPositionReanchor_ReachesTheCaptainOutboxWithBothSystemsNamed(t *testing.T) {
	recorder := &reanchorEventRecorder{}
	SetCaptainEventRecorder(recorder)
	defer SetCaptainEventRecorder(nil)

	shipPositionReanchorObserver{}.ShipPositionReanchored(context.Background(), api.PositionReanchor{
		ShipSymbol:       "TORWIND-41",
		PlayerID:         7,
		BelievedSystem:   "X1-GF41",
		ActualSystem:     "X1-KC84",
		BelievedWaypoint: "X1-GF41-I57",
		ActualWaypoint:   "X1-KC84-A1",
	})

	events := recorder.recorded()
	if len(events) != 1 {
		t.Fatalf("expected exactly one durable captain event for a re-anchor, got %d", len(events))
	}
	event := events[0]
	if event.Type != captain.EventShipPositionReanchored {
		t.Fatalf("expected %s, got %s", captain.EventShipPositionReanchored, event.Type)
	}
	if event.Ship != "TORWIND-41" {
		t.Fatalf("the event must be keyed to the hull whose position was wrong, got %q", event.Ship)
	}
	if event.PlayerID != 7 {
		t.Fatalf("expected player 7, got %d", event.PlayerID)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
		t.Fatalf("payload must be readable JSON: %v", err)
	}
	if payload["believed_system"] != "X1-GF41" {
		t.Fatalf("the payload must name the system we wrongly believed — without it the event cannot identify the lost write; got %v", payload["believed_system"])
	}
	if payload["actual_system"] != "X1-KC84" {
		t.Fatalf("the payload must name the system the hull is actually in, got %v", payload["actual_system"])
	}
	if payload["believed_waypoint"] != "X1-GF41-I57" || payload["actual_waypoint"] != "X1-KC84-A1" {
		t.Fatalf("both waypoints must survive to the outbox, got %v -> %v", payload["believed_waypoint"], payload["actual_waypoint"])
	}
}

// TestShipPositionReanchor_SurvivesAnUnwiredOutbox pins the fire-and-forget contract. The
// repository is wired long before the captain recorder is installed, so an emission during
// that window must be a no-op rather than a panic — an observability write may never break
// the sync it is reporting on.
func TestShipPositionReanchor_SurvivesAnUnwiredOutbox(t *testing.T) {
	SetCaptainEventRecorder(nil)

	shipPositionReanchorObserver{}.ShipPositionReanchored(context.Background(), api.PositionReanchor{
		ShipSymbol:     "TORWIND-41",
		BelievedSystem: "X1-GF41",
		ActualSystem:   "X1-KC84",
	})
}
