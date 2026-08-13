package ports

import (
	"encoding/json"
	"strings"
)

// ErrCodeNotAtShipyard is the API's refusal to scrap a hull that is not docked at
// a waypoint carrying the SHIPYARD trait.
const ErrCodeNotAtShipyard = 4263

// ScrapQuote is what a hull would fetch if scrapped, read without selling it.
type ScrapQuote struct {
	ShipSymbol     string
	WaypointSymbol string
	TotalPrice     int
	Timestamp      string
}

// ScrapResult is a completed scrap: the hull is gone from the game and the
// proceeds are banked.
type ScrapResult struct {
	ShipSymbol     string
	WaypointSymbol string
	TotalPrice     int
	Timestamp      string

	// AgentCredits is the balance AFTER the sale (data.agent.credits), nil when
	// the API omitted the agent block so an absent block stays distinct from zero.
	AgentCredits *int
}

// IsNotAtShipyard reports whether err is the 4263 refusal. It matches on the
// parsed code, so prose merely containing the digits is not mistaken for one.
func IsNotAtShipyard(err error) bool {
	body, ok := apiErrorBody(err)
	if !ok {
		return false
	}
	var envelope struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.NewDecoder(strings.NewReader(body)).Decode(&envelope) != nil {
		return false
	}
	return envelope.Error.Code == ErrCodeNotAtShipyard
}
