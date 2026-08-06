package commands

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
)

// sp-1f0ex: THE CALIBRATION TEST. A DELIVERY MUST BE RECORDABLE AT ALL.
//
// The sp-63r4f stall watchdog keys "did this material receive units" on
// ManufacturingTask.ActualQuantity. That field was never written by anything: SetActualQuantity had
// exactly one reference in the whole tree — its own definition — so all 44 rows in production read
// actual_quantity = 0. The watchdog therefore could never observe a delivery, and reported FAB_MATS
// as stalled for 30.8 hours while the pipeline was actively taking 134 units an hour.
//
// THIS TEST DRIVES THE PRODUCTION WRITE PATH ON PURPOSE. A test that constructs a task and calls
// SetActualQuantity itself passes against the broken code — the domain field works fine, nothing
// ever writes it — and would have proved nothing. Only completing a supply the way the drain
// completes one can tell a live field from a dead one.
func TestCompleteSupply_RecordsTheUnitsItDelivered(t *testing.T) {
	const delivered = 40

	f := newGateFactoryHandler(t)
	ship := gateTestHull(t, "GF-REC", gate.FactoryFleetTag)
	lot := gateTestLot(t, ship)
	pipeline, err := f.pipeline.FindByID(f.ctx(), gateTestPipelineID)
	if err != nil {
		t.Fatalf("reading the fixture pipeline: %v", err)
	}

	if !f.handler.completeSupply(f.ctx(), &supplyLeg{lot: lot, ship: ship, pipeline: pipeline}, delivered) {
		t.Fatal("completeSupply reported no completion")
	}

	snap, ok := f.taskRepo.snapshot(lot.task.ID())
	if !ok {
		t.Fatal("the completion persisted nothing at all")
	}
	if snap.status != manufacturing.TaskStatusCompleted {
		t.Fatalf("persisted status %q, want COMPLETED", snap.status)
	}
	if snap.actualQuantity != delivered {
		t.Fatalf("the completed task recorded actual_quantity = %d after delivering %d units. Nothing writes that field, so every completed task in production reads 0 — and the sp-63r4f stall watchdog, which keys on it, can NEVER see a delivery. That is how FAB_MATS reported a 30.8-hour stall while taking 134 units an hour",
			snap.actualQuantity, delivered)
	}
}

// AND THE WATCHDOG MUST THEN READ ZERO STALL FOR IT.
//
// The healthy fixture is an UNMET material with a recent delivery, deliberately. A satisfied bill
// also reads zero — that is the other zero-path — and would read zero even if delivery detection
// stayed completely broken, so it proves nothing. This one only passes if the delivery is actually
// observable.
func TestWatchGateProgress_AnUnmetMaterialReceivingUnitsIsNotStalled(t *testing.T) {
	f := newGateFactoryHandler(t)
	ship := gateTestHull(t, "GF-LIVE", gate.FactoryFleetTag)
	lot := gateTestLot(t, ship)
	pipeline, err := f.pipeline.FindByID(f.ctx(), gateTestPipelineID)
	if err != nil {
		t.Fatalf("reading the fixture pipeline: %v", err)
	}

	// NON-VACUITY: the material must be UNMET, or this is the satisfied-bill zero-path and says
	// nothing about whether a delivery can be seen.
	var unmet bool
	for _, m := range pipeline.Materials() {
		if m.TradeSymbol() == lot.task.Good() && m.RemainingQuantity() > 0 {
			unmet = true
		}
	}
	if !unmet {
		t.Fatalf("fixture is inert: %s is already satisfied, so a zero stall would be the satisfied-bill path rather than evidence that a delivery was observed", lot.task.Good())
	}

	if !f.handler.completeSupply(f.ctx(), &supplyLeg{lot: lot, ship: ship, pipeline: pipeline}, 40) {
		t.Fatal("completeSupply reported no completion")
	}

	stalls := f.handler.watchGateProgress(f.ctx(), pipeline, 1, time.Now().UTC())

	for _, v := range stalls {
		if v.Good == lot.task.Good() {
			t.Fatalf("%s reported STALLED for %s immediately after a 40-unit delivery. This is the false alarm: 30.8 hours claimed while the pipeline was actively receiving units",
				v.Good, v.StalledFor)
		}
	}
}

// AND A GENUINELY STALLED MATERIAL STILL REPORTS. The fix must not be a weakened alarm — an unmet
// material with NO completed delivery must still be caught.
func TestWatchGateProgress_AGenuinelyStalledMaterialStillReports(t *testing.T) {
	f := newGateFactoryHandler(t)
	pipeline, err := f.pipeline.FindByID(f.ctx(), gateTestPipelineID)
	if err != nil {
		t.Fatalf("reading the fixture pipeline: %v", err)
	}

	// TWO TICKS ARE REQUIRED, and that is the sp-zx0tu semantics rather than a weakening: the
	// watchdog now measures the REMAINING count's movement, so a single observation has nothing to
	// compare against and deliberately reports nothing (a cold start must never read as a stall).
	// The stall is the requirement failing to move BETWEEN ticks.
	now := time.Now().UTC()
	f.handler.watchGateProgress(f.ctx(), pipeline, 1, now)
	stalls := f.handler.watchGateProgress(f.ctx(), pipeline, 1, now.Add(48*time.Hour))

	if len(stalls) == 0 {
		t.Fatal("no stall reported for a pipeline that has delivered nothing in 48 hours — fixing the false alarm must not silence the true one")
	}
}
