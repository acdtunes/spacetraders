package shared

import "testing"

// TraitGrantsFuel owns the fuel-rule vocabulary (MARKETPLACE | FUEL_STATION). These
// tests pin that vocabulary so it cannot silently drift.
func TestTraitGrantsFuel(t *testing.T) {
	cases := []struct {
		name  string
		trait string
		want  bool
	}{
		{"marketplace grants fuel", "MARKETPLACE", true},
		{"fuel station grants fuel", "FUEL_STATION", true},
		{"shipyard does not grant fuel", "SHIPYARD", false},
		{"uncharted does not grant fuel", "UNCHARTED", false},
		{"empty trait does not grant fuel", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TraitGrantsFuel(tc.trait); got != tc.want {
				t.Errorf("TraitGrantsFuel(%q) = %v, want %v", tc.trait, got, tc.want)
			}
		})
	}
}

// WaypointGrantsFuel adds the TYPE floor to the trait rule. The API reports
// FUEL_STATION as a waypoint type and never as a trait, so the traits alone miss it.
func TestWaypointGrantsFuel(t *testing.T) {
	cases := []struct {
		name         string
		waypointType string
		traits       []string
		want         bool
	}{
		{"fuel station type survives an uncharted trait list", "FUEL_STATION", []string{"UNCHARTED"}, true},
		{"fuel station type with no traits at all", "FUEL_STATION", nil, true},
		{"marketplace trait grants fuel without the type", "ORBITAL_STATION", []string{"MARKETPLACE"}, true},
		{"neither type nor trait", "ORBITAL_STATION", []string{"UNCHARTED"}, false},
		{"the floor is exactly FUEL_STATION", "PLANET", []string{"UNCHARTED"}, false},
		{"an empty type grants nothing", "", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WaypointGrantsFuel(tc.waypointType, tc.traits); got != tc.want {
				t.Errorf("WaypointGrantsFuel(%q, %v) = %v, want %v", tc.waypointType, tc.traits, got, tc.want)
			}
		})
	}
}

// CanRefuel is the READ-side backstop: it answers over an already-cached waypoint
// whose has-fuel bit was derived from traits that never named the fuel station.
func TestWaypointCanRefuel(t *testing.T) {
	cases := []struct {
		name         string
		waypointType string
		hasFuel      bool
		want         bool
	}{
		{"the derived bit alone is enough", "PLANET", true, true},
		{"the type overrides a false derived bit", "FUEL_STATION", false, true},
		{"neither says fuel", "PLANET", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wp, err := NewWaypoint("X1-TZ71-C10D", 0, 0)
			if err != nil {
				t.Fatalf("NewWaypoint: %v", err)
			}
			wp.Type = tc.waypointType
			wp.HasFuel = tc.hasFuel

			if got := wp.CanRefuel(); got != tc.want {
				t.Errorf("CanRefuel() with type %q and has-fuel %v = %v, want %v", tc.waypointType, tc.hasFuel, got, tc.want)
			}
		})
	}
}

func TestTraitsGrantFuel(t *testing.T) {
	cases := []struct {
		name   string
		traits []string
		want   bool
	}{
		{"nil slice does not grant fuel", nil, false},
		{"empty slice does not grant fuel", []string{}, false},
		{"no fuel-granting trait", []string{"SHIPYARD", "UNCHARTED"}, false},
		{"marketplace among others grants fuel", []string{"SHIPYARD", "MARKETPLACE"}, true},
		{"fuel station among others grants fuel", []string{"FUEL_STATION", "UNCHARTED"}, true},
		{"only fuel station grants fuel", []string{"FUEL_STATION"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TraitsGrantFuel(tc.traits); got != tc.want {
				t.Errorf("TraitsGrantFuel(%v) = %v, want %v", tc.traits, got, tc.want)
			}
		})
	}
}
