package routing

import (
	"testing"

	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// buildTourRequestWithConstraints is the minimal request the constraint-only assertions
// below need: everything but the constraints is irrelevant to what they check.
func buildTourRequestWithConstraints(cons domainRouting.TourConstraints) *tourConstraintsView {
	req := buildTourRequest(nil, nil, domainRouting.TourShipState{}, cons, nil, nil)
	return &tourConstraintsView{perHopSeconds: req.Constraints.InterSystemTravelPerHopSeconds}
}

type tourConstraintsView struct{ perHopSeconds int32 }

// The measured per-hop toll must reach the wire, or the estimator prices nothing. A dropped
// scalar is silent: the solver simply keeps using its fitted default and the plan looks
// entirely normal, which is exactly how the constant went stale in the first place.
func TestBuildTourRequest_CarriesTheMeasuredPerHopToll(t *testing.T) {
	got := buildTourRequestWithConstraints(domainRouting.TourConstraints{
		InterSystemTravelPerHopSeconds: 1180,
	})
	if got.perHopSeconds != 1180 {
		t.Fatalf("InterSystemTravelPerHopSeconds = %d, want 1180", got.perHopSeconds)
	}
}

// 0 is the fail-open value and it must serialize to nothing: a fleet with too few measured
// hops has to produce a request byte-identical to one from a binary that predates the field.
func TestBuildTourRequest_UnmeasuredTollSerializesToNothing(t *testing.T) {
	got := buildTourRequestWithConstraints(domainRouting.TourConstraints{})
	if got.perHopSeconds != 0 {
		t.Fatalf("InterSystemTravelPerHopSeconds = %d, want 0 (proto3 default, absent on the wire)", got.perHopSeconds)
	}
}
