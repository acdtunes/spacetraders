package common

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// fakeTransferAPI embeds the full APIClient (nil) and overrides only the four
// methods the transfer seam uses: GetShip, OrbitShip, DockShip, TransferCargo. Any
// other call panics, keeping the fake honest about what the code under test touches.
type fakeTransferAPI struct {
	domainPorts.APIClient
	nav          map[string]string // symbol -> nav status ("DOCKED"/"IN_ORBIT")
	getShipErr   error
	orbitCalls   []string
	dockCalls    []string
	transferErrs []error // per-call errors; index past the slice (or nil entry) = success
	transferSeen [][2]string
}

func newFakeTransferAPI(nav map[string]string) *fakeTransferAPI {
	return &fakeTransferAPI{nav: nav}
}

func (f *fakeTransferAPI) GetShip(_ context.Context, symbol, _ string) (*navigation.ShipData, error) {
	if f.getShipErr != nil {
		return nil, f.getShipErr
	}
	st, ok := f.nav[symbol]
	if !ok {
		st = string(navigation.NavStatusInOrbit)
	}
	return &navigation.ShipData{Symbol: symbol, NavStatus: st}, nil
}

func (f *fakeTransferAPI) OrbitShip(_ context.Context, symbol, _ string) error {
	f.orbitCalls = append(f.orbitCalls, symbol)
	if f.nav == nil {
		f.nav = map[string]string{}
	}
	f.nav[symbol] = string(navigation.NavStatusInOrbit)
	return nil
}

func (f *fakeTransferAPI) DockShip(_ context.Context, symbol, _ string) error {
	f.dockCalls = append(f.dockCalls, symbol)
	if f.nav == nil {
		f.nav = map[string]string{}
	}
	f.nav[symbol] = string(navigation.NavStatusDocked)
	return nil
}

func (f *fakeTransferAPI) TransferCargo(_ context.Context, from, to, _ string, _ int, _ string) (*domainPorts.TransferResult, error) {
	i := len(f.transferSeen)
	f.transferSeen = append(f.transferSeen, [2]string{from, to})
	if i < len(f.transferErrs) && f.transferErrs[i] != nil {
		return nil, f.transferErrs[i]
	}
	return &domainPorts.TransferResult{FromShip: from, ToShip: to, UnitsTransferred: 1}, nil
}

func dockStateErr() error {
	// Mirrors the client's wrapped surface: the raw API body (carrying code 4271)
	// is embedded in the error string.
	return errors.New(`failed to transfer cargo: API error (status 400): {"error":{"message":"Both ships must be either docked or in orbit at the same location.","code":4271}}`)
}

// --- deposit direction (from = visitor, to = warehouse) ------------------

func TestAlignAndTransfer_Deposit_VisitorDockedWarehouseOrbit_OrbitsVisitorThenTransfers(t *testing.T) {
	// The exact sp-5qs1 incident: stocker arrived DOCKED, warehouse parks IN_ORBIT.
	api := newFakeTransferAPI(map[string]string{
		"VISITOR":   string(navigation.NavStatusDocked),
		"WAREHOUSE": string(navigation.NavStatusInOrbit),
	})

	result, alignedNav, err := AlignAndTransferCargo(context.Background(), api, "VISITOR", "WAREHOUSE", "WAREHOUSE", "FOOD", 80, "tok")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, []string{"VISITOR"}, api.orbitCalls, "the visitor is orbited to match the in-orbit warehouse")
	require.Empty(t, api.dockCalls, "the warehouse is never moved")
	require.Len(t, api.transferSeen, 1)
	require.Equal(t, [2]string{"VISITOR", "WAREHOUSE"}, api.transferSeen[0])
	require.Equal(t, navigation.NavStatusInOrbit, alignedNav)
}

func TestAlignAndTransfer_Deposit_VisitorOrbitWarehouseDocked_DocksVisitor(t *testing.T) {
	// Proves ALIGN-TO-WAREHOUSE, not hardcoded orbit: a docked warehouse docks the visitor.
	api := newFakeTransferAPI(map[string]string{
		"VISITOR":   string(navigation.NavStatusInOrbit),
		"WAREHOUSE": string(navigation.NavStatusDocked),
	})

	_, alignedNav, err := AlignAndTransferCargo(context.Background(), api, "VISITOR", "WAREHOUSE", "WAREHOUSE", "FOOD", 80, "tok")

	require.NoError(t, err)
	require.Equal(t, []string{"VISITOR"}, api.dockCalls, "the visitor is docked to match the docked warehouse")
	require.Empty(t, api.orbitCalls)
	require.Len(t, api.transferSeen, 1)
	require.Equal(t, navigation.NavStatusDocked, alignedNav)
}

func TestAlignAndTransfer_AlreadyAligned_NoExtraNavCall(t *testing.T) {
	api := newFakeTransferAPI(map[string]string{
		"VISITOR":   string(navigation.NavStatusInOrbit),
		"WAREHOUSE": string(navigation.NavStatusInOrbit),
	})

	_, _, err := AlignAndTransferCargo(context.Background(), api, "VISITOR", "WAREHOUSE", "WAREHOUSE", "FOOD", 80, "tok")

	require.NoError(t, err)
	require.Empty(t, api.orbitCalls, "no nav call when states already match")
	require.Empty(t, api.dockCalls, "no nav call when states already match")
	require.Len(t, api.transferSeen, 1)
}

func TestAlignAndTransfer_4271DespiteAlignment_RealignsAndRetriesOnce(t *testing.T) {
	api := newFakeTransferAPI(map[string]string{
		"VISITOR":   string(navigation.NavStatusInOrbit),
		"WAREHOUSE": string(navigation.NavStatusInOrbit),
	})
	api.transferErrs = []error{dockStateErr(), nil} // first 4271, second succeeds

	result, _, err := AlignAndTransferCargo(context.Background(), api, "VISITOR", "WAREHOUSE", "WAREHOUSE", "FOOD", 80, "tok")

	require.NoError(t, err, "a 4271 race is recovered by one aligned retry, not a crash")
	require.NotNil(t, result)
	require.Len(t, api.transferSeen, 2, "transfer attempted exactly twice")
}

func TestAlignAndTransfer_4271Persists_HonestFailureNotCrash(t *testing.T) {
	api := newFakeTransferAPI(map[string]string{
		"VISITOR":   string(navigation.NavStatusInOrbit),
		"WAREHOUSE": string(navigation.NavStatusInOrbit),
	})
	api.transferErrs = []error{dockStateErr(), dockStateErr()} // both attempts 4271

	result, _, err := AlignAndTransferCargo(context.Background(), api, "VISITOR", "WAREHOUSE", "WAREHOUSE", "FOOD", 80, "tok")

	require.Error(t, err, "a persistent 4271 surfaces as an error for the honest-failure path")
	require.Nil(t, result)
	require.True(t, IsDockStateMismatch(err), "the surfaced error is still the dock-state rejection")
	require.Len(t, api.transferSeen, 2, "retried exactly once (two attempts), then gave up")
}

func TestAlignAndTransfer_NonDockError_NotRetried(t *testing.T) {
	api := newFakeTransferAPI(map[string]string{
		"VISITOR":   string(navigation.NavStatusInOrbit),
		"WAREHOUSE": string(navigation.NavStatusInOrbit),
	})
	api.transferErrs = []error{errors.New("insufficient cargo space")}

	_, _, err := AlignAndTransferCargo(context.Background(), api, "VISITOR", "WAREHOUSE", "WAREHOUSE", "FOOD", 80, "tok")

	require.Error(t, err)
	require.Len(t, api.transferSeen, 1, "a non-4271 failure is not retried")
}

// --- withdrawal direction (from = warehouse, to = visitor) ---------------

func TestAlignAndTransfer_Withdrawal_VisitorDockedWarehouseOrbit_OrbitsVisitor(t *testing.T) {
	// Lane D withdrawal: contract worker (visitor) withdraws from the warehouse hull.
	api := newFakeTransferAPI(map[string]string{
		"VISITOR":   string(navigation.NavStatusDocked),
		"WAREHOUSE": string(navigation.NavStatusInOrbit),
	})

	_, alignedNav, err := AlignAndTransferCargo(context.Background(), api, "WAREHOUSE", "VISITOR", "WAREHOUSE", "IRON_ORE", 40, "tok")

	require.NoError(t, err)
	require.Equal(t, []string{"VISITOR"}, api.orbitCalls, "the visitor (transfer destination) is aligned, never the warehouse")
	require.Empty(t, api.dockCalls)
	require.Len(t, api.transferSeen, 1)
	require.Equal(t, [2]string{"WAREHOUSE", "VISITOR"}, api.transferSeen[0])
	require.Equal(t, navigation.NavStatusInOrbit, alignedNav)
}

func TestAlignAndTransfer_Withdrawal_VisitorOrbitWarehouseDocked_DocksVisitor(t *testing.T) {
	api := newFakeTransferAPI(map[string]string{
		"VISITOR":   string(navigation.NavStatusInOrbit),
		"WAREHOUSE": string(navigation.NavStatusDocked),
	})

	_, _, err := AlignAndTransferCargo(context.Background(), api, "WAREHOUSE", "VISITOR", "WAREHOUSE", "IRON_ORE", 40, "tok")

	require.NoError(t, err)
	require.Equal(t, []string{"VISITOR"}, api.dockCalls, "the visitor is docked to match the docked warehouse")
	require.Empty(t, api.orbitCalls)
}

// --- read-failure and detector ------------------------------------------

func TestAlignVisitorToWarehouse_WarehouseReadFails_SurfacesErrorNoTransfer(t *testing.T) {
	api := newFakeTransferAPI(nil)
	api.getShipErr = errors.New("network down")

	_, _, err := AlignAndTransferCargo(context.Background(), api, "VISITOR", "WAREHOUSE", "WAREHOUSE", "FOOD", 80, "tok")

	require.Error(t, err, "a nav-state read failure surfaces, not a crash")
	require.Empty(t, api.transferSeen, "no transfer attempted when alignment cannot be read")
}

func TestIsDockStateMismatch(t *testing.T) {
	require.True(t, IsDockStateMismatch(dockStateErr()))
	require.False(t, IsDockStateMismatch(errors.New("some other error")))
	require.False(t, IsDockStateMismatch(nil))
}
