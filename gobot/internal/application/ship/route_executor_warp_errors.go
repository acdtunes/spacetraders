package ship

import (
	"encoding/json"
	"errors"
	"fmt"

	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ErrShipHasNoWarpDrive is returned (fail-closed) when a warp is requested for a
// ship with no MODULE_WARP_DRIVE_* installed. Only a SHIP_EXPLORER
// carries the drive; refusing here keeps the executor from emitting a warp the
// live API would reject, and gives slice B/C a typed signal to pick a warp-capable
// hull instead.
type ErrShipHasNoWarpDrive struct {
	ShipSymbol string
}

func (e *ErrShipHasNoWarpDrive) Error() string {
	return fmt.Sprintf("ship %s cannot warp: no warp drive module installed", e.ShipSymbol)
}

// ErrWarpWouldStrand is the fuel-safety refusal. Its Required/Available figures
// are the SERVER's own, taken from the pre-flight refusal it returns for a leg the
// hull cannot afford; the executor never prices a warp itself. It is returned when
// that refusal cannot be satisfied - the leg costs more than a full tank, or the
// origin has no fuel to top off with - so a doomed leg terminates on the spot
// instead of being retried blindly. The typed fields let a caller report exactly
// why a target is unreachable.
type ErrWarpWouldStrand struct {
	ShipSymbol string
	From       string
	To         string
	Required   int
	Available  int
	Capacity   int
	Reason     string
}

func (e *ErrWarpWouldStrand) Error() string {
	return fmt.Sprintf(
		"refusing warp %s -> %s for ship %s: would strand (%s); required %d fuel, available %d, capacity %d",
		e.From, e.To, e.ShipSymbol, e.Reason, e.Required, e.Available, e.Capacity,
	)
}

// ErrWarpDeadEnd is the onward-viability refusal: the destination system has no way
// back out for the arriving hull. It is the one strand the server cannot prevent -
// it validates that a hull can AFFORD a leg, never that the far end has an exit -
// so this refusal is made locally, before any warp call, and fails CLOSED: a
// destination whose escape state cannot be read is refused exactly like one known
// to be a dead end.
type ErrWarpDeadEnd struct {
	ShipSymbol string
	From       string
	To         string
	System     string
	Reason     string
}

func (e *ErrWarpDeadEnd) Error() string {
	return fmt.Sprintf(
		"refusing warp %s -> %s for ship %s: %s is a dead end (%s)",
		e.From, e.To, e.ShipSymbol, e.System, e.Reason,
	)
}

// warpInsufficientFuelCode is the API's pre-flight verdict that a leg costs more
// fuel than the hull holds. The refusal happens BEFORE the hull moves and carries
// the server's own fuelRequired/fuelAvailable - the authoritative numbers, from the
// entity that enforces the rule.
const warpInsufficientFuelCode = 4203

// warpFuelRefusal is the server's pre-flight verdict on a leg's fuel: what it costs
// and what the hull holds, both measured by the entity that enforces the rule.
type warpFuelRefusal struct {
	required  int
	available int
}

// parseWarpFuelRefusal extracts the server's numbers from a 4203 refusal.
// ok=false for every other failure - INCLUDING a refusal whose body cannot be read -
// so an error the executor does not understand is surfaced as-is and never turned
// into a speculative refuel-and-retry.
func parseWarpFuelRefusal(err error) (warpFuelRefusal, bool) {
	var apiErr *domainPorts.APIError
	if !errors.As(err, &apiErr) {
		return warpFuelRefusal{}, false
	}
	var body struct {
		Error struct {
			Code int `json:"code"`
			Data struct {
				FuelRequired  int `json:"fuelRequired"`
				FuelAvailable int `json:"fuelAvailable"`
			} `json:"data"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(apiErr.Body), &body) != nil {
		return warpFuelRefusal{}, false
	}
	if body.Error.Code != warpInsufficientFuelCode || body.Error.Data.FuelRequired <= 0 {
		return warpFuelRefusal{}, false
	}
	return warpFuelRefusal{
		required:  body.Error.Data.FuelRequired,
		available: body.Error.Data.FuelAvailable,
	}, true
}

// deadEnd builds the typed onward-viability refusal, naming the system the hull
// would not have been able to leave so a caller can report the target as a one-way
// trip rather than a fuel problem.
func (e *RouteExecutor) deadEnd(
	ship *domainNavigation.Ship,
	destination *shared.Waypoint,
	reason string,
) error {
	return &ErrWarpDeadEnd{
		ShipSymbol: ship.ShipSymbol(),
		From:       ship.CurrentLocation().Symbol,
		To:         destination.Symbol,
		System:     destination.SystemSymbol,
		Reason:     reason,
	}
}

// strand builds the typed refusal, capturing the ship's fuel state at the moment
// the leg was refused so a caller can report exactly why the target is unreachable.
func (e *RouteExecutor) strand(
	ship *domainNavigation.Ship,
	origin, destination *shared.Waypoint,
	required, capacity int,
	reason string,
) error {
	return &ErrWarpWouldStrand{
		ShipSymbol: ship.ShipSymbol(),
		From:       origin.Symbol,
		To:         destination.Symbol,
		Required:   required,
		Available:  ship.Fuel().Current,
		Capacity:   capacity,
		Reason:     reason,
	}
}
