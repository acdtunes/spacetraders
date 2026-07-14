package commands

import (
	"context"
	"testing"

	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-vh1s Part A — the construction coordinator as a UNIFIED GATE-FILL wrapper. Under the toggle the
// drain drives a goods-factory run per gate material with feeding INHERENT in the tree: it
// short-circuits the old bespoke buy-vs-fabricate planner decision (which froze a pure-BUY at plan
// time and fed NOTHING — the bug) and stamps the run as a gate node so the output-buy is
// throughput-paced and lane B's per-node gates go margin-blind. OFF is byte-identical.

// gateProbeProducer captures, per ProduceGood call, the node handed to it and the gate-mode signals
// on the run context, so a test can prove the drain drove a gate-node run. Satisfies ConstructionProducer.
type gateProbeProducer struct {
	acquire, delivered int
	nodes              []*goods.SupplyChainNode
	gateNodeFlags      []bool
	deliveryTargets    []string
}

func (p *gateProbeProducer) ProduceGood(ctx context.Context, _ *navigation.Ship, node *goods.SupplyChainNode, _ string, _ int, _ *shared.OperationContext, _ bool) (*mfgServices.ProductionResult, error) {
	p.nodes = append(p.nodes, node)
	p.gateNodeFlags = append(p.gateNodeFlags, mfgServices.IsUnifiedGateNode(ctx))
	p.deliveryTargets = append(p.deliveryTargets, mfgServices.DeliveryTargetFromContext(ctx).SiteWaypoint())
	return &mfgServices.ProductionResult{QuantityAcquired: p.acquire}, nil
}

func (p *gateProbeProducer) DeliverToConstructionSite(_ context.Context, _, _, _ string, _ shared.PlayerID) (int, error) {
	return p.delivered, nil
}

// gateFabricateTree is a FAB_MATS fabrication tree (fabricate root + a bought input) the recording
// resolver returns, so a test can distinguish "the full tree drove the run" from a bare planner BUY.
func gateFabricateTree() *goods.SupplyChainNode {
	root := goods.NewSupplyChainNode("FAB_MATS", goods.AcquisitionFabricate)
	root.AddChild(goods.NewSupplyChainNode("IRON", goods.AcquisitionBuy))
	return root
}

// Short-circuit contract: a BUY-FINAL material (planner froze a pure-BUY, FactorySymbol == "") is
// sourced cold with zero feeding today. Under the toggle the drain IGNORES that frozen decision and
// drives the resolver's FULL scarcity-gated tree (feeding inherent); with the toggle OFF it keeps the
// frozen bare-BUY and never consults the resolver (byte-identical).
func TestConstructionDrain_UnifiedGateFill_ShortCircuitsFrozenBuyFinal(t *testing.T) {
	cases := []struct {
		name          string
		unified       bool
		wantResolver  int
		wantFabricate bool
	}{
		{name: "toggle off keeps the frozen pure-BUY (resolver bypassed)", unified: false, wantResolver: 0, wantFabricate: false},
		{name: "toggle on short-circuits to the full tree (resolver consulted)", unified: true, wantResolver: 1, wantFabricate: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pipeline := newDrainPipeline(t, "FAB_MATS", 100)
			task := readyConstructionTask(t, pipeline, "FAB_MATS") // factory "" => the planner's frozen pure-BUY
			producer := &gateProbeProducer{acquire: 40, delivered: 40}
			taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
			pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
			shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))
			resolver := &recordingResolver{tree: gateFabricateTree()}

			handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
			handler.SetTreeResolver(resolver)

			cmd := newDrainCommand()
			cmd.UnifiedGateFill = tc.unified
			if _, err := handler.drainOnce(context.Background(), cmd); err != nil {
				t.Fatalf("drainOnce: %v", err)
			}

			if resolver.calls != tc.wantResolver {
				t.Fatalf("resolver consulted %d times, want %d (toggle=%v)", resolver.calls, tc.wantResolver, tc.unified)
			}
			if len(producer.nodes) != 1 {
				t.Fatalf("expected exactly one ProduceGood call, got %d", len(producer.nodes))
			}
			gotFabricate := producer.nodes[0].AcquisitionMethod == goods.AcquisitionFabricate
			if gotFabricate != tc.wantFabricate {
				t.Fatalf("driven node fabricate=%v, want %v (toggle=%v) — ON must drive the full tree, OFF the frozen bare-BUY", gotFabricate, tc.wantFabricate, tc.unified)
			}
		})
	}
}

// Gate-mode stamp: under the toggle the drain marks the produce context a unified gate node (so the
// output-buy is throughput-paced and lane B's gates go margin-blind) carrying the gate waypoint; with
// the toggle OFF the run is never a gate node (byte-identical).
func TestConstructionDrain_UnifiedGateFill_StampsGateModeOnProduceContext(t *testing.T) {
	cases := []struct {
		name       string
		unified    bool
		wantGate   bool
		wantTarget string
	}{
		{name: "toggle off is never a gate node", unified: false, wantGate: false, wantTarget: ""},
		{name: "toggle on marks a gate node carrying the site", unified: true, wantGate: true, wantTarget: constructionSiteWP},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pipeline := newDrainPipeline(t, "FAB_MATS", 100)
			task := readyConstructionTask(t, pipeline, "FAB_MATS")
			producer := &gateProbeProducer{acquire: 40, delivered: 40}
			taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
			pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
			shipRepo := newDrainShipRepo(newTestHauler(t, "HAULER-7", nil))

			handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})

			cmd := newDrainCommand()
			cmd.UnifiedGateFill = tc.unified
			if _, err := handler.drainOnce(context.Background(), cmd); err != nil {
				t.Fatalf("drainOnce: %v", err)
			}

			if len(producer.gateNodeFlags) != 1 {
				t.Fatalf("expected exactly one ProduceGood call, got %d", len(producer.gateNodeFlags))
			}
			if producer.gateNodeFlags[0] != tc.wantGate {
				t.Fatalf("IsUnifiedGateNode on the produce ctx = %v, want %v (toggle=%v)", producer.gateNodeFlags[0], tc.wantGate, tc.unified)
			}
			if producer.deliveryTargets[0] != tc.wantTarget {
				t.Fatalf("delivery target waypoint = %q, want %q (toggle=%v)", producer.deliveryTargets[0], tc.wantTarget, tc.unified)
			}
		})
	}
}
