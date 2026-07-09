package dutycycle

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleInterval matches the cadence samples are assumed to have been taken
// at: each Sample represents this much wall-clock time for its hull.
const sampleInterval = 5 * time.Minute

func TestComputeReport_NoSamples_ReturnsEmptyReport(t *testing.T) {
	report := ComputeReport(nil, sampleInterval)

	assert.Zero(t, report.WindowHours)
	assert.Empty(t, report.Hulls)
}

func TestComputeReport_EarningSamplesAccumulateEarningHours(t *testing.T) {
	samples := []Sample{
		{Hull: "HULL-1", Earning: true},
		{Hull: "HULL-1", Earning: true},
		{Hull: "HULL-1", Earning: false},
	}

	report := ComputeReport(samples, sampleInterval)

	require.Len(t, report.Hulls, 1)
	hull := report.Hulls[0]
	assert.Equal(t, "HULL-1", hull.Hull)
	assert.InDelta(t, (10.0 * time.Minute).Hours(), hull.EarningHours, 0.0001, "2 earning samples * 5m")
	assert.InDelta(t, (5.0 * time.Minute).Hours(), hull.IdleHours, 0.0001, "1 idle sample * 5m")
	assert.Equal(t, 3, hull.SampleCount)
}

func TestComputeReport_EarningPctIsEarningOverTotalHours(t *testing.T) {
	samples := []Sample{
		{Hull: "HULL-1", Earning: true},
		{Hull: "HULL-1", Earning: true},
		{Hull: "HULL-1", Earning: true},
		{Hull: "HULL-1", Earning: false},
	}

	report := ComputeReport(samples, sampleInterval)

	require.Len(t, report.Hulls, 1)
	assert.InDelta(t, 75.0, report.Hulls[0].EarningPct, 0.0001, "3 of 4 samples earning = 75%")
}

func TestComputeReport_EarningPctWithNoSamples_IsZeroNotNaN(t *testing.T) {
	// A hull can appear with zero samples only via multi-hull aggregation paths;
	// guard the division directly via the pure helper behavior on an all-idle,
	// zero-count edge implicitly covered by NoSamples test above. This test
	// pins the NaN-safety contract explicitly for a single degenerate sample.
	report := ComputeReport([]Sample{{Hull: "HULL-1", Earning: false}}, 0)

	require.Len(t, report.Hulls, 1)
	assert.False(t, math.IsNaN(report.Hulls[0].EarningPct))
	assert.Zero(t, report.Hulls[0].EarningPct)
}

func TestComputeReport_MultipleHullsSortedByEarningHoursDesc(t *testing.T) {
	samples := []Sample{
		{Hull: "IDLE-1", Earning: false},
		{Hull: "BUSY-1", Earning: true},
		{Hull: "BUSY-1", Earning: true},
	}

	report := ComputeReport(samples, sampleInterval)

	require.Len(t, report.Hulls, 2)
	assert.Equal(t, "BUSY-1", report.Hulls[0].Hull, "hull with more earning hours sorts first")
	assert.Equal(t, "IDLE-1", report.Hulls[1].Hull)
}

func TestComputeReport_WindowHoursIsSampleIntervalTimesMaxSampleCount(t *testing.T) {
	samples := []Sample{
		{Hull: "HULL-1", Earning: true},
		{Hull: "HULL-1", Earning: true},
		{Hull: "HULL-2", Earning: false},
	}

	report := ComputeReport(samples, sampleInterval)

	assert.InDelta(t, (10 * time.Minute).Hours(), report.WindowHours, 0.0001, "widest observed hull history (2 samples * 5m)")
}
