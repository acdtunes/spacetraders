package commands

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// sp-duxru — deliveredQuantity is a CACHE of what the construction site holds, and it only ever
// drifts one way. It is written after a supply the server has already accepted, so any interruption
// in that gap leaves it permanently BEHIND. The pipeline then sources material the site no longer
// needs, and the surplus cannot be delivered because the server-side requirement is already met:
// stranded cargo plus wasted capital at ~3,675/unit. The Grafana gate panel reads the same row, so
// the drift also silently misreports gate progress.
//
// Observed 2026-07-24 05:07: the live site read ADVANCED_CIRCUITRY 20/400 while the pipeline row
// read 0/400, and it never healed.
//
// NOTE on the arithmetic being pinned here: the planner sets targetQuantity to the site's REMAINING
// units at planning time (construction_pipeline_planner.go: `remaining := mat.Remaining()`), not to
// its Required. So deliveredQuantity counts units THIS pipeline delivered, while the site's
// Fulfilled counts every unit ever delivered including earlier pipelines. The two are different
// quantities and assigning one to the other is wrong. The pipeline's own delivered total is
// target − site.Remaining(), which is what these tests assert.

// fakeConstructionSiteSource serves a scripted live site read at the ConstructionSiteRepository
// port. Only FindByWaypoint is used by the drain; SupplyMaterial is unreachable here and panics via
// the embedded interface if the drain ever reaches for it.
type fakeConstructionSiteSource struct {
	manufacturing.ConstructionSiteRepository
	mu        sync.Mutex
	materials []manufacturing.ConstructionMaterial
	err       error
	reads     int
}

func (s *fakeConstructionSiteSource) FindByWaypoint(_ context.Context, waypointSymbol string, _ int) (*manufacturing.ConstructionSite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.err != nil {
		return nil, s.err
	}
	return manufacturing.ReconstructConstructionSite(waypointSymbol, "", s.materials, false), nil
}

func (s *fakeConstructionSiteSource) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// liveSite builds a site source reporting one material at required/fulfilled.
func liveSite(good string, required, fulfilled int) *fakeConstructionSiteSource {
	return &fakeConstructionSiteSource{
		materials: []manufacturing.ConstructionMaterial{
			manufacturing.NewConstructionMaterial(good, required, fulfilled),
		},
	}
}

// REGRESSION (B6): a pipeline whose delivered counter is BEHIND the live site is corrected. The
// incident's exact numbers: site 20/400, pipeline row 0/400, never healing.
func TestConstructionDrain_ReconcilesDeliveredQuantityBehindLiveSite(t *testing.T) {
	pipeline := newDrainPipeline(t, "ADVANCED_CIRCUITRY", 400) // planned when the site was 0/400
	task := readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "TORWIND-9", nil))
	site := liveSite("ADVANCED_CIRCUITRY", 400, 20) // the server accepted 20 the row never recorded

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetConstructionSiteSource(site)
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	material := pipeline.GetMaterial("ADVANCED_CIRCUITRY")
	// This tick also delivers 40, so the counter must show the reconciled 20 PLUS this tick's 40.
	// Asserting 60 (not merely ">0") pins that the correction landed and was not double-counted.
	if material.DeliveredQuantity() != 60 {
		t.Fatalf("expected the 20 units the site already holds to be reconciled in and this tick's 40 added (60), got %d", material.DeliveredQuantity())
	}
	if site.readCount() == 0 {
		t.Fatal("the drain never read the live construction site — nothing reconciles the cached counter")
	}
}

// B6 (the economic consequence): once reconciled, the drain must NOT source material the site
// already holds. Site 20/20 means the bill is met; unguarded, the row read 0/20 and the drain
// bought another 20 units it could never deliver, because the server-side requirement was satisfied.
func TestConstructionDrain_ReconciledBillStopsResourcingDeliveredMaterial(t *testing.T) {
	pipeline := newDrainPipeline(t, "ADVANCED_CIRCUITRY", 20)
	task := readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY")

	producer := &fakeConstructionProducer{acquire: 20, delivered: 20}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "TORWIND-9", nil))
	site := liveSite("ADVANCED_CIRCUITRY", 400, 400) // nothing left to deliver at the site

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetConstructionSiteSource(site)
	resp, err := drainSettled(t, handler, context.Background(), newDrainCommand())
	if err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if len(producer.produceGoods) != 0 {
		t.Fatalf("the site's bill is already met — sourcing it again buys cargo that can never be delivered, got %v", producer.produceGoods)
	}
	if len(producer.deliverCalls) != 0 {
		t.Fatalf("expected no delivery attempt against a met bill, got %+v", producer.deliverCalls)
	}
	if resp.TasksDrained != 0 {
		t.Fatalf("expected a clean no-drain tick against a met bill, got TasksDrained=%d", resp.TasksDrained)
	}
}

// B7: a counter AHEAD of the live site is NOT clobbered — it is reported. A lower site reading
// cannot be told apart from a read that raced a delivery, so lowering would drop units that really
// were delivered. A genuine divergence is a different defect and must keep its evidence.
func TestConstructionDrain_SiteBehindLocalCounter_KeepsLocalAndWarnsLoudly(t *testing.T) {
	pipeline := newDrainPipeline(t, "ADVANCED_CIRCUITRY", 400)
	if err := pipeline.RecordMaterialDelivery("ADVANCED_CIRCUITRY", 100); err != nil {
		t.Fatalf("seed delivered: %v", err)
	}
	task := readyConstructionTask(t, pipeline, "ADVANCED_CIRCUITRY")

	producer := &fakeConstructionProducer{acquire: 0} // dry source: this tick adds nothing
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "TORWIND-9", nil))
	site := liveSite("ADVANCED_CIRCUITRY", 400, 40) // site claims far less than the row records
	logger := &capturingLogger{}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetConstructionSiteSource(site)
	if _, err := drainSettled(t, handler, common.WithLogger(context.Background(), logger), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := pipeline.GetMaterial("ADVANCED_CIRCUITRY").DeliveredQuantity(); got != 100 {
		t.Fatalf("a site reading BEHIND the local counter must never lower it (that drops delivered units), got %d", got)
	}
	warned := false
	for _, entry := range logger.snapshot() {
		if strings.Contains(entry.message, "ahead of the live site") && (entry.level == "WARNING" || entry.level == "ERROR") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("a backwards divergence must be reported loudly, not silently kept; logged: %+v", logger.snapshot())
	}
}

// B8 (no lost update): reconciliation writes the same counter recordDelivery serializes under
// recordMu. A site read is necessarily stale by the time it is applied, so a delivery recorded in
// that gap must survive. Deterministic here — the delivery lands first and the reconcile carries a
// site value from before it — which is exactly the interleaving that a clobbering implementation
// loses units on.
func TestConstructionDrain_StaleSiteRead_DoesNotDropAConcurrentDelivery(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 400)
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	task := readyConstructionTask(t, pipeline, "FAB_MATS")
	site := liveSite("FAB_MATS", 400, 0) // read BEFORE the delivery below landed

	handler := NewRunConstructionCoordinatorHandler(&drainStubTaskRepo{}, pipelineRepo, newDrainShipRepo(), &fakeConstructionProducer{}, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetConstructionSiteSource(site)

	// A worker's delivery lands first...
	handler.recordDelivery(context.Background(), task, 80)
	// ...then the stale reconcile applies.
	handler.reconcilePipelinesFromSite(context.Background(), []*manufacturing.ManufacturingTask{task}, 1)

	if got := pipeline.GetMaterial("FAB_MATS").DeliveredQuantity(); got != 80 {
		t.Fatalf("a stale site read must not erase a delivery already recorded, got %d (expected the 80 units to survive)", got)
	}
}

// B8 (under real concurrency): the reconcile and a worker's delivery run in parallel on the same
// pipeline. Whatever the interleaving, the delivered units must never be lost — the property that
// makes the raise-only reconcile safe against recordMu's critical section.
func TestConstructionDrain_ReconcileConcurrentWithDelivery_LosesNoUnits(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 400)
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	task := readyConstructionTask(t, pipeline, "FAB_MATS")
	site := liveSite("FAB_MATS", 400, 25)

	handler := NewRunConstructionCoordinatorHandler(&drainStubTaskRepo{}, pipelineRepo, newDrainShipRepo(), &fakeConstructionProducer{}, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetConstructionSiteSource(site)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); handler.recordDelivery(context.Background(), task, 80) }()
	go func() {
		defer wg.Done()
		handler.reconcilePipelinesFromSite(context.Background(), []*manufacturing.ManufacturingTask{task}, 1)
	}()
	wg.Wait()

	// Reconcile-then-deliver gives 25+80; deliver-then-reconcile gives 80 (the site value is lower,
	// so it is ignored). Either is correct; anything below 80 means the delivery was dropped.
	if got := pipeline.GetMaterial("FAB_MATS").DeliveredQuantity(); got < 80 {
		t.Fatalf("a concurrent reconcile dropped delivered units: got %d, the 80 delivered must always survive", got)
	}
}

// sp-12k38 — the correction is raise-only, so a pipeline whose every material already reads
// delivered >= target cannot be moved by a live reading: the Get Construction is pure API cost
// (measured 87/h against a gate whose bill was fully paid). These three pin the whole predicate —
// skip only when nothing is outstanding, and fail toward reading live otherwise.
func TestConstructionDrain_FullyDeliveredPipeline_SkipsTheLiveSiteRead(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 400)
	if err := pipeline.RecordMaterialDelivery("FAB_MATS", 400); err != nil {
		t.Fatalf("seed delivered: %v", err)
	}
	task := readyConstructionTask(t, pipeline, "FAB_MATS")
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	site := liveSite("FAB_MATS", 400, 400)

	handler := newReconcileHandler(pipelineRepo, site)
	handler.reconcilePipelinesFromSite(context.Background(), []*manufacturing.ManufacturingTask{task}, 1)

	if site.readCount() != 0 {
		t.Fatalf("a fully delivered pipeline must not spend a live site read (raise-only cannot change it), got %d read(s)", site.readCount())
	}
}

func TestConstructionDrain_MaterialStillShort_ReadsTheLiveSite(t *testing.T) {
	pipeline := manufacturing.NewConstructionPipeline(constructionSiteWP, 1, 1, 1)
	for _, material := range []*manufacturing.ConstructionMaterialTarget{
		manufacturing.NewConstructionMaterialTarget("FAB_MATS", 400),
		manufacturing.NewConstructionMaterialTarget("ADVANCED_CIRCUITRY", 100),
	} {
		if err := pipeline.AddMaterial(material); err != nil {
			t.Fatalf("AddMaterial: %v", err)
		}
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	if err := pipeline.RecordMaterialDelivery("FAB_MATS", 400); err != nil {
		t.Fatalf("seed delivered: %v", err)
	}
	task := readyConstructionTask(t, pipeline, "FAB_MATS")
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	site := liveSite("ADVANCED_CIRCUITRY", 100, 40)

	handler := newReconcileHandler(pipelineRepo, site)
	handler.reconcilePipelinesFromSite(context.Background(), []*manufacturing.ManufacturingTask{task}, 1)

	if site.readCount() != 1 {
		t.Fatalf("one material below target must still reconcile against the server, got %d read(s)", site.readCount())
	}
	if got := pipeline.GetMaterial("ADVANCED_CIRCUITRY").DeliveredQuantity(); got != 40 {
		t.Fatalf("the live reading must still be applied to the short material, got %d delivered", got)
	}
}

func TestConstructionDrain_PipelineLoadFailure_ReadsTheLiveSite(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 400)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")
	site := liveSite("FAB_MATS", 400, 0)

	handler := newReconcileHandler(&unreadablePipelineRepo{}, site)
	handler.reconcilePipelinesFromSite(context.Background(), []*manufacturing.ManufacturingTask{task}, 1)

	if site.readCount() != 1 {
		t.Fatalf("an unreadable pipeline must fall back to today's live read, got %d read(s)", site.readCount())
	}
}

// unreadablePipelineRepo fails every load, the "fail toward the current behaviour" branch.
type unreadablePipelineRepo struct {
	manufacturing.PipelineRepository
}

func (r *unreadablePipelineRepo) FindByID(context.Context, string) (*manufacturing.ManufacturingPipeline, error) {
	return nil, errors.New("pipeline row unavailable")
}

func newReconcileHandler(pipelineRepo manufacturing.PipelineRepository, site *fakeConstructionSiteSource) *RunConstructionCoordinatorHandler {
	handler := NewRunConstructionCoordinatorHandler(&drainStubTaskRepo{}, pipelineRepo, newDrainShipRepo(), &fakeConstructionProducer{}, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetConstructionSiteSource(site)
	return handler
}

// An unwired site source must be LOUD, not silent: a cache that only drifts one way and is never
// reconciled is exactly the incident, and a coordinator running without the read should say so
// rather than look healthy.
func TestConstructionDrain_UnwiredSiteSource_ReportsReconciliationDisabled(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := readyConstructionTask(t, pipeline, "FAB_MATS")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "TORWIND-9", nil))
	logger := &capturingLogger{}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	if _, err := drainSettled(t, handler, common.WithLogger(context.Background(), logger), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	reported := false
	for _, entry := range logger.snapshot() {
		if strings.Contains(entry.message, "reconciliation is DISABLED") {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("an unwired site source must announce that the delivered counters are unreconciled; logged: %+v", logger.snapshot())
	}
	// The drain still works — an unreconciled counter is a reporting hazard, never a reason to stall.
	if pipeline.ConstructionProgress() <= 0 {
		t.Fatal("an unwired site source must not stop the drain from delivering")
	}
}
