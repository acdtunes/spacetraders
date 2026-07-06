package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

type fakeHistoryProvider struct {
	eras       []persistence.EraOverview
	summary    *persistence.SummaryReport
	summaryErr error
}

func (f *fakeHistoryProvider) ListEras(ctx context.Context) ([]persistence.EraOverview, error) {
	return f.eras, nil
}

func (f *fakeHistoryProvider) GoodsStats(ctx context.Context, good string, eraID *int) ([]persistence.GoodsEraStat, error) {
	return nil, nil
}

func (f *fakeHistoryProvider) ContractsStats(ctx context.Context, eraID *int, good *string) ([]persistence.ContractsEraStat, error) {
	return nil, nil
}

func (f *fakeHistoryProvider) PnL(ctx context.Context, eraID *int, byOperation bool) (*persistence.PnLReport, error) {
	return &persistence.PnLReport{}, nil
}

func (f *fakeHistoryProvider) ManufacturingStats(ctx context.Context, eraID *int, good *string) ([]persistence.ManufacturingGoodStat, error) {
	return nil, nil
}

func (f *fakeHistoryProvider) EventStats(ctx context.Context, eraID *int, eventType *string) (*persistence.EventReport, error) {
	return &persistence.EventReport{}, nil
}

func (f *fakeHistoryProvider) Summary(ctx context.Context, eraID *int) (*persistence.SummaryReport, error) {
	return f.summary, f.summaryErr
}

func TestRunHistoryErasRendersTableByDefault(t *testing.T) {
	f := &fakeHistoryProvider{eras: []persistence.EraOverview{
		{EraID: 1, Name: "torwind", AgentSymbol: "TORWIND", FinalCredits: 7_700_000},
	}}
	var out bytes.Buffer

	err := runHistoryEras(context.Background(), f, &out, false)

	require.NoError(t, err)
	require.Contains(t, out.String(), "torwind")
	require.Contains(t, out.String(), "7700000")
}

func TestRunHistoryErasRendersJSONWhenFlagSet(t *testing.T) {
	f := &fakeHistoryProvider{eras: []persistence.EraOverview{
		{EraID: 1, Name: "torwind", AgentSymbol: "TORWIND"},
	}}
	var out bytes.Buffer

	err := runHistoryEras(context.Background(), f, &out, true)

	require.NoError(t, err)
	require.Contains(t, out.String(), `"name": "torwind"`)
}

func TestRunHistorySummaryResolvesDefaultEraThroughProvider(t *testing.T) {
	f := &fakeHistoryProvider{summary: &persistence.SummaryReport{
		EraID: 1, EraName: "torwind", FinalCredits: 7_700_000,
		IncomeMixPct: map[string]float64{"TRADING_REVENUE": 100},
	}}
	var out bytes.Buffer

	err := runHistorySummary(context.Background(), f, &out, nil, false)

	require.NoError(t, err)
	require.Contains(t, out.String(), "torwind (id 1)")
}

func TestParseEraFlagRejectsNonNumericValues(t *testing.T) {
	_, err := parseEraFlag("not-a-number")
	require.Error(t, err)

	eraID, err := parseEraFlag("")
	require.NoError(t, err)
	require.Nil(t, eraID)

	eraID, err = parseEraFlag("3")
	require.NoError(t, err)
	require.Equal(t, 3, *eraID)
}
