package grpc

import (
	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
	"github.com/andrescamacho/spacetraders-go/internal/domain/dutycycle"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// apiBudgetReportToProto maps an apibudget.Report onto its proto
// counterpart (sp-51ti task #42). Pure and independent of any gRPC/daemon
// machinery so it can be unit tested directly.
func apiBudgetReportToProto(report apibudget.Report) *pb.APIBudgetReport {
	purposeCounts := make(map[string]int32, len(report.PurposeCounts))
	for purpose, count := range report.PurposeCounts {
		purposeCounts[string(purpose)] = int32(count)
	}

	purposeSharePct := make(map[string]float64, len(report.PurposeSharePct))
	for purpose, pct := range report.PurposeSharePct {
		purposeSharePct[string(purpose)] = pct
	}

	perHull := make([]*pb.APIBudgetHullStats, 0, len(report.PerHull))
	for _, h := range report.PerHull {
		perHull = append(perHull, &pb.APIBudgetHullStats{
			Hull:             h.Hull,
			RequestsInWindow: int32(h.RequestsInWindow),
			ReqPerSec:        h.ReqPerSec,
		})
	}

	return &pb.APIBudgetReport{
		WindowSeconds:         report.WindowSeconds,
		TotalRequests:         int32(report.TotalRequests),
		GlobalReqPerSec:       report.GlobalReqPerSec,
		CeilingReqPerSec:      report.CeilingReqPerSec,
		UtilizationPct:        report.UtilizationPct,
		HeadroomReqPerSec:     report.HeadroomReqPerSec,
		RateLimited_429:       int32(report.RateLimited429),
		RateLimited_429PerMin: report.RateLimited429PerMin,
		PurposeCounts:         purposeCounts,
		PurposeSharePct:       purposeSharePct,
		HullsToCeiling:        report.HullsToCeiling,
		PerHull:               perHull,
	}
}

// dutyCycleReportToProto maps a dutycycle.Report onto its proto counterpart
// (sp-51ti captain amendment, task #42). Pure, mirroring
// apiBudgetReportToProto.
func dutyCycleReportToProto(report dutycycle.Report) *pb.DutyCycleReport {
	hulls := make([]*pb.DutyCycleHullStats, 0, len(report.Hulls))
	for _, h := range report.Hulls {
		hulls = append(hulls, &pb.DutyCycleHullStats{
			Hull:         h.Hull,
			EarningHours: h.EarningHours,
			IdleHours:    h.IdleHours,
			EarningPct:   h.EarningPct,
			SampleCount:  int32(h.SampleCount),
		})
	}

	return &pb.DutyCycleReport{
		WindowHours: report.WindowHours,
		Hulls:       hulls,
	}
}
