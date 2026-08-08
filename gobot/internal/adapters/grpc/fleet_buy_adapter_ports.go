package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// agentReader is the narrow slice of *api.SpaceTradersClient the money guards need (treasury).
// Declared here so the ports depend on behaviour, not the whole client.
type agentReader interface {
	GetAgent(ctx context.Context, token string) (*player.AgentData, error)
}

// fleetTreasuryReader answers the autosizer's (and the contract scaler's) treasury
// cushion guard. It prefers the LEDGER (sp-muq66) — the same balance, with no API call —
// and only falls back to a live read when the ledger is too old to trust; the fallback
// lives inside the ledger reader, so this type states the guard's contract and nothing
// more. An unwired ledger keeps the direct live read, which is what the daemon did before
// and what every test that constructs this type bare still exercises.
//
// Unreadable is reported as readable=false, never as a zero balance: a guard that cannot
// read treasury must refuse to buy, not size a purchase against 0 (RULINGS #4).
type fleetTreasuryReader struct {
	api    agentReader
	ledger *persistence.LedgerTreasury
}

func (r *fleetTreasuryReader) Treasury(ctx context.Context, playerID int) (int64, bool, error) {
	if r.ledger != nil {
		credits, err := r.ledger.Credits(ctx, playerID)
		if err != nil {
			return 0, false, nil // unreadable → the treasury guards fail closed
		}
		return credits, true, nil
	}
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, false, nil // no token in ctx → unreadable → the treasury guards fail closed
	}
	agent, err := r.api.GetAgent(ctx, token)
	if err != nil || agent == nil {
		return 0, false, nil
	}
	return int64(agent.Credits), true, nil
}

// apiBudgetReporter is the narrow read the API-util guard needs — the rolling utilization snapshot.
// Satisfied by *metrics.APIBudgetTracker (the daemon-startup singleton, fed one event per API attempt
// on the request path). Declared as an interface so the reader depends on behaviour, not the tracker.
type apiBudgetReporter interface {
	Report() apibudget.DualReport
}

// fleetAPIUtilReader surfaces the fleet-wide API request-utilization percent to the autosizer's
// api_util guard. It reads the rolling-5m window of the shared budget tracker — the SAME
// throughput/ceiling basis as the Prometheus ApproachCeiling alert (sum(rate(api_requests_total[5m]))
// / RateLimitPerSecond) — so the guard gates concurrency GROWTH against genuine API saturation.
// Fails CLOSED (readable=false) when no live surface exists (nil
// tracker, or an unconfigured/zero ceiling): a guard that cannot read its bound never permits growth
// (RULINGS #4). In the daemon the tracker is wired unconditionally at startup, so the normal case is
// readable; blocking only occurs on real saturation or a genuinely-absent metrics subsystem.
// THE TRACKER IS RESOLVED AT READ TIME, NOT CAPTURED AT WIRING TIME (sp-a75fz).
//
// It used to hold the pointer that metrics.GetGlobalAPIBudgetTracker() returned during wiring. That
// was correct ONLY because main.go happens to construct the tracker (:778) before it wires the
// autosizer (:1196), and nothing enforced, tested or documented that ordering.
//
// REVERSE IT AND THE FLEET STOPS GROWING, SILENTLY AND FOREVER. The captured pointer would be nil,
// the nil guard below would fail CLOSED — which is correct, and is exactly why nobody would find
// it — so GuardAPIUtil would refuse every hull purchase, everywhere, with no error and no metric.
// It would read as "the autosizer decided not to grow". That is the sp-ps2oc failure shape: a
// wiring assumption nothing pins, whose breach is invisible.
//
// A resolver function makes the ordering irrelevant rather than merely checked. The field can no
// longer HOLD a tracker, so there is no wiring-time capture left to get wrong — the bug is
// unexpressible in this type rather than absent from this instance. It also degrades better in the
// world where the order does reverse: a nil read is TRANSIENT and self-heals on the next tick once
// the global is set, where a captured nil was permanent for the life of the process.
//
// The per-read cost is one package-variable load on a reconcile tick that already does database and
// API work — not a hot path, and the guard is consulted once per sizing decision.
//
// Fails CLOSED (readable=false) when no live surface exists: no resolver, a resolver returning
// nothing, or an unconfigured/zero ceiling. A guard that cannot read its bound never permits growth
// (RULINGS #4). In the daemon the tracker is wired unconditionally at startup, so the normal case is
// readable and blocking only occurs on real saturation or a genuinely-absent metrics subsystem.
type fleetAPIUtilReader struct{ resolve func() apiBudgetReporter }

// globalAPIBudgetReporter adapts the package-level accessor to the reader's narrow interface.
//
// THE TYPED-NIL CONVERSION IS DELIBERATE AND MUST STAY EXPLICIT. GetGlobalAPIBudgetTracker returns a
// *metrics.APIBudgetTracker; assigning a nil one into an interface yields a NON-nil interface
// holding a nil pointer, which sails past `== nil`. Returning the untyped nil instead keeps the
// reader's own nil check meaningful rather than leaving it to the nil-receiver Report() one layer
// down. Both paths fail closed — this one just fails closed legibly.
func globalAPIBudgetReporter() apiBudgetReporter {
	tracker := metrics.GetGlobalAPIBudgetTracker()
	if tracker == nil {
		return nil
	}
	return tracker
}

func (r *fleetAPIUtilReader) UtilizationPct(ctx context.Context) (float64, bool, error) {
	if r == nil || r.resolve == nil {
		return 0, false, nil // no utilization surface wired → unreadable → guard fails CLOSED
	}
	reporter := r.resolve()
	if reporter == nil {
		return 0, false, nil // resolved to nothing this tick → unreadable → guard fails CLOSED
	}
	rolling := reporter.Report().Rolling5m
	if rolling.CeilingReqPerSec <= 0 {
		// A typed-nil tracker's nil-safe Report() (or a tracker built with no ceiling) yields a
		// zero-value report; without a ceiling there is no meaningful utilization → fail CLOSED
		// rather than let a spurious readable 0% permit unbounded growth.
		return 0, false, nil
	}
	return rolling.UtilizationPct, true, nil
}

// fleetHullPurchaser buys one hull through the money-integrity batch path, then dedicates it.
// It is driven by BOTH the autosizer and the dedicated contract scaler, so it speaks the neutral
// hullbuy vocabulary rather than either coordinator's.
type fleetHullPurchaser struct {
	med      common.Mediator
	shipRepo navigation.ShipRepository
	posts    scoutPostRoster // the borrowed-probe restraint roster; see fleet_buy_pairing.go
}

func (p *fleetHullPurchaser) BuyAndDedicate(ctx context.Context, order hullbuy.BuyOrder) (hullbuy.BuyResult, error) {
	pid, err := shared.NewPlayerID(order.PlayerID)
	if err != nil {
		return hullbuy.BuyResult{}, err
	}
	// The hull that signs must ALREADY STAND at the yard: the batch path's money-integrity guard
	// still runs, its navigation step simply never does.
	ships, err := p.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return hullbuy.BuyResult{}, err
	}
	manned, rosterOK := buyerRoster(ctx, p.posts, order.PlayerID)
	buyers := purchaseBuyers(ships, manned, rosterOK)
	if len(buyers) == 0 {
		return hullbuy.BuyResult{}, fmt.Errorf("no idle claimable hull available to execute the purchase")
	}
	// FAIL CLOSED and NAMED, never a quiet flight instead.
	buyer, standing := standingBuyerAt(buyers, order.Yard)
	if !standing {
		return hullbuy.BuyResult{}, fmt.Errorf("no claimable hull of ours stands at %s — refusing to fly one in to buy %s", order.Yard, order.ShipType)
	}
	purchaser := buyer.ship.ShipSymbol()

	// A BORROWED probe is held for the purchase and handed straight back — the claim is what stops its
	// own engine flying it out from under a buy about to dock it. FAIL CLOSED, release deferred.
	if buyer.Borrowed {
		release, cerr := p.holdBorrowedBuyer(ctx, pid, purchaser, buyer.ship.DedicatedFleet(), order.ContainerID)
		if cerr != nil {
			return hullbuy.BuyResult{}, cerr
		}
		defer release()
	}

	resp, err := p.med.Send(ctx, &shipyardCmd.BatchPurchaseShipsCommand{
		PurchasingShipSymbol: purchaser,
		ShipType:             order.ShipType,
		Quantity:             1,
		MaxBudget:            0,
		PlayerID:             pid,
		ShipyardWaypoint:     order.Yard,
	})
	if err != nil {
		return hullbuy.BuyResult{}, err
	}
	batch, ok := resp.(*shipyardCmd.BatchPurchaseShipsResponse)
	if !ok || batch.ShipsPurchasedCount == 0 || len(batch.PurchasedShips) == 0 {
		return hullbuy.BuyResult{}, fmt.Errorf("purchase returned no ship")
	}
	bought := batch.PurchasedShips[0]

	// Dedicate-at-purchase: tag heavy/explorer/contract hulls to their fleet in the same breath so no
	// coordinator tick can adopt them first. Idempotent; lights get no tag (they ARE workers) — the
	// asymmetry, and why it is load-bearing, is on hullbuy.DedicatedFleet.
	dedicated := false
	if fleet := hullbuy.DedicatedFleet(order.Class); fleet != "" {
		if aerr := p.shipRepo.AssignFleet(ctx, bought.ShipSymbol(), fleet, pid); aerr != nil {
			return hullbuy.BuyResult{}, fmt.Errorf("bought %s but failed to dedicate to %q: %w", bought.ShipSymbol(), fleet, aerr)
		}
		dedicated = true
	}
	return hullbuy.BuyResult{ShipSymbol: bought.ShipSymbol(), Price: int64(batch.TotalCost), Dedicated: dedicated}, nil
}

// fleetPurchaseNotifier records a purchase as a captain event — a buy is real news.
type fleetPurchaseNotifier struct{ store captain.EventStore }

func (n *fleetPurchaseNotifier) NotifyPurchase(ctx context.Context, playerID int, class fleetCmd.HullClass, shipType string, price int64, note string) error {
	if n.store == nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"class": string(class), "ship_type": shipType, "price": price, "note": note,
	})
	return n.store.Record(ctx, &captain.Event{
		Type:     captain.EventFleetAutosizerPurchase,
		Ship:     string(class),
		PlayerID: playerID,
		Payload:  string(payload),
	})
}

// holdBorrowedBuyer takes an exclusive claim on a hull the buy is BORROWING and returns the
// hand-back. The operation is the hull's OWN dedication, so ClaimShip admits it with no tag changing
// hands (RULINGS #7), and the exclusivity keeps its engine from moving it mid-purchase (RULINGS #3).
//
// FAIL CLOSED before any money moves: no owning container id means no claim is possible (the column
// carries a foreign key), and an unclaimable hull is not a signer. The release is idempotent and its
// failure a WARN — the purchase has happened, and boot's sweep is the backstop.
func (p *fleetHullPurchaser) holdBorrowedBuyer(ctx context.Context, pid shared.PlayerID, buyer, ownFleet, containerID string) (func(), error) {
	if strings.TrimSpace(containerID) == "" {
		return nil, fmt.Errorf("borrowed signer %s refused (fail-closed): the buy carries no owning container id, so no claim can be held over it", buyer)
	}
	if err := p.shipRepo.ClaimShip(ctx, buyer, containerID, pid, ownFleet); err != nil {
		return nil, fmt.Errorf("borrowed signer %s claim failed (fail-closed, no concurrent driver): %w", buyer, err)
	}
	return func() {
		if _, err := p.shipRepo.ReleaseContainerClaim(ctx, buyer, pid, borrowedBuyerReleaseReason); err != nil {
			common.LoggerFromContext(ctx).Log("WARN", "borrowed purchase signer release failed (the boot release sweep is the backstop)", map[string]interface{}{
				"action": "fleet_buy_borrowed_signer_release", "ship_symbol": buyer, "error": err.Error(),
			})
		}
	}, nil
}

// borrowedBuyerReleaseReason names the hand-back on the ship row.
const borrowedBuyerReleaseReason = "hull purchase signed; borrowed hull returned to its fleet"
