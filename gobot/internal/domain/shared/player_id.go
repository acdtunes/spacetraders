package shared

import "fmt"

// PlayerID is a value object representing a player's unique identifier
type PlayerID struct {
	value int
}

// NewPlayerID creates a new PlayerID value object
func NewPlayerID(id int) (PlayerID, error) {
	if id <= 0 {
		return PlayerID{}, fmt.Errorf("player_id must be positive")
	}
	return PlayerID{value: id}, nil
}

// MustNewPlayerID creates a new PlayerID value object, panicking if invalid
// Use this only when you're certain the ID is valid (e.g., from database)
func MustNewPlayerID(id int) PlayerID {
	playerID, err := NewPlayerID(id)
	if err != nil {
		panic(err)
	}
	return playerID
}

// Value returns the integer value of the PlayerID
func (p PlayerID) Value() int {
	return p.value
}

// String returns a string representation of the PlayerID
func (p PlayerID) String() string {
	return fmt.Sprintf("%d", p.value)
}

// Equals checks if two PlayerIDs are equal
func (p PlayerID) Equals(other PlayerID) bool {
	return p.value == other.value
}

// IsZero checks if the PlayerID is the zero value (uninitialized)
func (p PlayerID) IsZero() bool {
	return p.value == 0
}
