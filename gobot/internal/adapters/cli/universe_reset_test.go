package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

type recordingCloser struct {
	called bool
	report *persistence.CloseReport
}

func (r *recordingCloser) CloseEra(ctx context.Context, name string) (*persistence.CloseReport, error) {
	r.called = true
	return r.report, nil
}

type recordingScrubber struct {
	called bool
	report *persistence.ScrubReport
}

func (r *recordingScrubber) ScrubEra(ctx context.Context, name string) (*persistence.ScrubReport, error) {
	r.called = true
	return r.report, nil
}

func TestUniverseCloseRefusesWhenConfirmDoesNotEchoEra(t *testing.T) {
	closer := &recordingCloser{}
	err := runUniverseClose(context.Background(), closer, "torwind", "wrong", &bytes.Buffer{})
	require.Error(t, err)
	require.False(t, closer.called)
}

func TestUniverseCloseProceedsWhenConfirmEchoesEra(t *testing.T) {
	closer := &recordingCloser{report: &persistence.CloseReport{Era: &persistence.EraModel{Name: "torwind"}, FinalCredits: 10}}
	out := &bytes.Buffer{}
	err := runUniverseClose(context.Background(), closer, "torwind", "torwind", out)
	require.NoError(t, err)
	require.True(t, closer.called)
}

func TestUniverseScrubRefusesWhenConfirmDoesNotEchoEra(t *testing.T) {
	scrubber := &recordingScrubber{}
	err := runUniverseScrub(context.Background(), scrubber, "torwind", "", &bytes.Buffer{})
	require.Error(t, err)
	require.False(t, scrubber.called)
}

func TestUniverseScrubProceedsWhenConfirmEchoesEra(t *testing.T) {
	scrubber := &recordingScrubber{report: &persistence.ScrubReport{Era: &persistence.EraModel{Name: "torwind"}}}
	out := &bytes.Buffer{}
	err := runUniverseScrub(context.Background(), scrubber, "torwind", "torwind", out)
	require.NoError(t, err)
	require.True(t, scrubber.called)
}
