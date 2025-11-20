package contract

import "context"

// ContractRepository defines the interface for contract persistence operations
type ContractRepository interface {
	FindByID(ctx context.Context, contractID string, playerID int) (*Contract, error)
	FindActiveContracts(ctx context.Context, playerID int) ([]*Contract, error)
	Add(ctx context.Context, contract *Contract) error
}

// PurchaseHistoryRepository defines the interface for contract purchase history persistence
type PurchaseHistoryRepository interface {
	Add(ctx context.Context, history *PurchaseHistory) error
	FindRecentMarkets(ctx context.Context, playerID int, systemSymbol string, limit int, sinceDays int) ([]string, error)
}

// PurchaseHistoryData is the DTO for purchase history
type PurchaseHistoryData struct {
	PlayerID       int
	SystemSymbol   string
	WaypointSymbol string
	TradeGood      string
	PurchasedAt    string // RFC3339 format
	ContractID     string
}
