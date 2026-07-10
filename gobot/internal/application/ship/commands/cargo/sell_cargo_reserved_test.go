package cargo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- sp-1vhv reserved-cargo money guard -------------------------------------
//
// The loss: MODULE_CARGO_HOLD_III bought for 196,751cr, then auto-sold by a tour
// coordinator for 97,033cr an hour later because it treated hold contents as
// sellable manifest. The guard sits at the single choke point every sell funnels
// through (CargoTransactionHandler, behind SellCargoHandler): a reserved good is
// refused BEFORE any API call. These drive that real handler.

// reservedSpyAPI records every SellCargo that reaches the API, so a test can prove
// the reserved-cargo guard refuses the sale before any credits move.
type reservedSpyAPI struct {
	domainPorts.APIClient
	sells int
}

func (c *reservedSpyAPI) SellCargo(_ context.Context, _, _ string, units int, _ string) (*domainPorts.SellResult, error) {
	c.sells++
	return &domainPorts.SellResult{TotalRevenue: units * 20, UnitsSold: units}, nil
}

func newReservedSellHandler(t *testing.T, ship *navigation.Ship) (*SellCargoHandler, *reservedSpyAPI) {
	t.Helper()
	api := &reservedSpyAPI{}
	marketRepo := &buyFakeMarketRepo{}
	shipRepo := &buyFakeShipRepo{ship: ship}
	playerRepo := &buyFakePlayerRepo{player: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "tok")}
	h := NewSellCargoHandler(shipRepo, playerRepo, api, marketRepo, &buyRecordingMediator{}, nil)
	return h, api
}

func runReservedSell(t *testing.T, h *SellCargoHandler, good string, units int) *SellCargoResponse {
	t.Helper()
	ctx := auth.WithPlayerToken(context.Background(), "tok")
	resp, err := h.Handle(ctx, &SellCargoCommand{
		ShipSymbol: "OPTYPE-1", GoodSymbol: good, Units: units, PlayerID: shared.MustNewPlayerID(1),
	})
	require.NoError(t, err)
	return resp.(*SellCargoResponse)
}

// THE incident: a MODULE_ in the hold is reserved by DEFAULT (no persisted state),
// so a coordinator's sell is refused — zero credits moved, module held aboard, and
// the SellCargo API is never called.
func TestSellCargo_ReservedModule_NotSold(t *testing.T) {
	ship := newDockedShipWithCargo(t, 1, "MODULE_CARGO_HOLD_III", 1)
	h, api := newReservedSellHandler(t, ship)

	sr := runReservedSell(t, h, "MODULE_CARGO_HOLD_III", 1)

	require.True(t, sr.Reserved, "a reserved module sell must report Reserved")
	require.Equal(t, 0, sr.UnitsSold, "no units of a reserved module may sell")
	require.Equal(t, 0, sr.TotalRevenue, "no revenue may be booked for a reserved module")
	require.Equal(t, 0, api.sells, "the SellCargo API must never be called for reserved cargo")
}

// Explicit-UNreserve allows the deliberate resale: the same module sells once the
// per-hull override releases it.
func TestSellCargo_UnreservedModule_SellsNormally(t *testing.T) {
	ship := newDockedShipWithCargo(t, 1, "MODULE_CARGO_HOLD_III", 1)
	ship.SetCargoReservation("MODULE_CARGO_HOLD_III", false) // deliberate resale
	h, api := newReservedSellHandler(t, ship)

	sr := runReservedSell(t, h, "MODULE_CARGO_HOLD_III", 1)

	require.False(t, sr.Reserved, "an explicitly unreserved module must not be guarded")
	require.Equal(t, 1, sr.UnitsSold, "the released module must sell")
	require.Equal(t, 1, api.sells, "the SellCargo API must be called for a released module")
}

// Regression: an ordinary trade good sells exactly as before — the guard touches
// nothing but reserved cargo.
func TestSellCargo_UnreservedGood_SellsUnchanged(t *testing.T) {
	ship := newDockedShipWithCargo(t, 1, optypeGood, 40)
	h, api := newReservedSellHandler(t, ship)

	sr := runReservedSell(t, h, optypeGood, 40)

	require.False(t, sr.Reserved, "a trade good must never be reserved")
	require.Equal(t, 40, sr.UnitsSold, "the whole order must sell")
	require.Greater(t, api.sells, 0, "the SellCargo API must be called for a trade good")
}

// Fail-closed: a hull whose persisted override state is unreadable refuses to sell
// even an ordinary trade good — a read failure never converts reserved cargo into
// manifest (RULINGS #4).
func TestSellCargo_CorruptReservationState_FailsClosed(t *testing.T) {
	ship := newDockedShipWithCargo(t, 1, optypeGood, 40)
	ship.SetReservationOverrides(nil, true) // corrupt/unreadable override state
	h, api := newReservedSellHandler(t, ship)

	sr := runReservedSell(t, h, optypeGood, 40)

	require.True(t, sr.Reserved, "corrupt override state must fail closed")
	require.Equal(t, 0, api.sells, "no sale may reach the API while the override state is unreadable")
}
