package navigation

import "strings"

// defaultReservedCargoPrefixes classifies cargo that exists in a hold only to be
// installed on the hull, never traded: ship hardware bought for outfitting.
// A coordinator that liquidates hold contents as sellable manifest must treat
// these as do-not-sell BY DEFAULT. Parametrized as a single list (RULINGS #5)
// rather than scattered string literals so the classifier has one home; a
// per-hull override can still force-reserve another good or release one of
// these for a deliberate resale (see Ship.IsCargoReserved).
var defaultReservedCargoPrefixes = []string{"MODULE_", "MOUNT_"}

// IsDefaultReservedCargo reports whether a cargo good symbol is reserved
// (do-not-sell) by DEFAULT classification — ship hardware (MODULE_*/MOUNT_*) that
// rides a working hull only to be installed. This is PURE CODE with no persisted
// state, so it can never fail to load: the module money-guard holds even when a
// hull's per-hull override state is unreadable (RULINGS #4).
func IsDefaultReservedCargo(good string) bool {
	for _, prefix := range defaultReservedCargoPrefixes {
		if strings.HasPrefix(good, prefix) {
			return true
		}
	}
	return false
}

// IsCargoReserved reports whether a cargo good must NOT be sold from this hull by
// any coordinator or the CLI. Resolution order, fail-closed:
//  1. If the persisted override state is corrupt/unreadable, EVERY good is treated
//     as reserved — a read failure never converts reserved cargo into sellable
//     manifest (RULINGS #4).
//  2. An explicit per-hull override wins: true = reserved, false = sellable (the
//     deliberate module-resale escape hatch).
//  3. Otherwise the default classification applies: ship hardware
//     (MODULE_*/MOUNT_*) is reserved, everything else is sellable.
func (s *Ship) IsCargoReserved(good string) bool {
	if s.reservationStateCorrupt {
		return true
	}
	if decision, ok := s.reservationOverrides[good]; ok {
		return decision
	}
	return IsDefaultReservedCargo(good)
}

// SetReservationOverrides loads the per-hull override set and corrupt flag from
// persisted state — used by the repository on reconstruct. A corrupt flag makes
// IsCargoReserved fail closed (see there). A nil map clears the override set.
func (s *Ship) SetReservationOverrides(overrides map[string]bool, corrupt bool) {
	s.reservationOverrides = overrides
	s.reservationStateCorrupt = corrupt
}

// ReservationOverrides returns a copy of the per-hull override set
// (good -> reserved decision) for persistence and CLI display. Never nil.
func (s *Ship) ReservationOverrides() map[string]bool {
	out := make(map[string]bool, len(s.reservationOverrides))
	for k, v := range s.reservationOverrides {
		out[k] = v
	}
	return out
}

// ReservationStateCorrupt reports whether this hull's persisted override state
// failed to parse — the fail-closed signal read by IsCargoReserved.
func (s *Ship) ReservationStateCorrupt() bool {
	return s.reservationStateCorrupt
}

// SetCargoReservation sets an explicit per-hull override for a good: reserved=true
// force-protects it, reserved=false force-allows its sale (releasing the default
// module reservation for a deliberate resale). The domain mutation behind the
// `ship reserve-cargo`/`unreserve-cargo` CLI verbs.
func (s *Ship) SetCargoReservation(good string, reserved bool) {
	if s.reservationOverrides == nil {
		s.reservationOverrides = map[string]bool{}
	}
	s.reservationOverrides[good] = reserved
}
