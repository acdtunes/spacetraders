package cli

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

type fakeServerStatusProvider struct {
	status *api.ServerStatus
	err    error
}

func (f *fakeServerStatusProvider) GetServerStatus(ctx context.Context) (*api.ServerStatus, error) {
	return f.status, f.err
}

type fakeOpenEraProvider struct {
	era *persistence.EraModel
	err error
}

func (f *fakeOpenEraProvider) FindOpenEra(ctx context.Context) (*persistence.EraModel, error) {
	return f.era, f.err
}

func eraOn(name, date string) *persistence.EraModel {
	d, _ := time.Parse("2006-01-02", date)
	return &persistence.EraModel{Name: name, UniverseResetDate: &d}
}

func statusOn(resetDate string) *api.ServerStatus {
	return &api.ServerStatus{
		ResetDate:    resetDate,
		ServerResets: api.ServerResets{Next: "2026-07-13T16:00:00.000Z", Frequency: "fortnightly"},
	}
}

func TestUniverseStatusFlagsMismatchWithNonNilError(t *testing.T) {
	sp := &fakeServerStatusProvider{status: statusOn("2026-07-05")}
	ep := &fakeOpenEraProvider{era: eraOn("torwind", "2026-06-22")}
	var out bytes.Buffer

	err := runUniverseStatus(context.Background(), sp, ep, &out)

	require.Error(t, err)
	require.Contains(t, out.String(), "MISMATCH")
	require.Contains(t, out.String(), "2026-07-05")
	require.Contains(t, out.String(), "2026-06-22")
}

func TestUniverseStatusReportsInSyncWhenDatesMatch(t *testing.T) {
	sp := &fakeServerStatusProvider{status: statusOn("2026-06-22")}
	ep := &fakeOpenEraProvider{era: eraOn("torwind", "2026-06-22")}
	var out bytes.Buffer

	err := runUniverseStatus(context.Background(), sp, ep, &out)

	require.NoError(t, err)
	require.NotContains(t, out.String(), "MISMATCH")
	require.Contains(t, out.String(), "torwind")
}

func TestUniverseStatusReportsNoEraAndSucceedsWhenNoOpenEra(t *testing.T) {
	sp := &fakeServerStatusProvider{status: statusOn("2026-06-22")}
	ep := &fakeOpenEraProvider{era: nil}
	var out bytes.Buffer

	err := runUniverseStatus(context.Background(), sp, ep, &out)

	require.NoError(t, err)
	require.Contains(t, out.String(), "NO ERA")
	require.NotContains(t, out.String(), "MISMATCH")
}
