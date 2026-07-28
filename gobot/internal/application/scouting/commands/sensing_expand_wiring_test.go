package commands

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// sensing_expand_wiring_test.go pins that the expansion engine is actually HANDED the collaborators
// it needs — the class of defect where a correct engine ships dormant because one line of wiring is
// missing and no test notices.
//
// It is a real failure mode in this codebase rather than a hypothetical: an entire off-gate
// expansion chain sat complete and unreachable because its only driver was a retired coordinator.
// A mutation deleting the ListingMemo wiring below failed no test until this file existed.

type wiringMemo struct{}

func (wiringMemo) LastListingScan(context.Context, int, string) (bool, time.Time, bool, error) {
	return false, time.Time{}, false, nil
}

// The expansion engine receives the stored-listing memo, which is what lets seed staging prefer a
// yard we have EVIDENCE sells probes over one the shipyard-trait fallback merely guessed at. Without
// it staging has no evidence to rank on and silently reverts to the pre-fix behaviour.
func TestSensingEnginePorts_ExpandPortsCarriesTheListingMemo(t *testing.T) {
	memo := wiringMemo{}
	ports := SensingEnginePorts{ListingMemo: memo}

	if got := ports.expandPorts(1, map[string]bool{"FUEL": true}).ListingMemo; got == nil {
		t.Fatalf("expandPorts dropped the ListingMemo — seed staging would have no evidence to rank " +
			"yards on and would quietly stage onto shipyard-trait guesses again, which is the exact " +
			"defect this port was added to fix")
	}
}

// …and the buy queue keeps it too, so arming one consumer cannot silently disarm the other.
func TestSensingEnginePorts_BuyPortsStillCarriesTheListingMemo(t *testing.T) {
	ports := SensingEnginePorts{ListingMemo: wiringMemo{}}

	if got := ports.buyPorts("container-1", nil).ListingMemo; got == nil {
		t.Fatalf("buyPorts dropped the ListingMemo — the drain would pay to re-learn a standing fact")
	}
}

var _ parkedsensing.ProbeListingMemo = wiringMemo{}
