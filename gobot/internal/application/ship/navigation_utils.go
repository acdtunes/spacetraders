package ship

import (
	"log"
	"strings"
	"time"
)

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
