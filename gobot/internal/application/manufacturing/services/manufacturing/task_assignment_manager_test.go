package manufacturing

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// sp-q0xm: TaskQueue.GetReadyTasks() sorts by effective priority (base priority +
// an aging bonus of +2/min, capped at +100 - see task.go). Every task type shares
// the same aging formula, so a COLLECT_SELL task (base 50) that has waited over
// ~12.5 minutes reaches effective priority 75+, matching or beating a freshly-ready
// DELIVER_TO_CONSTRUCTION task's static 75 - and a 25+ minute wait pushes it past
// entirely. AssignTasks then walks readyTasks in that order and hands the idle-ship
// pool out greedily, one ship per task, stopping once ships run out. So once a
// COLLECT_SELL/ACQUIRE_DELIVER backlog ages enough (bead: "10-14 queued COLLECT_SELL
// tasks"), it can starve mission-critical construction of workers even though
// construction is the higher mission priority. prioritizeConstructionTasks restores
// a hard construction-first tier ahead of the existing priority+aging order, without
// disturbing that order *within* each tier.
func TestPrioritizeConstructionTasks_MovesConstructionAheadOfAgedManufacturing(t *testing.T) {
	construction := manufacturing.NewDeliverToConstructionTask("pipe-construction", 1, "ADVANCED_CIRCUITRY", "MARKET-A", "", "X1-SITE", nil)
	agedCollectSell := manufacturing.NewCollectSellTask("pipe-mfg", 1, "ELECTRONICS", "FACTORY-A", "MARKET-B", nil)
	agedAcquireDeliver := manufacturing.NewAcquireDeliverTask("pipe-mfg", 1, "COPPER_ORE", "MARKET-C", "FACTORY-B", nil)

	// Simulate the slice GetReadyTasks() would return once aging has let the
	// manufacturing backlog outrank the fresh construction task.
	readyTasks := []*manufacturing.ManufacturingTask{agedCollectSell, agedAcquireDeliver, construction}

	got := prioritizeConstructionTasks(readyTasks)

	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3", len(got))
	}
	if got[0] != construction {
		t.Errorf("got[0] = task %s (%s), want the DELIVER_TO_CONSTRUCTION task first", got[0].ID(), got[0].TaskType())
	}
}

// Acceptance scenario: given one idle hull, one ready construction task, and one
// ready manufacturing task, AssignTasks hands the sole idle ship to whichever task
// sits at index 0 of readyTasks - so construction must be first.
func TestPrioritizeConstructionTasks_SingleConstructionSingleManufacturing_ConstructionFirst(t *testing.T) {
	manufacturingTask := manufacturing.NewCollectSellTask("pipe-mfg", 1, "ELECTRONICS", "FACTORY-A", "MARKET-B", nil)
	constructionTask := manufacturing.NewDeliverToConstructionTask("pipe-construction", 1, "ADVANCED_CIRCUITRY", "MARKET-A", "", "X1-SITE", nil)

	got := prioritizeConstructionTasks([]*manufacturing.ManufacturingTask{manufacturingTask, constructionTask})

	if got[0].TaskType() != manufacturing.TaskTypeDeliverToConstruction {
		t.Errorf("got[0].TaskType() = %s, want %s", got[0].TaskType(), manufacturing.TaskTypeDeliverToConstruction)
	}
}

// The reordering must be a stable partition: it should not disturb the existing,
// well-tested priority+aging order within the construction group or within the
// non-construction group - only hoist construction as a group ahead of the rest.
func TestPrioritizeConstructionTasks_PreservesRelativeOrderWithinGroups(t *testing.T) {
	construction1 := manufacturing.NewDeliverToConstructionTask("pipe-c", 1, "GOOD_A", "MARKET-A", "", "X1-SITE", nil)
	construction2 := manufacturing.NewDeliverToConstructionTask("pipe-c", 1, "GOOD_B", "MARKET-A", "", "X1-SITE", nil)
	collectSell := manufacturing.NewCollectSellTask("pipe-mfg", 1, "ELECTRONICS", "FACTORY-A", "MARKET-B", nil)
	acquireDeliver := manufacturing.NewAcquireDeliverTask("pipe-mfg", 1, "COPPER_ORE", "MARKET-C", "FACTORY-B", nil)

	input := []*manufacturing.ManufacturingTask{collectSell, construction1, acquireDeliver, construction2}

	got := prioritizeConstructionTasks(input)

	want := []*manufacturing.ManufacturingTask{construction1, construction2, collectSell, acquireDeliver}
	for i, task := range want {
		if got[i] != task {
			t.Errorf("got[%d] = task %s, want task %s", i, got[i].ID(), task.ID())
		}
	}
}

// No ready construction tasks: nothing to hoist, order passes through unchanged.
func TestPrioritizeConstructionTasks_NoConstructionTasks_ReturnsInputUnchanged(t *testing.T) {
	collectSell := manufacturing.NewCollectSellTask("pipe-mfg", 1, "ELECTRONICS", "FACTORY-A", "MARKET-B", nil)
	acquireDeliver := manufacturing.NewAcquireDeliverTask("pipe-mfg", 1, "COPPER_ORE", "MARKET-C", "FACTORY-B", nil)
	input := []*manufacturing.ManufacturingTask{collectSell, acquireDeliver}

	got := prioritizeConstructionTasks(input)

	if len(got) != 2 || got[0] != collectSell || got[1] != acquireDeliver {
		t.Errorf("got = %v, want input unchanged [%v %v]", got, collectSell, acquireDeliver)
	}
}

// Empty/nil input must not panic.
func TestPrioritizeConstructionTasks_EmptyInput_ReturnsEmpty(t *testing.T) {
	got := prioritizeConstructionTasks(nil)
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

// All-construction input: already a single tier, order preserved, nothing lost.
func TestPrioritizeConstructionTasks_AllConstruction_ReturnsAllUnchanged(t *testing.T) {
	construction1 := manufacturing.NewDeliverToConstructionTask("pipe-c", 1, "GOOD_A", "MARKET-A", "", "X1-SITE", nil)
	construction2 := manufacturing.NewDeliverToConstructionTask("pipe-c", 1, "GOOD_B", "MARKET-A", "", "X1-SITE", nil)
	input := []*manufacturing.ManufacturingTask{construction1, construction2}

	got := prioritizeConstructionTasks(input)

	if len(got) != 2 || got[0] != construction1 || got[1] != construction2 {
		t.Errorf("got = %v, want input unchanged [%v %v]", got, construction1, construction2)
	}
}
