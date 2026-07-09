package grpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
	"github.com/andrescamacho/spacetraders-go/internal/domain/dutycycle"
)

// apiBudgetReportToProto and dutyCycleReportToProto are pure mappers from the
// domain report shapes (internal/domain/apibudget, internal/domain/dutycycle)
// to their GetAPIBudgetResponse proto counterparts (sp-51ti task #42). They
// are tested directly, independent of any gRPC/daemon machinery, mirroring
// how apibudget.ComputeReport and dutycycle.ComputeReport are themselves pure
// and independently tested.

func TestAPIBudgetReportToProto_MapsScalarFields(t *testing.T) {
	report := apibudget.Report{
		WindowSeconds:        300,
		TotalRequests:        900,
		GlobalReqPerSec:      3.0,
		CeilingReqPerSec:     2.0,
		UtilizationPct:       150,
		HeadroomReqPerSec:    -1.0,
		RateLimited429:       12,
		RateLimited429PerMin: 2.4,
		HullsToCeiling:       5.5,
		PurposeCounts:        map[apibudget.Purpose]int{},
		PurposeSharePct:      map[apibudget.Purpose]float64{},
	}

	proto := apiBudgetReportToProto(report)

	require.NotNil(t, proto)
	assert.Equal(t, 300.0, proto.WindowSeconds)
	assert.Equal(t, int32(900), proto.TotalRequests)
	assert.Equal(t, 3.0, proto.GlobalReqPerSec)
	assert.Equal(t, 2.0, proto.CeilingReqPerSec)
	assert.Equal(t, 150.0, proto.UtilizationPct)
	assert.Equal(t, -1.0, proto.HeadroomReqPerSec)
	assert.Equal(t, int32(12), proto.RateLimited_429)
	assert.Equal(t, 2.4, proto.RateLimited_429PerMin)
	assert.Equal(t, 5.5, proto.HullsToCeiling)
}

func TestAPIBudgetReportToProto_MapsPurposeCountsAndShare(t *testing.T) {
	report := apibudget.Report{
		PurposeCounts: map[apibudget.Purpose]int{
			apibudget.PurposePoll:     700,
			apibudget.PurposeTransact: 150,
			apibudget.PurposeRetry:    50,
		},
		PurposeSharePct: map[apibudget.Purpose]float64{
			apibudget.PurposePoll:     77.78,
			apibudget.PurposeTransact: 16.67,
			apibudget.PurposeRetry:    5.56,
		},
	}

	proto := apiBudgetReportToProto(report)

	require.NotNil(t, proto)
	assert.Equal(t, int32(700), proto.PurposeCounts["poll"])
	assert.Equal(t, int32(150), proto.PurposeCounts["transact"])
	assert.Equal(t, int32(50), proto.PurposeCounts["retry"])
	assert.InDelta(t, 77.78, proto.PurposeSharePct["poll"], 0.001)
	assert.InDelta(t, 16.67, proto.PurposeSharePct["transact"], 0.001)
	assert.InDelta(t, 5.56, proto.PurposeSharePct["retry"], 0.001)
}

func TestAPIBudgetReportToProto_MapsPerHullPreservingOrder(t *testing.T) {
	report := apibudget.Report{
		PurposeCounts:   map[apibudget.Purpose]int{},
		PurposeSharePct: map[apibudget.Purpose]float64{},
		PerHull: []apibudget.HullStats{
			{Hull: "TORWIND-1", RequestsInWindow: 100, ReqPerSec: 0.33},
			{Hull: "TORWIND-2", RequestsInWindow: 40, ReqPerSec: 0.13},
		},
	}

	proto := apiBudgetReportToProto(report)

	require.NotNil(t, proto)
	require.Len(t, proto.PerHull, 2)
	assert.Equal(t, "TORWIND-1", proto.PerHull[0].Hull)
	assert.Equal(t, int32(100), proto.PerHull[0].RequestsInWindow)
	assert.Equal(t, 0.33, proto.PerHull[0].ReqPerSec)
	assert.Equal(t, "TORWIND-2", proto.PerHull[1].Hull)
	assert.Equal(t, int32(40), proto.PerHull[1].RequestsInWindow)
}

func TestAPIBudgetReportToProto_EmptyPerHullMapsToEmptySlice(t *testing.T) {
	report := apibudget.Report{
		PurposeCounts:   map[apibudget.Purpose]int{},
		PurposeSharePct: map[apibudget.Purpose]float64{},
		PerHull:         []apibudget.HullStats{},
	}

	proto := apiBudgetReportToProto(report)

	require.NotNil(t, proto)
	assert.Empty(t, proto.PerHull)
}

func TestDutyCycleReportToProto_MapsWindowHoursAndHulls(t *testing.T) {
	report := dutycycle.Report{
		WindowHours: 24,
		Hulls: []dutycycle.HullDutyCycle{
			{Hull: "TORWIND-1", EarningHours: 20, IdleHours: 4, EarningPct: 83.33, SampleCount: 1440},
			{Hull: "TORWIND-2", EarningHours: 0, IdleHours: 24, EarningPct: 0, SampleCount: 1440},
		},
	}

	proto := dutyCycleReportToProto(report)

	require.NotNil(t, proto)
	assert.Equal(t, 24.0, proto.WindowHours)
	require.Len(t, proto.Hulls, 2)

	first := proto.Hulls[0]
	assert.Equal(t, "TORWIND-1", first.Hull)
	assert.Equal(t, 20.0, first.EarningHours)
	assert.Equal(t, 4.0, first.IdleHours)
	assert.InDelta(t, 83.33, first.EarningPct, 0.001)
	assert.Equal(t, int32(1440), first.SampleCount)

	second := proto.Hulls[1]
	assert.Equal(t, "TORWIND-2", second.Hull)
	assert.Zero(t, second.EarningHours)
}

func TestDutyCycleReportToProto_EmptyHullsMapsToEmptySlice(t *testing.T) {
	proto := dutyCycleReportToProto(dutycycle.Report{WindowHours: 1, Hulls: []dutycycle.HullDutyCycle{}})

	require.NotNil(t, proto)
	assert.Equal(t, 1.0, proto.WindowHours)
	assert.Empty(t, proto.Hulls)
}
