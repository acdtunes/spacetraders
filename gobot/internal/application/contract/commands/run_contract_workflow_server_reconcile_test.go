package commands

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	playerQueries "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// unreadableHullRepo is TORWIND-5 on 2026-08-05: a hull the upstream API could not return
// (`{"code":3000,"message":"The server did not return a valid response."}`) for 24h straight,
// while every other hull read fine. Every read fails and every read is counted, so a test can
// assert not merely that the workflow survived the hull but that it never touched it.
type unreadableHullRepo struct {
	navigation.ShipRepository
	reads int
}

func (r *unreadableHullRepo) errShipUnreadable() error {
	return fmt.Errorf("failed to get ship: API error (status 500): {\"error\":{\"code\":3000,\"message\":\"The server did not return a valid response.\"}}")
}

func (r *unreadableHullRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	r.reads++
	return nil, r.errShipUnreadable()
}

func (r *unreadableHullRepo) SyncShipFromAPI(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	r.reads++
	return nil, r.errShipUnreadable()
}

// stubServerContracts is the game server's view of a contract, the authority the local row is
// only ever a cache of. err drives the fail-open path.
type stubServerContracts struct {
	data  *domainPorts.ContractData
	err   error
	calls int
}

func (s *stubServerContracts) GetContract(_ context.Context, _, _ string) (*domainPorts.ContractData, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.data, nil
}

// reconcileMediator records the command types the workflow sends, so a test can assert on the
// fulfil AND on the absence of any deliver/purchase — the whole point of the guard is that a
// contract already delivered in full is never delivered into again.
type reconcileMediator struct {
	common.Mediator

	contractRepo *workflowStubContractRepo
	sent         []string
	deliverCalls int
	fulfillCalls int
}

func (m *reconcileMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *contractQueries.EvaluateContractProfitabilityQuery:
		m.sent = append(m.sent, "profitability")
		return &contractQueries.ProfitabilityResult{IsProfitable: true, MarketPrices: map[string]int{}}, nil

	case *AcceptContractCommand:
		m.sent = append(m.sent, "accept")
		c, err := m.contractRepo.FindByID(ctx, cmd.ContractID)
		if err != nil {
			return nil, err
		}
		return &AcceptContractResponse{Contract: c}, nil

	case *DeliverContractCommand:
		m.sent = append(m.sent, "deliver")
		m.deliverCalls++
		return nil, fmt.Errorf("a contract already delivered in full must never be delivered into again")

	case *FulfillContractCommand:
		m.sent = append(m.sent, "fulfill")
		m.fulfillCalls++
		c, err := m.contractRepo.FindByID(ctx, cmd.ContractID)
		if err != nil {
			return nil, err
		}
		// Drive the real domain guard: a fulfil on a contract the local row still believes is
		// partial fails here with "deliveries not complete", exactly as production does.
		if err := c.Fulfill(); err != nil {
			return nil, fmt.Errorf("failed to fulfill contract: %w", err)
		}
		return &FulfillContractResponse{Contract: c}, nil

	case *NegotiateContractCommand:
		m.sent = append(m.sent, "negotiate")
		return nil, fmt.Errorf("no contract available in test")

	case *playerQueries.GetPlayerQuery:
		return &playerQueries.GetPlayerResponse{Player: &player.Player{Credits: 1_000_000}}, nil

	default:
		return nil, fmt.Errorf("unexpected mediator command in test: %T", request)
	}
}

type reconcileLogger struct {
	actions []string
}

func (l *reconcileLogger) Log(_, _ string, fields map[string]interface{}) {
	if action, ok := fields["action"].(string); ok {
		l.actions = append(l.actions, action+":"+stringField(fields, "trade_symbol"))
	}
}

func (l *reconcileLogger) logged(entry string) bool {
	for _, a := range l.actions {
		if a == entry {
			return true
		}
	}
	return false
}

func stringField(fields map[string]interface{}, key string) string {
	v, _ := fields[key].(string)
	return v
}

// serverContractData builds the server's answer for a single-good contract.
func serverContractData(id, good string, required, fulfilledUnits int, fulfilled bool) *domainPorts.ContractData {
	return &domainPorts.ContractData{
		ID:            id,
		FactionSymbol: "COSMIC",
		Type:          "PROCUREMENT",
		Accepted:      true,
		Fulfilled:     fulfilled,
		Terms: domainPorts.ContractTermsData{
			DeadlineToAccept: "2026-01-01T00:00:00Z",
			Deadline:         "2027-01-01T00:00:00Z",
			Payment:          domainPorts.PaymentData{OnAccepted: 5000, OnFulfilled: 20000},
			Deliveries: []domainPorts.DeliveryData{{
				TradeSymbol:       good,
				DestinationSymbol: "X1-BG40-D40",
				UnitsRequired:     required,
				UnitsFulfilled:    fulfilledUnits,
			}},
		},
	}
}

// staleLocalContract is the row as the daemon held it: accepted, and behind the server.
func staleLocalContract(t *testing.T, id, good string, required, localFulfilled int) *contract.Contract {
	t.Helper()
	c, err := contract.NewContract(id, shared.MustNewPlayerID(1), "COSMIC", "PROCUREMENT", contract.Terms{
		Payment: contract.Payment{OnAccepted: 5000, OnFulfilled: 20000},
		Deliveries: []contract.Delivery{{
			TradeSymbol:       good,
			DestinationSymbol: "X1-BG40-D40",
			UnitsRequired:     required,
			UnitsFulfilled:    localFulfilled,
		}},
		DeadlineToAccept: "2026-01-01T00:00:00Z",
		Deadline:         "2027-01-01T00:00:00Z",
	}, nil)
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	if err := c.Accept(); err != nil {
		t.Fatalf("seed Accept: %v", err)
	}
	return c
}

type reconcileHarness struct {
	mediator *reconcileMediator
	server   *stubServerContracts
	shipRepo *unreadableHullRepo
	repo     *workflowStubContractRepo
	handler  *RunWorkflowHandler
	logger   *reconcileLogger
}

func newReconcileHarness(t *testing.T, seed *contract.Contract, server *stubServerContracts) *reconcileHarness {
	t.Helper()
	repo := newWorkflowStubContractRepo(seed)
	med := &reconcileMediator{contractRepo: repo}
	shipRepo := &unreadableHullRepo{}
	return &reconcileHarness{
		mediator: med,
		server:   server,
		shipRepo: shipRepo,
		repo:     repo,
		handler:  NewRunWorkflowHandler(med, shipRepo, repo, server, nil),
		logger:   &reconcileLogger{},
	}
}

func (h *reconcileHarness) run(t *testing.T) *RunWorkflowResponse {
	t.Helper()
	ctx := common.WithLogger(auth.WithPlayerToken(context.Background(), "test-token"), h.logger)
	resp, err := h.handler.Handle(ctx, &RunWorkflowCommand{
		ShipSymbol: "TORWIND-5",
		PlayerID:   shared.MustNewPlayerID(1),
	})
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	result, ok := resp.(*RunWorkflowResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	return result
}

// THE PROD WEDGE (sp-20eyn acceptance 4). Contract cmsfd1yqu3uavsl73370nq0ph read
// unitsFulfilled 94 against unitsRequired 47 on the server and `fulfilled: false`, while the
// daemon's own row read 0/47. Every worker resumed it, walked into the delivery leg, tried to
// reload TORWIND-5 — a hull the API could not return — died, and respawned onto the same
// contract and the same hull. 34,279 times. ~24h of zero income, with nothing left to deliver
// the whole time.
//
// The contract must be fulfilled off contract state alone, and the unreadable hull must never
// be read: a hull that cannot be reloaded must not be able to block collection of a contract
// that is already paid for in goods.
func TestRunWorkflow_ServerOverDelivered_FulfillsWithoutDeliveringOrReadingTheHull(t *testing.T) {
	seed := staleLocalContract(t, "cmsfd1yqu3uavsl73370nq0ph", "ALUMINUM", 47, 0)
	server := &stubServerContracts{data: serverContractData("cmsfd1yqu3uavsl73370nq0ph", "ALUMINUM", 47, 94, false)}
	h := newReconcileHarness(t, seed, server)

	result := h.run(t)

	if !result.Fulfilled {
		t.Fatalf("expected the already-delivered contract to be fulfilled, got %+v", result)
	}
	if h.mediator.fulfillCalls != 1 {
		t.Fatalf("fulfill calls = %d, want exactly 1", h.mediator.fulfillCalls)
	}
	if h.mediator.deliverCalls != 0 {
		t.Fatalf("deliver calls = %d, want 0 — the contract is already over-delivered at 94/47", h.mediator.deliverCalls)
	}
	if h.shipRepo.reads != 0 {
		t.Fatalf("ship reads = %d, want 0 — an unreadable hull must not gate a contract with nothing left to deliver", h.shipRepo.reads)
	}
	if h.server.calls != 1 {
		t.Fatalf("server contract reads = %d, want exactly 1", h.server.calls)
	}
}

// Acceptance 3, the recurrence guard: exactly at the required count is still "nothing left to
// deliver". This is the state the prod contract passed THROUGH on its way to 94/47, and the
// state a crash one instant later would resume from.
func TestRunWorkflow_ServerExactlyAtRequired_FulfillsWithoutDelivering(t *testing.T) {
	seed := staleLocalContract(t, "contract-exact", "ALUMINUM", 47, 0)
	server := &stubServerContracts{data: serverContractData("contract-exact", "ALUMINUM", 47, 47, false)}
	h := newReconcileHarness(t, seed, server)

	result := h.run(t)

	if !result.Fulfilled || h.mediator.fulfillCalls != 1 {
		t.Fatalf("expected exactly one fulfil, got fulfilled=%v calls=%d", result.Fulfilled, h.mediator.fulfillCalls)
	}
	if h.mediator.deliverCalls != 0 {
		t.Fatalf("deliver calls = %d, want 0", h.mediator.deliverCalls)
	}
	if h.shipRepo.reads != 0 {
		t.Fatalf("ship reads = %d, want 0", h.shipRepo.reads)
	}
}

// The healed row must be PERSISTED, not merely correct in memory for this pass. Otherwise the
// next worker re-reads the same stale 0/47 and re-derives the same wrong plan — the loop.
func TestRunWorkflow_ServerOverDelivered_PersistsTheReconciledRow(t *testing.T) {
	seed := staleLocalContract(t, "contract-persist", "ALUMINUM", 47, 0)
	server := &stubServerContracts{data: serverContractData("contract-persist", "ALUMINUM", 47, 94, false)}
	h := newReconcileHarness(t, seed, server)

	h.run(t)

	stored, err := h.repo.FindByID(context.Background(), "contract-persist")
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got := stored.Terms().Deliveries[0].UnitsFulfilled; got != 94 {
		t.Fatalf("persisted delivered = %d, want the server's 94 — a stale row re-wedges the next worker", got)
	}
}

// A contract the server has already fulfilled AND paid must stop reading as active work. The
// local row is the only thing still advertising it, and FindActiveContracts (accepted AND NOT
// fulfilled) would otherwise hand a finished contract to every worker that asks, forever.
func TestRunWorkflow_ServerAlreadyFulfilled_ReleasesTheRowWithoutFulfillingAgain(t *testing.T) {
	seed := staleLocalContract(t, "contract-done", "ALUMINUM", 47, 0)
	server := &stubServerContracts{data: serverContractData("contract-done", "ALUMINUM", 47, 47, true)}
	h := newReconcileHarness(t, seed, server)

	result := h.run(t)

	if !result.Fulfilled {
		t.Fatalf("expected the workflow to report the contract settled, got %+v", result)
	}
	if h.mediator.fulfillCalls != 0 {
		t.Fatalf("fulfill calls = %d, want 0 — the server already fulfilled and paid this contract", h.mediator.fulfillCalls)
	}
	if h.mediator.deliverCalls != 0 {
		t.Fatalf("deliver calls = %d, want 0", h.mediator.deliverCalls)
	}

	active, err := h.repo.FindActiveContracts(context.Background(), 1)
	if err != nil {
		t.Fatalf("FindActiveContracts: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active contracts = %d, want 0 — a server-fulfilled contract must stop being handed out", len(active))
	}
}

// FAIL-OPEN, and pinned so it stays deliberate. When the contracts endpoint cannot be read the
// workflow proceeds on the local counts, exactly as it does today — this is a NEW guard, not a
// weakened one (RULINGS #4). Failing closed would park every contract in the fleet on any
// transient contracts-endpoint hiccup, a strictly larger outage than the one this fixes.
func TestRunWorkflow_ServerContractUnreadable_ProceedsOnLocalStateInsteadOfParking(t *testing.T) {
	seed := staleLocalContract(t, "contract-blind", "ALUMINUM", 47, 47) // locally complete
	server := &stubServerContracts{err: fmt.Errorf("API error (status 500): server did not return a valid response")}
	h := newReconcileHarness(t, seed, server)

	result := h.run(t)

	if h.server.calls != 1 {
		t.Fatalf("server contract reads = %d, want exactly 1 attempt", h.server.calls)
	}
	if !result.Fulfilled || h.mediator.fulfillCalls != 1 {
		t.Fatalf("a blind reconcile must still let the locally-complete contract fulfil, got fulfilled=%v calls=%d", result.Fulfilled, h.mediator.fulfillCalls)
	}
}

// The guard must not fire on a contract with real work left. A server count BELOW required
// leaves the normal delivery path in charge — which, with an unreadable hull, is still an
// error, and that error must surface rather than be swallowed into a false fulfil.
func TestRunWorkflow_ServerBelowRequired_DoesNotFulfillAndStillRunsTheDeliveryLeg(t *testing.T) {
	seed := staleLocalContract(t, "contract-partial", "ALUMINUM", 47, 0)
	server := &stubServerContracts{data: serverContractData("contract-partial", "ALUMINUM", 47, 20, false)}
	h := newReconcileHarness(t, seed, server)

	ctx := common.WithLogger(auth.WithPlayerToken(context.Background(), "test-token"), h.logger)
	_, err := h.handler.Handle(ctx, &RunWorkflowCommand{ShipSymbol: "TORWIND-5", PlayerID: shared.MustNewPlayerID(1)})

	if err == nil {
		t.Fatal("expected the unreadable hull to fail the delivery leg; a partial contract must not be silently fulfilled")
	}
	if h.mediator.fulfillCalls != 0 {
		t.Fatalf("fulfill calls = %d, want 0 — 20/47 is not complete", h.mediator.fulfillCalls)
	}
	if h.shipRepo.reads == 0 {
		t.Fatal("expected the delivery leg to reach the hull for a contract with 27 units still owed")
	}

	// The raise still happened: the leg must plan against 20 delivered, not 0.
	stored, err := h.repo.FindByID(context.Background(), "contract-partial")
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got := stored.Terms().Deliveries[0].UnitsFulfilled; got != 20 {
		t.Fatalf("persisted delivered = %d, want the server's 20", got)
	}
}

// multiGoodServerData is the server's answer for a contract with one good already
// OVER-delivered and another still owed — the only shape in which the delivery leg ever sees an
// over-delivered good, since a contract where EVERY good is met is settled before it gets there.
func multiGoodServerData(id string) *domainPorts.ContractData {
	return &domainPorts.ContractData{
		ID: id, FactionSymbol: "COSMIC", Type: "PROCUREMENT", Accepted: true, Fulfilled: false,
		Terms: domainPorts.ContractTermsData{
			DeadlineToAccept: "2026-01-01T00:00:00Z",
			Deadline:         "2027-01-01T00:00:00Z",
			Payment:          domainPorts.PaymentData{OnAccepted: 5000, OnFulfilled: 20000},
			Deliveries: []domainPorts.DeliveryData{
				{TradeSymbol: "ALUMINUM", DestinationSymbol: "X1-BG40-D40", UnitsRequired: 47, UnitsFulfilled: 94},
				{TradeSymbol: "IRON_ORE", DestinationSymbol: "X1-BG40-D40", UnitsRequired: 30, UnitsFulfilled: 0},
			},
		},
	}
}

// ProcessAllDeliveries skipped a met delivery on `unitsRemaining == 0`, and the 2026-08-05
// contract read 94 against a required 47 — which is not 0. An over-delivered good therefore fell
// straight through the skip into the delivery leg, the one place the fatal ship read lives.
// The skip is now `<= 0`.
//
// The good still owed must be unaffected: this is a skip that may only ever ADD skips
// (RULINGS #1), never turn a good with real work left into a skipped one.
func TestRunWorkflow_OverDeliveredGoodIsSkipped_WhileTheOwedGoodStillRuns(t *testing.T) {
	server := &stubServerContracts{data: multiGoodServerData("contract-multi")}
	h := newReconcileHarness(t, twoGoodLocalContract(t, "contract-multi"), server)

	ctx := common.WithLogger(auth.WithPlayerToken(context.Background(), "test-token"), h.logger)
	_, _ = h.handler.Handle(ctx, &RunWorkflowCommand{ShipSymbol: "TORWIND-5", PlayerID: shared.MustNewPlayerID(1)})

	if !h.logger.logged("skip_delivery:ALUMINUM") {
		t.Fatalf("ALUMINUM (94 delivered against 47 required) must be skipped as already fulfilled; logged actions: %v", h.logger.actions)
	}
	if h.logger.logged("process_delivery:ALUMINUM") {
		t.Fatalf("an over-delivered good must never enter the delivery leg; logged actions: %v", h.logger.actions)
	}
	if !h.logger.logged("process_delivery:IRON_ORE") {
		t.Fatalf("the good with 30 units still owed must still be processed; logged actions: %v", h.logger.actions)
	}
}

// twoGoodLocalContract is the local row: both goods at zero, i.e. behind the server on
// ALUMINUM and correct on IRON_ORE.
func twoGoodLocalContract(t *testing.T, id string) *contract.Contract {
	t.Helper()
	c, err := contract.NewContract(id, shared.MustNewPlayerID(1), "COSMIC", "PROCUREMENT", contract.Terms{
		Payment: contract.Payment{OnAccepted: 5000, OnFulfilled: 20000},
		Deliveries: []contract.Delivery{
			{TradeSymbol: "ALUMINUM", DestinationSymbol: "X1-BG40-D40", UnitsRequired: 47, UnitsFulfilled: 0},
			{TradeSymbol: "IRON_ORE", DestinationSymbol: "X1-BG40-D40", UnitsRequired: 30, UnitsFulfilled: 0},
		},
		DeadlineToAccept: "2026-01-01T00:00:00Z",
		Deadline:         "2027-01-01T00:00:00Z",
	}, nil)
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	if err := c.Accept(); err != nil {
		t.Fatalf("seed Accept: %v", err)
	}
	return c
}
