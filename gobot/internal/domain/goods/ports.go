package goods

import "context"

// GoodsFactoryRepository defines the persistence interface for goods factories
type GoodsFactoryRepository interface {
	// Create persists a new goods factory
	Create(ctx context.Context, factory *GoodsFactory) error

	// Update persists changes to an existing goods factory
	Update(ctx context.Context, factory *GoodsFactory) error

	// FindByID retrieves a goods factory by ID and player ID
	FindByID(ctx context.Context, id string, playerID int) (*GoodsFactory, error)

	// FindActiveByPlayer retrieves all active factories for a player
	FindActiveByPlayer(ctx context.Context, playerID int) ([]*GoodsFactory, error)

	// FindByPlayer retrieves all factories for a player (any status)
	FindByPlayer(ctx context.Context, playerID int) ([]*GoodsFactory, error)

	// Delete removes a goods factory
	Delete(ctx context.Context, id string, playerID int) error
}
