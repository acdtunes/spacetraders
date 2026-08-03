package commands

// The ledger label a ship purchase is booked under.
//
// Labelling EVERY ship purchase "fleet expansion" miscounts the parked-sensing
// coordinator's coverage probes — which are not expansion at all, they are eyes on
// markets the fleet has already judged worth watching. With one label over two
// spenders, an operator who switched expansion off and then checked the ledger saw
// "fleet expansion" still spending and could only conclude the switch was broken.
// It was not; the label was. 907,545 credits went out unattributable (sp-com1h).
//
// The default is unchanged and deliberately so: a caller that names nothing IS
// growing the fleet, and every one of them (bootstrap, the autosizer, the frontier
// expansion engine, an operator's manual buy) keeps booking exactly as before.

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// recordingMediator captures the ledger command the purchase recorder sends.
// Embeds common.Mediator so any other request nil-panics.
type recordingMediator struct {
	common.Mediator
	recorded *ledgerCommands.RecordTransactionCommand
}

func (m *recordingMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	if cmd, ok := request.(*ledgerCommands.RecordTransactionCommand); ok {
		m.recorded = cmd
		return &ledgerCommands.RecordTransactionResponse{}, nil
	}
	return nil, errors.New("recordingMediator: unexpected request")
}

// unknownPlayerRepo makes the agent-symbol lookup fail, which the recorder already
// tolerates ("UNKNOWN"). Nothing about operation_type depends on it, and stubbing it
// out keeps the fixture to the one axis under test.
type unknownPlayerRepo struct{ player.PlayerRepository }

func (unknownPlayerRepo) FindByID(context.Context, shared.PlayerID) (*player.Player, error) {
	return nil, errors.New("no player")
}

// bookedOperationType drives the real recorder and returns the operation_type it
// filed the row under.
func bookedOperationType(t *testing.T, requested string) string {
	t.Helper()
	med := &recordingMediator{}
	h := &PurchaseShipHandler{playerRepo: unknownPlayerRepo{}, mediator: med}

	h.recordShipPurchaseTransaction(context.Background(),
		&PurchaseShipCommand{
			PurchasingShipSymbol: "TORWIND-11",
			ShipType:             "SHIP_PROBE",
			PlayerID:             shared.MustNewPlayerID(1),
			OperationType:        requested,
		},
		"X1-KP23-C38",
		&domainPorts.ShipPurchaseResult{
			Agent:       &player.AgentData{Credits: 950_000},
			Transaction: &domainPorts.ShipPurchaseTransaction{ShipType: "SHIP_PROBE", Price: 50_000},
		},
		1_000_000,
	)

	if med.recorded == nil {
		t.Fatal("the purchase was never recorded in the ledger")
	}
	return med.recorded.OperationType
}

// A caller that names its engine is booked under that name — the row an operator can
// attribute. Revert purchase_ship.go to the "fleet expansion" literal and this fails.
func TestPurchaseShip_BooksTheCallersNamedOperationType(t *testing.T) {
	if got := bookedOperationType(t, "sensing coverage"); got != "sensing coverage" {
		t.Fatalf("booked operation_type %q, want the caller's own %q — with the engine's name dropped, "+
			"coverage spend is indistinguishable from expansion spend in the ledger and a live money leak "+
			"looks exactly like the engine that was switched off (sp-com1h)", got, "sensing coverage")
	}
}

// Every caller that names nothing stays fleet expansion, byte for byte. This is what
// keeps the change scoped to the one engine that needed telling apart.
func TestPurchaseShip_UnnamedPurchaseStaysFleetExpansion(t *testing.T) {
	if got := bookedOperationType(t, ""); got != OperationTypeFleetExpansion {
		t.Fatalf("an unnamed purchase booked as %q, want %q: bootstrap, the autosizer and the frontier "+
			"engine all rely on this default and their history must stay comparable", got, OperationTypeFleetExpansion)
	}
}

// Whitespace is treated as unset rather than written through. operation_type is a
// GROUP BY key, so a row filed under " " forms its own silent bucket that no
// operator's query would ever name — the same invisibility this change exists to end.
func TestPurchaseShip_BlankOperationTypeIsNotWrittenThrough(t *testing.T) {
	if got := bookedOperationType(t, "   "); got != OperationTypeFleetExpansion {
		t.Fatalf("booked operation_type %q for a whitespace-only request, want the %q default", got, OperationTypeFleetExpansion)
	}
}
