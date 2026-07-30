package parkedsensing_test

// The proof that a coverage probe names its own engine in the ledger.
//
// THE DEFECT (sp-com1h). Two engines buy probes — the frontier expansion engine,
// and this coordinator buying coverage for markets it already watches — and both
// booked their purchases under operation_type='fleet expansion'. When the operator
// switched expansion off, expansion correctly stopped asking for hulls while this
// queue kept buying; the ledger reported "fleet expansion" spending money against a
// switch that read off, and the only available conclusion was that the switch was
// broken. It was not. The label was: two spenders, one name, no way to attribute
// 907,545 credits to the engine that actually spent them.
//
// WHY THIS IS TESTED AT THE ADAPTER AND NOT ONLY AT THE RECORDER. The recorder's
// fallback is "fleet expansion", so an adapter that simply FORGETS to name itself
// produces a perfectly valid row under the old, ambiguous label — the failure is
// silent and looks exactly like correct behaviour. The assertable fact has to be
// that this adapter sets the field, on the command it really dispatches.

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// opTypeMediator captures the purchase command the adapter dispatches. Embeds
// common.Mediator so any other request nil-panics, keeping the fake honest about
// what the adapter actually sends.
type opTypeMediator struct {
	common.Mediator
	sent *shipyardCmd.PurchaseShipCommand
}

func (m *opTypeMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*shipyardCmd.PurchaseShipCommand)
	if !ok {
		return nil, fmt.Errorf("opTypeMediator: unexpected request %T", request)
	}
	m.sent = cmd
	return &shipyardCmd.PurchaseShipResponse{
		Ship:          boughtProbeShip(t0Symbol, buyYard),
		PurchasePrice: 50_000,
		ShipType:      "SHIP_PROBE",
		AgentCredits:  900_000,
	}, nil
}

// THE REGRESSION PROOF. Drop the OperationType line from ProbePurchasePort.Buy and
// this fails: the command goes out unnamed, the recorder defaults it to "fleet
// expansion", and coverage spend becomes indistinguishable from expansion spend
// again — the exact state that made a live money leak unreadable.
func TestProbeBuy_BooksCoverageSpendUnderItsOwnOperationType(t *testing.T) {
	db := newShipPortsDB(t)

	// The driving coordinator's row: the claim's foreign-key parent, without which
	// Buy fails closed before ever dispatching the purchase.
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID:            sensingBuyContainerID,
		PlayerID:      testPlayerID,
		ContainerType: "coordinator",
		CommandType:   "probe_sensing_coordinator",
		Status:        "RUNNING",
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:     buyerHull,
		PlayerID:       testPlayerID,
		NavStatus:      string(navigation.NavStatusDocked),
		LocationSymbol: buyYard,
		Role:           "SATELLITE",
		DedicatedFleet: appSensing.SensingParkedFleetTag,
	}).Error)

	med := &opTypeMediator{}
	shipRepo := api.NewShipRepository(nil, nil, nil, nil, db, shared.NewRealClock())
	port := adapterSensing.NewProbePurchasePort(med, shipRepo, nil)

	_, err := port.Buy(context.Background(), testPlayerID, buyerHull, buyYard, sensingBuyContainerID)
	require.NoError(t, err)

	require.NotNil(t, med.sent, "the adapter never dispatched a purchase")
	require.Equal(t, appSensing.SensingCoverageOperationType, med.sent.OperationType,
		"a coverage probe must name its own engine in the ledger; left unset it books as %q alongside the "+
			"frontier engine's purchases, and an operator cannot tell which engine spent the money (sp-com1h)",
		shipyardCmd.OperationTypeFleetExpansion)

	// And the two labels are genuinely distinct — a constant that drifted into
	// equality would satisfy the assertion above while attributing nothing.
	require.NotEqual(t, shipyardCmd.OperationTypeFleetExpansion, appSensing.SensingCoverageOperationType,
		"coverage spend and expansion spend must not share an operation_type: one label for two spenders is "+
			"the whole defect")
}
