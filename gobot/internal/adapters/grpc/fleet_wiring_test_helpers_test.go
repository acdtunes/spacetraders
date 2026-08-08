package grpc

import (
	"context"

	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Shared wiring-test fakes, driven by the growth coordinator's wiring tests.

// fakeCensusShipRepo is a ship repository that DOES carry the tag-independent heavy census — the
// shape *persistence.GormShipRepository has in production.
type fakeCensusShipRepo struct {
	fakeHeavyShipRepo
	heavies int
}

func (r *fakeCensusShipRepo) CountHeavyHulls(_ context.Context, _ shared.PlayerID) (int, error) {
	return r.heavies, nil
}

// fakeFullYardFinder carries BOTH yard surfaces — the priced buy rank and the errand's rank that
// keeps availability-only rows — which is the shape *shipyardQueries.ReachableYardFinder has.
// fakeScannedYards deliberately carries only the first, and is the negative case.
type fakeFullYardFinder struct {
	fakeHeavyYardRanker
}

func (f *fakeFullYardFinder) NearestYardsSelling(_ context.Context, _ int, _, _ []string) ([]shipyardQueries.YardCandidate, error) {
	return nil, nil
}
