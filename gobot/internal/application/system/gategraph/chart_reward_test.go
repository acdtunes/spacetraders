package gategraph

// The gate graph charts a frontier gate from the hull standing on it, and that chart is PAID.
// The reward must reach the ledger from here as well as from the charting seed, or the same
// unrecorded inflow returns through the other door.

import (
	"context"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// chartSpyMediator records every RecordTransactionCommand the service dispatches.
type chartSpyMediator struct {
	recorded []*ledgerCommands.RecordTransactionCommand
}

func (m *chartSpyMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	if cmd, ok := request.(*ledgerCommands.RecordTransactionCommand); ok {
		m.recorded = append(m.recorded, cmd)
	}
	return nil, nil
}

func (m *chartSpyMediator) Register(_ reflect.Type, _ common.RequestHandler) error { return nil }

func (m *chartSpyMediator) RegisterMiddleware(_ common.Middleware) {}

func TestService_ChartPresentGate_RecordsTheChartingReward(t *testing.T) {
	credits := 1010000
	med := &chartSpyMediator{}
	api := &perSystemGateAPI{
		connectionsBySystem: map[string][]string{"X1-DA78": {"X1-GQ22-GATE"}},
		chartResult:         &ports.ChartResult{WaypointSymbol: "X1-DA78-C24B", Reward: 10000, AgentCredits: &credits},
	}
	svc := NewService(&perSystemMissStore{}, api, nil, &stubPlayerRepo{token: "tok"}, WithLedgerMediator(med))

	if _, err := svc.ChartPresentGate(context.Background(), "X1-DA78", "TORWIND-16", 1); err != nil {
		t.Fatalf("a present-ship read on an uncharted gate must succeed, got %v", err)
	}

	if len(med.recorded) != 1 {
		t.Fatalf("charting a frontier gate must record exactly one ledger transaction, got %d", len(med.recorded))
	}
	got := med.recorded[0]
	if got.TransactionType != string(ledger.TransactionTypeChart) {
		t.Fatalf("transaction type = %q, want CHART", got.TransactionType)
	}
	if got.Amount != 10000 {
		t.Fatalf("amount = %d, want the +10000 reward", got.Amount)
	}
	if got.AuthoritativeBalance == nil || *got.AuthoritativeBalance != 1010000 {
		t.Fatalf("in-band credits must re-anchor the chain, got %v", got.AuthoritativeBalance)
	}
}

// A gate another agent already charted pays nothing, and the benign 4230 is swallowed. Recording
// a phantom row there would put credits in the ledger that never entered the balance.
func TestService_ChartPresentGate_AlreadyCharted_RecordsNothing(t *testing.T) {
	med := &chartSpyMediator{}
	api := &perSystemGateAPI{
		connectionsBySystem: map[string][]string{"X1-DA78": {"X1-GQ22-GATE"}},
		chartErr:            &alreadyChartedError{},
	}
	svc := NewService(&perSystemMissStore{}, api, nil, &stubPlayerRepo{token: "tok"}, WithLedgerMediator(med))

	if _, err := svc.ChartPresentGate(context.Background(), "X1-DA78", "TORWIND-16", 1); err != nil {
		t.Fatalf("an already-charted gate must not fail the read, got %v", err)
	}

	if len(med.recorded) != 0 {
		t.Fatalf("an already-charted gate pays no reward, got %d ledger rows", len(med.recorded))
	}
}

type alreadyChartedError struct{}

func (e *alreadyChartedError) Error() string {
	return "failed to chart waypoint: API error 400 (code 4230): waypoint already charted"
}
