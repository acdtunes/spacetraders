package commands

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// loopWiringMediator drives RunWorkflowHandler.Handle end-to-end through the REAL
// per-contract cycle, one contract after another. Every negotiated contract is
// pre-built already fully delivered (UnitsFulfilled == UnitsRequired) so
// ProcessAllDeliveries is a no-op and no navigation/purchase command is needed —
// the point of this test is the LOOP (does Handle re-run the cycle?), not the
// delivery pipeline (covered by run_contract_workflow_test.go). It stops the
// container (cancels ctx) after stopAfter fulfillments so the loop terminates.
type loopWiringMediator struct {
	common.Mediator

	contractRepo *workflowStubContractRepo
	queue        []*contract.Contract // pre-built pre-fulfilled contracts, popped per negotiate
	cancel       context.CancelFunc
	stopAfter    int

	negotiated   int
	fulfilledIDs []string
}

func (m *loopWiringMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *contractQueries.EvaluateContractProfitabilityQuery:
		return nil, fmt.Errorf("no market data available in test") // non-fatal in executeWorkflow

	case *NegotiateContractCommand:
		if m.negotiated >= len(m.queue) {
			return nil, fmt.Errorf("test queue exhausted after %d negotiations", m.negotiated)
		}
		c := m.queue[m.negotiated]
		m.negotiated++
		if err := m.contractRepo.Add(ctx, c); err != nil {
			return nil, err
		}
		return &NegotiateContractResponse{Contract: c, WasNegotiated: true}, nil

	case *AcceptContractCommand:
		c, err := m.contractRepo.FindByID(ctx, cmd.ContractID)
		if err != nil {
			return nil, err
		}
		if !c.Accepted() {
			if err := c.Accept(); err != nil {
				return nil, err
			}
		}
		return &AcceptContractResponse{Contract: c}, nil

	case *FulfillContractCommand:
		c, err := m.contractRepo.FindByID(ctx, cmd.ContractID)
		if err != nil {
			return nil, err
		}
		if err := c.Fulfill(); err != nil {
			return nil, err
		}
		m.fulfilledIDs = append(m.fulfilledIDs, cmd.ContractID)
		if len(m.fulfilledIDs) >= m.stopAfter {
			m.cancel() // request container stop after enough contracts
		}
		return &FulfillContractResponse{Contract: c}, nil

	default:
		return nil, fmt.Errorf("unexpected mediator command in loop wiring test: %T", request)
	}
}

// buildPreFulfilledContracts returns n fresh contracts, each already fully
// delivered and not yet accepted, with unique IDs.
func buildPreFulfilledContracts(t *testing.T, n int) []*contract.Contract {
	t.Helper()
	out := make([]*contract.Contract, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, mustNewWorkflowTestContract(t, fmt.Sprintf("contract-%d", i+1), 80))
	}
	return out
}

// TestRunWorkflowHandler_Loop_RunsContractsContinuously proves Handle with
// Loop=true does NOT exit after one fulfillment — it re-negotiates and runs the
// next contract, repeatedly, until stopped. This is the sp-ehg9 gap: today's
// single-shot batch-contract fulfills exactly one and exits.
func TestRunWorkflowHandler_Loop_RunsContractsContinuously(t *testing.T) {
	ctx, cancel := context.WithCancel(auth.WithPlayerToken(context.Background(), "test-token"))

	contractRepo := newWorkflowStubContractRepo()
	mediator := &loopWiringMediator{
		contractRepo: contractRepo,
		queue:        buildPreFulfilledContracts(t, 8),
		cancel:       cancel,
		stopAfter:    3,
	}

	// MockClock so the inter-contract pacing does not wall-wait.
	handler := NewRunWorkflowHandler(mediator, nil, contractRepo, &shared.MockClock{})

	cmd := &RunWorkflowCommand{ShipSymbol: "TORWIND-1", PlayerID: shared.MustNewPlayerID(1), Loop: true}

	if _, err := handler.Handle(ctx, cmd); err != nil && err != context.Canceled {
		t.Fatalf("expected loop to end cleanly on stop, got: %v", err)
	}

	if len(mediator.fulfilledIDs) < 3 {
		t.Fatalf("expected the loop to fulfill at least 3 contracts continuously, got %d: %v",
			len(mediator.fulfilledIDs), mediator.fulfilledIDs)
	}
}

// TestRunWorkflowHandler_SingleShot_RunsExactlyOneContract pins the BYTE-IDENTICAL
// default: with Loop=false, Handle fulfills exactly ONE contract and returns —
// even though more are available — exactly as batch-contract behaves today
// (sp-6fsq). The trailing best-effort next-contract negotiation is not a second
// fulfillment.
func TestRunWorkflowHandler_SingleShot_RunsExactlyOneContract(t *testing.T) {
	ctx := auth.WithPlayerToken(context.Background(), "test-token")

	contractRepo := newWorkflowStubContractRepo()
	mediator := &loopWiringMediator{
		contractRepo: contractRepo,
		queue:        buildPreFulfilledContracts(t, 4),
		cancel:       func() {},
		stopAfter:    1 << 30, // never triggers; loop=false never re-enters anyway
	}

	handler := NewRunWorkflowHandler(mediator, nil, contractRepo, &shared.MockClock{})

	cmd := &RunWorkflowCommand{ShipSymbol: "TORWIND-1", PlayerID: shared.MustNewPlayerID(1)} // Loop defaults false

	resp, err := handler.Handle(ctx, cmd)
	if err != nil {
		t.Fatalf("single-shot workflow should succeed, got: %v", err)
	}
	result := resp.(*RunWorkflowResponse)
	if !result.Fulfilled {
		t.Fatalf("expected the single contract to be fulfilled, got %+v", result)
	}
	if len(mediator.fulfilledIDs) != 1 {
		t.Fatalf("single-shot must fulfill exactly one contract, got %d: %v",
			len(mediator.fulfilledIDs), mediator.fulfilledIDs)
	}
}
