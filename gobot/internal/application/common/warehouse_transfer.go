package common

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// apiErrCodeDockStateMismatch is the SpaceTraders API error code returned by a
// ship-to-ship cargo transfer when the two hulls are NOT both DOCKED or both
// IN_ORBIT at the same waypoint. The standard client surfaces the raw response
// body inside the returned error string (there is no structured code on the
// transfer path), so the code is matched as a substring — a distinctive 4-digit
// token whose only realistic source in a transfer error body is this rejection.
const apiErrCodeDockStateMismatch = "4271"

// IsDockStateMismatch reports whether err is a SpaceTraders 4271 rejection ("both
// ships must be docked or both in orbit"). It is the signal the deposit/withdrawal
// seams use to distinguish a recoverable nav-state race (re-align + retry) from a
// genuine failure that must surface to the caller's honest-failure path.
func IsDockStateMismatch(err error) bool {
	return err != nil && strings.Contains(err.Error(), apiErrCodeDockStateMismatch)
}

// AlignVisitorToWarehouse brings the mobile "visitor" hull into the SAME nav state
// (DOCKED vs IN_ORBIT) as the stationary warehouse hull, which SpaceTraders requires
// for a ship-to-ship transfer (API 4271). It reads BOTH hulls' live nav state and
// issues AT MOST ONE orbit/dock call, applied ONLY to the visitor — the warehouse
// hull is coordinator-owned and stationary, so it is never moved. It is a no-op when
// the two are already aligned (both docked or both in orbit).
//
// It returns the warehouse's nav state (the state the visitor was aligned to) so a
// caller that persists the visitor can reconcile its in-memory nav instead of writing
// back a value made stale by the alignment.
func AlignVisitorToWarehouse(ctx context.Context, apiClient domainPorts.APIClient, visitorSymbol, warehouseSymbol, token string) (navigation.NavStatus, error) {
	warehouse, err := apiClient.GetShip(ctx, warehouseSymbol, token)
	if err != nil {
		return "", fmt.Errorf("read warehouse hull %s nav state for transfer alignment: %w", warehouseSymbol, err)
	}
	visitor, err := apiClient.GetShip(ctx, visitorSymbol, token)
	if err != nil {
		return "", fmt.Errorf("read visitor hull %s nav state for transfer alignment: %w", visitorSymbol, err)
	}

	target := navigation.NavStatus(warehouse.NavStatus)
	warehouseDocked := warehouse.NavStatus == string(navigation.NavStatusDocked)
	visitorDocked := visitor.NavStatus == string(navigation.NavStatusDocked)
	if warehouseDocked == visitorDocked {
		return target, nil // already both docked or both in orbit
	}

	if warehouseDocked {
		if err := apiClient.DockShip(ctx, visitorSymbol, token); err != nil {
			return target, fmt.Errorf("dock visitor hull %s to match docked warehouse %s: %w", visitorSymbol, warehouseSymbol, err)
		}
		return target, nil
	}
	if err := apiClient.OrbitShip(ctx, visitorSymbol, token); err != nil {
		return target, fmt.Errorf("orbit visitor hull %s to match orbiting warehouse %s: %w", visitorSymbol, warehouseSymbol, err)
	}
	return target, nil
}

// AlignAndTransferCargo aligns the visitor hull's nav state to the stationary
// warehouse hull's, then transfers units of good from fromShip to toShip. warehouseSymbol
// MUST be one of fromShip/toShip; the other is the mobile visitor that gets orbited/docked
// to match (the warehouse is never moved). This is the shared deposit/withdrawal transfer
// seam:
//   - deposit:    fromShip = visitor,   toShip = warehouse   (warehouseSymbol = toShip)
//   - withdrawal: fromShip = warehouse, toShip = visitor     (warehouseSymbol = fromShip)
//
// SpaceTraders rejects the transfer with API 4271 unless both hulls share a nav state.
// The pre-alignment makes that precondition hold on the first attempt; if the transfer
// still returns 4271 (a nav-state race — e.g. the coordinator re-docked the warehouse
// between the read and the transfer), it re-aligns and retries the transfer EXACTLY ONCE.
// A persistent failure is RETURNED (never a panic/crash) so the caller's honest-failure
// path runs — reservation release, stranded-veto, or FAILED terminalization.
//
// The second return value is the nav state the visitor ended in (equal to the warehouse's),
// for callers that persist the visitor and want to reconcile its in-memory nav.
func AlignAndTransferCargo(
	ctx context.Context,
	apiClient domainPorts.APIClient,
	fromShip, toShip, warehouseSymbol, good string,
	units int,
	token string,
) (*domainPorts.TransferResult, navigation.NavStatus, error) {
	visitorSymbol := fromShip
	if fromShip == warehouseSymbol {
		visitorSymbol = toShip
	}

	alignedNav, err := AlignVisitorToWarehouse(ctx, apiClient, visitorSymbol, warehouseSymbol, token)
	if err != nil {
		return nil, alignedNav, err
	}

	result, err := apiClient.TransferCargo(ctx, fromShip, toShip, good, units, token)
	if err == nil {
		return result, alignedNav, nil
	}
	if !IsDockStateMismatch(err) {
		return nil, alignedNav, err // a non-dock-state failure is the caller's to handle
	}

	// 4271 despite the pre-alignment: a nav-state race. Re-align and retry once.
	alignedNav, alignErr := AlignVisitorToWarehouse(ctx, apiClient, visitorSymbol, warehouseSymbol, token)
	if alignErr != nil {
		return nil, alignedNav, fmt.Errorf("re-align after 4271 dock-state mismatch failed: %w (original transfer error: %v)", alignErr, err)
	}
	result, err = apiClient.TransferCargo(ctx, fromShip, toShip, good, units, token)
	return result, alignedNav, err
}
