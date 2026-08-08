package commands

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
)

// The BUY-PATH PORTS shared by every hull-buying coordinator in this package, declared here rather
// than beside any one coordinator's reconcile loop.
//
// Every one is nil-safe by contract: a reader that cannot answer reports readable=false (never a
// zero balance, never a fabricated count), and the guard stack fails CLOSED on that (RULINGS #4).

// TreasuryReader reads the player's live credit balance. readable=false ⇒ the treasury guards fail
// closed (a buy must never proceed on an unknown balance).
type TreasuryReader interface {
	Treasury(ctx context.Context, playerID int) (credits int64, readable bool, err error)
}

// HeavyCensusReader counts owned HEAVY HULLS regardless of fleet tag. Deliberately NOT the
// trade-pool count: that one sizes the pool, this one bounds capital exposure, and a tag-scoped
// count would make a heavy tagged elsewhere invisible and authorise re-buying a hull we own.
// An error ⇒ the heavy cap guard fails CLOSED.
type HeavyCensusReader interface {
	HeaviesOwned(ctx context.Context, playerID int) (int, error)
}

// HeavyYardReader reports the yard the purchase path would TARGET this tick, and its ask —
// deliberately NOT the cheapest ask on the map, which under-reserves whenever the nearest reachable
// yard is dearer. The same read backs the sensing buy-floor through ONE shared implementation, so
// the spender and the withholder can never save toward different yards.
type HeavyYardReader interface {
	HeavyTarget(ctx context.Context, playerID int) (HeavyTargetYard, error)
}

// HeavyTargetYard is the heavy purchase target as the reservation sees it. CapabilityOpen (a known
// yard sells the hull, priced or not) and Priced (a money guard can act) are SEPARATE because a yard
// discovered without presence is known and unpriceable at once — the gap the pricing errand closes.
type HeavyTargetYard struct {
	CapabilityOpen bool
	Priced         bool
	WaypointSymbol string
	PurchasePrice  int64
}

// APIUtilizationReader reads the sustained request-utilization percent. readable=false ⇒ the
// API-util guard fails CLOSED: an unreadable/absent utilization surface holds concurrency growth.
type APIUtilizationReader interface {
	UtilizationPct(ctx context.Context) (pct float64, readable bool, err error)
}

// The buy port's shapes are shared with the contract scaler and live in domain/hullbuy; these
// aliases keep the coordinator's internals reading as one package (see hull_classes.go).
type (
	YardPriceReader = hullbuy.YardPriceReader
	BuyOrder        = hullbuy.BuyOrder
	BuyResult       = hullbuy.BuyResult
)

// Purchaser buys ONE hull and dedicates it in the same breath, stamping DedicatedFleet before any
// coordinator tick can see an undedicated idle hull.
type Purchaser interface {
	BuyAndDedicate(ctx context.Context, order BuyOrder) (BuyResult, error)
}

// PurchaseNotifier posts a captain purchase notice — a buy is real news (parentless-equivalent).
type PurchaseNotifier interface {
	NotifyPurchase(ctx context.Context, playerID int, class HullClass, shipType string, price int64, note string) error
}

// MetricsSink records a buying coordinator's observation series (pure observation; nil-safe).
type MetricsSink interface {
	RecordDemand(class HullClass, demand, current int)
	RecordPurchase(class HullClass)
	RecordBlocked(class HullClass, guard GuardName)
	// RecordZeroEffectAlarm fires when demand persisted but the coordinator bought nothing for
	// zero_effect_alarm_ticks consecutive ticks — a fleet-level "stuck" signal, not per-class.
	RecordZeroEffectAlarm()
	// RecordHeavyReserve reports the per-tick heavy facts, EVERY tick. Reserve and target are both
	// required: since the hold became treasury-bounded, reserve=0 means either "nothing to save for"
	// or "saving toward an ask this era cannot reach", and only target tells them apart.
	RecordHeavyReserve(playerID string, reserve, target int64, owned, cap int)
	// ObserveHeavyPricePremium reports what one purchase paid above the cheapest KNOWN ask, in
	// percent — the cost of buying at the cheapest yard WITH PRESENCE.
	ObserveHeavyPricePremium(playerID string, paid, cheapestKnown int64)
}

// decisionWord renders a guard-stack verdict for the per-decision park line.
func decisionWord(d PurchaseDecision) string {
	if d.Approved {
		return "APPROVED"
	}
	return "BLOCKED by " + string(d.BlockedBy)
}
