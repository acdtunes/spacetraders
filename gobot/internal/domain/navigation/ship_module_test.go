package navigation_test

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

func TestNewShipModule_ExposesRequirements(t *testing.T) {
	req := navigation.NewShipRequirements(2, 0, 1)
	module := navigation.NewShipModule("MODULE_CARGO_HOLD_I", 15, 0, req)

	if module.Symbol() != "MODULE_CARGO_HOLD_I" {
		t.Errorf("Symbol() = %q, want MODULE_CARGO_HOLD_I", module.Symbol())
	}
	if module.Capacity() != 15 {
		t.Errorf("Capacity() = %d, want 15", module.Capacity())
	}
	if module.Requirements() != req {
		t.Errorf("Requirements() = %+v, want %+v", module.Requirements(), req)
	}
}

func TestNewShipModule_IsJumpDriveStillWorksWithRequirements(t *testing.T) {
	module := navigation.NewShipModule("MODULE_JUMP_DRIVE_I", 0, 500, navigation.NewShipRequirements(15, 2, 1))

	if !module.IsJumpDrive() {
		t.Errorf("IsJumpDrive() = false, want true")
	}
}
