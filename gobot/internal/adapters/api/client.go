package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/ports"
)

const (
	baseURL           = "https://api.spacetraders.io/v2"
	defaultTimeout    = 30 * time.Second
	defaultMaxRetries = 5
	defaultBackoffBase = time.Second
	defaultCircuitThreshold = 5
	defaultCircuitTimeout = 60 * time.Second
)

// SpaceTradersClient implements the APIClient interface
type SpaceTradersClient struct {
	httpClient     *http.Client
	rateLimiter    *rate.Limiter
	baseURL        string
	maxRetries     int
	backoffBase    time.Duration
	circuitBreaker *CircuitBreaker
	clock          shared.Clock
}

// NewSpaceTradersClient creates a new SpaceTraders API client with default settings
// Rate limit: 2 requests per second with burst of 2
// Retry: max 5 attempts with 1s exponential backoff
// Circuit breaker: opens after 5 consecutive failures, timeout 60s
func NewSpaceTradersClient() *SpaceTradersClient {
	return NewSpaceTradersClientWithConfig(
		baseURL,
		defaultMaxRetries,
		defaultBackoffBase,
		defaultCircuitThreshold,
		defaultCircuitTimeout,
		nil, // Use RealClock by default
	)
}

// NewSpaceTradersClientWithConfig creates a new SpaceTraders API client with custom configuration
// If clock is nil, uses RealClock for production
func NewSpaceTradersClientWithConfig(
	baseURL string,
	maxRetries int,
	backoffBase time.Duration,
	circuitThreshold int,
	circuitTimeout time.Duration,
	clock shared.Clock,
) *SpaceTradersClient {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &SpaceTradersClient{
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
		rateLimiter:    rate.NewLimiter(rate.Limit(2), 2), // 2 req/sec, burst 2
		baseURL:        baseURL,
		maxRetries:     maxRetries,
		backoffBase:    backoffBase,
		circuitBreaker: NewCircuitBreaker(circuitThreshold, circuitTimeout, clock),
		clock:          clock,
	}
}

// GetShip retrieves ship details
func (c *SpaceTradersClient) GetShip(ctx context.Context, symbol, token string) (*navigation.ShipData, error) {
	path := fmt.Sprintf("/my/ships/%s", symbol)

	var response struct {
		Data struct {
			Symbol     string `json:"symbol"`
			Nav        struct {
				SystemSymbol   string `json:"systemSymbol"`
				WaypointSymbol string `json:"waypointSymbol"`
				Status         string `json:"status"`
				Route          *struct {
					Arrival string `json:"arrival"`
				} `json:"route,omitempty"` // Only present when IN_TRANSIT
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
			Frame struct {
				Symbol string `json:"symbol"`
			} `json:"frame"`
		} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get ship: %w", err)
	}

	// Convert cargo inventory
	inventory := make([]navigation.CargoItemData, len(response.Data.Cargo.Inventory))
	for i, item := range response.Data.Cargo.Inventory {
		inventory[i] = navigation.CargoItemData{
			Symbol:      item.Symbol,
			Name:        item.Name,
			Description: item.Description,
			Units:       item.Units,
		}
	}

	cargo := &navigation.CargoData{
		Capacity:  response.Data.Cargo.Capacity,
		Units:     response.Data.Cargo.Units,
		Inventory: inventory,
	}

	// Extract arrival time if ship is IN_TRANSIT
	arrivalTime := ""
	if response.Data.Nav.Route != nil {
		arrivalTime = response.Data.Nav.Route.Arrival
	}

	return &navigation.ShipData{
		Symbol:        response.Data.Symbol,
		Location:      response.Data.Nav.WaypointSymbol,
		NavStatus:     response.Data.Nav.Status,
		ArrivalTime:   arrivalTime, // ISO8601 timestamp when IN_TRANSIT
		FuelCurrent:   response.Data.Fuel.Current,
		FuelCapacity:  response.Data.Fuel.Capacity,
		CargoCapacity: response.Data.Cargo.Capacity,
		CargoUnits:    response.Data.Cargo.Units,
		EngineSpeed:   response.Data.Engine.Speed,
		FrameSymbol:   response.Data.Frame.Symbol,
		Cargo:         cargo,
	}, nil
}

// ListShips retrieves all ships for the authenticated agent
// Uses pagination to fetch all ships (20 per page)
func (c *SpaceTradersClient) ListShips(ctx context.Context, token string) ([]*navigation.ShipData, error) {
	var allShips []*navigation.ShipData
	page := 1
	limit := 20

	for {
		path := fmt.Sprintf("/my/ships?page=%d&limit=%d", page, limit)

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
				Frame struct {
					Symbol string `json:"symbol"`
				} `json:"frame"`
			} `json:"data"`
			Meta struct {
				Total int `json:"total"`
				Page  int `json:"page"`
				Limit int `json:"limit"`
			} `json:"meta"`
		}

		if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
			return nil, fmt.Errorf("failed to list ships (page %d): %w", page, err)
		}

		// If no data returned, we've hit the end
		if len(response.Data) == 0 {
			break
		}

		// Convert this page's ships
		for _, ship := range response.Data {
			// Convert cargo inventory
			inventory := make([]navigation.CargoItemData, len(ship.Cargo.Inventory))
			for j, item := range ship.Cargo.Inventory {
				inventory[j] = navigation.CargoItemData{
					Symbol:      item.Symbol,
					Name:        item.Name,
					Description: item.Description,
					Units:       item.Units,
				}
			}

			cargo := &navigation.CargoData{
				Capacity:  ship.Cargo.Capacity,
				Units:     ship.Cargo.Units,
				Inventory: inventory,
			}

			allShips = append(allShips, &navigation.ShipData{
				Symbol:        ship.Symbol,
				Location:      ship.Nav.WaypointSymbol,
				NavStatus:     ship.Nav.Status,
				FuelCurrent:   ship.Fuel.Current,
				FuelCapacity:  ship.Fuel.Capacity,
				CargoCapacity: ship.Cargo.Capacity,
				CargoUnits:    ship.Cargo.Units,
				EngineSpeed:   ship.Engine.Speed,
				FrameSymbol:   ship.Frame.Symbol,
				Cargo:         cargo,
			})
		}

		// Move to next page
		page++
	}

	return allShips, nil
}

// NavigateShip navigates a ship to a destination
func (c *SpaceTradersClient) NavigateShip(ctx context.Context, symbol, destination, token string) (*navigation.NavigationResult, error) {
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

	return &navigation.NavigationResult{
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
func (c *SpaceTradersClient) RefuelShip(ctx context.Context, symbol, token string, units *int) (*navigation.RefuelResult, error) {
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

	return &navigation.RefuelResult{
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
func (c *SpaceTradersClient) GetAgent(ctx context.Context, token string) (*player.AgentData, error) {
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

	return &player.AgentData{
		AccountID:       response.Data.AccountID,
		Symbol:          response.Data.Symbol,
		Headquarters:    response.Data.Headquarters,
		Credits:         response.Data.Credits,
		StartingFaction: response.Data.StartingFaction,
	}, nil
}

// ListWaypoints retrieves waypoints for a system with pagination
func (c *SpaceTradersClient) ListWaypoints(ctx context.Context, systemSymbol, token string, page, limit int) (*system.WaypointsListResponse, error) {
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

	waypoints := make([]system.WaypointAPIData, len(response.Data))
	for i, wp := range response.Data {
		waypoints[i] = system.WaypointAPIData{
			Symbol:   wp.Symbol,
			Type:     wp.Type,
			X:        wp.X,
			Y:        wp.Y,
			Traits:   wp.Traits,
			Orbitals: wp.Orbitals,
		}
	}

	return &system.WaypointsListResponse{
		Data: waypoints,
		Meta: system.PaginationMeta{
			Total: response.Meta.Total,
			Page:  response.Meta.Page,
			Limit: response.Meta.Limit,
		},
	}, nil
}

// NegotiateContract negotiates a new contract for the ship
// Special handling for error 4511 (agent already has contract)
func (c *SpaceTradersClient) NegotiateContract(ctx context.Context, shipSymbol, token string) (*ports.ContractNegotiationResult, error) {
	path := fmt.Sprintf("/my/ships/%s/negotiate/contract", shipSymbol)

	var response struct {
		Data *struct {
			Contract map[string]interface{} `json:"contract"`
		} `json:"data"`
		Error *struct {
			Code int `json:"code"`
			Data struct {
				ContractID string `json:"contractId"`
			} `json:"data"`
		} `json:"error"`
	}

	// Send empty body as required by API
	emptyBody := map[string]interface{}{}
	err := c.request(ctx, "POST", path, token, emptyBody, &response)

	// Check for error 4511 - agent already has contract
	if response.Error != nil && response.Error.Code == 4511 {
		return &ports.ContractNegotiationResult{
			ErrorCode:          4511,
			ExistingContractID: response.Error.Data.ContractID,
		}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to negotiate contract: %w", err)
	}

	// Parse contract from response
	if response.Data == nil || response.Data.Contract == nil {
		return nil, fmt.Errorf("invalid response: missing contract data")
	}

	contractData, err := c.parseContractData(response.Data.Contract)
	if err != nil {
		return nil, fmt.Errorf("failed to parse contract: %w", err)
	}

	return &ports.ContractNegotiationResult{
		Contract: contractData,
	}, nil
}

// GetContract retrieves contract details
func (c *SpaceTradersClient) GetContract(ctx context.Context, contractID, token string) (*ports.ContractData, error) {
	path := fmt.Sprintf("/my/contracts/%s", contractID)

	var response struct {
		Data map[string]interface{} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get contract: %w", err)
	}

	return c.parseContractData(response.Data)
}

// AcceptContract accepts a contract
func (c *SpaceTradersClient) AcceptContract(ctx context.Context, contractID, token string) (*ports.ContractData, error) {
	path := fmt.Sprintf("/my/contracts/%s/accept", contractID)

	var response struct {
		Data struct {
			Contract map[string]interface{} `json:"contract"`
		} `json:"data"`
	}

	// Send empty body as required by API
	emptyBody := map[string]interface{}{}
	if err := c.request(ctx, "POST", path, token, emptyBody, &response); err != nil {
		return nil, fmt.Errorf("failed to accept contract: %w", err)
	}

	return c.parseContractData(response.Data.Contract)
}

// DeliverContract delivers cargo to a contract
func (c *SpaceTradersClient) DeliverContract(ctx context.Context, contractID, shipSymbol, tradeSymbol string, units int, token string) (*ports.ContractData, error) {
	path := fmt.Sprintf("/my/contracts/%s/deliver", contractID)

	body := map[string]interface{}{
		"shipSymbol":  shipSymbol,
		"tradeSymbol": tradeSymbol,
		"units":       units,
	}

	var response struct {
		Data struct {
			Contract map[string]interface{} `json:"contract"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to deliver contract: %w", err)
	}

	return c.parseContractData(response.Data.Contract)
}

// FulfillContract fulfills a contract
func (c *SpaceTradersClient) FulfillContract(ctx context.Context, contractID, token string) (*ports.ContractData, error) {
	path := fmt.Sprintf("/my/contracts/%s/fulfill", contractID)

	var response struct {
		Data struct {
			Contract map[string]interface{} `json:"contract"`
		} `json:"data"`
	}

	// Send empty body as required by API
	emptyBody := map[string]interface{}{}
	if err := c.request(ctx, "POST", path, token, emptyBody, &response); err != nil {
		return nil, fmt.Errorf("failed to fulfill contract: %w", err)
	}

	return c.parseContractData(response.Data.Contract)
}

// PurchaseCargo purchases cargo at the current market
func (c *SpaceTradersClient) PurchaseCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*ports.PurchaseResult, error) {
	path := fmt.Sprintf("/my/ships/%s/purchase", shipSymbol)

	body := map[string]interface{}{
		"symbol": goodSymbol,
		"units":  units,
	}

	var response struct {
		Data struct {
			Transaction struct {
				TotalPrice int `json:"totalPrice"`
				Units      int `json:"units"`
			} `json:"transaction"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to purchase cargo: %w", err)
	}

	return &ports.PurchaseResult{
		TotalCost:  response.Data.Transaction.TotalPrice,
		UnitsAdded: response.Data.Transaction.Units,
	}, nil
}

// SellCargo sells cargo from the ship
func (c *SpaceTradersClient) SellCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*ports.SellResult, error) {
	path := fmt.Sprintf("/my/ships/%s/sell", shipSymbol)

	body := map[string]interface{}{
		"symbol": goodSymbol,
		"units":  units,
	}

	var response struct {
		Data struct {
			Transaction struct {
				TotalPrice int `json:"totalPrice"`
				Units      int `json:"units"`
			} `json:"transaction"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to sell cargo: %w", err)
	}

	return &ports.SellResult{
		TotalRevenue: response.Data.Transaction.TotalPrice,
		UnitsSold:    response.Data.Transaction.Units,
	}, nil
}

// JettisonCargo jettisons cargo from the ship
func (c *SpaceTradersClient) JettisonCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) error {
	path := fmt.Sprintf("/my/ships/%s/jettison", shipSymbol)

	body := map[string]interface{}{
		"symbol": goodSymbol,
		"units":  units,
	}

	if err := c.request(ctx, "POST", path, token, body, nil); err != nil {
		return fmt.Errorf("failed to jettison cargo: %w", err)
	}

	return nil
}

// GetMarket retrieves market data for a waypoint
func (c *SpaceTradersClient) GetMarket(ctx context.Context, systemSymbol, waypointSymbol, token string) (*ports.MarketData, error) {
	path := fmt.Sprintf("/systems/%s/waypoints/%s/market", systemSymbol, waypointSymbol)

	var response struct {
		Data struct {
			Symbol     string `json:"symbol"`
			TradeGoods []struct {
				Symbol        string `json:"symbol"`
				Supply        string `json:"supply"`
				SellPrice     int    `json:"sellPrice"`
				PurchasePrice int    `json:"purchasePrice"`
				TradeVolume   int    `json:"tradeVolume"`
			} `json:"tradeGoods"`
		} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get market: %w", err)
	}

	tradeGoods := make([]ports.TradeGoodData, len(response.Data.TradeGoods))
	for i, good := range response.Data.TradeGoods {
		tradeGoods[i] = ports.TradeGoodData{
			Symbol:        good.Symbol,
			Supply:        good.Supply,
			SellPrice:     good.SellPrice,
			PurchasePrice: good.PurchasePrice,
			TradeVolume:   good.TradeVolume,
		}
	}

	return &ports.MarketData{
		Symbol:     response.Data.Symbol,
		TradeGoods: tradeGoods,
	}, nil
}

// parseContractData parses contract data from API response
func (c *SpaceTradersClient) parseContractData(data map[string]interface{}) (*ports.ContractData, error) {
	// Extract contract ID
	id, ok := data["id"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid contract id")
	}

	// Extract faction symbol
	factionSymbol, ok := data["factionSymbol"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid factionSymbol")
	}

	// Extract type
	contractType, ok := data["type"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid contract type")
	}

	// Extract accepted status
	accepted, ok := data["accepted"].(bool)
	if !ok {
		return nil, fmt.Errorf("missing or invalid accepted status")
	}

	// Extract fulfilled status
	fulfilled, ok := data["fulfilled"].(bool)
	if !ok {
		return nil, fmt.Errorf("missing or invalid fulfilled status")
	}

	// Extract terms
	termsData, ok := data["terms"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("missing or invalid terms")
	}

	// Parse terms
	deadlineToAccept, _ := termsData["deadline"].(string)
	deadline, _ := termsData["deadline"].(string)

	// Parse payment
	var payment ports.PaymentData
	if paymentData, ok := termsData["payment"].(map[string]interface{}); ok {
		if onAccepted, ok := paymentData["onAccepted"].(float64); ok {
			payment.OnAccepted = int(onAccepted)
		}
		if onFulfilled, ok := paymentData["onFulfilled"].(float64); ok {
			payment.OnFulfilled = int(onFulfilled)
		}
	}

	// Parse deliveries
	var deliveries []ports.DeliveryData
	if deliveriesData, ok := termsData["deliver"].([]interface{}); ok {
		deliveries = make([]ports.DeliveryData, len(deliveriesData))
		for i, deliveryItem := range deliveriesData {
			if delivery, ok := deliveryItem.(map[string]interface{}); ok {
				tradeSymbol, _ := delivery["tradeSymbol"].(string)
				destinationSymbol, _ := delivery["destinationSymbol"].(string)
				unitsRequired := 0
				if ur, ok := delivery["unitsRequired"].(float64); ok {
					unitsRequired = int(ur)
				}
				unitsFulfilled := 0
				if uf, ok := delivery["unitsFulfilled"].(float64); ok {
					unitsFulfilled = int(uf)
				}

				deliveries[i] = ports.DeliveryData{
					TradeSymbol:       tradeSymbol,
					DestinationSymbol: destinationSymbol,
					UnitsRequired:     unitsRequired,
					UnitsFulfilled:    unitsFulfilled,
				}
			}
		}
	}

	return &ports.ContractData{
		ID:            id,
		FactionSymbol: factionSymbol,
		Type:          contractType,
		Terms: ports.ContractTermsData{
			DeadlineToAccept: deadlineToAccept,
			Deadline:         deadline,
			Payment:          payment,
			Deliveries:       deliveries,
		},
		Accepted:  accepted,
		Fulfilled: fulfilled,
	}, nil
}

// request makes an HTTP request with rate limiting, circuit breaker, and retries
func (c *SpaceTradersClient) request(ctx context.Context, method, path, token string, body interface{}, result interface{}) error {
	url := c.baseURL + path

	var lastErr error

	// Wrap the entire retry loop in circuit breaker
	// Circuit breaker only opens after ALL retries fail
	err := c.circuitBreaker.Call(func() error {
		// Attempt the request with retries
		for attempt := 0; attempt <= c.maxRetries; attempt++ {
			// Wait for rate limiter
			if err := c.rateLimiter.Wait(ctx); err != nil {
				return fmt.Errorf("rate limiter error: %w", err)
			}

			// Prepare request body
			var reqBody io.Reader
			if body != nil {
				jsonData, err := json.Marshal(body)
				if err != nil {
					return fmt.Errorf("failed to marshal request body: %w", err)
				}
				reqBody = bytes.NewBuffer(jsonData)
			}

			// Create HTTP request
			req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
			if err != nil {
				return fmt.Errorf("failed to create request: %w", err)
			}

			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)

			// Execute HTTP request
			resp, err := c.httpClient.Do(req)
			if err != nil {
				// Network error - retryable
				lastErr = &retryableError{
					message: fmt.Errorf("network error: %w", err).Error(),
				}

				// Last attempt - don't sleep, just record error
				if attempt >= c.maxRetries {
					break
				}

				// Check for context cancellation before sleeping
				if ctx.Err() != nil {
					return fmt.Errorf("context cancelled: %w", ctx.Err())
				}

				// Calculate backoff and sleep using clock (instant in tests with MockClock)
				backoffDelay := c.backoffBase * time.Duration(1<<attempt)
				c.clock.Sleep(backoffDelay)
				continue
			}

			// Only close response body if we have a valid response
			if resp != nil && resp.Body != nil {
				defer resp.Body.Close()
			}

			// Read response body
			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				return fmt.Errorf("failed to read response: %w", err)
			}

			// Handle 429 Too Many Requests - retryable
			if resp.StatusCode == http.StatusTooManyRequests {
				fmt.Printf("[DEBUG] Got 429, attempt=%d\n", attempt)
				var retryAfterDuration time.Duration

				// Check for Retry-After header
				if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
					if seconds, err := strconv.Atoi(retryAfter); err == nil {
						retryAfterDuration = time.Duration(seconds) * time.Second
					}
				}

				lastErr = &retryableError{
					message:    "rate limited (429)",
					retryAfter: retryAfterDuration,
				}

				// Last attempt - don't sleep
				if attempt >= c.maxRetries {
					fmt.Printf("[DEBUG] Last attempt reached, breaking\n")
					break
				}

				// Check for context cancellation before sleeping
				if ctx.Err() != nil {
					return fmt.Errorf("context cancelled: %w", ctx.Err())
				}

				// Calculate backoff delay
				backoffDelay := c.backoffBase * time.Duration(1<<attempt)
				if retryAfterDuration > 0 {
					backoffDelay = retryAfterDuration
				}

				fmt.Printf("[DEBUG] Sleeping for %v before retry\n", backoffDelay)
				// Sleep using clock (instant in tests with MockClock)
				c.clock.Sleep(backoffDelay)
				fmt.Printf("[DEBUG] Sleep done, continuing to next attempt\n")
				continue
			}

			// Handle 503 Service Unavailable - retryable
			if resp.StatusCode == http.StatusServiceUnavailable {
				lastErr = &retryableError{
					message: "service unavailable (503)",
				}

				// Last attempt - don't sleep
				if attempt >= c.maxRetries {
					break
				}

				// Check for context cancellation before sleeping
				if ctx.Err() != nil {
					return fmt.Errorf("context cancelled: %w", ctx.Err())
				}

				// Calculate backoff and sleep using clock (instant in tests with MockClock)
				backoffDelay := c.backoffBase * time.Duration(1<<attempt)
				c.clock.Sleep(backoffDelay)
				continue
			}

			// Handle 5xx server errors - retryable
			if resp.StatusCode >= 500 {
				lastErr = &retryableError{
					message: fmt.Sprintf("server error (%d)", resp.StatusCode),
				}

				// Last attempt - don't sleep
				if attempt >= c.maxRetries {
					break
				}

				// Check for context cancellation before sleeping
				if ctx.Err() != nil {
					return fmt.Errorf("context cancelled: %w", ctx.Err())
				}

				// Calculate backoff and sleep using clock (instant in tests with MockClock)
				backoffDelay := c.backoffBase * time.Duration(1<<attempt)
				c.clock.Sleep(backoffDelay)
				continue
			}

			// Handle 4xx client errors (except 429) - NOT retryable
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
			}

			// Handle non-2xx status codes - NOT retryable
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
			}

			// Parse response if result is provided
			if result != nil {
				if err := json.Unmarshal(respBody, result); err != nil {
					return fmt.Errorf("failed to unmarshal response: %w", err)
				}
			}

			// Success!
			return nil
		}

		// All retries exhausted
		if lastErr != nil {
			return fmt.Errorf("max retries exceeded: %w", lastErr)
		}
		return fmt.Errorf("max retries exceeded")
	})

	// Circuit breaker open - fail immediately
	if errors.Is(err, ErrCircuitOpen) {
		return fmt.Errorf("circuit breaker open: %w", err)
	}

	return err
}

// retryableError represents an error that should trigger a retry
type retryableError struct {
	message    string
	retryAfter time.Duration
}

func (e *retryableError) Error() string {
	return e.message
}

// GetCircuitBreakerState returns the current circuit breaker state (for testing)
func (c *SpaceTradersClient) GetCircuitBreakerState() CircuitState {
	return c.circuitBreaker.GetState()
}

// GetCircuitBreakerFailureCount returns the consecutive failure count (for testing)
func (c *SpaceTradersClient) GetCircuitBreakerFailureCount() int {
	return c.circuitBreaker.GetFailureCount()
}

// SetCircuitBreakerState sets the circuit breaker state (for testing)
func (c *SpaceTradersClient) SetCircuitBreakerState(state CircuitState, failures int, lastFailure time.Time) {
	c.circuitBreaker.SetState(state, failures, lastFailure)
}

// ResetCircuitBreaker resets the circuit breaker to closed state (for testing)
func (c *SpaceTradersClient) ResetCircuitBreaker() {
	c.circuitBreaker.Reset()
}
