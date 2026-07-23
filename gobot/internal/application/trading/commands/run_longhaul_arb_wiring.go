package commands

// run_longhaul_arb_wiring.go — the composition root for the per-hull long-haul worker
// (sp-mepj). It is WIRING, not logic: it assembles the engine's already-built, unexported
// pieces (the discoverer, the reused-arb leg executor, the ship loader) behind the one
// exported seam the daemon calls, so the unexported discoverer/legExecutor types stay
// encapsulated in this package while main.go passes only concrete daemon collaborators.
// It touches none of the discovery/pricing/selection/execution algorithms.

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// longHaulMarketPort is the single read-only market capability the discovery pass needs: the
// goods universe plus the two shared global-best scanners (sp-mtvg's sink half + its symmetric
// source half). The daemon's *persistence.MarketRepositoryGORM satisfies all three, so main.go
// passes it once. A combined port keeps the builder signature honest about what it reads.
type longHaulMarketPort interface {
	goodUniverse
	longHaulSinkScanner
	longHaulSourceScanner
}

// shipRepoLoader adapts the domain ShipRepository to the worker's shipLoader port (LoadShip):
// the worker names hulls by (symbol, int playerID); FindBySymbol takes a shared.PlayerID. A
// thin, allocation-free bridge — no logic.
type shipRepoLoader struct{ repo navigation.ShipRepository }

func (l shipRepoLoader) LoadShip(ctx context.Context, shipSymbol string, playerID int) (*navigation.Ship, error) {
	return l.repo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
}

// NewLongHaulArbWorkerHandler composes the per-hull long-haul worker over the daemon's REUSED
// collaborators (design REUSE INVARIANT): the discovery pass (goods universe + the shared
// BestSinks/BestSources scanners + the shared gate graph + the ONE shared price-impact model),
// the leg executor (the reused one-shot arb handler), the ship loader, the shared travel
// machinery's reposition, and the live treasury reader. buyImpact/sellImpact are the era-3
// coefficients that drive both ranking AND optimal-volume; debt is the shared cooldown-ledger
// lookup (nil contributes zero — safe for the exotic cross-system lanes the trade fleet does
// not hammer). maxAge bounds market-data staleness; minSpread is the coarse pre-filter floor;
// marginFloor is the per-unit floor the marginal unit must still clear for the optimal tranche.
//
// v1 leaves two nil-safe ports unwired by design (both documented follow-ups): the
// routeCostEstimator (the func(gateHops) shape cannot consult a per-waypoint planner; ranking
// falls to the honest gate-hop time, and the reused arb executor's spend guards backstop the
// omitted route fuel) and the cross-worker absorption headroom (each lane's VolumeCap already
// bounds the tranche to single-visit absorption depth).
func NewLongHaulArbWorkerHandler(
	shipRepo navigation.ShipRepository,
	market longHaulMarketPort,
	gateGraph gateGraphPathfinder,
	arb *RunArbCoordinatorHandler,
	reposition hullRepositioner,
	treasury longHaulTreasuryReader,
	buyImpact float64,
	sellImpact float64,
	debt func(l trading.ArbitrageLane) float64,
	clock shared.Clock,
	maxAge time.Duration,
	minSpread int,
	marginFloor float64,
) *RunLongHaulArbHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	discoverer := &longHaulDiscoverer{
		universe:    market,
		sinks:       market,
		sources:     market,
		gateGraph:   gateGraph,
		cost:        nil, // v1: rank on honest gate-hop time; route fuel backstopped by the arb guards
		model:       laneImpactModel{buyImpact: buyImpact, sellImpact: sellImpact, debt: debt},
		clock:       clock,
		maxAge:      maxAge,
		minSpread:   minSpread,
		marginFloor: marginFloor,
	}
	return NewRunLongHaulArbHandler(
		shipRepoLoader{repo: shipRepo},
		discoverer,
		newLongHaulLegExecutor(arb),
		reposition,
		treasury,
	)
}
