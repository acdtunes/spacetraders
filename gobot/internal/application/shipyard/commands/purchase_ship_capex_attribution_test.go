package commands

import (
	"context"
	"errors"
	"reflect"
	"testing"

	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// capturingMediator records the RecordTransactionCommand the purchase path
// dispatches. Embedding nothing and implementing the whole (3-method) interface
// keeps the ledger command the only observable effect under test.
type capturingMediator struct {
	recorded []*ledgerCommands.RecordTransactionCommand
	sendErr  error
}

func (m *capturingMediator) Send(_ context.Context, request mediator.Request) (mediator.Response, error) {
	if cmd, ok := request.(*ledgerCommands.RecordTransactionCommand); ok {
		m.recorded = append(m.recorded, cmd)
	}
	return nil, m.sendErr
}

func (m *capturingMediator) Register(_ reflect.Type, _ mediator.RequestHandler) error { return nil }

func (m *capturingMediator) RegisterMiddleware(_ mediator.Middleware) {}

// stubPlayerRepo supplies just the agent symbol the ledger metadata stamps.
type stubPlayerRepo struct {
	player.PlayerRepository

	agentSymbol string
	findErr     error
}

func (s *stubPlayerRepo) FindByID(_ context.Context, _ shared.PlayerID) (*player.Player, error) {
	if s.findErr != nil {
		return nil, s.findErr
	}
	return &player.Player{AgentSymbol: s.agentSymbol}, nil
}

// purchaseResultFixture mirrors the shape a real ship purchase returns: the
// created ship carries the hull symbol the server minted, while the shipyard
// transaction's own shipSymbol field carries the API's deprecated TYPE alias.
// The two MUST differ for this fixture to discriminate between them.
func purchaseResultFixture(hull, shipType string, price int, timestamp string) *domainPorts.ShipPurchaseResult {
	return &domainPorts.ShipPurchaseResult{
		Agent: &player.AgentData{Credits: 4_000_000},
		Ship:  &navigation.ShipData{Symbol: hull},
		Transaction: &domainPorts.ShipPurchaseTransaction{
			WaypointSymbol: "X1-J58-A1",
			ShipSymbol:     shipType, // the API's deprecated type alias, NOT the hull
			ShipType:       shipType,
			Price:          price,
			AgentSymbol:    "TORWIND",
			Timestamp:      timestamp,
		},
	}
}

func recordPurchase(t *testing.T, result *domainPorts.ShipPurchaseResult) *ledgerCommands.RecordTransactionCommand {
	t.Helper()

	med := &capturingMediator{}
	handler := &PurchaseShipHandler{
		playerRepo: &stubPlayerRepo{agentSymbol: "TORWIND"},
		mediator:   med,
	}
	cmd := &PurchaseShipCommand{
		PurchasingShipSymbol: "TORWIND-1",
		ShipType:             result.Transaction.ShipType,
		PlayerID:             shared.MustNewPlayerID(1),
	}

	handler.recordShipPurchaseTransaction(context.Background(), cmd, "X1-J58-A1", result, 5_900_000)

	if len(med.recorded) != 1 {
		t.Fatalf("expected exactly one ledger recording, got %d", len(med.recorded))
	}
	return med.recorded[0]
}

// The whole point of sp-mjmqf: metadata->>'ship_symbol' on a PURCHASE_SHIP row
// must be the hull the purchase produced, so its capex can be joined per hull.
// Reading the shipyard transaction's shipSymbol field instead stamps the ship
// TYPE ("SHIP_HEAVY_FREIGHTER") and leaves the row unattributable forever.
func TestRecordShipPurchase_StampsRealHullSymbolNotShipType(t *testing.T) {
	rec := recordPurchase(t, purchaseResultFixture(
		"TORWIND-1F", "SHIP_HEAVY_FREIGHTER", 1_900_000, "2026-07-30T11:22:33.000Z",
	))

	gotSymbol, _ := rec.Metadata["ship_symbol"].(string)
	if gotSymbol != "TORWIND-1F" {
		t.Fatalf("metadata ship_symbol: want the real hull %q, got %q", "TORWIND-1F", gotSymbol)
	}
	if gotSymbol == "SHIP_HEAVY_FREIGHTER" {
		t.Fatal("metadata ship_symbol holds the ship TYPE — capex cannot be joined to a hull")
	}

	// The type keeps its own key: probe-buy cooldown and spend guards read
	// ship_type to recognise a SHIP_PROBE purchase, so it must survive intact.
	if gotType, _ := rec.Metadata["ship_type"].(string); gotType != "SHIP_HEAVY_FREIGHTER" {
		t.Fatalf("metadata ship_type: want %q, got %q", "SHIP_HEAVY_FREIGHTER", gotType)
	}
}

// A light hull is purchased as SHIP_LIGHT_HAULER while its frame is
// FRAME_LIGHT_FREIGHTER, so the symbol can never be derived from either the
// type or the frame — only the created ship in the response names it.
func TestRecordShipPurchase_StampsRealHullSymbolForLightHauler(t *testing.T) {
	rec := recordPurchase(t, purchaseResultFixture(
		"TORWIND-2A", "SHIP_LIGHT_HAULER", 720_000, "2026-07-30T11:22:33Z",
	))

	if gotSymbol, _ := rec.Metadata["ship_symbol"].(string); gotSymbol != "TORWIND-2A" {
		t.Fatalf("metadata ship_symbol: want the real hull %q, got %q", "TORWIND-2A", gotSymbol)
	}
	if gotType, _ := rec.Metadata["ship_type"].(string); gotType != "SHIP_LIGHT_HAULER" {
		t.Fatalf("metadata ship_type: want %q, got %q", "SHIP_LIGHT_HAULER", gotType)
	}
}

// related_entity_type/id give the join an indexed column pair instead of a JSON
// key. They were left empty on every historical row.
func TestRecordShipPurchase_LinksTransactionToShipEntity(t *testing.T) {
	rec := recordPurchase(t, purchaseResultFixture(
		"TORWIND-1F", "SHIP_HEAVY_FREIGHTER", 1_900_000, "2026-07-30T11:22:33Z",
	))

	if rec.RelatedEntityType != "ship" {
		t.Fatalf("related_entity_type: want %q, got %q", "ship", rec.RelatedEntityType)
	}
	if rec.RelatedEntityID != "TORWIND-1F" {
		t.Fatalf("related_entity_id: want the hull %q, got %q", "TORWIND-1F", rec.RelatedEntityID)
	}
}

// transaction_id duplicated the ship type on every historical row. It carries
// the hull now, matching what its own comment always claimed.
func TestRecordShipPurchase_TransactionIDReferencesTheHull(t *testing.T) {
	rec := recordPurchase(t, purchaseResultFixture(
		"TORWIND-1F", "SHIP_HEAVY_FREIGHTER", 1_900_000, "2026-07-30T11:22:33Z",
	))

	if got, _ := rec.Metadata["transaction_id"].(string); got != "TORWIND-1F" {
		t.Fatalf("metadata transaction_id: want the hull %q, got %q", "TORWIND-1F", got)
	}
}

// Hull age must be readable from the purchase receipt rather than inferred from
// the hull's first trade, which understates it by the whole
// purchase-to-first-tour gap. The stamped value is canonical RFC3339 UTC so a
// consumer can cast it without a guard.
func TestRecordShipPurchase_StampsServerPurchaseTimestamp(t *testing.T) {
	rec := recordPurchase(t, purchaseResultFixture(
		"TORWIND-1F", "SHIP_HEAVY_FREIGHTER", 1_900_000, "2026-07-30T11:22:33.456Z",
	))

	got, ok := rec.Metadata["purchased_at"].(string)
	if !ok {
		t.Fatal("metadata purchased_at absent — hull age still has to be inferred from first trade")
	}
	if got != "2026-07-30T11:22:33Z" {
		t.Fatalf("metadata purchased_at: want canonical RFC3339 UTC %q, got %q", "2026-07-30T11:22:33Z", got)
	}
}

// An unparseable API timestamp omits the key rather than stamping a value a
// timestamptz cast would choke on.
func TestRecordShipPurchase_OmitsUnparseablePurchaseTimestamp(t *testing.T) {
	for _, raw := range []string{"", "not-a-timestamp", "2026-07-30 11:22:33"} {
		rec := recordPurchase(t, purchaseResultFixture(
			"TORWIND-1F", "SHIP_HEAVY_FREIGHTER", 1_900_000, raw,
		))

		if v, present := rec.Metadata["purchased_at"]; present {
			t.Fatalf("timestamp %q: expected purchased_at omitted, got %v", raw, v)
		}
	}
}

// If the response carried no created ship the row stays unattributed — never
// re-stamped with the type, and never given a dangling "ship" link with no id.
// Such a hull falls back to the frame-class average on the payback panels.
func TestRecordShipPurchase_LeavesRowUnattributedWhenNoShipReturned(t *testing.T) {
	result := purchaseResultFixture("TORWIND-1F", "SHIP_HEAVY_FREIGHTER", 1_900_000, "2026-07-30T11:22:33Z")
	result.Ship = nil

	rec := recordPurchase(t, result)

	if got, _ := rec.Metadata["ship_symbol"].(string); got != "" {
		t.Fatalf("metadata ship_symbol: want empty for an unattributable row, got %q", got)
	}
	if rec.RelatedEntityType != "" || rec.RelatedEntityID != "" {
		t.Fatalf("want no entity link at all, got type=%q id=%q", rec.RelatedEntityType, rec.RelatedEntityID)
	}
}

// A ledger failure must not fail an already-completed purchase (the credits are
// spent and the hull exists); the helper logs and returns.
func TestRecordShipPurchase_LedgerFailureIsSwallowed(t *testing.T) {
	med := &capturingMediator{sendErr: errors.New("db connection reset")}
	handler := &PurchaseShipHandler{
		playerRepo: &stubPlayerRepo{agentSymbol: "TORWIND"},
		mediator:   med,
	}
	cmd := &PurchaseShipCommand{
		PurchasingShipSymbol: "TORWIND-1",
		ShipType:             "SHIP_HEAVY_FREIGHTER",
		PlayerID:             shared.MustNewPlayerID(1),
	}

	handler.recordShipPurchaseTransaction(context.Background(), cmd,
		"X1-J58-A1",
		purchaseResultFixture("TORWIND-1F", "SHIP_HEAVY_FREIGHTER", 1_900_000, "2026-07-30T11:22:33Z"),
		5_900_000)

	if len(med.recorded) != 1 {
		t.Fatalf("expected the recording attempt to still be made, got %d", len(med.recorded))
	}
}
