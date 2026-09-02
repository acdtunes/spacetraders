package commands

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func TestBuildTourLaunchSpec_CarriesFleetTag(t *testing.T) {
	cmd := &RunTradeFleetCoordinatorCommand{MaxHops: 3, MaxSpend: 10, MinMargin: 1, ReplanLimit: 2, AgentSymbol: "A", PlayerID: shared.MustNewPlayerID(7)}
	spec := buildTourLaunchSpec(cmd, "T-1", "trade-mvt", true, 500)
	if spec.Fleet != "trade-mvt" || spec.ShipSymbol != "T-1" || !spec.RepositionReachEscalated || spec.WorkingCapitalReserve != 500 {
		t.Fatalf("spec = %+v", spec)
	}
}
