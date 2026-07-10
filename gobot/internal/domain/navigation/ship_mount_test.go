package navigation_test

import (
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

func TestNewShipMount_ExposesFields(t *testing.T) {
	req := navigation.NewShipRequirements(1, 0, 1)
	mount := navigation.NewShipMount(
		"MOUNT_MINING_LASER_I",
		"Mining Laser I",
		30,
		[]string{"IRON_ORE", "COPPER_ORE"},
		req,
	)

	if mount.Symbol() != "MOUNT_MINING_LASER_I" {
		t.Errorf("Symbol() = %q, want MOUNT_MINING_LASER_I", mount.Symbol())
	}
	if mount.Name() != "Mining Laser I" {
		t.Errorf("Name() = %q, want %q", mount.Name(), "Mining Laser I")
	}
	if mount.Strength() != 30 {
		t.Errorf("Strength() = %d, want 30", mount.Strength())
	}
	if !reflect.DeepEqual(mount.Deposits(), []string{"IRON_ORE", "COPPER_ORE"}) {
		t.Errorf("Deposits() = %v, want [IRON_ORE COPPER_ORE]", mount.Deposits())
	}
	if mount.Requirements() != req {
		t.Errorf("Requirements() = %+v, want %+v", mount.Requirements(), req)
	}
}

// TestNewShipMount_NilDeposits reproduces a sensor/weapon mount (e.g. a
// non-mining mount) whose API response omits "deposits" entirely - it must
// not panic and Deposits() must report an empty slice rather than nil
// dereferencing.
func TestNewShipMount_NilDeposits(t *testing.T) {
	mount := navigation.NewShipMount("MOUNT_SENSOR_ARRAY_I", "Sensor Array I", 0, nil, navigation.NewShipRequirements(1, 0, 1))

	if len(mount.Deposits()) != 0 {
		t.Errorf("Deposits() = %v, want empty", mount.Deposits())
	}
}
