package commands

import (
	"context"

	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

type fakeFreshnessReader struct {
	snapshots []domainScouting.SystemFreshnessSnapshot
	err       error
}

func (f *fakeFreshnessReader) SystemsFreshness(_ context.Context, _ int) ([]domainScouting.SystemFreshnessSnapshot, error) {
	return f.snapshots, f.err
}

func snap(system string, markets int, oldestAgeSecs, cycleSecs float64, samples int) domainScouting.SystemFreshnessSnapshot {
	return domainScouting.SystemFreshnessSnapshot{
		SystemSymbol: system, MarketCount: markets, OldestAgeSeconds: oldestAgeSecs,
		MeasuredCycleSeconds: cycleSecs, CycleSamples: samples,
	}
}
