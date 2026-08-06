package parkedsensing

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	shipyardDomain "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

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
	// listings persists what a quote learns about a yard's stock. OPTIONAL: a nil
	// writer quotes exactly as before and simply records nothing.
	listings shipyardDomain.InventoryRepository
}

// NewProbePurchasePort wires the price-and-buy port.
//
// listings may be nil, in which case nothing is persisted and every quote costs a
// live call, which is the behaviour that existed before the listing memo.
func NewProbePurchasePort(mediator common.Mediator, shipRepo navigation.ShipRepository, listings shipyardDomain.InventoryRepository) *ProbePurchasePort {
	return &ProbePurchasePort{mediator: mediator, shipRepo: shipRepo, listings: listings}
}

// persistListings records the whole shipyard listing set a quote just read.
//
// THE WHOLE SET, not a "no probe" marker. It is the same API response either way,
// it is what shipyard_inventory already stores for every other consumer (the
// autosizer's yard price signal and ListProbeYards both read it), and a bespoke
// negative marker would be a second store to keep in step with this one.
//
// Rows are derived from ShipTypes rather than from the priced Listings, because
// the two differ and the difference is load-bearing: SpaceTraders returns priced
// listings only where a hull is present, while the type list is always given. A
// type with no priced listing is stored at price 0, which ShipTypeAvailability
// documents as "listed, availability known, never usable by a price guard" — so
// the memo can tell "this yard does not sell probes" from "this yard sells probes
// we could not price", and only the first is a reason to stop asking.
//
// BEST-EFFORT: a write failure is logged and swallowed. The quote itself succeeded
// and the caller needs its price; failing the purchase path because a cache write
// missed would trade a real buy for a bookkeeping error. The only cost of a missed
// write is that the yard is asked again next tick — exactly today's behaviour.
func (p *ProbePurchasePort) persistListings(ctx context.Context, playerID int, yardWaypoint string, yard shipyardDomain.Shipyard, now time.Time) {
	if p.listings == nil {
		return
	}
	priced := make(map[string]int, len(yard.Listings))
	for _, listing := range yard.Listings {
		if listing.PurchasePrice > priced[listing.ShipType] {
			priced[listing.ShipType] = listing.PurchasePrice
		}
	}
	system := shared.ExtractSystemSymbol(yardWaypoint)
	rows := make([]shipyardDomain.ShipTypeAvailability, 0, len(yard.ShipTypes))
	for _, shipType := range yard.ShipTypes {
		rows = append(rows, shipyardDomain.ShipTypeAvailability{
			SystemSymbol:   system,
			WaypointSymbol: yardWaypoint,
			ShipType:       shipType,
			PurchasePrice:  priced[shipType],
			LastScanned:    now,
		})
	}
	if len(rows) == 0 {
		// A shipyard that lists no types at all tells us nothing we can act on, and
		// writing zero rows would read back as "never scanned" anyway. Left alone
		// rather than written, so the distinction stays honest.
		return
	}
	if err := p.listings.ReplaceScan(ctx, playerID, system, yardWaypoint, rows, now); err != nil {
		logging.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf(
			"Sensing quote at %s could not persist the shipyard's listings; the yard will be re-quoted next tick: %v", yardWaypoint, err), map[string]interface{}{
			"action": "parked_sensing_listing_persist_failed", "waypoint": yardWaypoint,
		})
	}
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
		// PRE-COMMIT, despite living in the sensing adapter. The buy queue
		// subtracts this quote from the running treasury and compares the result
		// against the working-capital floor, and buys on the very next line if it
		// clears (application/parkedsensing/buyqueue.go, fillSlot). A quote served
		// from store would be a stale number a live floor check is spending
		// against, so this read is Earning: metered, never denied (RULINGS #4).
		Class: marketscan.Earning,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to read shipyard listings at %s: %w", yardWaypoint, err)
	}
	listings, ok := resp.(*shipyardQueries.GetShipyardListingsResponse)
	if !ok {
		return 0, fmt.Errorf("shipyard at %s returned no listings", yardWaypoint)
	}
	// Persisted BEFORE the probe check, so the yards that teach us the MOST — the
	// ones that sell no probe and are about to fail this quote — are exactly the
	// ones whose answer is recorded. Recording only on success would leave the
	// re-quote loop this memo exists to break running forever.
	p.persistListings(ctx, playerID, yardWaypoint, listings.Shipyard, time.Now())

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
		// This engine names itself in the ledger. Left at the default it would be
		// booked "fleet expansion" alongside the frontier engine's probes, which is
		// what made a live money leak indistinguishable from the engine the operator
		// had just switched off. See appSensing.SensingCoverageOperationType.
		OperationType: appSensing.SensingCoverageOperationType,
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
// refuses the write, so no purchase can complete. The empty-owner guard below is
// what stops that failing silently, in any form.
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
