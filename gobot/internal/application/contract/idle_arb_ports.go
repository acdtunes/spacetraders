package contract

import (
	"context"

	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
)

// IdleArbLauncher starts one recovery-safe, guarded one-shot arb container and
// confirms the hull is CLAIMED (atomically, operation-checked) before
// returning. Implemented by the daemon server; the dispatcher stays a pure
// decision loop (RULINGS #3: new operations are daemon containers, and the
// daemon remains the single writer of ship state).
type IdleArbLauncher interface {
	LaunchIdleArb(ctx context.Context, spec IdleArbSpec) (containerID string, err error)
}

// TreasuryReader reports the player's live credit balance for the dispatcher's
// working-capital reserve gate (sp-zq635 §4a). Optional, wired via SetTreasuryReader:
// a nil reader leaves the gate inert (the pre-sp-zq635 behavior), the same optional-port
// contract the absorption ledger and re-homer use. Production wires it so the pass's
// concurrent legs can never collectively drain treasury below the immutable reserve.
type TreasuryReader interface {
	LiveTreasury(ctx context.Context) (int64, error)
}

// ShipHomer re-homes ONE idle dedicated hull to its balanced standby station
// through the EXISTING HomeShipCommand path — never a parallel homing
// algorithm (RULINGS #7). A narrow port, implemented by the coordinator over
// the mediator and faked trivially in tests, that keeps the dispatcher a pure
// decision loop.
//
// HomeShip must return as soon as the home is DISPATCHED, not when the hull
// lands: HomeShipCommand navigates synchronously and blocks until the hull
// arrives, so a blocking call here would stall an entire dispatch tick for a
// full flight. The hull is marked in-transit within a hop, so the next
// discovery pass excludes it (FindIdleShipsByFleet skips in-transit hulls); a
// returned error means the home could not even be dispatched, and the
// dispatcher leaves the hull for the next pass.
type ShipHomer interface {
	// HomeShip re-homes shipSymbol to the balanced standby station of the given
	// standbyStations set. The set is passed per call rather than frozen in the
	// homer, so the dispatcher can hand it the LIVE hub set it resolved this
	// pass — a `fleet hub add|remove` is honored with no restart.
	HomeShip(ctx context.Context, shipSymbol string, standbyStations []string) error
}

// ContractGoodsProvider lists the delivery goods of the player's OPEN
// contracts so the dispatcher never dispatches an arb leg on a good we are
// actively sourcing for a contract — the idle harvest must never compete
// with, or bid up, our own contract sourcing. A narrow port (not the full
// ContractRepository) keeps the dispatcher testable with a trivial fake.
type ContractGoodsProvider interface {
	// OpenContractGoods returns the set of trade symbols under the player's
	// active contracts. An error is fatal to a dispatch pass (fail-closed): the
	// dispatcher would rather skip a tick than risk sourcing-competition it
	// cannot rule out.
	OpenContractGoods(ctx context.Context, playerID int) (map[string]struct{}, error)
}

// activeContractGoods adapts the domain ContractRepository to
// ContractGoodsProvider by reading every active contract's delivery symbols.
type activeContractGoods struct {
	repo domainContract.ContractRepository
}

// NewActiveContractGoods wires the default provider over the contract repo.
func NewActiveContractGoods(repo domainContract.ContractRepository) ContractGoodsProvider {
	return activeContractGoods{repo: repo}
}

func (a activeContractGoods) OpenContractGoods(ctx context.Context, playerID int) (map[string]struct{}, error) {
	contracts, err := a.repo.FindActiveContracts(ctx, playerID)
	if err != nil {
		return nil, err
	}
	goods := make(map[string]struct{})
	for _, c := range contracts {
		for _, delivery := range c.Terms().Deliveries {
			goods[delivery.TradeSymbol] = struct{}{}
		}
	}
	return goods, nil
}
