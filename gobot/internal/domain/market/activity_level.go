package market

type ActivityLevel string

const (
	ActivityLevelWeak       ActivityLevel = "WEAK"
	ActivityLevelGrowing    ActivityLevel = "GROWING"
	ActivityLevelStrong     ActivityLevel = "STRONG"
	ActivityLevelRestricted ActivityLevel = "RESTRICTED"
)

func (a ActivityLevel) BuyerActivityScore() int {
	switch a {
	case ActivityLevelWeak:
		return 4
	case ActivityLevelGrowing:
		return 3
	case ActivityLevelStrong:
		return 2
	case ActivityLevelRestricted:
		return 1
	default:
		return 2
	}
}

func (a ActivityLevel) SellerActivityScore() int {
	switch a {
	case ActivityLevelStrong:
		return 4
	case ActivityLevelGrowing:
		return 3
	case ActivityLevelWeak:
		return 2
	case ActivityLevelRestricted:
		return 1
	default:
		return 2
	}
}

func (a ActivityLevel) String() string {
	return string(a)
}
