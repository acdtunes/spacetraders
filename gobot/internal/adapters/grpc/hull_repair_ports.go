package grpc

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/hullrepair"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// hullPartsReader is the bisect the concrete API client offers over the domain port; test
// doubles satisfy only the port, so the richer read is taken when it is there.
type hullPartsReader interface {
	ReadShipRecord(ctx context.Context, symbol, token string) (api.ShipReadVerdict, error)
	ProbeShipParts(ctx context.Context, symbol, token string) (api.ShipPartsReading, error)
}

// hullTokens resolves the agent token a probe or a write is made with.
type hullTokens struct {
	playerRepo player.PlayerRepository
}

func (t hullTokens) token(ctx context.Context, playerID int) (string, error) {
	if t.playerRepo == nil {
		return "", errors.New("no player repository wired")
	}
	id, err := shared.NewPlayerID(playerID)
	if err != nil {
		return "", err
	}
	p, err := t.playerRepo.FindByID(ctx, id)
	if err != nil {
		return "", err
	}
	if p == nil || p.Token == "" {
		return "", fmt.Errorf("player %d has no usable token", playerID)
	}
	return p.Token, nil
}

// hullRepairProbe reads a hull's composite record and, when it refuses, its parts.
type hullRepairProbe struct {
	parts    hullPartsReader
	tokens   hullTokens
	playerID int
}

func (p *hullRepairProbe) ReadComposite(ctx context.Context, symbol string) (hullrepair.Verdict, error) {
	token, err := p.tokens.token(ctx, p.playerID)
	if err != nil {
		return hullrepair.ReadUnavailable, err
	}
	verdict, readErr := p.parts.ReadShipRecord(ctx, symbol, token)
	return hullReadVerdict(verdict), readErr
}

func (p *hullRepairProbe) ProbeSubresources(ctx context.Context, symbol string) (hullrepair.Subresources, error) {
	token, err := p.tokens.token(ctx, p.playerID)
	if err != nil {
		return hullrepair.Subresources{}, err
	}
	reading, err := p.parts.ProbeShipParts(ctx, symbol, token)
	if err != nil {
		return hullrepair.Subresources{}, err
	}
	out := hullrepair.Subresources{Answered: reading.Answered, Refused: reading.Refused}
	if reading.Nav != nil {
		out.Nav = &hullrepair.NavReading{
			WaypointSymbol: reading.Nav.WaypointSymbol,
			Status:         reading.Nav.Status,
			ArrivalAt:      reading.Nav.ArrivalAt,
		}
	}
	return out, nil
}

func hullReadVerdict(v api.ShipReadVerdict) hullrepair.Verdict {
	switch v {
	case api.ShipReadOK:
		return hullrepair.ReadOK
	case api.ShipReadServerRefused:
		return hullrepair.ReadRefusedServer
	case api.ShipReadClientRefused:
		return hullrepair.ReadRefusedClient
	default:
		return hullrepair.ReadUnavailable
	}
}

// hullRepairWriter performs the repair's game actions straight against the API.
//
// It deliberately does NOT route through the refuel command handler: that handler reasons
// off the hull's stored row — its location, and its fuel level — and the stored fuel is the
// very field that will not read. A stale full tank there would skip the write that is the
// whole repair. The spend is still booked into the ledger so the treasury guards keep
// reading a complete balance.
type hullRepairWriter struct {
	api      domainPorts.APIClient
	tokens   hullTokens
	mediator common.Mediator
	playerID int
}

func (w *hullRepairWriter) Dock(ctx context.Context, symbol string) error {
	token, err := w.tokens.token(ctx, w.playerID)
	if err != nil {
		return err
	}
	return w.api.DockShip(ctx, symbol, token)
}

func (w *hullRepairWriter) Orbit(ctx context.Context, symbol string) error {
	token, err := w.tokens.token(ctx, w.playerID)
	if err != nil {
		return err
	}
	return w.api.OrbitShip(ctx, symbol, token)
}

func (w *hullRepairWriter) Refuel(ctx context.Context, symbol string) (hullrepair.RefuelReceipt, error) {
	token, err := w.tokens.token(ctx, w.playerID)
	if err != nil {
		return hullrepair.RefuelReceipt{}, err
	}
	// nil units fills the tank: the deficit is unknown precisely because fuel is the field
	// that will not read, and a partial write may not overwrite what is corrupt.
	result, err := w.api.RefuelShip(ctx, symbol, token, nil)
	if err != nil {
		return hullrepair.RefuelReceipt{}, err
	}
	w.recordSpend(ctx, symbol, result)
	return hullrepair.RefuelReceipt{
		FuelCurrent:  result.FuelCurrent,
		FuelCapacity: result.FuelCapacity,
		CreditsCost:  result.CreditsCost,
	}, nil
}

// recordSpend books the repair's fuel purchase. An unbooked spend leaves the ledger-first
// treasury reading stale-high, which is the wrong direction for every money guard.
func (w *hullRepairWriter) recordSpend(ctx context.Context, symbol string, result *navigation.RefuelResult) {
	if w.mediator == nil || result == nil || result.CreditsCost <= 0 {
		return
	}
	cmd := &ledgerCommands.RecordTransactionCommand{
		PlayerID:             w.playerID,
		TransactionType:      "REFUEL",
		Amount:               -result.CreditsCost,
		BalanceBefore:        0,
		BalanceAfter:         -result.CreditsCost,
		AuthoritativeBalance: result.AgentCredits,
		Description:          fmt.Sprintf("Repair refuel for unreadable ship %s", symbol),
		OperationType:        "hull_repair",
		Metadata: map[string]interface{}{
			"ship_symbol": symbol,
			"fuel_added":  result.FuelAdded,
		},
	}
	if _, err := w.mediator.Send(context.Background(), cmd); err != nil {
		hullRepairLogf("WARNING [hull_repair_ledger] ship=%s: the repair refuel was not booked to the ledger: %v", symbol, err)
	}
}

// hullTreasury is the shared ledger-first treasury read with the agent token put in
// context for it. The repair sweep runs outside the mediator, so nothing else supplies the
// token the live fallback needs, and a guard that cannot read refuses to spend.
type hullTreasury struct {
	inner  *persistence.LedgerTreasury
	tokens hullTokens
}

func (t *hullTreasury) Credits(ctx context.Context, playerID int) (int64, error) {
	if token, err := t.tokens.token(ctx, playerID); err == nil {
		ctx = common.WithPlayerToken(ctx, token)
	}
	return t.inner.Credits(ctx, playerID)
}

// hullFuelMarket answers whether a waypoint sells fuel and what a unit costs there.
type hullFuelMarket struct {
	db *gorm.DB
}

// FuelAsk reads the fuel market at a waypoint. The price is market_data.purchase_price —
// the ASK, what we pay.
//
// It falls back to the highest FUEL ask this player has recorded anywhere when the
// waypoint's own listing is missing or unpriced. That is a measured bound and strictly more
// conservative than the local price, which is what the guard needs; failing closed on a
// waypoint whose market has simply not been scanned would block the repair on a gap that
// has nothing to do with affordability. With no fuel price recorded at all it still fails
// closed (RULINGS #4).
func (m *hullFuelMarket) FuelAsk(ctx context.Context, playerID int, waypoint string) (int, bool, error) {
	if m.db == nil {
		return 0, false, errors.New("no database wired for the fuel-market read")
	}

	var wp persistence.WaypointModel
	if err := m.db.WithContext(ctx).
		Where("waypoint_symbol = ?", waypoint).
		First(&wp).Error; err != nil {
		return 0, false, fmt.Errorf("read waypoint %s: %w", waypoint, err)
	}
	// The same predicate every other refuel path gates on, never a second one.
	if wp.HasFuel == 0 && !shared.WaypointGrantsFuel(wp.Type, nil) {
		return 0, false, nil
	}

	var local persistence.MarketData
	err := m.db.WithContext(ctx).
		Where("player_id = ? AND waypoint_symbol = ? AND good_symbol = ?", playerID, waypoint, fuelGoodSymbol).
		First(&local).Error
	if err == nil && local.PurchasePrice > 0 {
		return local.PurchasePrice, true, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, true, fmt.Errorf("read the fuel listing at %s: %w", waypoint, err)
	}

	var worst struct{ Price int }
	if err := m.db.WithContext(ctx).
		Model(&persistence.MarketData{}).
		Select("COALESCE(MAX(purchase_price), 0) AS price").
		Where("player_id = ? AND good_symbol = ?", playerID, fuelGoodSymbol).
		Scan(&worst).Error; err != nil {
		return 0, true, fmt.Errorf("read the fleet's worst-case fuel price: %w", err)
	}
	return worst.Price, true, nil
}

const fuelGoodSymbol = "FUEL"

// hullTankSize reads a hull's tank from its stored row. Capacity is a composite-only field,
// so the last good read is the only source; it changes only on a refit, which is what makes
// a stored value safe here where a stored position would not be.
type hullTankSize struct {
	db *gorm.DB
}

func (t *hullTankSize) FuelCapacity(ctx context.Context, playerID int, symbol string) (int, error) {
	if t.db == nil {
		return 0, errors.New("no database wired for the tank-size read")
	}
	var row persistence.ShipModel
	if err := t.db.WithContext(ctx).
		Where("player_id = ? AND ship_symbol = ?", playerID, symbol).
		First(&row).Error; err != nil {
		return 0, fmt.Errorf("read the stored row for %s: %w", symbol, err)
	}
	return row.FuelCapacity, nil
}

// hullRowRefresher re-reads a repaired hull into its row.
type hullRowRefresher struct {
	shipRepo navigation.ShipRepository
}

func (r *hullRowRefresher) Refresh(ctx context.Context, playerID int, symbol string) error {
	if r.shipRepo == nil {
		return errors.New("no ship repository wired")
	}
	id, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	_, err = r.shipRepo.SyncShipFromAPI(ctx, symbol, id)
	return err
}

// hullRepairReporter publishes the repair's outcomes. Best-effort throughout.
type hullRepairReporter struct{}

func (hullRepairReporter) Attempted(symbol string, outcome hullrepair.Outcome) {
	metrics.RecordHullRepair(symbol, string(outcome))
}

func (hullRepairReporter) Escalated(symbol, reason string) {
	metrics.RecordHullRepairEscalated(symbol, reason)
}
