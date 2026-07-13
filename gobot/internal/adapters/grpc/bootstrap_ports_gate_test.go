package grpc

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// sp-382j: the bootstrap GATE false-adoption fix. Before this, a planner-set EXECUTING
// pipeline status read as "adopted" even with NO executor running, so the gate silently
// skipped the ensure/launch and the pipeline sat EXECUTING@0% forever. Adoption must key on a
// RUNNING construction executor, not the pipeline-status string. The construction drain
// re-polls its worklist every tick, so a running drain IS continuously adopting — running is
// the adoption signal (keying on live progress instead would thrash-bounce a supply-starved
// drain, which a restart cannot help).

func TestConstructionExecutorAdopted_NoExecutor_NotAdopted(t *testing.T) {
	executing := "EXECUTING"
	// #2: EXECUTING with no running ConstructionCoordinator must NOT read as adopted — this is
	// what lets the gate fall through to EnsureRunning (which launches the drain).
	if constructionExecutorAdopted(false, &executing) {
		t.Fatal("EXECUTING with no running ConstructionCoordinator must NOT be adopted (the false-adoption bug)")
	}
	// Even a nil/absent status must not be adopted without a running executor.
	if constructionExecutorAdopted(false, nil) {
		t.Fatal("no running ConstructionCoordinator must never read as adopted")
	}
}

func TestConstructionExecutorAdopted_RunningExecutor_Adopted(t *testing.T) {
	executing := "EXECUTING"
	planning := "PLANNING"
	// #3: a running ConstructionCoordinator IS adopted — it re-polls and works the pipeline's
	// tasks continuously, regardless of the pipeline-status string.
	if !constructionExecutorAdopted(true, &executing) {
		t.Fatal("a running ConstructionCoordinator must read as adopted")
	}
	if !constructionExecutorAdopted(true, &planning) {
		t.Fatal("adoption must key on the running executor, not the pipeline-status string")
	}
}

// The gate must look for the dedicated ConstructionCoordinator (the real drain), not the
// vestigial manufacturing/goods-factory container types, so EnsureRunning/BounceForAdoption
// target the actual executor.
func TestExecutorContainerTypes_TargetsConstructionCoordinator(t *testing.T) {
	found := false
	for _, ct := range executorContainerTypes {
		if ct == container.ContainerTypeConstructionCoordinator {
			found = true
		}
	}
	if !found {
		t.Fatalf("executorContainerTypes must include ConstructionCoordinator, got %v", executorContainerTypes)
	}
}
