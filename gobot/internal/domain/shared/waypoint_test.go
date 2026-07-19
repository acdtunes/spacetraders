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
