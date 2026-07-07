package watchkeeper

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// DefaultBurstWindow is the "same incident" window for emission-side burst
// grouping (sp-kb61). Long enough to swallow a respawn-die storm or a transient
// API-error burst into one incident; short enough that a genuinely separate
// failure of the same entity after the captain has had time to react resurfaces
// as a new event. Config-tunability is a follow-up (mirrors the crash-loop
// window's path, sp-d3xi).
const DefaultBurstWindow = 15 * time.Minute

// burstCooldownStore is the narrow slice of captain.EventStore the burst grouper
// needs: record an event and evaluate the shared HasSince cooldown. Kept narrow
// so tests can substitute a recording fake (mirrors cityGateway), and so this
// decorator depends on nothing more than the cooldown primitive the detectors
// already use. *persistence.GormCaptainEventRepository satisfies it.
type burstCooldownStore interface {
	Record(ctx context.Context, e *captain.Event) error
	HasSince(ctx context.Context, playerID int, t captain.EventType, ship string, since time.Time) (bool, error)
}

// BurstGroupingRecorder collapses emission-side "retry burst" events — many
// rows of the same type for the same entity in a short window — to ONE event in
// the strategic stream, the captain's attention budget (sp-kb61). It generalizes
// sp-okwk's one-container.crashed-per-death fix from a bespoke source rewrite to
// a reusable, type-configurable emission filter.
//
// It reuses the shared HasSince cooldown idiom (sp-1hak: detectIdleShips,
// detectStaleHeartbeats, detectCrashLoops): before recording a burst-prone event
// it checks whether a twin of the same (type, ship) landed within the window and,
// if so, suppresses the write. The daemon's raw per-retry rows still go to the
// container logs — only the EVENT stream is thinned.
//
// Reconciliation with sp-no9i: container.crashed is deliberately NOT a burst type
// here. sp-okwk made it count true (unrecoverable) deaths one row apiece, and
// detectCrashLoops counts those rows into a single crash-LOOP interrupt. Grouping
// container.crashed at emission would starve that detector, so this decorator
// stays out of its way. What it DOES tame is the interrupt-class workflow.failed
// that container_runner emits alongside every death: that duplicate was still
// waking the captain per death after no9i demoted the crashed signal.
type BurstGroupingRecorder struct {
	store  burstCooldownStore
	window time.Duration
	burst  map[captain.EventType]bool
	now    func() time.Time
}

var _ captain.EventRecorder = (*BurstGroupingRecorder)(nil)

// NewBurstGroupingRecorder wraps store so that emissions of any burstType within
// window collapse to one event per (type, ship). Types outside burstTypes pass
// straight through unchanged, as does everything when window <= 0.
func NewBurstGroupingRecorder(store burstCooldownStore, window time.Duration, burstTypes ...captain.EventType) *BurstGroupingRecorder {
	burst := make(map[captain.EventType]bool, len(burstTypes))
	for _, t := range burstTypes {
		burst[t] = true
	}
	return &BurstGroupingRecorder{
		store:  store,
		window: window,
		burst:  burst,
		now:    time.Now,
	}
}

// Record writes e, unless it is a burst-prone type whose (type, ship) twin
// already landed within the window — in which case the emission is suppressed
// (the raw row remains in the container logs). A cooldown-check error fails OPEN
// (records): an emission is one-shot, so — unlike a detector that re-checks on
// the next poll — dropping it on a transient DB hiccup would lose the signal.
func (b *BurstGroupingRecorder) Record(ctx context.Context, e *captain.Event) error {
	if b.window <= 0 || !b.burst[e.Type] {
		return b.store.Record(ctx, e)
	}
	recent, err := b.store.HasSince(ctx, e.PlayerID, e.Type, e.Ship, b.now().Add(-b.window))
	if err == nil && recent {
		return nil
	}
	return b.store.Record(ctx, e)
}
