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

// workflowStubContractRepo is an in-memory ContractRepository stub keyed by
// contract ID. FindActiveContracts mirrors GormContractRepository's exact
// filter (player_id match AND accepted AND NOT fulfilled) so the test proves
// the just-fulfilled contract is excluded and a fresh negotiate is required.
type workflowStubContractRepo struct {
	contracts map[string]*contract.Contract
}

func newWorkflowStubContractRepo(seed ...*contract.Contract) *workflowStubContractRepo {
	r := &workflowStubContractRepo{contracts: make(map[string]*contract.Contract)}
	for _, c := range seed {
		r.contracts[c.ContractID()] = c
	}
	return r
}

func (r *workflowStubContractRepo) FindByID(_ context.Context, contractID string) (*contract.Contract, error) {
	c, ok := r.contracts[contractID]
	if !ok {
		return nil, fmt.Errorf("contract not found: %s", contractID)
	}
	return c, nil
}

func (r *workflowStubContractRepo) FindActiveContracts(_ context.Context, playerID int) ([]*contract.Contract, error) {
	var active []*contract.Contract
	for _, c := range r.contracts {
		if c.PlayerID().Value() == playerID && c.Accepted() && !c.Fulfilled() {
			active = append(active, c)
		}
	}
	return active, nil
}

func (r *workflowStubContractRepo) Add(_ context.Context, c *contract.Contract) error {
	r.contracts[c.ContractID()] = c
	return nil
}

// workflowFakeMediator routes exactly the commands ContractLifecycleService
// sends for a workflow whose deliveries are already fully satisfied: no
// navigation, docking, or purchase command should ever reach it. Any other
// command type fails the test - that absence is how this test proves the
// next contract is claimed with zero deadhead travel back to base.
//
// It embeds common.Mediator so only Send needs a concrete implementation;
// Register/RegisterMiddleware are never exercised by Handle() and would
// nil-panic if the test ever called them by mistake.
type workflowFakeMediator struct {
	common.Mediator

	contractRepo *workflowStubContractRepo
	nextContract *contract.Contract

	negotiateCalls int
	acceptedIDs    []string
	fulfilledIDs   []string
}

func (m *workflowFakeMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *contractQueries.EvaluateContractProfitabilityQuery:
		// Profitability evaluation failure is explicitly non-fatal in
		// executeWorkflow, so an error here keeps the stub minimal while
		// still exercising that non-fatal path.
		return nil, fmt.Errorf("no market data available in test")

	case *NegotiateContractCommand:
		m.negotiateCalls++
		if err := m.contractRepo.Add(ctx, m.nextContract); err != nil {
			return nil, err
		}
		return &NegotiateContractResponse{Contract: m.nextContract, WasNegotiated: true}, nil

	case *AcceptContractCommand:
		c, err := m.contractRepo.FindByID(ctx, cmd.ContractID)
		if err != nil {
			return nil, err
		}
		if err := c.Accept(); err != nil {
			return nil, err
		}
		m.acceptedIDs = append(m.acceptedIDs, cmd.ContractID)
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
		return &FulfillContractResponse{Contract: c}, nil

	default:
		return nil, fmt.Errorf("unexpected mediator command in test: %T (a ship that just fulfilled should not navigate, dock, or purchase before claiming its next contract)", request)
	}
}

func mustNewWorkflowTestContract(t *testing.T, id string, deliveredUnits int) *contract.Contract {
	t.Helper()
	terms := contract.Terms{
		Payment: contract.Payment{OnAccepted: 5000, OnFulfilled: 20000},
		Deliveries: []contract.Delivery{
			{TradeSymbol: "ALUMINUM", DestinationSymbol: "X1-TEST-A1", UnitsRequired: 80, UnitsFulfilled: deliveredUnits},
		},
		DeadlineToAccept: "2026-01-01T00:00:00Z",
		Deadline:         "2026-01-01T00:00:00Z",
	}
	c, err := contract.NewContract(id, shared.MustNewPlayerID(1), "COSMIC", "PROCUREMENT", terms, nil)
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	return c
}

// mustNewWorkflowTestContractWithPayment is mustNewWorkflowTestContract with
// a caller-supplied payment, for value-floor tests (sp-snmb) that need to
// control TotalPayout precisely instead of the fixed 25,000cr the other
// helper hardcodes.
func mustNewWorkflowTestContractWithPayment(t *testing.T, id string, onAccepted, onFulfilled int) *contract.Contract {
	t.Helper()
	terms := contract.Terms{
		Payment: contract.Payment{OnAccepted: onAccepted, OnFulfilled: onFulfilled},
		Deliveries: []contract.Delivery{
			{TradeSymbol: "ALUMINUM", DestinationSymbol: "X1-TEST-A1", UnitsRequired: 80, UnitsFulfilled: 0},
		},
		DeadlineToAccept: "2026-01-01T00:00:00Z",
		Deadline:         "2026-01-01T00:00:00Z",
	}
	c, err := contract.NewContract(id, shared.MustNewPlayerID(1), "COSMIC", "PROCUREMENT", terms, nil)
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	return c
}

// Reproduces the sp-qpmi between-contract gap. A ship that just fulfilled a
// contract is still DOCKED at the delivery waypoint (DeliverContractCargo
// always navigates-and-docks there before delivering), and neither negotiate
// nor accept require any particular ship location - only DOCKED state, which
// already holds. Before this fix, executeWorkflow returned immediately after
// FulfillContract, leaving the ship idle until the fleet coordinator's
// discover -> negotiate -> accept cycle eventually got around to it (measured
// fleet-wide at 74 ship-hours/day). This test drives RunWorkflowHandler.Handle
// end-to-end (all deliveries pre-fulfilled, so ProcessAllDeliveries is a
// no-op and the only commands that should reach the mediator are the contract
// lifecycle ones) and proves the ship claims its OWN next contract - with no
// navigation, docking, or purchase call - in the same invocation that
// fulfilled the previous one.
func TestRunWorkflowHandler_AfterFulfill_NegotiatesAndAcceptsNextContractWithoutReturningToBase(t *testing.T) {
	currentContract := mustNewWorkflowTestContract(t, "contract-current", 80) // fully delivered, ready to fulfill
	if err := currentContract.Accept(); err != nil {
		t.Fatalf("seed Accept: %v", err)
	}
	nextContract := mustNewWorkflowTestContract(t, "contract-next", 0) // freshly negotiated, not yet accepted

	contractRepo := newWorkflowStubContractRepo(currentContract)
	mediator := &workflowFakeMediator{contractRepo: contractRepo, nextContract: nextContract}

	// valueFloor is set well below this test's 25,000cr (5,000+20,000)
	// contracts so the new sp-snmb gate never engages here - this test's
	// job is the qpmi negotiate-chaining behavior, unchanged.
	const valueFloor = 10000
	handler := NewRunWorkflowHandler(mediator, nil, contractRepo, nil, valueFloor)

	ctx := auth.WithPlayerToken(context.Background(), "test-token")
	cmd := &RunWorkflowCommand{
		ShipSymbol: "TORWIND-3",
		PlayerID:   shared.MustNewPlayerID(1),
	}

	resp, err := handler.Handle(ctx, cmd)
	if err != nil {
		t.Fatalf("expected workflow to succeed, got: %v", err)
	}

	result, ok := resp.(*RunWorkflowResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if !result.Fulfilled {
		t.Fatalf("expected current contract to be fulfilled, got %+v", result)
	}

	if len(mediator.fulfilledIDs) != 1 || mediator.fulfilledIDs[0] != "contract-current" {
		t.Fatalf("expected contract-current to be fulfilled exactly once, got %v", mediator.fulfilledIDs)
	}
	if mediator.negotiateCalls != 1 {
		t.Fatalf("expected exactly one negotiate call (for the NEXT contract; the current one resumed from the repo without negotiating), got %d", mediator.negotiateCalls)
	}
	if len(mediator.acceptedIDs) != 1 || mediator.acceptedIDs[0] != "contract-next" {
		t.Fatalf("expected the ship to accept its own next contract (contract-next) immediately, got %v", mediator.acceptedIDs)
	}

	saved, err := contractRepo.FindByID(ctx, "contract-next")
	if err != nil {
		t.Fatalf("expected next contract to be persisted: %v", err)
	}
	if !saved.Accepted() {
		t.Fatalf("expected next contract to be durably accepted so the coordinator's next pass can source it directly")
	}
}

// Reproduces the bead's literal cited failure mode (sp-snmb): a freshly
// negotiated contract paying only 2,500cr total (OnAccepted+OnFulfilled)
// burns the exact same ship-hours across negotiate/accept/deliver/fulfill
// as a 142,340cr one. Below the configured value floor, the workflow must
// leave the contract UNACCEPTED (never calling AcceptContractCommand) so it
// can never reach ProcessAllDeliveries or FulfillContract - Contract.Fulfill
// hard-requires Accepted()==true, so an unaccepted contract is structurally
// unfulfillable. The skip is reported cleanly via RejectedBelowFloor /
// RejectedPayout, not as an error: nothing failed, the workflow just
// declined to spend a cycle on this contract.
func TestRunWorkflowHandler_RejectsFreshlyNegotiatedContractBelowValueFloor(t *testing.T) {
	cheapContract := mustNewWorkflowTestContractWithPayment(t, "contract-cheap", 1000, 1500) // 2,500cr total

	contractRepo := newWorkflowStubContractRepo() // no active contract: forces a fresh negotiate
	mediator := &workflowFakeMediator{contractRepo: contractRepo, nextContract: cheapContract}

	const valueFloor = 10000
	handler := NewRunWorkflowHandler(mediator, nil, contractRepo, nil, valueFloor)

	ctx := auth.WithPlayerToken(context.Background(), "test-token")
	cmd := &RunWorkflowCommand{
		ShipSymbol: "TORWIND-9",
		PlayerID:   shared.MustNewPlayerID(1),
	}

	resp, err := handler.Handle(ctx, cmd)
	if err != nil {
		t.Fatalf("expected a below-floor contract to be a clean skip, not an error: %v", err)
	}

	result, ok := resp.(*RunWorkflowResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}

	if !result.Negotiated {
		t.Fatalf("expected the cheap contract to still be negotiated (only acceptance is gated), got %+v", result)
	}
	if result.Accepted {
		t.Fatalf("expected a below-floor contract to NOT be accepted, got %+v", result)
	}
	if result.Fulfilled {
		t.Fatalf("expected a below-floor contract to NOT be fulfilled, got %+v", result)
	}
	if !result.RejectedBelowFloor {
		t.Fatalf("expected RejectedBelowFloor=true, got %+v", result)
	}
	if result.RejectedPayout != 2500 {
		t.Fatalf("expected RejectedPayout=2500, got %d", result.RejectedPayout)
	}

	if len(mediator.acceptedIDs) != 0 {
		t.Fatalf("expected zero AcceptContractCommand calls for a below-floor contract, got %v", mediator.acceptedIDs)
	}
	if len(mediator.fulfilledIDs) != 0 {
		t.Fatalf("expected zero FulfillContractCommand calls for a below-floor contract, got %v", mediator.fulfilledIDs)
	}

	saved, err := contractRepo.FindByID(ctx, "contract-cheap")
	if err != nil {
		t.Fatalf("expected the negotiated contract to still be persisted: %v", err)
	}
	if saved.Accepted() {
		t.Fatalf("expected the persisted contract to remain unaccepted so it can never be fulfilled")
	}
}

// Applies the same value-floor gate to the qpmi best-effort next-contract
// path: a ship that just fulfilled a good contract must not accept a
// garbage one immediately afterward just because negotiateNextContract-
// BestEffort runs automatically post-fulfillment. This must remain a
// silent skip (logged only), exactly like an existing best-effort failure
// already is - it must never turn the overall (successful) workflow result
// into an error, since the CURRENT contract's fulfillment already succeeded.
func TestRunWorkflowHandler_BestEffortNextContract_SkipsAcceptWhenBelowValueFloor(t *testing.T) {
	currentContract := mustNewWorkflowTestContract(t, "contract-current", 80) // fully delivered, ready to fulfill
	if err := currentContract.Accept(); err != nil {
		t.Fatalf("seed Accept: %v", err)
	}
	cheapNextContract := mustNewWorkflowTestContractWithPayment(t, "contract-next-cheap", 1000, 1500) // 2,500cr total

	contractRepo := newWorkflowStubContractRepo(currentContract)
	mediator := &workflowFakeMediator{contractRepo: contractRepo, nextContract: cheapNextContract}

	const valueFloor = 10000
	handler := NewRunWorkflowHandler(mediator, nil, contractRepo, nil, valueFloor)

	ctx := auth.WithPlayerToken(context.Background(), "test-token")
	cmd := &RunWorkflowCommand{
		ShipSymbol: "TORWIND-3",
		PlayerID:   shared.MustNewPlayerID(1),
	}

	resp, err := handler.Handle(ctx, cmd)
	if err != nil {
		t.Fatalf("expected the current contract's fulfillment to succeed regardless of the next contract's payout: %v", err)
	}

	result, ok := resp.(*RunWorkflowResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if !result.Fulfilled {
		t.Fatalf("expected the current contract to be fulfilled, got %+v", result)
	}

	if len(mediator.fulfilledIDs) != 1 || mediator.fulfilledIDs[0] != "contract-current" {
		t.Fatalf("expected contract-current to be fulfilled exactly once, got %v", mediator.fulfilledIDs)
	}
	if mediator.negotiateCalls != 1 {
		t.Fatalf("expected exactly one negotiate call (for the cheap NEXT contract), got %d", mediator.negotiateCalls)
	}
	if len(mediator.acceptedIDs) != 0 {
		t.Fatalf("expected the below-floor next contract to NOT be accepted, got %v", mediator.acceptedIDs)
	}

	saved, err := contractRepo.FindByID(ctx, "contract-next-cheap")
	if err != nil {
		t.Fatalf("expected the negotiated next contract to still be persisted: %v", err)
	}
	if saved.Accepted() {
		t.Fatalf("expected the next contract to remain unaccepted so it can never be fulfilled")
	}
}
