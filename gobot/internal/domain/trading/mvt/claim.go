package mvt

import (
	"context"
	"time"
)

// Claim is a hull's durable statement of which system it is working or travelling to.
// ArrivedAt nil means in transit. One row per hull; a penalty for the ranker, never a lock.
type Claim struct {
	Hull      string
	System    string
	ClaimedAt time.Time
	ArrivedAt *time.Time
}

// ClaimRegistry is the durable claim table. Upsert resets ArrivedAt to nil.
type ClaimRegistry interface {
	Upsert(ctx context.Context, playerID int, hull, system string, at time.Time) error
	MarkArrived(ctx context.Context, playerID int, hull string, at time.Time) error
	Release(ctx context.Context, playerID int, hull string) error
	Get(ctx context.Context, playerID int, hull string) (Claim, bool, error)
	// InTransit counts unarrived claims per system for the open era.
	InTransit(ctx context.Context, playerID int) (map[string]int, error)
}
