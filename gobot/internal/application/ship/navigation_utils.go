package ship

import (
	"log"
	"strings"
	"time"
)

// ExtractSystemSymbol extracts system symbol from waypoint symbol
// Format: SYSTEM-SECTOR-WAYPOINT -> SYSTEM-SECTOR
// Example: X1-ABC123-AB12 -> X1-ABC123
func ExtractSystemSymbol(waypointSymbol string) string {
	// Find the last hyphen
	for i := len(waypointSymbol) - 1; i >= 0; i-- {
		if waypointSymbol[i] == '-' {
			return waypointSymbol[:i]
		}
	}
	return waypointSymbol
}

// CalculateArrivalWaitTime calculates seconds to wait until arrival
//
// Args:
//
//	arrivalTimeStr: ISO format arrival time from API (e.g., "2024-01-01T12:00:00Z")
//
// Returns:
//
//	Seconds to wait (minimum 0)
func CalculateArrivalWaitTime(arrivalTimeStr string) int {
	// Handle both Z suffix and +00:00 suffix
	arrivalTimeStr = strings.Replace(arrivalTimeStr, "Z", "+00:00", 1)

	arrivalTime, err := time.Parse(time.RFC3339, arrivalTimeStr)
	if err != nil {
		log.Printf("Warning: failed to parse arrival time %s: %v", arrivalTimeStr, err)
		return 0
	}

	now := time.Now().UTC()
	waitSeconds := arrivalTime.Sub(now).Seconds()

	if waitSeconds < 0 {
		return 0
	}

	return int(waitSeconds)
}
