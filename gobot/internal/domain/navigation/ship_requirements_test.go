package navigation_test

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

func TestNewShipRequirements_ExposesPowerCrewSlots(t *testing.T) {
	req := navigation.NewShipRequirements(3, 1, 1)

	if req.Power() != 3 {
		t.Errorf("Power() = %d, want 3", req.Power())
	}
	if req.Crew() != 1 {
		t.Errorf("Crew() = %d, want 1", req.Crew())
	}
	if req.Slots() != 1 {
		t.Errorf("Slots() = %d, want 1", req.Slots())
	}
}

// TestNewShipRequirements_ZeroValue reproduces the common case: the API
// omits the requirements sub-object's fields when they don't apply (e.g. a
// module with no crew requirement) - the OpenAPI schema marks power/crew/slots
// as optional with no "required" array, so a zero value must be a valid,
// meaningful requirement (not an error state).
func TestNewShipRequirements_ZeroValue(t *testing.T) {
	req := navigation.NewShipRequirements(0, 0, 0)

	if req.Power() != 0 || req.Crew() != 0 || req.Slots() != 0 {
		t.Errorf("expected all-zero requirements, got power=%d crew=%d slots=%d", req.Power(), req.Crew(), req.Slots())
	}
}
