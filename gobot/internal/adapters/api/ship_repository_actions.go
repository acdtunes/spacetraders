package api

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Navigate executes ship navigation via API and persists state to database.
// Returns navigation result with arrival time from API.
func (r *ShipRepository) Navigate(ctx context.Context, ship *navigation.Ship, destination *shared.Waypoint, playerID shared.PlayerID) (*navigation.Result, error) {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find player: %w", err)
	}

	// Call API to navigate ship
	navResult, err := r.apiClient.NavigateShip(ctx, ship.ShipSymbol(), destination.Symbol, player.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to navigate ship: %w", err)
	}

	// Update ship domain entity state from API response
	if err := ship.StartTransit(destination); err != nil {
		return nil, fmt.Errorf("failed to update ship state: %w", err)
	}

	// Consume fuel based on API response
	if err := ship.ConsumeFuel(navResult.FuelConsumed); err != nil {
		return nil, fmt.Errorf("failed to consume fuel: %w", err)
	}

	// Set flight mode from result
	if navResult.FlightMode != "" {
		ship.SetFlightMode(navResult.FlightMode)
	}

	// Set arrival time from API response
	if navResult.ArrivalTimeStr != "" {
		if arrivalTime, err := time.Parse(time.RFC3339, navResult.ArrivalTimeStr); err == nil {
			ship.SetArrivalTime(arrivalTime)
		}
	}

	// Persist state to database
	if err := r.persistOwnedColumns(ctx, ship, navigateColumns(ship)); err != nil {
		log.Printf("Warning: failed to persist ship %s after navigate: %v", ship.ShipSymbol(), err)
	}

	// Invalidate cache for this player
	r.shipListCache.Delete(playerID.Value())

	// Notify arrival scheduler to set up transition timer
	if r.arrivalScheduler != nil {
		r.arrivalScheduler.ScheduleArrival(ship)
	}

	return navResult, nil
}

// Dock docks the ship via API (idempotent) and persists state to database.
func (r *ShipRepository) Dock(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to find player: %w", err)
	}

	// Call API to dock ship (API itself is idempotent - will succeed if already docked)
	if err := r.apiClient.DockShip(ctx, ship.ShipSymbol(), player.Token); err != nil {
		// Check if error is because ship is already docked (API returns specific error)
		// If so, this is idempotent behavior - not an error
		if !isAlreadyDockedError(err) {
			return fmt.Errorf("failed to dock ship: %w", err)
		}
	}

	// Update ship domain entity state using idempotent method
	if _, err := ship.EnsureDocked(); err != nil {
		return fmt.Errorf("failed to update ship state: %w", err)
	}

	// Persist state to database
	if err := r.persistOwnedColumns(ctx, ship, navStatusColumns(ship)); err != nil {
		log.Printf("Warning: failed to persist ship %s after dock: %v", ship.ShipSymbol(), err)
	}

	// Invalidate cache for this player
	r.shipListCache.Delete(playerID.Value())

	return nil
}

// Orbit puts ship in orbit via API (idempotent) and persists state to database.
func (r *ShipRepository) Orbit(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to find player: %w", err)
	}

	// Call API to orbit ship (API itself is idempotent - will succeed if already in orbit)
	if err := r.apiClient.OrbitShip(ctx, ship.ShipSymbol(), player.Token); err != nil {
		// Check if error is because ship is already in orbit (API returns specific error)
		// If so, this is idempotent behavior - not an error
		if !isAlreadyInOrbitError(err) {
			return fmt.Errorf("failed to orbit ship: %w", err)
		}
	}

	// Update ship domain entity state using idempotent method
	if _, err := ship.EnsureInOrbit(); err != nil {
		return fmt.Errorf("failed to update ship state: %w", err)
	}

	// Clear arrival time when ship arrives in orbit
	ship.ClearArrivalTime()

	// Persist state to database. The arrival clock is owned here too: clearing it
	// on the entity alone would leave the row claiming a transit that has landed.
	columns := navStatusColumns(ship)
	columns["arrival_time"] = ship.ArrivalTime()
	if err := r.persistOwnedColumns(ctx, ship, columns); err != nil {
		log.Printf("Warning: failed to persist ship %s after orbit: %v", ship.ShipSymbol(), err)
	}

	// Invalidate cache for this player
	r.shipListCache.Delete(playerID.Value())

	return nil
}

// Refuel refuels the ship via API and persists state to database.
func (r *ShipRepository) Refuel(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID, units *int) (*navigation.RefuelResult, error) {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find player: %w", err)
	}

	// Never ask the pump for more than the tank can hold. A metered
	// request past the headroom is narrowed to it, and one against a tank with no
	// headroom left is not sent at all - buying zero units costs an API call and
	// a transaction to accomplish nothing, and it is one of the ways a hull ends
	// up in a refuel-retry storm over fuel it does not need.
	units, skip := clampRefuelUnits(ship, units)
	if skip {
		return &navigation.RefuelResult{
			FuelCurrent:  ship.Fuel().Current,
			FuelCapacity: ship.Fuel().Capacity,
		}, nil
	}

	// Call API to refuel ship
	refuelResult, err := r.apiClient.RefuelShip(ctx, ship.ShipSymbol(), player.Token, units)
	if err != nil {
		return nil, fmt.Errorf("failed to refuel ship: %w", err)
	}

	if err := adoptRefuelledFuel(ship, refuelResult, units); err != nil {
		return nil, fmt.Errorf("failed to update ship fuel: %w", err)
	}

	// Persist state to database
	if err := r.persistOwnedColumns(ctx, ship, fuelColumns(ship)); err != nil {
		log.Printf("Warning: failed to persist ship %s after refuel: %v", ship.ShipSymbol(), err)
	}

	// Invalidate cache for this player
	r.shipListCache.Delete(playerID.Value())

	return refuelResult, nil
}

// clampRefuelUnits narrows a metered refuel request to what the tank can still
// take, and reports whether the request should be dropped entirely.
//
// Only an EXPLICIT unit count is touched. nil means "fill the tank" - the whole
// fleet's refuel path sends nil, and the API tops up to capacity and no further -
// so rewriting nil into a number would hand every fleet refuel over to whatever
// the local row happened to believe about the tank. It is left alone.
//
// The clamp only ever NARROWS a request (RULINGS #4: a money guard fails closed
// and is never weakened). A tank whose fuel state is unknown is passed through
// unchanged rather than treated as full: refusing to refuel on missing
// information strands a hull, which is far worse than one redundant request, and
// "we have not looked" is not evidence that there is no headroom.
func clampRefuelUnits(ship *navigation.Ship, units *int) (clamped *int, skip bool) {
	fuel := ship.Fuel()
	if units == nil || fuel == nil {
		return units, false
	}

	headroom := fuel.Capacity - fuel.Current
	if headroom <= 0 {
		log.Printf("WARNING [refuel_units_clamped] ship=%s requested_units=%d fuel=%d/%d: tank has no headroom, skipping the refuel call entirely",
			ship.ShipSymbol(), *units, fuel.Current, fuel.Capacity)
		return nil, true
	}
	if *units > headroom {
		log.Printf("WARNING [refuel_units_clamped] ship=%s requested_units=%d clamped_units=%d fuel=%d/%d: a refuel was requested for more units than the tank can take",
			ship.ShipSymbol(), *units, headroom, fuel.Current, fuel.Capacity)
		return &headroom, false
	}
	return units, false
}

// adoptRefuelledFuel folds the post-refuel tank into the ship. How much fuel a
// refuel actually lands is the API's to decide - market supply and its 100-unit
// blocks both bound it - so the response is authoritative over the requested fill,
// the same way Navigate and Warp adopt their in-band fuel. A response carrying no
// fuel state (mock/older API) falls back to what was asked for.
//
// The API has been observed reporting an IMPOSSIBLE tank right after a
// refuel - 729/600, 726/600, 613/600 on three separate hulls. The clamp to
// capacity itself is not new (shared.ReconstructFuel has always applied it, which
// is why those rows read 600/600 locally while the API read 729/600) but it was
// SILENT, so an impossible tank left no trace anywhere an operator would look
// while the hull it happened to went on to burn 41 minutes on a refuel it did not
// need. The clamp is now applied HERE, explicitly, and said out loud: local, so it
// cannot be undone by a change to a distant value object, and visible, so the
// anomaly is greppable.
func adoptRefuelledFuel(ship *navigation.Ship, result *navigation.RefuelResult, units *int) error {
	if result.FuelCapacity > 0 {
		current := result.FuelCurrent
		if current > result.FuelCapacity {
			log.Printf("WARNING [fuel_over_capacity] ship=%s api_fuel_current=%d api_fuel_capacity=%d clamped_to=%d: the refuel API reported more fuel than the tank can hold; the impossible value is clamped and never persisted",
				ship.ShipSymbol(), result.FuelCurrent, result.FuelCapacity, result.FuelCapacity)
			current = result.FuelCapacity
		}
		return ship.UpdateFuelFromAPI(current, result.FuelCapacity)
	}
	if units != nil {
		return ship.Refuel(*units)
	}
	_, err := ship.RefuelToFull()
	return err
}

// SetFlightMode sets the ship's flight mode via API and persists state to database.
func (r *ShipRepository) SetFlightMode(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID, mode string) error {
	// Skip if already set
	if ship.FlightMode() == mode {
		return nil
	}

	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to find player: %w", err)
	}

	// Call API to set flight mode
	if err := r.apiClient.SetFlightMode(ctx, ship.ShipSymbol(), mode, player.Token); err != nil {
		return fmt.Errorf("failed to set flight mode: %w", err)
	}

	// Update domain entity
	ship.SetFlightMode(mode)

	// Persist state to database
	if err := r.persistOwnedColumns(ctx, ship, map[string]interface{}{"flight_mode": ship.FlightMode()}); err != nil {
		log.Printf("Warning: failed to persist ship %s after set flight mode: %v", ship.ShipSymbol(), err)
	}

	// Invalidate cache for this player
	r.shipListCache.Delete(playerID.Value())

	return nil
}

// JettisonCargo jettisons cargo from the ship via API
func (r *ShipRepository) JettisonCargo(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID, goodSymbol string, units int) error {
	// Get player token
	player, err := r.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to find player: %w", err)
	}

	// Call API to jettison cargo
	if err := r.apiClient.JettisonCargo(ctx, ship.ShipSymbol(), goodSymbol, units, player.Token); err != nil {
		return fmt.Errorf("failed to jettison cargo: %w", err)
	}

	// Note: Cargo is updated by the API, and we refetch ship state when needed
	// No need to update the domain entity here

	return nil
}

// isAlreadyDockedError checks if the error is due to ship already being docked
// API returns error code 4237 when trying to dock an already docked ship
func isAlreadyDockedError(err error) bool {
	if err == nil {
		return false
	}
	// Check if error message contains indication that ship is already docked
	errMsg := err.Error()
	return strings.Contains(errMsg, "already docked") || strings.Contains(errMsg, "4237")
}

// isAlreadyInOrbitError checks if the error is due to ship already being in orbit
// API returns error code when trying to orbit an already orbiting ship
//
// Matches only the exact "already in orbit" phrase, not the bare substring "in
// orbit" (which any orbit-precondition error could also contain) — Orbit()
// treats a match as success, so a broader match risks silently swallowing a
// real failure.
func isAlreadyInOrbitError(err error) bool {
	if err == nil {
		return false
	}
	// Check if error message contains indication that ship is already in orbit
	errMsg := err.Error()
	return strings.Contains(errMsg, "already in orbit")
}
