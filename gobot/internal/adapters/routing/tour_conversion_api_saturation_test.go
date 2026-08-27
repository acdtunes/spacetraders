package routing

import (
	"testing"

	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// The measured request-budget pressure must reach the wire, or the second resource a tour
// spends is priced nowhere. A dropped scalar is silent: the solver keeps ranking on credits
// per hour and every plan looks entirely normal.
func TestBuildTourRequest_CarriesTheMeasuredAPISaturation(t *testing.T) {
	req := buildTourRequest(nil, nil, domainRouting.TourShipState{},
		domainRouting.TourConstraints{APISaturationPermille: 820}, nil, nil)
	if got := req.Constraints.ApiSaturationPermille; got != 820 {
		t.Fatalf("ApiSaturationPermille = %d, want 820", got)
	}
}

// 0 is the fail-open value and it must serialize to nothing: a fleet with headroom has to
// produce a request byte-identical to one from a binary that predates the field.
func TestBuildTourRequest_UnmeasuredSaturationSerializesToNothing(t *testing.T) {
	req := buildTourRequest(nil, nil, domainRouting.TourShipState{},
		domainRouting.TourConstraints{}, nil, nil)
	if got := req.Constraints.ApiSaturationPermille; got != 0 {
		t.Fatalf("ApiSaturationPermille = %d, want 0 (proto3 default, absent on the wire)", got)
	}
}
