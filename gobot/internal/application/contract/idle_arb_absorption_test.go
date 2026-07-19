package contract

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
)

// The absorption ledger consult skips a sink the ledger shows occupied (in flight
// or recovering) and records each launched leg's sell side so other dispatchers
// see it — layered alongside the in-memory lane mutex, not a replacement for it.

// nearSink is the one profitable sink in idleArbHarness (hub→X1-HUB-D40, MACHINERY).
var nearSink = absorption.LaneKey{Waypoint: "X1-HUB-D40", Good: "MACHINERY", Side: absorption.SideSell}

// fakeAbsorptionLedger is a stateful in-memory absorption.Ledger for the dispatcher
// tests: RecordPlanned populates the same pools Outstanding returns, so one
// dispatcher's launch is visible to another's consult (the cross-engine coordination
// under test). outErr / recordErr force the fail paths.
type fakeAbsorptionLedger struct {
	mu        sync.Mutex
	pools     map[absorption.LaneKey]absorption.KeyOccupancy
	recorded  []absorption.ReserveEntry
	converts  int
	releases  int
	outErr    error
	recordErr error
}

func newFakeAbsorptionLedger() *fakeAbsorptionLedger {
	return &fakeAbsorptionLedger{pools: map[absorption.LaneKey]absorption.KeyOccupancy{}}
}

func (f *fakeAbsorptionLedger) preset(k absorption.LaneKey, occ absorption.KeyOccupancy) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pools[k] = occ
}

func (f *fakeAbsorptionLedger) recordedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.recorded)
}

func (f *fakeAbsorptionLedger) Reserve(context.Context, int, string, string, []absorption.ReserveEntry) ([]string, bool, error) {
	return nil, true, nil // idle-arb records rather than conditionally reserves; unused here
}

func (f *fakeAbsorptionLedger) RecordPlanned(_ context.Context, _ int, _ string, _ string, entry absorption.ReserveEntry) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return "", f.recordErr
	}
	f.recorded = append(f.recorded, entry)
	k := absorption.LaneKey{Waypoint: entry.Waypoint, Good: entry.Good, Side: entry.Side}
	occ := f.pools[k]
	occ.PlannedUnits += entry.Units
	f.pools[k] = occ
	return fmt.Sprintf("res-%d", len(f.recorded)), nil
}

func (f *fakeAbsorptionLedger) Outstanding(context.Context, int) (map[absorption.LaneKey]absorption.KeyOccupancy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.outErr != nil {
		return nil, f.outErr
	}
	out := make(map[absorption.LaneKey]absorption.KeyOccupancy, len(f.pools))
	for k, v := range f.pools {
		out[k] = v
	}
	return out, nil
}

func (f *fakeAbsorptionLedger) ConvertByContainer(context.Context, string, int, absorption.LaneKey, int, string, int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.converts++
	return nil
}

func (f *fakeAbsorptionLedger) Release(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	return nil
}

func (f *fakeAbsorptionLedger) ReleaseByContainer(context.Context, string, int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	return 0, nil
}

var _ absorption.Ledger = (*fakeAbsorptionLedger)(nil)

// A sink the ledger shows reserved so heavily that the remaining depth can't fit
// the leg's tranche is skipped with skip:reserved — the cross-engine collision the
// in-memory mutex misses. nearSink's trade volume is 100 and a leg carries 40, so a
// 70-unit PLANNED reservation leaves only 30 — below the 40-unit tranche.
func TestIdleArb_Consult_ReservedSink_Skips(t *testing.T) {
	dispatcher, _, launcher := idleArbHarness(t, 2, IdleArbConfig{ReserveHulls: 1})
	ledger := newFakeAbsorptionLedger()
	ledger.preset(nearSink, absorption.KeyOccupancy{PlannedUnits: 70})
	dispatcher.SetAbsorptionLedger(ledger, false, 0)

	logger := &idleArbCapturingLogger{}
	launched := dispatcher.DispatchOnce(common.WithLogger(context.Background(), logger))

	if launched != 0 || len(launcher.launches) != 0 {
		t.Fatalf("a reserved sink must not be dumped into, got %d launches", launched)
	}
	// Both idle hulls attempt the single (reserved) sink and each skips — a skip does
	// not consume the reserve the way a launch does, so the loop tries every hull.
	if dispatcher.skipReserved != 2 {
		t.Fatalf("both attempts must be attributed to the ledger consult (reserved), got skipReserved=%d", dispatcher.skipReserved)
	}
	candidate := logger.messageWithPrefix(t, "Idle-arb candidate:")
	if !strings.Contains(candidate, "verdict skipped:reserved") {
		t.Fatalf("candidate line must show skipped:reserved, got: %s", candidate)
	}
}

// A sink under a recovering shadow still above its floor (Outstanding reports a
// positive RecoveringResidual) is likewise skipped:reserved.
func TestIdleArb_Consult_RecoveringShadow_Skips(t *testing.T) {
	dispatcher, _, launcher := idleArbHarness(t, 2, IdleArbConfig{ReserveHulls: 1})
	ledger := newFakeAbsorptionLedger()
	ledger.preset(nearSink, absorption.KeyOccupancy{RecoveringResidual: 12.5})
	dispatcher.SetAbsorptionLedger(ledger, false, 0)

	if launched := dispatcher.DispatchOnce(context.Background()); launched != 0 || len(launcher.launches) != 0 {
		t.Fatalf("a sink under a live recovery shadow must not be dumped into, got %d launches", launched)
	}
	if dispatcher.skipReserved != 2 {
		t.Fatalf("expected both recovering-shadow attempts attributed to reserved, got %d", dispatcher.skipReserved)
	}
}

// A sink whose shadow has recovered past its floor (Outstanding drops it, residual 0)
// is NOT reserved — the leg dispatches, and the dispatcher records ITS OWN absorption.
func TestIdleArb_Consult_RecoveredPastFloor_DispatchesAndRecords(t *testing.T) {
	dispatcher, _, launcher := idleArbHarness(t, 2, IdleArbConfig{ReserveHulls: 1})
	ledger := newFakeAbsorptionLedger()
	// Recovered past the floor: Outstanding reports zero residual and no planned.
	ledger.preset(nearSink, absorption.KeyOccupancy{PlannedUnits: 0, RecoveringResidual: 0})
	dispatcher.SetAbsorptionLedger(ledger, false, 0)

	launched := dispatcher.DispatchOnce(context.Background())
	if launched != 1 || len(launcher.launches) != 1 {
		t.Fatalf("a recovered sink must accept a new leg, got %d launches", launched)
	}
	if dispatcher.skipReserved != 0 {
		t.Fatalf("a recovered sink is not a reserved skip, got skipReserved=%d", dispatcher.skipReserved)
	}
	// The launched leg's sell side is recorded for other engines to consult.
	if ledger.recordedCount() != 1 {
		t.Fatalf("the launched leg's absorption must be recorded, got %d records", ledger.recordedCount())
	}
	rec := ledger.recorded[0]
	if rec.Waypoint != nearSink.Waypoint || rec.Good != nearSink.Good || rec.Side != absorption.SideSell {
		t.Fatalf("recorded the wrong sink: %+v", rec)
	}
	if rec.Units <= 0 || rec.TTL <= 0 {
		t.Fatalf("recorded leg must carry a positive worst-case hold and TTL, got units=%d ttl=%s", rec.Units, rec.TTL)
	}
}

// Fail-closed: an unreadable ledger declines every candidate this pass rather than
// dispatch blind into depth another engine may hold (RULINGS #4).
func TestIdleArb_Consult_LedgerUnreadable_FailsClosed(t *testing.T) {
	dispatcher, _, launcher := idleArbHarness(t, 2, IdleArbConfig{ReserveHulls: 1})
	ledger := newFakeAbsorptionLedger()
	ledger.outErr = fmt.Errorf("ledger down")
	dispatcher.SetAbsorptionLedger(ledger, false, 0)

	if launched := dispatcher.DispatchOnce(context.Background()); launched != 0 || len(launcher.launches) != 0 {
		t.Fatalf("an unreadable ledger must decline all candidates (fail-closed), got %d launches", launched)
	}
	if dispatcher.skipReserved != 2 {
		t.Fatalf("both fail-closed declines are attributed to reserved, got %d", dispatcher.skipReserved)
	}
}

// The consult kill-switch suppresses skip:reserved (so a wedged consult cannot halt
// the harvest) but recording CONTINUES, so the ledger keeps serving other engines.
func TestIdleArb_Consult_KillSwitch_DisablesConsultButStillRecords(t *testing.T) {
	dispatcher, _, launcher := idleArbHarness(t, 2, IdleArbConfig{ReserveHulls: 1})
	ledger := newFakeAbsorptionLedger()
	ledger.preset(nearSink, absorption.KeyOccupancy{PlannedUnits: 20}) // reserved, but consult is OFF
	dispatcher.SetAbsorptionLedger(ledger, true /* consultDisabled */, 0)

	launched := dispatcher.DispatchOnce(context.Background())
	if launched != 1 || len(launcher.launches) != 1 {
		t.Fatalf("with the consult killed the leg dispatches despite the reservation, got %d launches", launched)
	}
	if dispatcher.skipReserved != 0 {
		t.Fatalf("the kill-switch must suppress skip:reserved, got %d", dispatcher.skipReserved)
	}
	if ledger.recordedCount() != 1 {
		t.Fatalf("recording must continue with the consult killed, got %d records", ledger.recordedCount())
	}
}

// A nil ledger leaves the integration fully inert, byte for byte.
func TestIdleArb_NilLedger_Inert(t *testing.T) {
	dispatcher, _, launcher := idleArbHarness(t, 2, IdleArbConfig{ReserveHulls: 1})
	// No SetAbsorptionLedger call → ledger nil.
	if launched := dispatcher.DispatchOnce(context.Background()); launched != 1 || len(launcher.launches) != 1 {
		t.Fatalf("with no ledger the dispatcher behaves exactly as before, got %d launches", launched)
	}
}

// Two dispatchers sharing one ledger collide on a sink whose depth is nearly full —
// the first tops it up over the fillable line, the second's consult sees no
// remaining tranche and skips:reserved. Each dispatcher has its OWN in-memory lane
// mutex (empty of the other's leg), so ONLY the shared ledger prevents the co-dump.
// The sink already carries another engine's 40-unit in-flight leg (of 100 depth), so
// A's 40-unit leg fits (60 remaining) but then leaves only 20 — below B's 40-unit
// tranche.
func TestIdleArb_TwoDispatcherCollision_SecondSkipsReserved(t *testing.T) {
	ledger := newFakeAbsorptionLedger()
	ledger.preset(nearSink, absorption.KeyOccupancy{PlannedUnits: 40}) // another engine's in-flight leg

	dA, _, launcherA := idleArbHarness(t, 2, IdleArbConfig{ReserveHulls: 1})
	dA.SetAbsorptionLedger(ledger, false, 0)
	dB, _, launcherB := idleArbHarness(t, 2, IdleArbConfig{ReserveHulls: 1})
	dB.SetAbsorptionLedger(ledger, false, 0)

	// A goes first: 60 units remain (100 depth − 40 reserved), enough for its 40-unit
	// tranche, so it launches and records its absorption (planned now 80).
	if launched := dA.DispatchOnce(context.Background()); launched != 1 || len(launcherA.launches) != 1 {
		t.Fatalf("dispatcher A must launch into the sink with room for its tranche, got %d", launched)
	}
	if ledger.recordedCount() != 1 {
		t.Fatalf("dispatcher A must record its leg for B to see, got %d records", ledger.recordedCount())
	}

	// B runs next: only 20 units remain (below its 40-unit tranche), and its own mutex
	// is empty of A's leg, so ONLY the shared ledger can stop the collision — and it does.
	logger := &idleArbCapturingLogger{}
	if launched := dB.DispatchOnce(common.WithLogger(context.Background(), logger)); launched != 0 || len(launcherB.launches) != 0 {
		t.Fatalf("dispatcher B must skip the sink A reserved (no co-dump), got %d launches", launched)
	}
	if dB.skipReserved == 0 {
		t.Fatalf("B's skip must be attributed to the ledger (reserved), got %d", dB.skipReserved)
	}
	candidate := logger.messageWithPrefix(t, "Idle-arb candidate:")
	if !strings.Contains(candidate, "verdict skipped:reserved") {
		t.Fatalf("B's candidate line must show skipped:reserved, got: %s", candidate)
	}
}

// The harvest summary carries the reserved counter alongside the existing skip
// reasons for burn-in telemetry. A 70-unit reservation of the 100-deep sink leaves
// 30 — below the 40-unit tranche — so both hulls skip:reserved.
func TestIdleArb_HarvestSummary_IncludesReservedCounter(t *testing.T) {
	dispatcher, _, _ := idleArbHarness(t, 2, IdleArbConfig{ReserveHulls: 1})
	ledger := newFakeAbsorptionLedger()
	ledger.preset(nearSink, absorption.KeyOccupancy{PlannedUnits: 70})
	dispatcher.SetAbsorptionLedger(ledger, false, 0)

	logger := &idleArbCapturingLogger{}
	dispatcher.DispatchOnce(common.WithLogger(context.Background(), logger))

	summary := logger.messageWithPrefix(t, "Idle-arb harvest:")
	if !strings.Contains(summary, "reserved 2") {
		t.Fatalf("harvest summary must report the reserved skip count, got: %s", summary)
	}
}
