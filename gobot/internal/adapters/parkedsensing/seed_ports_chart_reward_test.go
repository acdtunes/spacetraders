package parkedsensing_test

// The charting reward is real income the API pays per waypoint. Charting through the seed
// port must therefore leave exactly one CHART row behind, carrying the in-band credits so
// the ledger chain re-anchors instead of gaining an unexplained gap.

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// spyMediator records every RecordTransactionCommand the port dispatches.
type spyMediator struct {
	recorded []*ledgerCommands.RecordTransactionCommand
}

func (s *spyMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	if cmd, ok := request.(*ledgerCommands.RecordTransactionCommand); ok {
		s.recorded = append(s.recorded, cmd)
	}
	return nil, nil
}

func (s *spyMediator) Register(_ reflect.Type, _ common.RequestHandler) error { return nil }

func (s *spyMediator) RegisterMiddleware(_ common.Middleware) {}

func chartingSeedPort(med common.Mediator, api *fakeChartAPI) *adapterSensing.SeedCommandPort {
	return adapterSensing.NewSeedCommandPort(med, api, &stubPlayerRepo{}, &fakeWaypointCache{}, &fakeSeedScanner{}, nil)
}

func TestSeedCommandPort_Chart_RecordsTheRewardOnce(t *testing.T) {
	credits := 1010000
	med := &spyMediator{}
	port := chartingSeedPort(med, &fakeChartAPI{
		chartResult: &ports.ChartResult{WaypointSymbol: "X1-BB-A1", Reward: 10000, AgentCredits: &credits},
	})

	require.NoError(t, port.Chart(context.Background(), 1, "PROBE-7"))

	require.Len(t, med.recorded, 1, "charting must record exactly one ledger transaction")
	got := med.recorded[0]
	require.Equal(t, string(ledger.TransactionTypeChart), got.TransactionType)
	require.Equal(t, 10000, got.Amount, "a charting reward is income, recorded positive")
	require.NotNil(t, got.AuthoritativeBalance, "in-band credits must re-anchor the chain")
	require.Equal(t, 1010000, *got.AuthoritativeBalance)
	require.Equal(t, 1, got.PlayerID)
}

// ledger.Validate rejects amount == 0, and a zero reward moved no credits: there is nothing
// to record and nothing to re-anchor.
func TestSeedCommandPort_Chart_RecordsNothingForAZeroReward(t *testing.T) {
	med := &spyMediator{}
	port := chartingSeedPort(med, &fakeChartAPI{chartResult: &ports.ChartResult{WaypointSymbol: "X1-BB-A1"}})

	require.NoError(t, port.Chart(context.Background(), 1, "PROBE-7"))

	require.Empty(t, med.recorded, "a zero reward must not produce a ledger row")
}

// An already-charted waypoint (4230) pays nothing and returns no body. The verdict stays a
// success for the tour, but there is no reward to record.
func TestSeedCommandPort_Chart_RecordsNothingWhenAlreadyCharted(t *testing.T) {
	med := &spyMediator{}
	port := chartingSeedPort(med, &fakeChartAPI{chartErr: errors.New("failed to chart waypoint: API error 400 (code 4230)")})

	require.NoError(t, port.Chart(context.Background(), 1, "PROBE-7"))

	require.Empty(t, med.recorded, "an already-charted waypoint pays no reward")
}
