package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/assignment"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// These tests pin the atomic buy+dedicate for a manual shipyard purchase
// (sp-0ms61). A `spacetraders shipyard purchase` lands a hull UNDEDICATED
// (dedicated_fleet=''), and every automated dedication path treats an empty tag
// as "provisioning pool — fair game": isReclaimable requires
// DedicatedFleet()=="" (contract_scaler_ports.go:731) and the depot launcher
// only crews an undedicated hull (container_ops_depot_launch.go). So in the
// window between an operator's manual purchase and a later manual `fleet assign`,
// a coordinator tick can legitimately poach the hull. The optional --fleet role
// closes that window: each purchased hull is dedicated to the operator-named
// fleet in the SAME daemon operation as the buy, through the SINGLE sanctioned
// dedication write (AssignShipFleetCommand -> shipRepo.AssignFleet, RULINGS
// #2/#3) — no new/parallel dedication path.

const (
	dedicateShipType = "SHIP_HEAVY_FREIGHTER"
	dedicatePinYard  = "X1-GZ7-C37"
	dedicateHullCap  = 225 // a heavy freighter hauls; cargo-capable (isReclaimable also gates on cargo>0)
	dedicatePrice    = 2000000
)

// inMemShipRepo is a minimal in-memory ShipRepository holding the freshly-bought
// hulls, so the dedication write (AssignFleet) is REAL and re-reading a hull
// shows its persisted tag — the observable outcome a coordinator would see.
// Embeds the interface so only the two methods AssignShipFleetHandler touches
// need bodies; any other call nil-panics, keeping the fake honest.
type inMemShipRepo struct {
	navigation.ShipRepository
	ships map[string]*navigation.Ship
}

func (r *inMemShipRepo) FindBySymbol(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	return r.ships[symbol], nil
}

func (r *inMemShipRepo) AssignFleet(_ context.Context, symbol, fleet string, _ shared.PlayerID) error {
	ship, ok := r.ships[symbol]
	if !ok {
		return fmt.Errorf("ship %s not found", symbol)
	}
	ship.SetDedicatedFleet(fleet)
	return nil
}

// dedicateRoutingMediator routes the two commands executePurchaseLoop dispatches:
// PurchaseShipCommand hands out the next pre-seeded hull (modeling a buy that
// persisted it); AssignShipFleetCommand is routed to a REAL AssignShipFleetHandler
// over the in-memory repo, so the dedication actually mutates hull state (no
// mock-only theater). assignFail models a dedication write failure. It embeds
// common.Mediator so any other request nil-panics.
type dedicateRoutingMediator struct {
	common.Mediator

	repo          *inMemShipRepo
	assignHandler *assignment.AssignShipFleetHandler
	queue         []*navigation.Ship
	idx           int
	price         int
	credits       int
	assignFail    error

	assignSends int
	lastAssign  *assignment.AssignShipFleetCommand
}

func (m *dedicateRoutingMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *PurchaseShipCommand:
		ship := m.queue[m.idx]
		m.idx++
		return &PurchaseShipResponse{
			Ship:          ship,
			PurchasePrice: m.price,
			AgentCredits:  m.credits,
			ShipType:      dedicateShipType,
		}, nil
	case *assignment.AssignShipFleetCommand:
		m.assignSends++
		m.lastAssign = cmd
		if m.assignFail != nil {
			return nil, m.assignFail
		}
		return m.assignHandler.Handle(ctx, cmd)
	default:
		return nil, nil
	}
}

// newBoughtHull builds an IDLE, docked, cargo-capable, UNDEDICATED hull — the
// exact state a freshly-purchased heavy freighter lands in and the state
// isReclaimable would treat as fair game (idle + cargo>0 + undedicated).
func newBoughtHull(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(dedicateHullCap, 0, nil)
	if err != nil {
		t.Fatalf("build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("build fuel: %v", err)
	}
	waypoint, err := shared.NewWaypoint(dedicatePinYard, 0, 0)
	if err != nil {
		t.Fatalf("build waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(4), waypoint, fuel, 100,
		dedicateHullCap, cargo, 30, "FRAME_HEAVY_FREIGHTER", "HAULER", nil,
		navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("build ship: %v", err)
	}
	return ship
}

func newDedicateRoutingMediator(repo *inMemShipRepo, queue []*navigation.Ship) *dedicateRoutingMediator {
	return &dedicateRoutingMediator{
		repo:          repo,
		assignHandler: assignment.NewAssignShipFleetHandler(repo, nil),
		queue:         queue,
		price:         dedicatePrice,
		credits:       100000000,
	}
}

// With --fleet set, every purchased hull ends dedicated to the operator-named
// fleet through the single sanctioned write, so no reclaim window is ever
// observable: isReclaimable (contract_scaler_ports.go:731) and the depot
// launcher (container_ops_depot_launch.go) both treat ONLY an undedicated hull
// (DedicatedFleet()=="") as fair game, and a non-empty tag is never-poach
// (RULINGS #7). Driven through the command's Handle entry point (auto-discover
// path: no shipyard/agent reads needed).
func TestBatchPurchase_WithFleet_DedicatesEachHullAndClosesReclaimWindow(t *testing.T) {
	hullA := newBoughtHull(t, "TORWIND-55")
	hullB := newBoughtHull(t, "TORWIND-56")
	repo := &inMemShipRepo{ships: map[string]*navigation.Ship{"TORWIND-55": hullA, "TORWIND-56": hullB}}
	med := newDedicateRoutingMediator(repo, []*navigation.Ship{hullA, hullB})
	handler := &BatchPurchaseShipsHandler{mediator: med}

	cmd := &BatchPurchaseShipsCommand{
		PurchasingShipSymbol: "TORWIND-1",
		ShipType:             dedicateShipType,
		Quantity:             2,
		MaxBudget:            0,
		PlayerID:             shared.MustNewPlayerID(4),
		ShipyardWaypoint:     "", // auto-discover: Handle skips the shipyard/agent reads
		DedicateFleet:        "trade",
	}

	ctx := common.WithPlayerToken(context.Background(), "test-token")
	resp, err := handler.Handle(ctx, cmd)
	if err != nil {
		t.Fatalf("expected atomic purchase+dedicate to succeed, got: %v", err)
	}
	batch, ok := resp.(*BatchPurchaseShipsResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if batch.ShipsPurchasedCount != 2 {
		t.Fatalf("expected 2 hulls purchased, got %d", batch.ShipsPurchasedCount)
	}

	// reclaimWindowOpen mirrors the ONLY hull-state clause isReclaimable and the
	// depot launcher use to treat a hull as poachable: an EMPTY dedicated tag.
	reclaimWindowOpen := func(s *navigation.Ship) bool { return s.DedicatedFleet() == "" }
	for _, symbol := range []string{"TORWIND-55", "TORWIND-56"} {
		got, _ := repo.FindBySymbol(ctx, symbol, shared.MustNewPlayerID(4))
		if reclaimWindowOpen(got) {
			t.Fatalf("hull %s left UNDEDICATED — a reclaimer/idle-reuse tick could poach it (the operator-intent-drift gap sp-0ms61 closes)", symbol)
		}
		if got.DedicatedFleet() != "trade" {
			t.Fatalf("hull %s must be dedicated to the operator-named fleet 'trade', got %q", symbol, got.DedicatedFleet())
		}
	}
	if med.assignSends != 2 {
		t.Fatalf("expected exactly one sanctioned dedication write per hull (2), got %d", med.assignSends)
	}
	// The dedication carries operator authority + a named assigner, exactly like
	// a manual `fleet assign` (Manual=true lets the captain pin any named fleet).
	if med.lastAssign == nil || !med.lastAssign.Manual {
		t.Fatalf("manual purchase dedication must set Manual=true (operator authority), got %+v", med.lastAssign)
	}
	if med.lastAssign.Fleet != "trade" {
		t.Fatalf("dedication must target the operator-named fleet 'trade', got %q", med.lastAssign.Fleet)
	}
}

// A failed dedication write must surface LOUDLY: a bought-but-undedicated hull
// is the exact reclaimable orphan this bead prevents, so the loop aborts naming
// the hull and stops — it must never keep buying more undedicated hulls.
func TestBatchPurchase_WithFleet_DedicationWriteFailure_AbortsLoudlyWithoutOrphaning(t *testing.T) {
	hullA := newBoughtHull(t, "TORWIND-58")
	hullB := newBoughtHull(t, "TORWIND-59")
	repo := &inMemShipRepo{ships: map[string]*navigation.Ship{"TORWIND-58": hullA, "TORWIND-59": hullB}}
	med := newDedicateRoutingMediator(repo, []*navigation.Ship{hullA, hullB})
	med.assignFail = fmt.Errorf("db write conflict")
	handler := &BatchPurchaseShipsHandler{mediator: med}

	cmd := &BatchPurchaseShipsCommand{
		PurchasingShipSymbol: "TORWIND-1",
		ShipType:             dedicateShipType,
		Quantity:             2,
		PlayerID:             shared.MustNewPlayerID(4),
		ShipyardWaypoint:     dedicatePinYard,
		DedicateFleet:        "trade",
	}

	ships, totalSpent, err := handler.executePurchaseLoop(context.Background(), cmd, cmd.Quantity, dedicatePinYard, dedicatePrice)
	if err == nil {
		t.Fatalf("a failed dedication must abort loudly — a bought-but-undedicated hull is the orphan sp-0ms61 prevents")
	}
	if !strings.Contains(err.Error(), "TORWIND-58") || !strings.Contains(err.Error(), "dedicate") {
		t.Fatalf("the abort error must name the orphaned hull and the failed dedication, got: %v", err)
	}
	if len(ships) != 0 || totalSpent != 0 {
		t.Fatalf("a dedication failure is a hard abort (zero reported), got %d ships / %d spent", len(ships), totalSpent)
	}
	if med.assignSends != 1 {
		t.Fatalf("expected the loop to abort after the FIRST failed dedication (1 attempt), got %d — it kept buying undedicated hulls", med.assignSends)
	}
}

// No --fleet (or a whitespace-only value) is treated as omitted: no hull is ever
// dedicated and the full requested quantity is purchased exactly as today
// (byte-identical back-compat). Input variations of the same "omitted" behavior
// are parametrized (one behavior, not three tests).
func TestBatchPurchase_NoFleetOrWhitespace_NeverDedicates_ByteIdentical(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fleet string
	}{
		{"omitted", ""},
		{"spaces", "   "},
		{"tab", "\t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hullA := newBoughtHull(t, "TORWIND-70")
			hullB := newBoughtHull(t, "TORWIND-71")
			repo := &inMemShipRepo{ships: map[string]*navigation.Ship{"TORWIND-70": hullA, "TORWIND-71": hullB}}
			med := newDedicateRoutingMediator(repo, []*navigation.Ship{hullA, hullB})
			handler := &BatchPurchaseShipsHandler{mediator: med}

			cmd := &BatchPurchaseShipsCommand{
				PurchasingShipSymbol: "TORWIND-1",
				ShipType:             dedicateShipType,
				Quantity:             2,
				PlayerID:             shared.MustNewPlayerID(4),
				ShipyardWaypoint:     dedicatePinYard,
				DedicateFleet:        tc.fleet,
			}

			ships, totalSpent, err := handler.executePurchaseLoop(context.Background(), cmd, cmd.Quantity, dedicatePinYard, dedicatePrice)
			if err != nil {
				t.Fatalf("a plain purchase (no --fleet) must succeed, got: %v", err)
			}
			if len(ships) != 2 || totalSpent != 2*dedicatePrice {
				t.Fatalf("expected the full quantity purchased unchanged, got %d ships / %d spent", len(ships), totalSpent)
			}
			if med.assignSends != 0 {
				t.Fatalf("no --fleet (or whitespace-only) must NEVER dedicate — byte-identical to today, got %d dedication writes", med.assignSends)
			}
			for _, symbol := range []string{"TORWIND-70", "TORWIND-71"} {
				got, _ := repo.FindBySymbol(context.Background(), symbol, shared.MustNewPlayerID(4))
				if got.DedicatedFleet() != "" {
					t.Fatalf("hull %s must remain undedicated without --fleet, got %q", symbol, got.DedicatedFleet())
				}
			}
		})
	}
}
