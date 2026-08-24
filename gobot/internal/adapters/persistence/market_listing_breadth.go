package persistence

import (
	"context"

	"gorm.io/gorm"
)

// MarketListingBreadthGORM answers how many goods one market lists, sharing the sink prior's one
// grouped read. Player-scoped: a breadth borrowed across players would discount an unseen source.
type MarketListingBreadthGORM struct {
	db       *gorm.DB
	playerID int
}

// NewMarketListingBreadth builds the breadth reader for one player's market cache.
func NewMarketListingBreadth(db *gorm.DB, playerID int) *MarketListingBreadthGORM {
	return &MarketListingBreadthGORM{db: db, playerID: playerID}
}

// ListingBreadth reports how many goods the waypoint's cached market trades. An uncached market
// and an unreadable cache both report (0, false), which the caller keeps at the uniform prior.
func (r *MarketListingBreadthGORM) ListingBreadth(ctx context.Context, waypoint string) (int, bool) {
	if r == nil || r.db == nil || waypoint == "" {
		return 0, false
	}
	breadth := marketListingBreadth(r.db.WithContext(ctx), r.playerID, []string{waypoint})
	listings, ok := breadth[waypoint]
	if !ok || listings <= 0 {
		return 0, false
	}
	return listings, true
}
