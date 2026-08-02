package api

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// GetShip retrieves ship details
func (c *SpaceTradersClient) GetShip(ctx context.Context, symbol, token string) (*navigation.ShipData, error) {
	path := fmt.Sprintf("/my/ships/%s", symbol)

	var response struct {
		Data shipDTO `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get ship: %w", err)
	}

	return response.Data.toShipData(), nil
}

// ListShips retrieves all ships for the authenticated agent.
//
// Pages at the API maximum of 20 per request (openapi.json: limit maximum 20)
// and stops as soon as two independent signals agree the collection is
// exhausted: the page came back short, AND meta.total accounts for every hull
// already read. Either signal alone can lie — a short page from a server that
// under-fills, a total that is stale against a fleet growing mid-pagination —
// and a short ship list is not a cheap error: SyncAllFromAPI treats a successful
// response as the authoritative fleet and prunes every row missing from it. When
// the signals do not agree, or the server reports no usable total, the loop falls
// back to probing for an empty page.
func (c *SpaceTradersClient) ListShips(ctx context.Context, token string) ([]*navigation.ShipData, error) {
	var allShips []*navigation.ShipData
	page := 1
	limit := apiPageLimitMax

	for {
		path := fmt.Sprintf("/my/ships?page=%d&limit=%d", page, limit)

		var response struct {
			Data []shipDTO `json:"data"`
			Meta struct {
				Total int `json:"total"`
				Page  int `json:"page"`
				Limit int `json:"limit"`
			} `json:"meta"`
		}

		if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
			return nil, fmt.Errorf("failed to list ships (page %d): %w", page, err)
		}

		if len(response.Data) == 0 {
			break
		}

		for i := range response.Data {
			allShips = append(allShips, response.Data[i].toShipData())
		}

		// total is re-read every page, so a fleet that grows mid-pagination
		// pushes the finish line out rather than cutting the loop short.
		shortPage := len(response.Data) < limit
		totalAccountedFor := response.Meta.Total > 0 && len(allShips) >= response.Meta.Total
		if shortPage && totalAccountedFor {
			break
		}

		page++
	}

	return allShips, nil
}

// NavigateShip navigates a ship to a destination
func (c *SpaceTradersClient) NavigateShip(ctx context.Context, symbol, destination, token string) (*navigation.Result, error) {
	path := fmt.Sprintf("/my/ships/%s/navigate", symbol)

	body := map[string]string{
		"waypointSymbol": destination,
	}

	var response struct {
		Data struct {
			Fuel struct {
				Current  int `json:"current"`
				Capacity int `json:"capacity"`
				Consumed struct {
					Amount int `json:"amount"`
				} `json:"consumed"`
			} `json:"fuel"`
			Nav struct {
				WaypointSymbol string `json:"waypointSymbol"`
				Route          struct {
					DepartureTime string `json:"departureTime"`
					Arrival       string `json:"arrival"`
				} `json:"route"`
			} `json:"nav"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to navigate ship: %w", err)
	}

	arrivalTimeStr := response.Data.Nav.Route.Arrival

	arrivalTime := travelSeconds(response.Data.Nav.Route.DepartureTime, arrivalTimeStr)

	return &navigation.Result{
		Destination:    response.Data.Nav.WaypointSymbol,
		ArrivalTime:    arrivalTime,
		ArrivalTimeStr: arrivalTimeStr, // ISO8601 string from API
		FuelConsumed:   response.Data.Fuel.Consumed.Amount,
		FuelCurrent:    response.Data.Fuel.Current,
		FuelCapacity:   response.Data.Fuel.Capacity,
	}, nil
}

// WarpShip warps a ship to a destination waypoint in ANOTHER system, off the
// jump-gate network. Requires a MODULE_WARP_DRIVE_I; fuel is consumed
// by inter-system distance. The wire contract mirrors NavigateShip exactly - the
// live API's POST /my/ships/{shipSymbol}/warp takes "waypointSymbol" in the body
// and returns the same fuel + nav.route envelope - so the parsing/return shape is
// identical to a navigate leg (destination, arrival time, post-warp fuel state).
func (c *SpaceTradersClient) WarpShip(ctx context.Context, symbol, destination, token string) (*navigation.Result, error) {
	path := fmt.Sprintf("/my/ships/%s/warp", symbol)

	body := map[string]string{
		"waypointSymbol": destination,
	}

	var response struct {
		Data struct {
			Fuel struct {
				Current  int `json:"current"`
				Capacity int `json:"capacity"`
				Consumed struct {
					Amount int `json:"amount"`
				} `json:"consumed"`
			} `json:"fuel"`
			Nav struct {
				WaypointSymbol string `json:"waypointSymbol"`
				Route          struct {
					DepartureTime string `json:"departureTime"`
					Arrival       string `json:"arrival"`
				} `json:"route"`
			} `json:"nav"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to warp ship: %w", err)
	}

	arrivalTimeStr := response.Data.Nav.Route.Arrival
	arrivalTime := travelSeconds(response.Data.Nav.Route.DepartureTime, arrivalTimeStr)

	return &navigation.Result{
		Destination:    response.Data.Nav.WaypointSymbol,
		ArrivalTime:    arrivalTime,
		ArrivalTimeStr: arrivalTimeStr,
		FuelConsumed:   response.Data.Fuel.Consumed.Amount,
		FuelCurrent:    response.Data.Fuel.Current,
		FuelCapacity:   response.Data.Fuel.Capacity,
	}, nil
}

// OrbitShip puts ship into orbit
func (c *SpaceTradersClient) OrbitShip(ctx context.Context, symbol, token string) error {
	path := fmt.Sprintf("/my/ships/%s/orbit", symbol)

	// Send empty JSON object {} instead of nil to satisfy API requirements
	emptyBody := map[string]interface{}{}
	if err := c.request(ctx, "POST", path, token, emptyBody, nil); err != nil {
		return fmt.Errorf("failed to orbit ship: %w", err)
	}

	return nil
}

// DockShip docks a ship
func (c *SpaceTradersClient) DockShip(ctx context.Context, symbol, token string) error {
	path := fmt.Sprintf("/my/ships/%s/dock", symbol)

	// Send empty JSON object {} instead of nil to satisfy API requirements
	emptyBody := map[string]interface{}{}
	if err := c.request(ctx, "POST", path, token, emptyBody, nil); err != nil {
		return fmt.Errorf("failed to dock ship: %w", err)
	}

	return nil
}

// RefuelShip refuels a ship
func (c *SpaceTradersClient) RefuelShip(ctx context.Context, symbol, token string, units *int) (*navigation.RefuelResult, error) {
	path := fmt.Sprintf("/my/ships/%s/refuel", symbol)

	// Always send an object (empty {} if no units specified)
	body := map[string]interface{}{}
	if units != nil {
		body["units"] = *units
	}

	var response struct {
		Data struct {
			// Agent is a pointer so an omitted block (nil) is distinguishable
			// from a real zero balance; the in-band credits are the ledger's
			// authoritative post-transaction balance.
			Agent *struct {
				Credits int `json:"credits"`
			} `json:"agent"`
			Fuel struct {
				Current  int `json:"current"`
				Capacity int `json:"capacity"`
			} `json:"fuel"`
			Transaction struct {
				Units      int `json:"units"`
				TotalPrice int `json:"totalPrice"`
			} `json:"transaction"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to refuel ship: %w", err)
	}
	c.invalidateAgentCache() // refuel spends credits -> drop the stale-high cache

	result := &navigation.RefuelResult{
		FuelAdded:    response.Data.Transaction.Units,
		CreditsCost:  response.Data.Transaction.TotalPrice,
		FuelCurrent:  response.Data.Fuel.Current,
		FuelCapacity: response.Data.Fuel.Capacity,
	}
	if response.Data.Agent != nil {
		credits := response.Data.Agent.Credits
		result.AgentCredits = &credits
	}
	return result, nil
}

// SetFlightMode sets the flight mode for a ship
func (c *SpaceTradersClient) SetFlightMode(ctx context.Context, symbol, flightMode, token string) error {
	path := fmt.Sprintf("/my/ships/%s/nav", symbol)

	body := map[string]string{
		"flightMode": flightMode,
	}

	if err := c.request(ctx, "PATCH", path, token, body, nil); err != nil {
		return fmt.Errorf("failed to set flight mode: %w", err)
	}

	return nil
}

// JumpShip executes a jump through a jump gate to a different system.
// waypointSymbol must be the destination JUMP_GATE waypoint symbol (e.g.
// "X1-GQ92-I51") - not a bare system symbol. The live SpaceTraders API
// requires "waypointSymbol" in the request body and 422s with
// "waypointSymbol Required, received undefined" otherwise.
func (c *SpaceTradersClient) JumpShip(ctx context.Context, shipSymbol, waypointSymbol, token string) (*domainPorts.JumpResult, error) {
	path := fmt.Sprintf("/my/ships/%s/jump", shipSymbol)

	body := map[string]string{
		"waypointSymbol": waypointSymbol,
	}

	var response struct {
		Data struct {
			// Agent is a pointer so an omitted block (nil) is distinguishable
			// from a real zero balance; the in-band credits are the ledger's
			// authoritative post-fee balance (mirrors RefuelShip/SellCargo).
			Agent *struct {
				Credits int `json:"credits"`
			} `json:"agent"`
			Nav struct {
				SystemSymbol   string `json:"systemSymbol"`
				WaypointSymbol string `json:"waypointSymbol"`
			} `json:"nav"`
			Cooldown struct {
				ShipSymbol       string `json:"shipSymbol"`
				TotalSeconds     int    `json:"totalSeconds"`
				RemainingSeconds int    `json:"remainingSeconds"`
				Expiration       string `json:"expiration"`
			} `json:"cooldown"`
			Transaction struct {
				WaypointSymbol string `json:"waypointSymbol"`
				ShipSymbol     string `json:"shipSymbol"`
				TotalPrice     int    `json:"totalPrice"`
			} `json:"transaction"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to jump ship: %w", err)
	}
	c.invalidateAgentCache() // jump charges a gate fee (transaction.totalPrice) -> drop the stale-high cache

	result := &domainPorts.JumpResult{
		DestinationSystem:   response.Data.Nav.SystemSymbol,
		DestinationWaypoint: response.Data.Nav.WaypointSymbol,
		CooldownSeconds:     response.Data.Cooldown.RemainingSeconds,
		TotalPrice:          response.Data.Transaction.TotalPrice,
	}
	if response.Data.Agent != nil {
		credits := response.Data.Agent.Credits
		result.AgentCredits = &credits
	}
	return result, nil
}

// CreateChart PUBLICLY charts the ship's CURRENT waypoint (POST /my/ships/{shipSymbol}/chart).
// Once a waypoint is charted, every future read of it — notably the GetJumpGate that resolves a
// jump's destination WAYPOINT — succeeds WITHOUT a ship physically present. An UNcharted frontier
// jump gate otherwise 400s that read unless a hull is sitting on it, so an our-ships-only gate
// stays uncharted-public forever and every jump-OUT re-reads it live and 400s (the ~49% of
// GetJumpGate calls that fail). Charting it once from a present hull collapses that
// re-read storm and unblocks frontier jump-outs.
//
// The ship must be AT the waypoint (the API 400s otherwise); a waypoint that is already charted
// 400s with code 4230 "waypoint already charted" — a benign no-op the gate-graph caller detects
// and swallows. Mirrors JumpShip/OrbitShip's shape: an empty-body POST through the rate-limited
// request() path, with a typed *APIError surfaced on any non-2xx so the caller can classify the
// already-charted case (charting is free, so there is no agent-cache to invalidate).
func (c *SpaceTradersClient) CreateChart(ctx context.Context, shipSymbol, token string) error {
	path := fmt.Sprintf("/my/ships/%s/chart", shipSymbol)

	// Send an empty JSON object {} (not nil) to satisfy the API, exactly as OrbitShip/DockShip do.
	emptyBody := map[string]interface{}{}
	if err := c.request(ctx, "POST", path, token, emptyBody, nil); err != nil {
		return fmt.Errorf("failed to chart waypoint: %w", err)
	}

	return nil
}

func travelSeconds(departureTimeStr, arrivalTimeStr string) int {
	departure, err := time.Parse(time.RFC3339, departureTimeStr)
	if err != nil {
		return 0
	}
	arrival, err := time.Parse(time.RFC3339, arrivalTimeStr)
	if err != nil {
		return 0
	}
	seconds := int(arrival.Sub(departure).Seconds())
	if seconds < 0 {
		return 0
	}
	return seconds
}
