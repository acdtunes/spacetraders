package commands

import (
	"context"
	"errors"
	"testing"

	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// sp-yfzi — the construction drain's FABRICATE branch now builds the FULL scarcity-gated dependency
// tree via the shared resolver, not the flat one-level "fabricate root, buy every input" node.
// These pin the DRAIN's orchestration at the produce seam (fakeConstructionProducer captures the
// exact node handed to ProduceGood):
//   - a FABRICATE material resolves a RECURSIVE tree (scarce intermediate with a factory fabricates,
//     abundant terminates) through the real resolver, depth threaded from the pipeline;
//   - a BUY-FINAL material never consults the resolver — byte-identical AcquisitionBuy;
//   - a resolver error falls back to the one-level node (RULING #1, never dies, never worse).

// treeMarketRepo answers only the two reads the resolver makes (mirrors the services-package
// depthCapMarketRepo, which is unexported there). A good is fabricable in-system iff it has a
// factory entry; buyable iff it has a buyable entry.
type treeMarketRepo struct {
	market.MarketRepository
	factories map[string]*market.FactoryResult
	buyable   map[string]*market.BestBuyingMarketResult
}

func (r *treeMarketRepo) FindFactoryForGood(_ context.Context, good, _ string, _ int) (*market.FactoryResult, error) {
	return r.factories[good], nil
}

func (r *treeMarketRepo) FindBestMarketForBuying(_ context.Context, good, _ string, _ int) (*market.BestBuyingMarketResult, error) {
	return r.buyable[good], nil
}

// scarceChainResolver wires ADVANCED_CIRCUITRY -> ELECTRONICS -> SILICON_CRYSTALS where ELECTRONICS
// is SCARCE with a factory (the recursion target) and SILICON is ABUNDANT (the terminator).
func scarceChainResolver() *mfgServices.SupplyChainResolver {
	supplyChainMap := map[string][]string{
		"ADVANCED_CIRCUITRY": {"ELECTRONICS"},
		"ELECTRONICS":        {"SILICON_CRYSTALS"},
		"SILICON_CRYSTALS":   {},
	}
	repo := &treeMarketRepo{
		factories: map[string]*market.FactoryResult{
			"ADVANCED_CIRCUITRY": {WaypointSymbol: "X1-GT-AC", Supply: "MODERATE", Activity: "STRONG"},
			"ELECTRONICS":        {WaypointSymbol: "X1-GT-EL", Supply: "SCARCE", Activity: "STRONG"},
		},
		buyable: map[string]*market.BestBuyingMarketResult{
			"ELECTRONICS":      {WaypointSymbol: "X1-GT-EL", Supply: "SCARCE", Activity: "STRONG", SellPrice: 2595},
			"SILICON_CRYSTALS": {WaypointSymbol: "X1-GT-SC", Supply: "ABUNDANT", Activity: "STRONG", SellPrice: 50},
		},
	}
	return mfgServices.NewSupplyChainResolver(supplyChainMap, repo)
}

// newDrainPipelineWithDepth builds an EXECUTING construction pipeline with a chosen SupplyChainDepth
// so the drain threads it as the fabricate depth cap.
func newDrainPipelineWithDepth(t *testing.T, good string, targetQty, depth int) *manufacturing.ManufacturingPipeline {
	t.Helper()
	pipeline := manufacturing.NewConstructionPipeline(constructionSiteWP, 1, depth, 1)
	if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget(good, targetQty)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	return pipeline
}

func fabricateReadyTask(t *testing.T, pipeline *manufacturing.ManufacturingPipeline, good, factory string) *manufacturing.ManufacturingTask {
	t.Helper()
	task := manufacturing.NewDeliverToConstructionTask(pipeline.ID(), 1, good, "", factory, constructionSiteWP, nil)
	if err := task.MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	return task
}

// A FABRICATE material resolves the RECURSIVE scarcity-gated tree: ADVANCED_CIRCUITRY fabricates,
// its SCARCE-with-a-factory input ELECTRONICS fabricates (recurses), and the ABUNDANT grandchild
// SILICON is bought. The pipeline's SupplyChainDepth of 0 resolves to the depth-3 default, so the
// recursion is NOT collapsed to the old one-level node.
func TestConstructionDrain_FabricateMaterial_ResolvesScarcityGatedTree(t *testing.T) {
	pipeline := newDrainPipelineWithDepth(t, "ADVANCED_CIRCUITRY", 100, 0)
	task := fabricateReadyTask(t, pipeline, "ADVANCED_CIRCUITRY", "X1-GT-AC")

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetTreeResolver(scarceChainResolver())

	cmd := newDrainCommand()
	cmd.ProductionStrategy = mfgServices.DefaultProductionStrategy // the launch build's smart default
	if _, err := handler.drainOnce(context.Background(), cmd); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if len(producer.produceNodes) != 1 {
		t.Fatalf("expected exactly one ProduceGood call, got %d", len(producer.produceNodes))
	}
	root := producer.produceNodes[0]
	if root.Good != "ADVANCED_CIRCUITRY" || root.AcquisitionMethod != goods.AcquisitionFabricate {
		t.Fatalf("root must be ADVANCED_CIRCUITRY FABRICATE, got good=%s method=%s", root.Good, root.AcquisitionMethod)
	}
	// The KEY assertion vs. the old one-level node: the SCARCE input is FABRICATED and recurses,
	// not bought flat.
	electronics := childByGoodNode(root, "ELECTRONICS")
	if electronics == nil || electronics.AcquisitionMethod != goods.AcquisitionFabricate {
		t.Fatalf("SCARCE ELECTRONICS with a factory must FABRICATE (recursive tree, not one-level buy), got %+v", electronics)
	}
	silicon := childByGoodNode(electronics, "SILICON_CRYSTALS")
	if silicon == nil || silicon.AcquisitionMethod != goods.AcquisitionBuy {
		t.Fatalf("ABUNDANT SILICON_CRYSTALS must BUY (terminate the recursion), got %+v", silicon)
	}
}

// recordingResolver records every BuildDependencyTree call and returns a scripted result, so a test
// can prove whether the drain consulted the resolver.
type recordingResolver struct {
	calls int
	tree  *goods.SupplyChainNode
	err   error
}

func (r *recordingResolver) BuildDependencyTree(_ context.Context, _, _ string, _ int) (*goods.SupplyChainNode, error) {
	r.calls++
	return r.tree, r.err
}

// A BUY-FINAL material (no factory) never consults the resolver and stays a direct AcquisitionBuy —
// byte-identical to pre-sp-yfzi, even with the resolver wired.
func TestConstructionDrain_BuyFinalMaterial_BypassesResolver(t *testing.T) {
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := readyConstructionTask(t, pipeline, "FAB_MATS") // factory "" => buy-final branch

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	resolver := &recordingResolver{tree: goods.NewSupplyChainNode("FAB_MATS", goods.AcquisitionFabricate)}
	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetTreeResolver(resolver)

	cmd := newDrainCommand()
	cmd.ProductionStrategy = mfgServices.DefaultProductionStrategy
	if _, err := handler.drainOnce(context.Background(), cmd); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if resolver.calls != 0 {
		t.Fatalf("buy-final material must NOT consult the resolver, got %d calls", resolver.calls)
	}
	if len(producer.produceNodes) != 1 {
		t.Fatalf("expected exactly one ProduceGood call, got %d", len(producer.produceNodes))
	}
	node := producer.produceNodes[0]
	if node.AcquisitionMethod != goods.AcquisitionBuy || len(node.Children) != 0 {
		t.Fatalf("buy-final material must be a direct BUY leaf, got %+v", node)
	}
}

// RULING #1 — when the resolver errors (stale/absent market data), the FABRICATE branch falls back
// to the original one-level node rather than dying: FAB_MATS fabricates with its immediate inputs
// bought.
func TestConstructionDrain_ResolverError_FallsBackToOneLevelNode(t *testing.T) {
	const factoryWP = "X1-TEST-FACTORY"
	pipeline := newDrainPipeline(t, "FAB_MATS", 100)
	task := fabricateReadyTask(t, pipeline, "FAB_MATS", factoryWP)

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

	resolver := &recordingResolver{err: errors.New("no factory: stale market data")}
	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetTreeResolver(resolver)

	cmd := newDrainCommand()
	cmd.ProductionStrategy = mfgServices.DefaultProductionStrategy
	if _, err := handler.drainOnce(context.Background(), cmd); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if resolver.calls != 1 {
		t.Fatalf("fabricate branch must consult the resolver once, got %d calls", resolver.calls)
	}
	if len(producer.produceNodes) != 1 {
		t.Fatalf("expected exactly one ProduceGood call, got %d", len(producer.produceNodes))
	}
	node := producer.produceNodes[0]
	if node.Good != "FAB_MATS" || node.AcquisitionMethod != goods.AcquisitionFabricate {
		t.Fatalf("resolver-error fallback must be the one-level FAB_MATS FABRICATE node, got good=%s method=%s", node.Good, node.AcquisitionMethod)
	}
	gotInputs := map[string]goods.AcquisitionMethod{}
	for _, c := range node.Children {
		gotInputs[c.Good] = c.AcquisitionMethod
	}
	for _, want := range []string{"IRON", "QUARTZ_SAND"} {
		if gotInputs[want] != goods.AcquisitionBuy {
			t.Fatalf("fallback one-level node must buy immediate input %s, got children %+v", want, node.Children)
		}
	}
}

// childByGoodNode returns the direct child of node with the given good, or nil.
func childByGoodNode(node *goods.SupplyChainNode, good string) *goods.SupplyChainNode {
	for _, c := range node.Children {
		if c.Good == good {
			return c
		}
	}
	return nil
}
