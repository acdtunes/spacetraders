package expansion

import (
	"context"
	"sync"

	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
)

// ExplorerOffGateBridge is the cross-coordinator seam between slice B's off-gate demand
// (raised in the FRONTIER expansion coordinator) and slice C's explorer BUY (in the FLEET autosizer).
// One object plays two ports — the contract_delivery_bridge idiom:
//
//   - expansionCmd.OffGateDemandSink (WRITE): the frontier coordinator mirrors each tick's off-gate
//     signal here (emitOffGateDemand);
//   - fleetCmd.OffGateDemandSource (READ, satisfied structurally): the autosizer's
//     ExplorerDemandProvider reads it to gate the buy on BOTH arming AND live off-gate demand.
//
// It is the decoupling object that lets the explorer demand provider be registered on the autosizer
// handler at construction (before the autosizer's own mediator registration), while the frontier —
// built later in the daemon wiring — connects the write side. Both coordinators depend on the bridge,
// not on each other. DORMANT until the frontier's first emit for a player: until then it reads
// UNREADABLE (ok=false), so the autosizer fails CLOSED (never buys on a demand it was never told).
// Written on the frontier goroutine and read on the autosizer's, so access is mutex-guarded.
type ExplorerOffGateBridge struct {
	mu     sync.Mutex
	latest map[int]expansionCmd.OffGateDemandSignal
	seen   map[int]bool
}

// NewExplorerOffGateBridge builds an empty bridge (reads unreadable until the first frontier emit).
func NewExplorerOffGateBridge() *ExplorerOffGateBridge {
	return &ExplorerOffGateBridge{
		latest: make(map[int]expansionCmd.OffGateDemandSignal),
		seen:   make(map[int]bool),
	}
}

// EmitOffGateDemand implements expansionCmd.OffGateDemandSink — the frontier coordinator's per-tick
// mirror of the off-gate signal (write side).
func (b *ExplorerOffGateBridge) EmitOffGateDemand(playerID int, signal expansionCmd.OffGateDemandSignal) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.latest[playerID] = signal
	b.seen[playerID] = true
}

// ExplorerDemand implements fleetCmd.OffGateDemandSource (read side): (demanded, wantCount, ok). ok is
// false until the frontier's first emit for the player — an un-emitted signal reads UNREADABLE so the
// autosizer's explorer pass fails CLOSED.
func (b *ExplorerOffGateBridge) ExplorerDemand(_ context.Context, playerID int) (bool, int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.seen[playerID] {
		return false, 0, false
	}
	signal := b.latest[playerID]
	return signal.Demanded, signal.ExplorerCount, true
}
