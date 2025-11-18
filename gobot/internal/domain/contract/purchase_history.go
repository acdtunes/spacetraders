package contract

import (
	"fmt"
	"time"
)

// PurchaseHistory tracks where contract cargo purchases are made
// This data is used to intelligently position idle ships at frequently-used markets
type PurchaseHistory struct {
	playerID       int
	systemSymbol   string
	waypointSymbol string
	tradeGood      string
	purchasedAt    time.Time
	contractID     string
}

// NewPurchaseHistory creates a new purchase history record
func NewPurchaseHistory(
	playerID int,
	systemSymbol string,
	waypointSymbol string,
	tradeGood string,
	contractID string,
) (*PurchaseHistory, error) {
	if playerID <= 0 {
		return nil, fmt.Errorf("invalid player ID")
	}
	if systemSymbol == "" {
		return nil, fmt.Errorf("system symbol cannot be empty")
	}
	if waypointSymbol == "" {
		return nil, fmt.Errorf("waypoint symbol cannot be empty")
	}
	if tradeGood == "" {
		return nil, fmt.Errorf("trade good cannot be empty")
	}
	if contractID == "" {
		return nil, fmt.Errorf("contract ID cannot be empty")
	}

	return &PurchaseHistory{
		playerID:       playerID,
		systemSymbol:   systemSymbol,
		waypointSymbol: waypointSymbol,
		tradeGood:      tradeGood,
		purchasedAt:    time.Now().UTC(),
		contractID:     contractID,
	}, nil
}

// Getters
func (p *PurchaseHistory) PlayerID() int           { return p.playerID }
func (p *PurchaseHistory) SystemSymbol() string    { return p.systemSymbol }
func (p *PurchaseHistory) WaypointSymbol() string  { return p.waypointSymbol }
func (p *PurchaseHistory) TradeGood() string       { return p.tradeGood }
func (p *PurchaseHistory) PurchasedAt() time.Time  { return p.purchasedAt }
func (p *PurchaseHistory) ContractID() string      { return p.contractID }
