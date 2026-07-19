package market

import "github.com/andrescamacho/spacetraders-go/internal/domain/shared"

// ActivityLevel is DEFINED once in internal/domain/shared and re-exported here as a
// type alias so existing market.ActivityLevel references compile and behave
// identically. Consolidating the definition into the shared leaf package keeps the
// activity vocabulary (WEAK..RESTRICTED) and its buyer/seller scores in a single
// place alongside SupplyLevel, removing cross-package enum drift.
// See shared/activity_level.go for the scoring rules.
type ActivityLevel = shared.ActivityLevel

const (
	ActivityLevelWeak       = shared.ActivityLevelWeak
	ActivityLevelGrowing    = shared.ActivityLevelGrowing
	ActivityLevelStrong     = shared.ActivityLevelStrong
	ActivityLevelRestricted = shared.ActivityLevelRestricted
)
