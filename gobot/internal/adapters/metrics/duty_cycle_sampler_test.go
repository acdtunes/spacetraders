package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/dutycycle"
)

// A crash in the duty-cycle sampler must never take down the caller — the
// same best-effort contract as APIBudgetTracker and the Prometheus
// collectors.
func TestDutyCycleSampler_NilReceiver_DoesNotPanic(t *testing.T) {
	var s *DutyCycleSampler
	require.NotPanics(t, func() {
		s.Sample(context.Background())
	})
	require.NotPanics(t, func() {
		report := s.Report()
		assert.Zero(t, report.WindowHours)
		assert.Empty(t, report.Hulls)
	})
}

func TestDutyCycleSampler_NilSource_SampleIsNoOp(t *testing.T) {
	s := NewDutyCycleSampler(nil, time.Hour)
	require.NotPanics(t, func() {
		s.Sample(context.Background())
	})
	assert.Empty(t, s.Report().Hulls)
}

func TestDutyCycleSampler_Sample_RecordsEarningAndIdleHulls(t *testing.T) {
	source := func(ctx context.Context) ([]ShipEarningStatus, error) {
		return []ShipEarningStatus{
			{Hull: "TORWIND-1", Earning: true},
			{Hull: "TORWIND-2", Earning: false},
		}, nil
	}
	s := NewDutyCycleSampler(source, time.Hour)

	s.Sample(context.Background())
	report := s.Report()

	require.Len(t, report.Hulls, 2)
	assert.Equal(t, 1.0, report.WindowHours)

	byHull := map[string]dutycycle.HullDutyCycle{}
	for _, h := range report.Hulls {
		byHull[h.Hull] = h
	}

	earning := byHull["TORWIND-1"]
	assert.Equal(t, 1.0, earning.EarningHours)
	assert.Zero(t, earning.IdleHours)
	assert.Equal(t, 100.0, earning.EarningPct)

	idle := byHull["TORWIND-2"]
	assert.Zero(t, idle.EarningHours)
	assert.Equal(t, 1.0, idle.IdleHours)
	assert.Zero(t, idle.EarningPct)
}

func TestDutyCycleSampler_Sample_AccumulatesAcrossMultipleCalls(t *testing.T) {
	earningNext := true
	source := func(ctx context.Context) ([]ShipEarningStatus, error) {
		status := []ShipEarningStatus{{Hull: "TORWIND-1", Earning: earningNext}}
		earningNext = !earningNext
		return status, nil
	}
	s := NewDutyCycleSampler(source, time.Hour)

	s.Sample(context.Background()) // earning
	s.Sample(context.Background()) // idle
	s.Sample(context.Background()) // earning

	report := s.Report()
	require.Len(t, report.Hulls, 1)
	hull := report.Hulls[0]
	assert.Equal(t, 3, hull.SampleCount)
	assert.Equal(t, 2.0, hull.EarningHours)
	assert.Equal(t, 1.0, hull.IdleHours)
}

func TestDutyCycleSampler_Sample_SourceErrorAddsNoSamples(t *testing.T) {
	source := func(ctx context.Context) ([]ShipEarningStatus, error) {
		return nil, errors.New("db unavailable")
	}
	s := NewDutyCycleSampler(source, time.Hour)

	require.NotPanics(t, func() {
		s.Sample(context.Background())
	})
	assert.Empty(t, s.Report().Hulls)
}

func TestDutyCycleSampler_StartAndStop_SamplesOnTicker(t *testing.T) {
	source := func(ctx context.Context) ([]ShipEarningStatus, error) {
		return []ShipEarningStatus{{Hull: "TORWIND-1", Earning: true}}, nil
	}
	s := NewDutyCycleSampler(source, 10*time.Millisecond)

	s.Start()
	defer s.Stop()

	require.Eventually(t, func() bool {
		return len(s.Report().Hulls) == 1
	}, 300*time.Millisecond, 5*time.Millisecond, "expected the background ticker to take at least one sample")
}

func TestGlobalDutyCycleSampler_SetThenGet_ReturnsSameInstance(t *testing.T) {
	t.Cleanup(func() { SetGlobalDutyCycleSampler(nil) })

	s := NewDutyCycleSampler(nil, time.Hour)
	SetGlobalDutyCycleSampler(s)

	assert.Same(t, s, GetGlobalDutyCycleSampler())
}

func TestGlobalDutyCycleSampler_Unset_ReturnsNil(t *testing.T) {
	t.Cleanup(func() { SetGlobalDutyCycleSampler(nil) })
	SetGlobalDutyCycleSampler(nil)

	assert.Nil(t, GetGlobalDutyCycleSampler())
}
