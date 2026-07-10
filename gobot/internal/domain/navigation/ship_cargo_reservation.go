package navigation

import "strings"

// defaultReservedCargoPrefixes classifies cargo that exists in a hold only to be
// installed on the hull, never traded: ship hardware bought for outfitting
// (sp-1vhv). A coordinator that liquidates hold contents as sellable manifest
// must treat these as do-not-sell BY DEFAULT — the exact class of the
// MODULE_CARGO_HOLD_III bought for 196,751cr and auto-sold for 97,033cr an hour
// later. Parametrized as a single list (RULINGS #5) rather than scattered string
// literals so the classifier has one home; a per-hull override can still
// force-reserve another good or release one of these for a deliberate resale (see
// Ship.IsCargoReserved).
var defaultReservedCargoPrefixes = []string{"MODULE_", "MOUNT_"}

// IsDefaultReservedCargo reports whether a cargo good symbol is reserved
// (do-not-sell) by DEFAULT classification — ship hardware (MODULE_*/MOUNT_*) that
// rides a working hull only to be installed. This is PURE CODE with no persisted
// state, so it can never fail to load: the module money-guard holds even when a
// hull's per-hull override state is unreadable (sp-1vhv, RULINGS #4).
func IsDefaultReservedCargo(good string) bool {
	for _, prefix := range defaultReservedCargoPrefixes {
		if strings.HasPrefix(good, prefix) {
			return true
		}
	}
	return false
}
