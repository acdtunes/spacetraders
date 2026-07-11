package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

var (
	captainRecorderMu sync.RWMutex
	captainRecorder   captain.EventRecorder
)

// SetCaptainEventRecorder wires the strategic-event outbox. Called once from
// the daemon main; nil disables recording (tests, CLI-only runs).
func SetCaptainEventRecorder(rec captain.EventRecorder) {
	captainRecorderMu.Lock()
	defer captainRecorderMu.Unlock()
	captainRecorder = rec
}

// currentCaptainEventRecorder returns the recorder installed by main (may be
// nil in minimal boots/tests). Package-internal: the daemon server hands it
// to the supervise layer at Start (sp-i01z).
func currentCaptainEventRecorder() captain.EventRecorder {
	captainRecorderMu.RLock()
	defer captainRecorderMu.RUnlock()
	return captainRecorder
}

// recordCaptainEvent is fire-and-forget: outbox failures must never break
// container execution, so errors are printed and swallowed.
func recordCaptainEvent(t captain.EventType, ship string, playerID int, payload map[string]any) {
	captainRecorderMu.RLock()
	rec := captainRecorder
	captainRecorderMu.RUnlock()
	if rec == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte("{}")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rec.Record(ctx, &captain.Event{
		Type: t, Ship: ship, PlayerID: playerID, Payload: string(raw),
	}); err != nil {
		fmt.Printf("captain outbox: failed to record %s: %v\n", t, err)
	}
}
