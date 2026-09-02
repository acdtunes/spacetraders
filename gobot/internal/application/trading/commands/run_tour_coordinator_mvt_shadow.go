package commands

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

// mvtShadow logs where the MVT ranker would send an old-path hull after a productive
// tour. Migration step 1: readers only, no behaviour change, ships ON for every trade hull.
func (h *RunTourCoordinatorHandler) mvtShadow(ctx context.Context, cmd *RunTourCoordinatorCommand) {
	if h.mvt.transitions == nil {
		return
	}
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil || ship == nil || ship.CurrentLocation() == nil {
		return
	}
	current := ship.CurrentLocation().SystemSymbol
	st := h.mvtState(cmd)
	st.mu.Lock()
	yieldHere, _ := st.yield.Estimate()
	st.mu.Unlock()
	ranked, err := h.mvtRank(ctx, cmd, ship)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", "MVT shadow: ranker unreadable", map[string]interface{}{"hull": cmd.ShipSymbol, "error": err.Error()})
		h.mvtRecord(ctx, cmd, mvt.StateTrade, mvt.StateTrade, current, yieldHere, 0, 0, mvtReasonRankerUnreadable)
		return
	}
	to := mvt.StateTrade
	alt, hasAlt := mvt.BestAlternative(ranked, current)
	if hasAlt && len(ranked) > 0 && ranked[0].System != current {
		to = mvt.StateClaim
	}
	h.mvtRecord(ctx, cmd, mvt.StateTrade, to, current, yieldHere, alt.Score, alt.TravelPerUnit, mvtReasonShadow)
}
