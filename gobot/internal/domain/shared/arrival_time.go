package shared

import (
	"fmt"
	"strings"
	"time"
)

// ArrivalTime represents an immutable arrival time from the SpaceTraders API
// This value object encapsulates the ISO8601 timestamp and provides domain logic
// for calculating wait times until arrival.
type ArrivalTime struct {
	timestamp string // ISO8601 format (e.g., "2024-01-01T12:00:00Z")
}

// NewArrivalTime creates a new ArrivalTime value object with validation
// The timestamp must be in ISO8601 format (RFC3339).
//
// Args:
//
//	timestamp: ISO8601 format arrival time from API (e.g., "2024-01-01T12:00:00Z")
//
// Returns:
//
//	ArrivalTime value object or error if timestamp is invalid
func NewArrivalTime(timestamp string) (*ArrivalTime, error) {
	if timestamp == "" {
		return nil, fmt.Errorf("arrival time timestamp cannot be empty")
	}

	if _, err := parseArrivalTimestamp(timestamp); err != nil {
		return nil, fmt.Errorf("invalid arrival time format: %w", err)
	}

	return &ArrivalTime{
		timestamp: timestamp,
	}, nil
}

func parseArrivalTimestamp(timestamp string) (time.Time, error) {
	// Normalize timestamp (handle both Z suffix and +00:00 suffix)
	normalizedTimestamp := strings.Replace(timestamp, "Z", "+00:00", 1)
	return time.Parse(time.RFC3339, normalizedTimestamp)
}

// CalculateWaitTime calculates the number of seconds to wait until arrival
// Returns 0 if the arrival time is in the past or if parsing fails.
//
// Returns:
//
//	Seconds to wait (minimum 0)
func (a *ArrivalTime) CalculateWaitTime() int {
	arrivalTime, err := parseArrivalTimestamp(a.timestamp)
	if err != nil {
		return 0
	}

	now := time.Now().UTC()
	waitSeconds := arrivalTime.Sub(now).Seconds()

	if waitSeconds < 0 {
		return 0
	}

	return int(waitSeconds)
}

func (a *ArrivalTime) Timestamp() string {
	return a.timestamp
}

func (a *ArrivalTime) String() string {
	return fmt.Sprintf("ArrivalTime(%s)", a.timestamp)
}
