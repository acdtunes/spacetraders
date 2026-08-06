package tactics

// Attribution tests for the REFUEL ledger row: which HULL it names, and which
// OPERATION it books under.
//
// Both facts are recorded by the same async recorder, so they are covered
// together — a refuel row that names neither is unattributable twice over.

import (
	"context"
	"reflect"
	"testing"
	"time"

	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// payingRefuelRepo refuels successfully at a real cost, which is what makes the
// ledger recorder run at all (a zero-cost refuel is skipped before it records).
type payingRefuelRepo struct {
	domainNavigation.ShipRepository // embedded: any unused method panics if hit

	// hull is served to the symbol-only caller shape, which loads by lookup.
	hull *domainNavigation.Ship
}

func (r *payingRefuelRepo) Dock(_ context.Context, _ *domainNavigation.Ship, _ shared.PlayerID) error {
	return nil
}

func (r *payingRefuelRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.Ship, error) {
	return r.hull, nil
}

func (r *payingRefuelRepo) Refuel(_ context.Context, ship *domainNavigation.Ship, _ shared.PlayerID, _ *int) (*domainNavigation.RefuelResult, error) {
	_, _ = ship.RefuelToFull()
	return &domainNavigation.RefuelResult{
		CreditsCost:  144,
		FuelCurrent:  ship.Fuel().Current,
		FuelCapacity: ship.Fuel().Capacity,
	}, nil
}

func (r *payingRefuelRepo) Save(_ context.Context, _ *domainNavigation.Ship) error { return nil }

// stubPlayerRepo answers the recorder's agent-symbol lookup.
type stubPlayerRepo struct {
	player.PlayerRepository // embedded: any unused method panics if hit
}

func (s *stubPlayerRepo) FindByID(_ context.Context, id shared.PlayerID) (*player.Player, error) {
	return &player.Player{ID: id, AgentSymbol: "ENDURANCE"}, nil
}

// capturingMediator captures the ledger record command the refuel handler emits.
// The recorder runs on its own goroutine, so the capture is a channel rather
// than a field: the test must wait for the row, not race it.
type capturingMediator struct {
	recorded chan *ledgerCommands.RecordTransactionCommand
}

func newCapturingMediator() *capturingMediator {
	return &capturingMediator{recorded: make(chan *ledgerCommands.RecordTransactionCommand, 4)}
}

func (m *capturingMediator) Send(_ context.Context, request mediator.Request) (mediator.Response, error) {
	if rec, ok := request.(*ledgerCommands.RecordTransactionCommand); ok {
		m.recorded <- rec
	}
	return nil, nil
}

func (m *capturingMediator) Register(_ reflect.Type, _ mediator.RequestHandler) error { return nil }
func (m *capturingMediator) RegisterMiddleware(_ mediator.Middleware)                 {}

// awaitRecord waits for the async ledger recorder to emit its row.
func (m *capturingMediator) awaitRecord(t *testing.T) *ledgerCommands.RecordTransactionCommand {
	t.Helper()
	select {
	case rec := <-m.recorded:
		return rec
	case <-time.After(5 * time.Second):
		t.Fatalf("no ledger row was recorded for the refuel")
		return nil
	}
}

// refuelUnderContext drives the handler exactly as the given caller shape would.
// hull is what a symbol-only command resolves to; the route-executor shape
// carries its own and never looks one up.
func refuelUnderContext(t *testing.T, ctx context.Context, cmd *types.RefuelShipCommand, hull *domainNavigation.Ship) *ledgerCommands.RecordTransactionCommand {
	t.Helper()
	med := newCapturingMediator()
	handler := NewRefuelShipHandler(&payingRefuelRepo{hull: hull}, &stubPlayerRepo{}, nil, med)

	if _, err := handler.Handle(ctx, cmd); err != nil {
		t.Fatalf("refuel failed: %v", err)
	}
	return med.awaitRecord(t)
}

// TestRefuelLedger_RouteExecutorShapedCommand_NamesTheHull drives the command in
// the shape the route executor builds it — the loaded hull in Ship, ShipSymbol
// left empty — which is the shape behind almost every refuel the fleet flies.
// The recorded row must still name the hull.
func TestRefuelLedger_RouteExecutorShapedCommand_NamesTheHull(t *testing.T) {
	ship := newShipAtFuelStation(t, domainNavigation.NavStatusDocked)

	rec := refuelUnderContext(t, context.Background(), &types.RefuelShipCommand{
		Ship:     ship, // route-executor shape: hull already loaded
		PlayerID: shared.MustNewPlayerID(1),
	}, nil)

	if got := rec.Metadata["ship_symbol"]; got != "SHIP-1" {
		t.Errorf("metadata.ship_symbol = %q, want %q — the row cannot be attributed to a hull", got, "SHIP-1")
	}
	if want := "Refueled ship SHIP-1"; rec.Description != want {
		t.Errorf("description = %q, want %q", rec.Description, want)
	}
}

// TestRefuelLedger_SymbolOnlyCommand_NamesTheHull pins the other caller shape —
// the CLI/RPC verb, which names the hull by symbol and leaves Ship nil — so the
// resolution cannot be fixed for one caller by breaking the other.
func TestRefuelLedger_SymbolOnlyCommand_NamesTheHull(t *testing.T) {
	ship := newShipAtFuelStation(t, domainNavigation.NavStatusDocked)

	rec := refuelUnderContext(t, context.Background(), &types.RefuelShipCommand{
		ShipSymbol: "SHIP-1", // CLI/RPC shape: symbol only
		PlayerID:   shared.MustNewPlayerID(1),
	}, ship)

	if got := rec.Metadata["ship_symbol"]; got != "SHIP-1" {
		t.Errorf("metadata.ship_symbol = %q, want %q", got, "SHIP-1")
	}
}

// TestRefuelLedger_InheritsEnclosingOperation proves a refuel flown inside an
// operation's route books under that operation. Fuel is a continuous cost of
// whatever drove the leg, so leaving it in the else branch understates that
// operation's true cost.
func TestRefuelLedger_InheritsEnclosingOperation(t *testing.T) {
	ship := newShipAtFuelStation(t, domainNavigation.NavStatusDocked)

	ctx := shared.WithOperationContext(context.Background(),
		shared.NewOperationContext("sensing-abc123", "sensing coverage"))

	rec := refuelUnderContext(t, ctx, &types.RefuelShipCommand{
		Ship:     ship,
		PlayerID: shared.MustNewPlayerID(1),
	}, nil)

	if rec.OperationType != "sensing coverage" {
		t.Errorf("operation_type = %q, want %q", rec.OperationType, "sensing coverage")
	}
	if rec.RelatedEntityType != "container" || rec.RelatedEntityID != "sensing-abc123" {
		t.Errorf("related entity = (%q,%q), want (container,sensing-abc123)",
			rec.RelatedEntityType, rec.RelatedEntityID)
	}
}

// TestRefuelLedger_OperatorInitiatedStaysManual is the other half of the
// attribution contract. 'manual' is the else branch, and an operator-initiated
// refuel with no enclosing operation is the one case that genuinely belongs
// there — labelling it as automated would be the same defect pointing the other
// way.
func TestRefuelLedger_OperatorInitiatedStaysManual(t *testing.T) {
	ship := newShipAtFuelStation(t, domainNavigation.NavStatusDocked)

	rec := refuelUnderContext(t, context.Background(), &types.RefuelShipCommand{
		Ship:     ship,
		PlayerID: shared.MustNewPlayerID(1),
	}, nil)

	if rec.OperationType != "manual" {
		t.Errorf("operation_type = %q, want %q for an operator-initiated refuel", rec.OperationType, "manual")
	}
	if rec.RelatedEntityID != "" {
		t.Errorf("related_entity_id = %q, want empty — no operation drove this refuel", rec.RelatedEntityID)
	}
}
