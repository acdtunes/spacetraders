package commands

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests pin the sp-sc6u fix for the contract paths: acceptance/fulfilment
// payments must be recorded from the credits the accept/fulfill response returns
// in-band, NOT from a separately fetched GetAgent snapshot. The old code fetched
// credits before the API call (a value that could already be stale) and skipped
// recording entirely when that fetch failed (dropping real income from the
// ledger and forking balance_after from the live API).

type fakeContractRepo struct {
	contract.ContractRepository
	c *contract.Contract
}

func (r *fakeContractRepo) FindByID(ctx context.Context, contractID string) (*contract.Contract, error) {
	return r.c, nil
}
func (r *fakeContractRepo) Add(ctx context.Context, c *contract.Contract) error { return nil }

type fakeContractPlayerRepo struct {
	player.PlayerRepository
	p *player.Player
}

func (r *fakeContractPlayerRepo) FindByID(ctx context.Context, playerID shared.PlayerID) (*player.Player, error) {
	return r.p, nil
}

// fakeContractAPIClient returns the contract responses with in-band agent
// credits. GetAgent errors so any code path that still depends on a pre-fetched
// balance snapshot is exposed as a dropped/incorrect ledger entry.
type fakeContractAPIClient struct {
	domainPorts.APIClient
	acceptResult   *domainPorts.ContractData
	fulfillResult  *domainPorts.ContractData
	getAgentCalled bool
}

func (c *fakeContractAPIClient) AcceptContract(ctx context.Context, contractID, token string) (*domainPorts.ContractData, error) {
	return c.acceptResult, nil
}
func (c *fakeContractAPIClient) FulfillContract(ctx context.Context, contractID, token string) (*domainPorts.ContractData, error) {
	return c.fulfillResult, nil
}
func (c *fakeContractAPIClient) GetAgent(ctx context.Context, token string) (*player.AgentData, error) {
	c.getAgentCalled = true
	return nil, errors.New("GetAgent must not be needed for ledger recording")
}

type contractRecordingMediator struct {
	mu       sync.Mutex
	recorded []*ledgerCommands.RecordTransactionCommand
}

func (m *contractRecordingMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	if cmd, ok := request.(*ledgerCommands.RecordTransactionCommand); ok {
		m.mu.Lock()
		m.recorded = append(m.recorded, cmd)
		m.mu.Unlock()
	}
	return nil, nil
}
func (m *contractRecordingMediator) Register(requestType reflect.Type, handler common.RequestHandler) error {
	return nil
}
func (m *contractRecordingMediator) RegisterMiddleware(middleware common.Middleware) {}
func (m *contractRecordingMediator) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.recorded)
}
func (m *contractRecordingMediator) first() *ledgerCommands.RecordTransactionCommand {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recorded[0]
}

func contractTestTerms(unitsFulfilled int) contract.Terms {
	return contract.Terms{
		Payment:    contract.Payment{OnAccepted: 50000, OnFulfilled: 150000},
		Deliveries: []contract.Delivery{{TradeSymbol: "IRON_ORE", DestinationSymbol: "X1-A", UnitsRequired: 10, UnitsFulfilled: unitsFulfilled}},
		Deadline:   "2999-01-01T00:00:00Z",
	}
}

func TestAcceptContractRecordsInBandCreditsWithoutGetAgentSnapshot(t *testing.T) {
	pid := shared.MustNewPlayerID(1)
	c, err := contract.NewContract("C-1", pid, "COSMIC", "PROCUREMENT", contractTestTerms(0), nil)
	require.NoError(t, err)

	credits := 250000
	api := &fakeContractAPIClient{acceptResult: &domainPorts.ContractData{ID: "C-1", AgentCredits: &credits}}
	med := &contractRecordingMediator{}
	h := NewAcceptContractHandler(&fakeContractRepo{c: c}, &fakeContractPlayerRepo{p: player.NewPlayer(pid, "AGENT", "tok")}, api, med)

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	_, err = h.Handle(ctx, &AcceptContractCommand{ContractID: "C-1", PlayerID: pid})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return med.count() == 1 }, time.Second, 5*time.Millisecond,
		"acceptance payment must be recorded from in-band credits, not gated on a GetAgent prefetch")
	tx := med.first()
	require.Equal(t, "CONTRACT_ACCEPTED", tx.TransactionType)
	require.Equal(t, 50000, tx.Amount)
	require.NotNil(t, tx.AuthoritativeBalance, "must carry the in-band agent.credits")
	require.Equal(t, 250000, *tx.AuthoritativeBalance)
	require.False(t, api.getAgentCalled, "must not fetch a separate (stale) GetAgent snapshot")
}

func TestFulfillContractRecordsInBandCreditsWithoutGetAgentSnapshot(t *testing.T) {
	pid := shared.MustNewPlayerID(1)
	c, err := contract.NewContract("C-1", pid, "COSMIC", "PROCUREMENT", contractTestTerms(10), nil)
	require.NoError(t, err)
	require.NoError(t, c.Accept())

	credits := 400000
	api := &fakeContractAPIClient{fulfillResult: &domainPorts.ContractData{ID: "C-1", AgentCredits: &credits}}
	med := &contractRecordingMediator{}
	h := NewFulfillContractHandler(&fakeContractRepo{c: c}, &fakeContractPlayerRepo{p: player.NewPlayer(pid, "AGENT", "tok")}, api, med)

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	_, err = h.Handle(ctx, &FulfillContractCommand{ContractID: "C-1", PlayerID: pid})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return med.count() == 1 }, time.Second, 5*time.Millisecond,
		"fulfilment payment must be recorded from in-band credits")
	tx := med.first()
	require.Equal(t, "CONTRACT_FULFILLED", tx.TransactionType)
	require.Equal(t, 150000, tx.Amount)
	require.NotNil(t, tx.AuthoritativeBalance, "must carry the in-band agent.credits")
	require.Equal(t, 400000, *tx.AuthoritativeBalance)
	require.False(t, api.getAgentCalled, "must not fetch a separate (stale) GetAgent snapshot")
}
