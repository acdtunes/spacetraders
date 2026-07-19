package shared

import "fmt"

// PlayerID is a value object representing a player's unique identifier
type PlayerID struct {
	value int
}

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

func (p PlayerID) Value() int {
	return p.value
}

func (p PlayerID) String() string {
	return fmt.Sprintf("%d", p.value)
}

func (p PlayerID) Equals(other PlayerID) bool {
	return p.value == other.value
}

func (p PlayerID) IsZero() bool {
	return p.value == 0
}
