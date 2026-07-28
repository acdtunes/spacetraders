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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipQueries "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainSystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
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

// ---- TreasuryPort -----------------------------------------------------------

// TreasuryPort adapts the shared live-treasury reader — the same one every
// other money guard reads, whose cache is invalidated on each credit-decreasing
// call — to the engine's plain-int player identity.
type TreasuryPort struct{ reader probebuy.TreasuryReader }

// NewTreasuryPort wires the live-treasury read.
func NewTreasuryPort(reader probebuy.TreasuryReader) *TreasuryPort {
	return &TreasuryPort{reader: reader}
}

// LiveCredits returns the player's current spendable balance.
func (p *TreasuryPort) LiveCredits(ctx context.Context, playerID int) (int64, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}
	credits, err := p.reader.LiveCredits(sensingCtx(ctx), pid)
	if err != nil {
		return 0, err
	}
	return int64(credits), nil
}

// ---- CargoSpendPort ---------------------------------------------------------

// CargoSpendPort measures the trading fleet's recent cargo outflow from the
// persisted ledger, which is what makes the probe-buy floor rise when the fleet
// is busy. Derived fresh from the ledger on every call, so it survives a restart
// with no in-memory counter to lose.
type CargoSpendPort struct{ txns ledger.TransactionRepository }

// NewCargoSpendPort wires the ledger-derived cargo-spend read.
func NewCargoSpendPort(txns ledger.TransactionRepository) *CargoSpendPort {
	return &CargoSpendPort{txns: txns}
}

// AbsCargoBuySpendSince sums cargo-purchase spend booked since `since`.
//
// Ledger expenses are stored NEGATIVE. The absolute value of each row is taken
// individually rather than negating the sum, so a stray positive row (a refund,
// a correction) still ADDS to measured outflow instead of cancelling real spend
// out of it — the floor is a guard, and a malformed row must only ever make it
// stricter.
func (p *CargoSpendPort) AbsCargoBuySpendSince(ctx context.Context, playerID int, since time.Time) (int64, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}
	cargo := ledger.TransactionTypePurchaseCargo
	rows, err := p.txns.FindByPlayer(ctx, pid, ledger.QueryOptions{
		TransactionType: &cargo,
		StartDate:       &since,
		Limit:           cargoSpendScan,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to read recent cargo spend: %w", err)
	}
	var sum int64
	for _, row := range rows {
		amount := int64(row.Amount())
		if amount < 0 {
			amount = -amount
		}
		sum += amount
	}
	return sum, nil
}

// ---- ProbePurchasePort ------------------------------------------------------

// ProbePurchasePort prices and buys one probe through the existing purchase-ship
// mediator path (the daemon stays the single writer). It mirrors the frontier
// coordinator's buyer — claim the hull, re-check the live price at the counter,
// purchase, verify what was delivered — MINUS its relay and its cooldown:
//
//   - no relay, because the engine only ever names a purchasing hull ALREADY
//     standing at the yard (it will not buy where it has no presence), so the
//     hull has nowhere to be flown;
//   - no cooldown, because this engine's spend is bounded by its own probe cap
//     and treasury floor rather than by pacing. It still WRITES the shared
//     ledger row every purchase-ship flow records, so the other probe buyers'
//     cooldowns continue to see these purchases and pace themselves around them.
type ProbePurchasePort struct {
	mediator common.Mediator
	shipRepo navigation.ShipRepository
}

// NewProbePurchasePort wires the price-and-buy port.
func NewProbePurchasePort(mediator common.Mediator, shipRepo navigation.ShipRepository) *ProbePurchasePort {
	return &ProbePurchasePort{mediator: mediator, shipRepo: shipRepo}
}

// Quote reads the live SHIP_PROBE price at a yard. An unreadable or unpriced
// listing is an error, never a zero: the caller checks its treasury floor
// against this number, and a zero would clear any floor.
func (p *ProbePurchasePort) Quote(ctx context.Context, playerID int, yardWaypoint string) (int64, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, err
	}
	resp, err := p.mediator.Send(sensingCtx(ctx), &shipyardQueries.GetShipyardListingsQuery{
		SystemSymbol:   shared.ExtractSystemSymbol(yardWaypoint),
		WaypointSymbol: yardWaypoint,
		PlayerID:       pid,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to read shipyard listings at %s: %w", yardWaypoint, err)
	}
	listings, ok := resp.(*shipyardQueries.GetShipyardListingsResponse)
	if !ok {
		return 0, fmt.Errorf("shipyard at %s returned no listings", yardWaypoint)
	}
	listing, found := listings.Shipyard.FindListingByType(probeShipType)
	if !found || listing.PurchasePrice <= 0 {
		return 0, fmt.Errorf("shipyard at %s has no priced %s listing", yardWaypoint, probeShipType)
	}
	return int64(listing.PurchasePrice), nil
}

// Buy purchases exactly one probe at yardWaypoint through purchasingShip.
//
// The claim is held for the whole purchase so no other coordinator can drive the
// same hull mid-buy; a lost claim fails the buy CLOSED, which is safe because
// nothing has been spent at that point. After the purchase settles, the yard's
// echoed ship type is verified — a yard that substituted a different hull for
// the one asked for has taken money for something we did not want, and that must
// surface as an error rather than be recorded as a probe.
func (p *ProbePurchasePort) Buy(ctx context.Context, playerID int, purchasingShip, yardWaypoint, claimOwnerContainerID string) (appSensing.BoughtProbe, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return appSensing.BoughtProbe{}, err
	}
	ctx = sensingCtx(ctx)

	release, err := p.claimBuyer(ctx, pid, purchasingShip, claimOwnerContainerID)
	if err != nil {
		return appSensing.BoughtProbe{}, err
	}
	defer release()

	resp, err := p.mediator.Send(ctx, &shipyardCmd.PurchaseShipCommand{
		PurchasingShipSymbol: purchasingShip,
		ShipType:             probeShipType,
		PlayerID:             pid,
		ShipyardWaypoint:     yardWaypoint,
	})
	if err != nil {
		return appSensing.BoughtProbe{}, fmt.Errorf("probe purchase at %s failed: %w", yardWaypoint, err)
	}
	purchase, ok := resp.(*shipyardCmd.PurchaseShipResponse)
	if !ok || purchase.Ship == nil {
		return appSensing.BoughtProbe{}, fmt.Errorf("probe purchase at %s returned no hull", yardWaypoint)
	}
	if purchase.ShipType != probeShipType {
		return appSensing.BoughtProbe{}, fmt.Errorf("yard %s delivered %q, not %q", yardWaypoint, purchase.ShipType, probeShipType)
	}
	return appSensing.BoughtProbe{
		ShipSymbol:   purchase.Ship.ShipSymbol(),
		Price:        int64(purchase.PurchasePrice),
		CreditsAfter: int64(purchase.AgentCredits),
	}, nil
}

// claimBuyer takes the exclusive single-writer claim on the purchasing hull and
// returns a release closure that is always safe to defer (the underlying release
// is idempotent). The claim operation is the sensing fleet tag, so the ship
// repository's dedication guard accepts a hull this engine already owns instead
// of rejecting it as another fleet's.
//
// owner MUST be the driving coordinator's real container id. ClaimShip's second
// parameter is written to ships.container_id, which carries a foreign key to
// containers(id) — a descriptive label has no row to reference and the database
// refuses the write. Passing one is precisely the defect that kept this engine
// from ever completing a purchase; the empty-owner guard below is what stops it
// silently returning, in any form.
func (p *ProbePurchasePort) claimBuyer(ctx context.Context, playerID shared.PlayerID, buyer, owner string) (func(), error) {
	// Fail CLOSED, before any claim is attempted and before any money moves. An
	// unnamed owner cannot own a claim, and there is no safe default to fall back
	// to: every candidate is a label, and a label is exactly what the foreign key
	// rejects. Refusing here costs one unfilled placement that the next tick
	// retries for free.
	if strings.TrimSpace(owner) == "" {
		return nil, fmt.Errorf("sensing probe buyer %s claim refused (fail-closed): no owning container id was supplied, and the claim's owner must be a real container row", buyer)
	}
	if err := p.shipRepo.ClaimShip(ctx, buyer, owner, playerID, appSensing.SensingParkedFleetTag); err != nil {
		return nil, fmt.Errorf("sensing probe buyer %s claim failed (fail-closed, no concurrent driver): %w", buyer, err)
	}
	return func() {
		if _, err := p.shipRepo.ReleaseContainerClaim(ctx, buyer, playerID, sensingBuyClaimReason); err != nil {
			common.LoggerFromContext(ctx).Log("WARN", "sensing probe buyer claim release failed (boot release sweep is the backstop)", map[string]interface{}{
				"action":      "parked_sensing_buy_claim_release",
				"ship_symbol": buyer,
				"error":       err.Error(),
			})
		}
	}, nil
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

// ---- MoverPort --------------------------------------------------------------

// MoverPort issues movement commands through the mediator. The two navigation
// verbs are separate machinery, and the caller picks between them by comparing
// systems — this type does not second-guess that choice.
//
// EVERY VERB HERE DISPATCHES AND RETURNS. The placement machine behind this port
// is a tick machine: it takes one action per placement per tick, holds nothing in
// memory between ticks, and reads the ships table on a LATER tick to see what
// happened ("a hull dispatched by this tick is not also examined for arrival by
// it", placement.go). A verb that waited out the flight would hold the whole tick
// open for the length of a journey, starving every placement behind it in the
// ledger read and every stage after it in the coordinator — the buy-queue drain,
// the reaper, adoption, and the expansion pass where charting seeds launch.
//
// So the command choice is load-bearing, not incidental. NavigateRouteCommand
// plans and FLIES a whole route, ending in WaitForShipArrival;
// NavigateDirectCommand hands one hop to the API and returns with the arrival
// time. Only the second is admissible here, and the arrival it does not wait for
// is recorded anyway: ShipRepository.Navigate persists the IN_TRANSIT row with
// its arrival clock and arms ShipStateScheduler, whose timer (backed by a
// 60-second sweeper, and re-armed for every in-flight hull on daemon restart)
// writes the landing back to the ships table. That row IS how the next tick
// reconciles the arrival, which is exactly what the placement machine already
// reads.
type MoverPort struct {
	mediator common.Mediator
	// neighbours is the stored gate adjacency the cross-system walk picks its
	// next system from. A PURE STORE read (see GateNeighbourPort): topology is
	// never worth an API call here, and a system the store cannot answer for is
	// one this port declines to walk through rather than guesses at.
	neighbours appSensing.GateNeighbours
}

// NewMoverPort wires the movement verbs. neighbours may be nil, in which case
// the cross-system walk fails closed — it holds the placement and warns, exactly
// as it does for a destination the stored graph cannot reach.
func NewMoverPort(mediator common.Mediator, neighbours appSensing.GateNeighbours) *MoverPort {
	return &MoverPort{mediator: mediator, neighbours: neighbours}
}

// NavigateWithin dispatches a hull toward a waypoint inside its current system
// and returns as soon as the API has accepted the move.
//
// The orbit first is not a precaution, it is the API's precondition: a DOCKED
// hull cannot navigate, and a parked sensing probe is docked by definition — the
// placement machine docks it on arrival and re-tasked spares are taken straight
// off a berth. OrbitShipCommand is free for a hull already in orbit (it answers
// "already_in_orbit" off the local row without touching the network), so the
// common case costs nothing, and issuing it explicitly keeps the navigate off
// NavigateDirectCommand's not-in-orbit self-heal path — which would otherwise
// spend a REJECTED live call plus a warning on every single docked dispatch.
//
// Both commands together are ONE placement action: the pair either starts the
// hull moving or leaves it exactly where it was, and the caller's free retry
// (hold the slot, re-read the position next tick) covers the failure either way.
func (p *MoverPort) NavigateWithin(ctx context.Context, playerID int, shipSymbol, destination string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	return dispatchHop(ctx, p.mediator, pid, shipSymbol, destination)
}

// maxWalkRings bounds how far the cross-system walk will look for a route.
//
// NOT A NUMBER, A REFERENCE. The bound is declared once beside the RouteAcross
// contract it describes, because the walk is not its only reader: the caller that
// picks destinations for idle hulls has to refuse anything this walk cannot
// resolve, and a second copy of the number here is what would let the two drift
// into handing out unreachable errands. See appSensing.MaxWalkRings for what the
// bound is and why it is two.
const maxWalkRings = appSensing.MaxWalkRings

// RouteAcross advances a hull ONE STEP of its gate walk toward destination, and
// returns either way. It NEVER waits.
//
// WHY THIS REPLACED A REFUSAL. There is no dispatch-and-return primitive for a
// whole gate crossing: RouteShipCommand, delegating to the shared multi-jump
// travel(), resolves a gate path and FLIES it, waiting out every leg and every
// jump cooldown in between. Sending that from inside a tick is the exact failure
// this port exists to prevent, so this verb used to refuse instead. The refusal
// was safe but it was a wall: with it in place a probe could never leave the
// system it was bought in, so only the system we already occupied ever had
// usable yards, and the frontier could not widen.
//
// The way out is the one the charting seed's gate hop already took: don't fly
// the crossing, WALK it. A crossing is a sequence of steps — onto the gate, then
// off it, once per gate on the way — and each step is a command that returns as
// soon as the API accepts it. This performs exactly the one step the hull's
// current position calls for, and the next tick performs the next.
//
// THE DISCRIMINATOR IS THE WAYPOINT, NEVER THE DISTANCE. fromWaypoint is
// compared against the gate SYMBOL, because orbitals share coordinates with the
// body they orbit — a hull can sit at zero distance from a gate it is not
// standing on, and "jumping" from there is precisely what puts us back on the
// blocking branch, since JumpShipCommand would navigate it to the gate first and
// wait out that flight.
//
// NOTHING IS PERSISTED HERE, and nothing needs to be. How far the crossing has
// got is fully described by two rows this machine already keeps: the slot naming
// the destination and the hull, and the ships table naming where that hull now
// stands. Every tick re-reads both and re-decides from scratch, which is what
// makes the walk resume across a daemon restart and self-correct if a hull ends
// up somewhere the last step did not send it.
//
// FAIL CLOSED, AND BEFORE SPENDING MOVEMENT. The next system is resolved FIRST,
// so a destination the stored graph cannot reach costs no flight at all: the
// placement holds exactly where it was, keeps its hull (so the probe cap still
// counts it — the over-count direction RULINGS #4 requires), and the next tick
// retries for free.
func (p *MoverPort) RouteAcross(ctx context.Context, playerID int, shipSymbol, fromWaypoint, destination string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}

	currentSystem := shared.ExtractSystemSymbol(fromWaypoint)
	if currentSystem == shared.ExtractSystemSymbol(destination) {
		// Already across: the gates are behind it and what is left is one
		// in-system hop. Defensive — flyToSlot sends the in-system verb in this
		// case — but it costs nothing and keeps the walk correct on its own.
		return dispatchHop(ctx, p.mediator, pid, shipSymbol, destination)
	}

	// Resolved before any movement, so an unreachable destination never buys a
	// wasted flight to a gate.
	nextSystem, err := p.nextHopToward(ctx, currentSystem, shared.ExtractSystemSymbol(destination))
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Sensing placement wants %s walked from %s to %s, but no gate route could be named within %d jumps of stored adjacency — the placement is held and retried, and the hull keeps counting against the probe cap: %v",
			shipSymbol, currentSystem, destination, maxWalkRings, err), map[string]interface{}{
			"action":         "parked_sensing_gate_walk_unroutable",
			"ship_symbol":    shipSymbol,
			"from_system":    currentSystem,
			"destination":    destination,
			"max_walk_rings": maxWalkRings,
		})
		return fmt.Errorf("failed to name the next system for %s walking to %s: %w", shipSymbol, destination, err)
	}

	gate, err := gateToLeaveFrom(sensingCtx(ctx), p.mediator, pid.Value(), shipSymbol)
	if err != nil {
		return err
	}
	if gate != fromWaypoint {
		// Step one: get it onto the gate, dispatch-only. The slot holding where
		// it is — BOUGHT, or IN_TRANSIT once dispatched — is what makes the next
		// tick pick up from here.
		return dispatchHop(ctx, p.mediator, pid, shipSymbol, gate)
	}

	// Step two. The hull is ON the gate, so JumpShipCommand's navigate branch is
	// unreachable and the command is the bare jump it was named for. The jump is
	// instantaneous at the API, leaving only a reactor cooldown — which gates the
	// next JUMP rather than this navigate, and a cooldown-rejected jump is just
	// the free retry every step in this engine already gets.
	id := pid.Value()
	if _, err := p.mediator.Send(sensingCtx(ctx), &shipNav.JumpShipCommand{
		ShipSymbol:        shipSymbol,
		DestinationSystem: nextSystem,
		PlayerID:          &id,
	}); err != nil {
		return fmt.Errorf("failed to jump %s from %s to %s: %w", shipSymbol, currentSystem, nextSystem, err)
	}
	return nil
}

// nextHopToward names the ADJACENT system the walk should jump to next to get
// from fromSystem toward toSystem, by breadth-first search over the stored gate
// adjacency out to maxWalkRings.
//
// It returns the FIRST-RING system, not the path: the only thing a single step
// can act on is the next jump, and re-deriving it each tick from where the hull
// actually is beats carrying a stale path forward. Breadth-first means the
// system it names is on a SHORTEST route, so the walk cannot circle.
//
// PURE STORE READS. Neighbours already drops under-construction and stale edges
// and answers an unknown system with nothing, so a topology we are unsure of
// ends the search rather than routing a hull into a gate that will reject it.
func (p *MoverPort) nextHopToward(ctx context.Context, fromSystem, toSystem string) (string, error) {
	if p.neighbours == nil {
		return "", fmt.Errorf("no stored gate adjacency is wired to name a next system toward %s", toSystem)
	}

	// via carries, for each system the search reaches, the FIRST-RING system the
	// walk would leave through to get there — which is the answer being looked
	// for. seen keeps the search from revisiting, so each system costs one read.
	type reached struct{ system, via string }
	seen := map[string]bool{fromSystem: true}
	frontier := []reached{{system: fromSystem}}

	for ring := 0; ring < maxWalkRings && len(frontier) > 0; ring++ {
		var next []reached
		for _, cur := range frontier {
			systems, err := p.neighbours.Neighbours(ctx, cur.system)
			if err != nil {
				return "", fmt.Errorf("failed to read the gate neighbours of %q: %w", cur.system, err)
			}
			for _, candidate := range systems {
				if seen[candidate] {
					continue
				}
				seen[candidate] = true
				via := cur.via
				if via == "" {
					// First ring: reaching this system IS the jump to make now.
					via = candidate
				}
				if candidate == toSystem {
					return via, nil
				}
				next = append(next, reached{system: candidate, via: via})
			}
		}
		frontier = next
	}
	return "", fmt.Errorf("no stored gate route from %s to %s within %d jumps", fromSystem, toSystem, maxWalkRings)
}

// dispatchHop orbits a hull and hands it ONE in-system hop, returning as soon as
// the API has accepted the move. Shared by the placement mover and the charting
// seed's tour hop, which have the same contract for the same reason: both are
// driven by tick machines that re-read the ships table rather than wait.
func dispatchHop(ctx context.Context, med common.Mediator, pid shared.PlayerID, shipSymbol, destination string) error {
	mctx := sensingCtx(ctx)
	if _, err := med.Send(mctx, &shipTypes.OrbitShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   pid,
	}); err != nil {
		return fmt.Errorf("failed to orbit %s before sending it to %s: %w", shipSymbol, destination, err)
	}
	if _, err := med.Send(mctx, &shipTypes.NavigateDirectCommand{
		ShipSymbol:  shipSymbol,
		Destination: destination,
		PlayerID:    pid,
	}); err != nil {
		return fmt.Errorf("failed to navigate %s to %s: %w", shipSymbol, destination, err)
	}
	return nil
}

// Dock docks a hull where it currently sits.
func (p *MoverPort) Dock(ctx context.Context, playerID int, shipSymbol string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	if _, err := p.mediator.Send(sensingCtx(ctx), &shipTypes.DockShipCommand{
		ShipSymbol: shipSymbol,
		PlayerID:   pid,
	}); err != nil {
		return fmt.Errorf("failed to dock %s: %w", shipSymbol, err)
	}
	return nil
}

// ---- SeedCommandPort --------------------------------------------------------

// seedChartAPI is the slice of the SpaceTraders API a charting seed needs: chart
// the waypoint under the hull, then read back what that revealed. Narrowed to
// two methods so the port is testable without an API client.
type seedChartAPI interface {
	CreateChart(ctx context.Context, shipSymbol, token string) error
	GetWaypoint(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.WaypointDetail, error)
	// ListWaypoints reads one page of a system's waypoint list — the sweep that
	// turns a system we have never visited into one we know the shape of.
	ListWaypoints(ctx context.Context, systemSymbol, token string, page, limit int) (*domainSystem.WaypointsListResponse, error)
}

// waypointCacheWriter persists a freshly-read waypoint. *persistence.GormWaypointRepository
// satisfies it.
type waypointCacheWriter interface {
	UpsertFromDetail(ctx context.Context, detail *domainPorts.WaypointDetail) error
}

// playerTokenReader resolves the player's API token. Narrowed to the one lookup
// the charting verbs need, so this adapter cannot reach the rest of the player
// repository — it has no business creating or updating one.
type playerTokenReader interface {
	FindByID(ctx context.Context, playerID shared.PlayerID) (*player.Player, error)
}

// SeedCommandPort drives one charting seed: the gate hop out, the hops between
// waypoints, the chart itself, and the two reads that turn a charted waypoint
// into something the screen can judge.
//
// Every call is tagged SourceCharting and NOTHING sets a priority. That
// asymmetry against the scan port next door is deliberate: charting is bounded
// work with a hull idling until it completes, so it competes for a rate-limit
// token on equal terms, while a parked scan has unbounded appetite and no
// deadline and is the one class that yields to everyone.
type SeedCommandPort struct {
	mediator  common.Mediator
	api       seedChartAPI
	players   playerTokenReader
	waypoints waypointCacheWriter
	scanner   marketScanAPI
}

// NewSeedCommandPort wires the charting-seed verbs.
func NewSeedCommandPort(
	mediator common.Mediator,
	api seedChartAPI,
	players playerTokenReader,
	waypoints waypointCacheWriter,
	scanner marketScanAPI,
) *SeedCommandPort {
	return &SeedCommandPort{mediator: mediator, api: api, players: players, waypoints: waypoints, scanner: scanner}
}

// JumpTo advances a hull ONE step of its gate hop to targetSystem: the in-system
// move onto the gate, or the jump off it. It returns either way.
//
// WHY THE HOP IS SPLIT. A gate crossing is two physical moves, and only the first
// is a flight. JumpShipCommand does both — it finds the nearest gate, flies the
// hull there with NavigateRouteCommand, and only then jumps — so sending it from
// off-gate waits out that flight inside the tick. That is the same defect as the
// placement mover's, one layer in, and it lands on the stage this engine exists
// for: expansion is where charting seeds launch, so a seed blocking here stops
// the fleet discovering new systems at all.
//
// Split, each step is a command that returns, and the ships table carries the
// hull between them exactly as it does everywhere else in this engine: the hop is
// dispatched, ShipStateScheduler records the landing, and the NEXT tick reads a
// hull standing on the gate and jumps it. The seed's own machine already skips a
// hull the ships table reports IN_TRANSIT, so the intervening ticks cost nothing.
//
// THE DISCRIMINATOR IS THE WAYPOINT, NEVER THE DISTANCE. fromWaypoint is compared
// to the gate symbol, because orbitals share coordinates with the body they orbit
// — a hull can sit at zero distance from a gate it is not standing on, and
// jumping "from" there would put us straight back on the blocking branch.
//
// The jump itself needs no wait: it is instantaneous at the API, leaving only a
// reactor cooldown, which gates the next JUMP rather than the navigate that
// follows it — and a cooldown-rejected jump is just the free retry every step in
// this engine already gets.
func (p *SeedCommandPort) JumpTo(ctx context.Context, playerID int, shipSymbol, fromWaypoint, targetSystem string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	id := pid.Value()
	mctx := sensingCtx(ctx)

	gate, err := gateToLeaveFrom(mctx, p.mediator, id, shipSymbol)
	if err != nil {
		return err
	}
	if gate != fromWaypoint {
		// Step one: get it onto the gate, dispatch-only. Holding the errand at
		// DISPATCHED is what makes the next tick pick up where this left off.
		return dispatchHop(ctx, p.mediator, pid, shipSymbol, gate)
	}

	// Step two. The hull is ON the gate, so JumpShipCommand's navigate branch is
	// unreachable and the command is the bare jump it was named for.
	if _, err := p.mediator.Send(mctx, &shipNav.JumpShipCommand{
		ShipSymbol:        shipSymbol,
		DestinationSystem: targetSystem,
		PlayerID:          &id,
	}); err != nil {
		return fmt.Errorf("failed to jump %s to %s: %w", shipSymbol, targetSystem, err)
	}
	return nil
}

// gateToLeaveFrom names the jump gate in the hull's CURRENT system that a hop
// leaves from. Reuses the same query JumpShipCommand uses to pick one, so the
// gate this port moves the hull to is by construction the gate that command
// would have chosen — there is no window in which the two disagree and the hull
// is flown to one gate and jumped from another.
//
// Shared by BOTH gate walkers — the charting seed's hop and the placement
// mover's — for the same reason dispatchHop is: they answer the same question,
// and two implementations of it could drift into moving a hull to one gate and
// jumping it from another.
func gateToLeaveFrom(ctx context.Context, med common.Mediator, playerID int, shipSymbol string) (string, error) {
	res, err := med.Send(ctx, &shipQueries.FindNearestJumpGateQuery{
		ShipSymbol: shipSymbol,
		PlayerID:   &playerID,
	})
	if err != nil {
		return "", fmt.Errorf("failed to find the jump gate %s leaves from: %w", shipSymbol, err)
	}
	resp, ok := res.(*shipQueries.FindNearestJumpGateResponse)
	if !ok || resp.JumpGate == nil || resp.JumpGate.Symbol == "" {
		// Fail closed: never move a hull toward a gate we cannot name. The
		// errand holds and the next tick asks again.
		return "", fmt.Errorf("no jump gate could be named for %s", shipSymbol)
	}
	return resp.JumpGate.Symbol, nil
}

// NavigateTo dispatches a hull to a waypoint inside the system it is already in
// and returns as soon as the API has accepted the move.
//
// DISPATCH, NOT JOURNEY, and SeedCommander says so in its own words: "every
// method is a single command with no retry and no waiting: the tick issues one
// and returns, and the next tick reads the ships table to see what happened".
// The pass that drives this already opens by skipping any seed the ships table
// reports IN_TRANSIT ("already flying, under this or an earlier tick's
// command"), so waiting here buys nothing at all — it only holds the tick open
// for a leg of the tour, starving the rest of the tick behind it.
//
// Shares dispatchHop with the placement mover, including its orbit-first step:
// a seed charts from a berth, so it is docked at the waypoint it just finished.
func (p *SeedCommandPort) NavigateTo(ctx context.Context, playerID int, shipSymbol, waypoint string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	return dispatchHop(ctx, p.mediator, pid, shipSymbol, waypoint)
}

// Chart publicly charts the waypoint the hull is standing on.
//
// A waypoint somebody else has already charted answers 4230, and that is
// SUCCESS, not failure: the waypoint is public, which is the entire outcome the
// call was after. Swallowing it here (rather than letting the engine reason
// about API codes) is what keeps a frontier another agent got to first from
// stalling a tour on an error it can do nothing about.
func (p *SeedCommandPort) Chart(ctx context.Context, playerID int, shipSymbol string) error {
	token, err := p.token(ctx, playerID)
	if err != nil {
		return err
	}
	if err := p.api.CreateChart(sensingCtx(ctx), shipSymbol, token); err != nil && !isAlreadyCharted(err) {
		return fmt.Errorf("failed to chart the waypoint under %s: %w", shipSymbol, err)
	}
	return nil
}

// RefreshWaypoint re-reads a charted waypoint and writes it back to the cache,
// reporting whether it carries a marketplace.
//
// The persist is the point. A waypoint stays UNCHARTED in the cache until this
// row is rewritten, and the tour picks its next stop from exactly that set — so
// without this the seed would chart the same waypoint on every tick forever.
// The read is therefore only reported as a success once the write has landed.
func (p *SeedCommandPort) RefreshWaypoint(ctx context.Context, playerID int, system, waypoint string) (bool, error) {
	token, err := p.token(ctx, playerID)
	if err != nil {
		return false, err
	}
	detail, err := p.api.GetWaypoint(sensingCtx(ctx), system, waypoint, token)
	if err != nil {
		return false, fmt.Errorf("failed to re-read waypoint %s: %w", waypoint, err)
	}
	if err := p.waypoints.UpsertFromDetail(ctx, detail); err != nil {
		return false, fmt.Errorf("failed to persist the charted waypoint %s: %w", waypoint, err)
	}
	for _, trait := range detail.Traits {
		if trait == marketplaceTrait {
			return true, nil
		}
	}
	return false, nil
}

// SyncWaypoints sweeps a system's whole waypoint list and persists every row.
//
// This is what turns "we have never been here" into a catalog the screen and the
// charting tour can both read. It is the ONLY method on this port that issues
// more than one API call — the list is paginated — and the pages are walked here
// rather than by the engine so a partially-swept catalog is never visible: the
// tour picks its next stop from the stored uncharted set, and half a catalog
// reads as a system that is nearly finished.
//
// A page that fails aborts the sweep. The caller does not stamp the catalog as
// synced unless this returns cleanly, so a torn sweep is simply retried.
func (p *SeedCommandPort) SyncWaypoints(ctx context.Context, playerID int, system string) error {
	token, err := p.token(ctx, playerID)
	if err != nil {
		return err
	}
	ctx = sensingCtx(ctx)

	for page := 1; page <= maxCatalogPages; page++ {
		listing, err := p.api.ListWaypoints(ctx, system, token, page, catalogPageSize)
		if err != nil {
			return fmt.Errorf("failed to sweep the waypoint catalog of %s at page %d: %w", system, page, err)
		}
		for _, waypoint := range listing.Data {
			detail := &domainPorts.WaypointDetail{
				Symbol:   waypoint.Symbol,
				Type:     waypoint.Type,
				X:        waypoint.X,
				Y:        waypoint.Y,
				Traits:   traitSymbols(waypoint.Traits),
				Orbitals: orbitalSymbols(waypoint.Orbitals),
			}
			if err := p.waypoints.UpsertFromDetail(ctx, detail); err != nil {
				return fmt.Errorf("failed to persist swept waypoint %s: %w", waypoint.Symbol, err)
			}
		}
		if len(listing.Data) < catalogPageSize {
			return nil
		}
	}
	// Ran out of pages to walk. Reported as an error rather than a silent
	// truncation: the caller must not stamp a catalog it only partly has.
	return fmt.Errorf("waypoint catalog of %s exceeds %d pages; sweep abandoned", system, maxCatalogPages)
}

// traitSymbols flattens the API's trait objects to their symbols.
func traitSymbols(traits []map[string]interface{}) []string {
	out := make([]string, 0, len(traits))
	for _, trait := range traits {
		if symbol, ok := trait["symbol"].(string); ok {
			out = append(out, symbol)
		}
	}
	return out
}

// orbitalSymbols flattens the API's orbital objects to their symbols.
func orbitalSymbols(orbitals []map[string]string) []string {
	out := make([]string, 0, len(orbitals))
	for _, orbital := range orbitals {
		if symbol := orbital["symbol"]; symbol != "" {
			out = append(out, symbol)
		}
	}
	return out
}

// ReadMarketAt scans the market the hull is standing at and persists its prices
// and price history, through the same scanner the rest of the fleet uses.
func (p *SeedCommandPort) ReadMarketAt(ctx context.Context, playerID int, waypoint string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	return p.scanner.ScanAndSaveMarket(sensingCtx(ctx), uint(pid.Value()), waypoint)
}

// token resolves the player's API token, mirroring the gate graph's own lookup.
func (p *SeedCommandPort) token(ctx context.Context, playerID int) (string, error) {
	return playerToken(ctx, p.players, playerID)
}

// playerToken is the package's single token lookup, shared by every adapter here
// that talks to the API directly. One implementation so a change in how the
// token is resolved cannot apply to some outbound calls and not others.
func playerToken(ctx context.Context, players playerTokenReader, playerID int) (string, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return "", err
	}
	entity, err := players.FindByID(ctx, pid)
	if err != nil {
		return "", fmt.Errorf("failed to load player %d: %w", playerID, err)
	}
	return entity.Token, nil
}

// isAlreadyCharted reports whether a CreateChart failure is the API's benign
// "waypoint already charted" verdict (HTTP 400, code 4230). Matching on the code
// and the message mirrors the gate graph's identical classifier and is robust to
// the adapter wrapping the underlying *APIError.
func isAlreadyCharted(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "4230") || strings.Contains(msg, "already charted")
}

// ---- LedgerPort -------------------------------------------------------------

// LedgerPort translates the durable sensing ledger's rows into the engine's own
// view of a placement, and its transitions back into row writes.
//
// The translation exists because the engine must not see persistence types: it
// flattens the ledger's nullable ship and yard columns to plain strings, since
// nothing in the engine distinguishes NULL from empty and every check it makes
// is "is a hull recorded here?".
type LedgerPort struct {
	repo *persistence.SensingLedgerRepository
}

// NewLedgerPort wires the placement ledger.
func NewLedgerPort(repo *persistence.SensingLedgerRepository) *LedgerPort {
	return &LedgerPort{repo: repo}
}

// SlotsByState returns the player's placements in any of the given states.
func (p *LedgerPort) SlotsByState(ctx context.Context, playerID int, states ...string) ([]appSensing.QueuedSlot, error) {
	models, err := p.repo.SlotsByState(ctx, playerID, states...)
	if err != nil {
		return nil, err
	}
	return queuedSlots(models), nil
}

// SlotsBySystem returns every placement in one system, in any state.
func (p *LedgerPort) SlotsBySystem(ctx context.Context, playerID int, system string) ([]appSensing.QueuedSlot, error) {
	models, err := p.repo.SlotsBySystem(ctx, playerID, system)
	if err != nil {
		return nil, err
	}
	return queuedSlots(models), nil
}

// ExistingSlots returns the placements a system already holds, carrying the
// whitelisted goods and depth recorded when each was written.
//
// DECODING whitelist_goods is the whole point of this method, not a detail of
// it. The screen uses the recorded goods as a CACHE: a waypoint discovered
// remotely has no market_data rows until a probe actually parks there, so
// without the recorded list the screen would pay the API to re-fetch the same
// answer on every re-screen of the system. Worse, it must SUPPLY the goods
// rather than merely suppress the fetch — an empty list drops the waypoint out
// of the hit set and takes its own slot back out of the plan, so a remotely
// discovered market would be re-found and re-dropped forever. A decode failure
// is therefore an error, never a silent empty list.
func (p *LedgerPort) ExistingSlots(ctx context.Context, playerID int, system string) ([]appSensing.ExistingSlot, error) {
	models, err := p.repo.SlotsBySystem(ctx, playerID, system)
	if err != nil {
		return nil, err
	}
	out := make([]appSensing.ExistingSlot, 0, len(models))
	for _, m := range models {
		goods, derr := decodeWhitelistGoods(m.WhitelistGoods)
		if derr != nil {
			return nil, fmt.Errorf("failed to decode the recorded goods at %q: %w", m.WaypointSymbol, derr)
		}
		out = append(out, appSensing.ExistingSlot{
			Waypoint:       m.WaypointSymbol,
			WhitelistGoods: goods,
			DepthCredits:   m.DepthCredits,
		})
	}
	return out, nil
}

// ParkedSlotViews returns every PARKED placement as the scan rotation sees it.
//
// It is a separate read from SlotsByState because QueuedSlot is a STATE
// projection and the rotation paces on three columns it does not carry: the
// whitelist the slot exists to watch, the smoothed spread that weights it, and
// the last scan stamp that makes it due. YardCadence is deliberately left zero —
// it is a knob, and the coordinator stamps it, because an adapter that invented
// a cadence would be making an operator's decision from the persistence layer.
func (p *LedgerPort) ParkedSlotViews(ctx context.Context, playerID int) ([]appSensing.SensingSlotView, error) {
	models, err := p.repo.SlotsByState(ctx, playerID, appSensing.SlotStateParked)
	if err != nil {
		return nil, err
	}
	out := make([]appSensing.SensingSlotView, 0, len(models))
	for _, m := range models {
		goods, derr := decodeWhitelistGoods(m.WhitelistGoods)
		if derr != nil {
			return nil, fmt.Errorf("failed to decode the watched goods at %q: %w", m.WaypointSymbol, derr)
		}
		view := appSensing.SensingSlotView{
			Waypoint:   m.WaypointSymbol,
			Kind:       m.SlotKind,
			State:      m.State,
			Whitelist:  goods,
			SpreadEWMA: m.SpreadEWMA,
		}
		if m.LastScanAt != nil {
			view.LastScan = *m.LastScanAt
		}
		out = append(out, view)
	}
	return out, nil
}

// decodeWhitelistGoods reads the JSON goods column. An empty column is an empty
// list rather than an error: rows written before the column had a default, and
// YARD slots, both legitimately carry nothing.
func decodeWhitelistGoods(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var goods []string
	if err := json.Unmarshal([]byte(raw), &goods); err != nil {
		return nil, err
	}
	return goods, nil
}

// SystemsByVerdict returns the systems carrying a screening verdict.
func (p *LedgerPort) SystemsByVerdict(ctx context.Context, playerID int, verdict string) ([]appSensing.ScreenedSystem, error) {
	models, err := p.repo.SystemsByVerdict(ctx, playerID, verdict)
	if err != nil {
		return nil, err
	}
	out := make([]appSensing.ScreenedSystem, 0, len(models))
	for _, m := range models {
		out = append(out, appSensing.ScreenedSystem{System: m.SystemSymbol, DepthCredits: m.DepthCredits})
	}
	return out, nil
}

// CountOwnedProbes reports how many probe hulls the ledger accounts for.
func (p *LedgerPort) CountOwnedProbes(ctx context.Context, playerID int) (int64, error) {
	return p.repo.CountOwnedProbes(ctx, playerID)
}

// TransitionSlot advances one placement, applying any field writes atomically
// with the state flip.
//
// A lost optimistic-transition race is translated to the engine's own
// ErrSlotClaimed sentinel, which is the whole point of this mapping: the engine
// must be able to tell routine contention (skip the placement) from a ledger
// that is refusing writes (stop), and it cannot import the persistence error to
// do it.
func (p *LedgerPort) TransitionSlot(
	ctx context.Context,
	playerID int,
	waypoint, fromState, toState string,
	set appSensing.SlotFields,
) error {
	err := p.repo.TransitionSlot(ctx, playerID, waypoint, fromState, toState, func(m *persistence.SensingSlotModel) {
		if set.AssignedShip != nil {
			m.AssignedShip = nullableString(*set.AssignedShip)
		}
		if set.PurchaseYard != nil {
			m.PurchaseYard = nullableString(*set.PurchaseYard)
		}
	})
	if errors.Is(err, persistence.ErrSlotStateConflict) {
		return fmt.Errorf("%s: %w", waypoint, appSensing.ErrSlotClaimed)
	}
	return err
}

// MarkScanned records that a parked slot was scanned, and what its prices showed.
//
// It writes the freshness stamp and the smoothed spread and nothing else. A slot
// that no longer exists is an error rather than an upsert — the repository will
// not conjure a placement row from a scan.
//
// WHY IT IS SAFE TO RUN CONCURRENTLY WITH THE RECONCILE: the write sets are
// disjoint BY COLUMN (sp-wgjb7). last_scan_at and spread_ewma have exactly one
// writer — this one — and every other writer of sensing_slots names only the
// columns it owns: a transition writes state (plus the hull or yard it actually
// changed), a screen re-declaration writes what it measured, a stand-down writes
// which hull is standing where. Whatever rows these paths meet on, and in
// whatever order they commit, none of them can carry a scan back to a stale
// value. See sensingSlotMetadataUpdateColumns in the repository for the
// full ownership table.
//
// This used to rest on an invariant instead, and it is worth knowing why that was
// not enough. TransitionSlot re-wrote the WHOLE row from the copy it loaded at the
// top of its transaction, so a scan committing inside that window was reverted;
// what kept the two apart was that the rotation admitted only PARKED MARKET/YARD
// slots while the only PARKED→X transition skipped non-SPARE kinds, so they never
// met on a ROW. That held only for as long as nobody transitioned a parked market
// — which the slot reaper (sp-l3f3d) does by design.
//
// The kind-based separation still holds and is still worth keeping as
// defence in depth, but it is no longer what makes this safe.
func (p *LedgerPort) MarkScanned(ctx context.Context, playerID int, waypoint string, at time.Time, spreadEWMA float64) error {
	return p.repo.MarkScanned(ctx, playerID, waypoint, at, spreadEWMA)
}

// Systems returns every system row the player holds, carrying the uncharted
// count and the charting errand the expansion engine drives off.
func (p *LedgerPort) Systems(ctx context.Context, playerID int) ([]appSensing.ExpandSystem, error) {
	models, err := p.repo.Systems(ctx, playerID)
	if err != nil {
		return nil, err
	}
	out := make([]appSensing.ExpandSystem, 0, len(models))
	for _, m := range models {
		out = append(out, appSensing.ExpandSystem{
			System:         m.SystemSymbol,
			Verdict:        m.Verdict,
			UnchartedCount: m.UnchartedCount,
			SeedShip:       derefString(m.SeedShip),
			SeedState:      derefString(m.SeedState),
			CatalogKnown:   m.CatalogSyncedAt != nil,
		})
	}
	return out, nil
}

// UpsertSystem records a system's screening verdict.
//
// A screen that found the waypoint catalog known stamps that fact, so a system
// the fleet swept long before this model existed is recognised on its first
// screen instead of being sent a charting seed to rediscover a catalog already
// sitting in the database. A screen that did NOT find it known leaves the stamp
// NULL, which is what keeps the system a seed target.
func (p *LedgerPort) UpsertSystem(ctx context.Context, playerID int, record appSensing.SystemRecord) error {
	now := time.Now().UTC()
	model := persistence.SensingSystemModel{
		PlayerID:       playerID,
		SystemSymbol:   record.System,
		Verdict:        record.Verdict,
		UnchartedCount: record.UnchartedCount,
		DepthCredits:   record.DepthCredits,
		// screened_at is stamped on EVERY verdict write, PENDING included: this
		// call IS the screening, so the column answers "when was this system last
		// looked at". That is the question an operator asks of a system stuck
		// PENDING — whether the sweep is reaching it at all — and leaving the
		// column NULL beside a populated catalog_synced_at read as though nothing
		// had ever screened it.
		ScreenedAt: &now,
	}
	if record.CatalogKnown {
		model.CatalogSyncedAt = &now
	}
	return p.repo.UpsertSystem(ctx, model)
}

// StampCatalogSynced records that a system's waypoint list has been swept.
func (p *LedgerPort) StampCatalogSynced(ctx context.Context, playerID int, system string) error {
	return p.repo.StampCatalogSynced(ctx, playerID, system, time.Now().UTC())
}

// UpsertSlotMetadata declares a placement the screen wants: what the waypoint
// deals in, and how deep its book is.
//
// On a waypoint that already carries a placement it refreshes those measurements
// and leaves the placement's progress alone — see sensingSlotMetadataUpdateColumns
// for why writing state or assigned_ship from here is a money bug rather than an
// inaccuracy.
func (p *LedgerPort) UpsertSlotMetadata(ctx context.Context, playerID int, slot appSensing.SlotRecord) error {
	model, err := slotModel(playerID, slot)
	if err != nil {
		return err
	}
	return p.repo.UpsertSlotMetadata(ctx, model)
}

// UpsertSpareSlot records a hull already standing at a waypoint as a placement: a
// charting seed standing down, or a probe adopted from a retired scout post.
//
// On conflict it re-points the row at this hull and leaves the screen's
// measurements and the scan history where they are.
func (p *LedgerPort) UpsertSpareSlot(ctx context.Context, playerID int, slot appSensing.SlotRecord) error {
	model, err := slotModel(playerID, slot)
	if err != nil {
		return err
	}
	return p.repo.UpsertSpareSlot(ctx, model)
}

// slotModel maps a placement record onto its row. Both upsert variants insert the
// WHOLE row on a waypoint that has none, so they share this shape; they differ
// only in what they re-assert when one is already there.
func slotModel(playerID int, slot appSensing.SlotRecord) (persistence.SensingSlotModel, error) {
	goods, err := json.Marshal(slot.WhitelistGoods)
	if err != nil {
		return persistence.SensingSlotModel{}, fmt.Errorf("failed to encode the whitelisted goods at %q: %w", slot.Waypoint, err)
	}
	return persistence.SensingSlotModel{
		PlayerID:       playerID,
		WaypointSymbol: slot.Waypoint,
		SystemSymbol:   slot.System,
		SlotKind:       slot.Kind,
		State:          slot.State,
		AssignedShip:   nullableString(slot.AssignedShip),
		WhitelistGoods: string(goods),
		DepthCredits:   slot.DepthCredits,
	}, nil
}

// SetSeed writes a system's charting errand and only that.
func (p *LedgerPort) SetSeed(ctx context.Context, playerID int, system, shipSymbol, seedState string) error {
	return p.repo.SetSeed(ctx, playerID, system, shipSymbol, seedState)
}

// DeleteSlot removes a placement row outright — the one write that hands a
// parked spare's hull from the ledger to a charting errand.
func (p *LedgerPort) DeleteSlot(ctx context.Context, playerID int, waypoint string) error {
	return p.repo.DeleteSlot(ctx, playerID, waypoint)
}

// queuedSlots flattens ledger rows into the engine's placement view.
func queuedSlots(models []persistence.SensingSlotModel) []appSensing.QueuedSlot {
	out := make([]appSensing.QueuedSlot, 0, len(models))
	for _, m := range models {
		out = append(out, appSensing.QueuedSlot{
			Waypoint:     m.WaypointSymbol,
			System:       m.SystemSymbol,
			Kind:         m.SlotKind,
			State:        m.State,
			AssignedShip: derefString(m.AssignedShip),
			PurchaseYard: derefString(m.PurchaseYard),
			DepthCredits: m.DepthCredits,
		})
	}
	return out
}

// nullableString maps the engine's "" back onto a NULL column. Storing an empty
// string instead would leave a slot matching the probe-count's "has a hull"
// predicate with no hull behind it, inflating the count against a released
// spare.
func nullableString(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// derefString flattens a nullable column to the empty string.
func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
