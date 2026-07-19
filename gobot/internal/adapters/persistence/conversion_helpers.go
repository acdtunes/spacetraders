package persistence

import "time"

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringToPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// derefTime maps a nullable timestamp column to a domain value, treating NULL as the
// zero time — the shape a domain type uses when "unset" is the zero value rather than a
// pointer (e.g. ScoutPost.RespawnParkedUntil).
func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

// timeToPtr is the inverse of derefTime: a zero time persists as NULL so an unset
// deadline leaves the column empty rather than storing the Go epoch.
func timeToPtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
