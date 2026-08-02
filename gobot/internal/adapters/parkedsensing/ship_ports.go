// Package parkedsensing holds the infrastructure adapters behind the
// parked-probe sensing engine's ports: the money reads its buy queue guards on,
// the purchase and movement verbs it drives hulls with, the ships-table reads it
// locates them by, and the translation of its durable placement ledger.
//
// The engine's decision logic lives in internal/application/parkedsensing and
// knows none of this; these are the seams the daemon binds it to at composition
// time. Nothing here decides anything — every adapter is a thin wrapper over
// machinery that already ships.
//
// API ATTRIBUTION. Every outbound call is tagged at this boundary rather than in
// the application layer, which keeps the engine's core free of an adapter import
// and still catches every call — the application layer reaches the network only
// through these types.
//
// The placement machinery in this file is tagged apibudget.SourceCharting,
// because standing a probe up IS charting-envelope spend: the same budget the
// scan pacer sizes its residual against. The scans those probes then issue are
// the deliberate exception and are tagged SourceScanning at LOW priority — see
// scan_ports.go, which explains why that one class of call must yield a
// contended rate-limit token to every other consumer.
package parkedsensing

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// probeShipType is the purchase type for a scout hull, matching the
	// engine's own constant.
	probeShipType = "SHIP_PROBE"

	// satelliteRole is the role a purchased SHIP_PROBE reports. It is what makes
	// a hull eligible to stand in as a purchasing ship at a yard.
	satelliteRole = "SATELLITE"

	// sensingBuyClaimReason labels the release of the exclusive single-writer
	// claim held over a purchasing hull for the length of a buy, for the audit
	// trail. A claim stranded by a crash is freed by the daemon's boot-time
	// release sweep, so cleanup never depends on this value.
	//
	// THERE IS DELIBERATELY NO COMPANION OWNER CONSTANT. The claim's OWNER is
	// ships.container_id, which carries a foreign key to containers(id): only a
	// real, live container id can be written there. This constant block used to
	// carry `sensingBuyClaimOwner = "parked_sensing_buy"`, a descriptive label
	// passed straight into ClaimShip's containerID parameter, and Postgres
	// rejected every such claim with fk_ships_container (23503). The claim fails
	// CLOSED, so the effect was total: this engine never completed a single probe
	// purchase in its existence, and the fleet stopped discovering new systems.
	// The owner now arrives per-call from the driving coordinator — see
	// claimBuyer. Do not reintroduce a constant here.
	sensingBuyClaimReason = "parked_sensing_buy_complete"

	// marketplaceTrait is the waypoint trait that makes a charted waypoint worth
	// a market read.
	marketplaceTrait = "MARKETPLACE"

	// catalogPageSize is the page size for a waypoint-catalog sweep, and
	// maxCatalogPages bounds how far it will walk. 20 is the API's own maximum;
	// ten pages is 200 waypoints, several times the largest system observed, so
	// exhausting the bound means the response shape has changed rather than that
	// a real system is that big — which is why it is reported as an error rather
	// than accepted as a truncated sweep.
	catalogPageSize = 20
	maxCatalogPages = 10

	// cargoSpendScan bounds the transaction rows summed for the buy floor's
	// cargo-runway term. Generous enough to cover an hour of a busy trading
	// fleet: under-reading here would UNDERSTATE the floor, which is the
	// permissive direction, so the bound is set well above observed volume.
	cargoSpendScan = 1000
)

// Compile-time proof that every adapter here still satisfies the port the
// engine declares for it. These are what turn a signature drift on either side
// into a build failure at the seam, rather than a nil interface discovered when
// the coordinator is wired.
var (
	_ appSensing.TreasuryReader   = (*TreasuryPort)(nil)
	_ appSensing.CargoSpendReader = (*CargoSpendPort)(nil)
	_ appSensing.ProbePurchaser   = (*ProbePurchasePort)(nil)
	_ appSensing.ParkedShipReader = (*ShipPositionPort)(nil)
	_ appSensing.FleetTagger      = (*FleetTagPort)(nil)
	_ appSensing.ShipMover        = (*MoverPort)(nil)
	_ appSensing.SeedCommander    = (*SeedCommandPort)(nil)
	_ appSensing.BuyLedger        = (*LedgerPort)(nil)
	_ appSensing.PlacementLedger  = (*LedgerPort)(nil)
	_ appSensing.ScanLedger       = (*LedgerPort)(nil)
	_ appSensing.ExpandLedger     = (*LedgerPort)(nil)
	_ appSensing.SlotLedger       = (*LedgerPort)(nil)
)

// sensingCtx tags a context as charting-envelope work. See the package comment.
func sensingCtx(ctx context.Context) context.Context {
	return api.WithSource(ctx, apibudget.SourceCharting)
}

// ---- ShipPositionPort -------------------------------------------------------

// ShipPositionPort answers "where is this hull?" and "is one of ours standing
// here?" from the ships table, which is the source of truth for ship state once
// the daemon has synced.
//
// Both queries are indexed lookups scoped to ONE ship or ONE waypoint. Neither
// ever loads the fleet: the sensing engine's per-tick cost must not grow with
// how many hulls the player owns.
type ShipPositionPort struct{ db *gorm.DB }

// NewShipPositionPort wires the ships-table reads.
func NewShipPositionPort(db *gorm.DB) *ShipPositionPort {
	return &ShipPositionPort{db: db}
}

// DockedProbeAt returns a probe docked at waypoint that this engine may drive.
//
// DOCKED is required, not merely "present": the purchase flow buys at a counter,
// and a hull in orbit is not at one.
//
// The dedication filter is the load-bearing part. A hull tagged to another fleet
// cannot be claimed by this engine — the claim path rejects it, and rejects it
// PERMANENTLY rather than transiently — so returning one would hand the buy
// queue a purchasing hull it can never actually use. The queue would select it,
// pay for a live shipyard price read, fail the claim, and select the very same
// hull again on the next tick, forever. Filtering here is what keeps that from
// becoming a standing API drain, and is why the port documents this as part of
// its contract rather than an implementation detail.
//
// The filter admits only undedicated hulls and our own. A NULL tag (which the
// schema's ” default should preclude) matches neither and is therefore skipped,
// which is the conservative direction.
//
// Ordered by symbol so repeated calls pick the same hull rather than alternating
// between equally valid candidates.
func (p *ShipPositionPort) DockedProbeAt(ctx context.Context, playerID int, waypoint string) (string, bool, error) {
	var model persistence.ShipModel
	err := p.db.WithContext(ctx).
		Where("player_id = ? AND location_symbol = ? AND nav_status = ? AND role = ? AND dedicated_fleet IN ?",
			playerID, waypoint, string(navigation.NavStatusDocked), satelliteRole,
			[]string{"", appSensing.SensingParkedFleetTag}).
		Order("ship_symbol").
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("failed to look for a docked probe at %q: %w", waypoint, err)
	}
	return model.ShipSymbol, true, nil
}

// ShipAt returns one hull's recorded position. A hull the table does not know is
// reported as not-found rather than as an error — it is an answer, and the
// caller's response to it (leave the placement alone) is the same either way.
func (p *ShipPositionPort) ShipAt(ctx context.Context, playerID int, shipSymbol string) (appSensing.ShipPos, error) {
	var model persistence.ShipModel
	err := p.db.WithContext(ctx).
		Where("player_id = ? AND ship_symbol = ?", playerID, shipSymbol).
		First(&model).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return appSensing.ShipPos{}, nil
		}
		return appSensing.ShipPos{}, fmt.Errorf("failed to read position of %q: %w", shipSymbol, err)
	}
	return appSensing.ShipPos{
		Waypoint:  model.LocationSymbol,
		NavStatus: navigation.NavStatus(model.NavStatus),
		Found:     true,
	}, nil
}

// ---- FleetTagPort -----------------------------------------------------------

// FleetTagPort writes the dedicated-fleet tag through the single write path for
// fleet dedication. Idempotent, so re-asserting a tag already persisted performs
// no write at all.
type FleetTagPort struct{ shipRepo navigation.ShipRepository }

// NewFleetTagPort wires fleet dedication.
func NewFleetTagPort(shipRepo navigation.ShipRepository) *FleetTagPort {
	return &FleetTagPort{shipRepo: shipRepo}
}

// AssignFleet sets a hull's dedicated-fleet tag.
func (p *FleetTagPort) AssignFleet(ctx context.Context, playerID int, shipSymbol, fleet string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	return p.shipRepo.AssignFleet(sensingCtx(ctx), shipSymbol, fleet, pid)
}
