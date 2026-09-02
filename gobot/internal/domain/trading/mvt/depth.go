// Package mvt holds the pure decision logic of the marginal-value-theorem trade loop:
// system ranking, the departure rule, fleet statistics, and specialist-pool sizing.
// Nothing here performs I/O; adapters feed it rows and the tour handler acts on its answers.
package mvt

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// LaneDepth is one market-good row of a system with the absorption ledger's occupancy on
// both sides. BuyPlanned/BuyResidual sit on the source (a hull buying there);
// SellPlanned/SellResidual on the sink (a hull selling there).
type LaneDepth struct {
	Listing      trading.GoodListing
	BuyPlanned   int
	BuyResidual  float64
	SellPlanned  int
	SellResidual float64
}

// SystemDepthReader returns, for each requested system, every priced market-good row
// joined with the ledger's outstanding occupancy. A system with no rows maps to nil.
type SystemDepthReader interface {
	SystemDepths(ctx context.Context, playerID int, systems []string) (map[string][]LaneDepth, error)
}
