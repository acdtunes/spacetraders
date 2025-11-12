package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/time/rate"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

const (
	baseURL           = "https://api.spacetraders.io/v2"
	defaultTimeout    = 30 * time.Second
	maxRetries        = 3
	retryBackoffBase  = time.Second
)

// SpaceTradersClient implements the APIClient interface
type SpaceTradersClient struct {
	httpClient  *http.Client
	rateLimiter *rate.Limiter
	baseURL     string
}

// NewSpaceTradersClient creates a new SpaceTraders API client
// Rate limit: 2 requests per second with burst of 2
func NewSpaceTradersClient() *SpaceTradersClient {
	return &SpaceTradersClient{
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
		rateLimiter: rate.NewLimiter(rate.Limit(2), 2), // 2 req/sec, burst 2
		baseURL:     baseURL,
	}
}

// GetShip retrieves ship details
func (c *SpaceTradersClient) GetShip(ctx context.Context, symbol, token string) (*common.ShipData, error) {
	path := fmt.Sprintf("/my/ships/%s", symbol)

	var response struct {
		Data struct {
			Symbol     string `json:"symbol"`
			Nav        struct {
				SystemSymbol   string `json:"systemSymbol"`
				WaypointSymbol string `json:"waypointSymbol"`
				Status         string `json:"status"`
			} `json:"nav"`
			Fuel struct {
				Current  int `json:"current"`
				Capacity int `json:"capacity"`
			} `json:"fuel"`
			Cargo struct {
				Capacity  int `json:"capacity"`
				Units     int `json:"units"`
				Inventory []struct {
					Symbol      string `json:"symbol"`
					Name        string `json:"name"`
					Description string `json:"description"`
					Units       int    `json:"units"`
				} `json:"inventory"`
			} `json:"cargo"`
			Engine struct {
				Speed int `json:"speed"`
			} `json:"engine"`
		} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get ship: %w", err)
	}

	// Convert cargo inventory
	inventory := make([]common.CargoItemData, len(response.Data.Cargo.Inventory))
	for i, item := range response.Data.Cargo.Inventory {
		inventory[i] = common.CargoItemData{
			Symbol:      item.Symbol,
			Name:        item.Name,
			Description: item.Description,
			Units:       item.Units,
		}
	}

	cargo := &common.CargoData{
		Capacity:  response.Data.Cargo.Capacity,
		Units:     response.Data.Cargo.Units,
		Inventory: inventory,
	}

	return &common.ShipData{
		Symbol:        response.Data.Symbol,
		Location:      response.Data.Nav.WaypointSymbol,
		NavStatus:     response.Data.Nav.Status,
		FuelCurrent:   response.Data.Fuel.Current,
		FuelCapacity:  response.Data.Fuel.Capacity,
		CargoCapacity: response.Data.Cargo.Capacity,
		CargoUnits:    response.Data.Cargo.Units,
		EngineSpeed:   response.Data.Engine.Speed,
		Cargo:         cargo,
	}, nil
}

// ListShips retrieves all ships for the authenticated agent
// Uses pagination - fetches up to 20 ships per page (SpaceTraders API default limit)
func (c *SpaceTradersClient) ListShips(ctx context.Context, token string) ([]*common.ShipData, error) {
	path := "/my/ships"

	var response struct {
		Data []struct {
			Symbol     string `json:"symbol"`
			Nav        struct {
				SystemSymbol   string `json:"systemSymbol"`
				WaypointSymbol string `json:"waypointSymbol"`
				Status         string `json:"status"`
			} `json:"nav"`
			Fuel struct {
				Current  int `json:"current"`
				Capacity int `json:"capacity"`
			} `json:"fuel"`
			Cargo struct {
				Capacity  int `json:"capacity"`
				Units     int `json:"units"`
				Inventory []struct {
					Symbol      string `json:"symbol"`
					Name        string `json:"name"`
					Description string `json:"description"`
					Units       int    `json:"units"`
				} `json:"inventory"`
			} `json:"cargo"`
			Engine struct {
				Speed int `json:"speed"`
			} `json:"engine"`
		} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to list ships: %w", err)
	}

	// Convert API response to ShipData DTOs
	ships := make([]*common.ShipData, len(response.Data))
	for i, ship := range response.Data {
		// Convert cargo inventory
		inventory := make([]common.CargoItemData, len(ship.Cargo.Inventory))
		for j, item := range ship.Cargo.Inventory {
			inventory[j] = common.CargoItemData{
				Symbol:      item.Symbol,
				Name:        item.Name,
				Description: item.Description,
				Units:       item.Units,
			}
		}

		cargo := &common.CargoData{
			Capacity:  ship.Cargo.Capacity,
			Units:     ship.Cargo.Units,
			Inventory: inventory,
		}

		ships[i] = &common.ShipData{
			Symbol:        ship.Symbol,
			Location:      ship.Nav.WaypointSymbol,
			NavStatus:     ship.Nav.Status,
			FuelCurrent:   ship.Fuel.Current,
			FuelCapacity:  ship.Fuel.Capacity,
			CargoCapacity: ship.Cargo.Capacity,
			CargoUnits:    ship.Cargo.Units,
			EngineSpeed:   ship.Engine.Speed,
			Cargo:         cargo,
		}
	}

	return ships, nil
}

// NavigateShip navigates a ship to a destination
func (c *SpaceTradersClient) NavigateShip(ctx context.Context, symbol, destination, token string) (*common.NavigationResult, error) {
	path := fmt.Sprintf("/my/ships/%s/navigate", symbol)

	body := map[string]string{
		"waypointSymbol": destination,
	}

	var response struct {
		Data struct {
			Fuel struct {
				Consumed struct {
					Amount int `json:"amount"`
				} `json:"consumed"`
			} `json:"fuel"`
			Nav struct {
				WaypointSymbol string `json:"waypointSymbol"`
				Route          struct {
					Arrival string `json:"arrival"`
				} `json:"route"`
			} `json:"nav"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to navigate ship: %w", err)
	}

	// Extract arrival time string (ISO8601 timestamp from API)
	arrivalTimeStr := response.Data.Nav.Route.Arrival

	// Parse arrival time for legacy ArrivalTime field (can be removed later)
	arrivalTime := 0

	return &common.NavigationResult{
		Destination:    response.Data.Nav.WaypointSymbol,
		ArrivalTime:    arrivalTime,
		ArrivalTimeStr: arrivalTimeStr, // ISO8601 string from API
		FuelConsumed:   response.Data.Fuel.Consumed.Amount,
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
func (c *SpaceTradersClient) RefuelShip(ctx context.Context, symbol, token string, units *int) (*common.RefuelResult, error) {
	path := fmt.Sprintf("/my/ships/%s/refuel", symbol)

	// Always send an object (empty {} if no units specified)
	body := map[string]interface{}{}
	if units != nil {
		body["units"] = *units
	}

	var response struct {
		Data struct {
			Transaction struct {
				Units      int `json:"units"`
				TotalPrice int `json:"totalPrice"`
			} `json:"transaction"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to refuel ship: %w", err)
	}

	return &common.RefuelResult{
		FuelAdded:   response.Data.Transaction.Units,
		CreditsCost: response.Data.Transaction.TotalPrice,
	}, nil
}

// SetFlightMode sets the flight mode for a ship
func (c *SpaceTradersClient) SetFlightMode(ctx context.Context, symbol, flightMode, token string) error {
	path := fmt.Sprintf("/my/ships/%s/nav", symbol)

	body := map[string]string{
		"flightMode": flightMode,
	}

	// Use the existing request method with PATCH verb
	if err := c.request(ctx, "PATCH", path, token, body, nil); err != nil {
		return fmt.Errorf("failed to set flight mode: %w", err)
	}

	return nil
}

// GetAgent retrieves agent information
func (c *SpaceTradersClient) GetAgent(ctx context.Context, token string) (*common.AgentData, error) {
	path := "/my/agent"

	var response struct {
		Data struct {
			AccountID       string `json:"accountId"`
			Symbol          string `json:"symbol"`
			Headquarters    string `json:"headquarters"`
			Credits         int    `json:"credits"`
			StartingFaction string `json:"startingFaction"`
		} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get agent: %w", err)
	}

	return &common.AgentData{
		AccountID:       response.Data.AccountID,
		Symbol:          response.Data.Symbol,
		Headquarters:    response.Data.Headquarters,
		Credits:         response.Data.Credits,
		StartingFaction: response.Data.StartingFaction,
	}, nil
}

// ListWaypoints retrieves waypoints for a system with pagination
func (c *SpaceTradersClient) ListWaypoints(ctx context.Context, systemSymbol, token string, page, limit int) (*common.WaypointsListResponse, error) {
	path := fmt.Sprintf("/systems/%s/waypoints?page=%d&limit=%d", systemSymbol, page, limit)

	var response struct {
		Data []struct {
			Symbol   string  `json:"symbol"`
			Type     string  `json:"type"`
			X        float64 `json:"x"`
			Y        float64 `json:"y"`
			Traits   []map[string]interface{} `json:"traits"`
			Orbitals []map[string]string `json:"orbitals"`
		} `json:"data"`
		Meta struct {
			Total int `json:"total"`
			Page  int `json:"page"`
			Limit int `json:"limit"`
		} `json:"meta"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to list waypoints: %w", err)
	}

	waypoints := make([]common.WaypointAPIData, len(response.Data))
	for i, wp := range response.Data {
		waypoints[i] = common.WaypointAPIData{
			Symbol:   wp.Symbol,
			Type:     wp.Type,
			X:        wp.X,
			Y:        wp.Y,
			Traits:   wp.Traits,
			Orbitals: wp.Orbitals,
		}
	}

	return &common.WaypointsListResponse{
		Data: waypoints,
		Meta: common.PaginationMeta{
			Total: response.Meta.Total,
			Page:  response.Meta.Page,
			Limit: response.Meta.Limit,
		},
	}, nil
}

// request makes an HTTP request with rate limiting and retries
func (c *SpaceTradersClient) request(ctx context.Context, method, path, token string, body interface{}, result interface{}) error {
	url := c.baseURL + path

	var reqBody io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonData)
	}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		// Wait for rate limiter
		if err := c.rateLimiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter error: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			// Retry on network errors
			time.Sleep(retryBackoffBase * time.Duration(1<<attempt))
			continue
		}

		defer resp.Body.Close()

		// Handle 429 Too Many Requests
		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("rate limited (429)")
			// Exponential backoff
			time.Sleep(retryBackoffBase * time.Duration(1<<attempt))
			continue
		}

		// Read response body
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read response: %w", err)
		}

		// Handle non-2xx status codes
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
		}

		// Parse response if result is provided
		if result != nil {
			if err := json.Unmarshal(respBody, result); err != nil {
				return fmt.Errorf("failed to unmarshal response: %w", err)
			}
		}

		return nil
	}

	return fmt.Errorf("max retries exceeded: %w", lastErr)
}
