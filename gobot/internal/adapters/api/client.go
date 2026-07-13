package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

const (
	baseURL            = "https://api.spacetraders.io/v2"
	defaultTimeout     = 30 * time.Second
	defaultMaxRetries  = 10               // Increased from 5 to handle persistent 429s
	defaultBackoffBase = 2 * time.Second  // Increased from 1s for more aggressive backoff
	maxBackoffDuration = 30 * time.Second // Cap exponential backoff to prevent extreme waits

	// RateLimitPerSecond is the sustained request-rate ceiling this client
	// enforces against SpaceTraders. Exported so the budget
	// tracker (sp-51ti, internal/adapters/grpc composition root) can compute
	// utilization-vs-ceiling against the same number the limiter actually
	// uses, instead of a second hardcoded copy that could drift.
	RateLimitPerSecond = 2.0

	// RateLimitBurst is the token-bucket burst the limiter actually uses
	// (SpaceTraders allows ~30 req/60s burst). Exported as the SINGLE source of
	// truth for the burst, the twin of RateLimitPerSecond: sp-a5dq found the
	// config.yaml api.rate_limit.burst knob (default 10) was never plumbed to
	// this client and only surfaced in `config show`, so the displayed 10
	// drifted from the real 30. The config default now mirrors this constant;
	// wiring the knob through to make burst live-tunable is a deferred decision.
	RateLimitBurst = 30

	errCodeAgentHasContract = 4511
	errCodeShipMustBeDocked = 4214
	errCodeShipNotDocked    = 4244
)

// APIMetricsRecorder defines the interface for recording API metrics
type APIMetricsRecorder interface {
	RecordAPIRequest(method string, endpoint string, statusCode int, duration float64)
	RecordAPIRetry(method string, endpoint string, reason string)
	RecordRateLimitWait(method string, endpoint string, duration float64)
	SetRateLimiterTokens(tokens float64)
}

// SpaceTradersClient implements the APIClient interface
type SpaceTradersClient struct {
	httpClient       *http.Client
	rateLimiter      *rate.Limiter
	baseURL          string
	maxRetries       int
	backoffBase      time.Duration
	clock            shared.Clock
	metricsCollector APIMetricsRecorder
	budgetTracker    *metrics.APIBudgetTracker
}

// NewSpaceTradersClient creates a new SpaceTraders API client with default settings
// Rate limit: 2 requests per second with burst of 30
// Retry: max 10 attempts with 2s exponential backoff + jitter (capped at 30s)
func NewSpaceTradersClient() *SpaceTradersClient {
	return NewSpaceTradersClientWithConfig(
		baseURL,
		defaultMaxRetries,
		defaultBackoffBase,
		nil, // Use RealClock by default
	)
}

// NewSpaceTradersClientWithConfig creates a new SpaceTraders API client with custom configuration
// If clock is nil, uses RealClock for production
// Automatically uses the global API metrics collector if one is set
func NewSpaceTradersClientWithConfig(
	baseURL string,
	maxRetries int,
	backoffBase time.Duration,
	clock shared.Clock,
) *SpaceTradersClient {
	if clock == nil {
		clock = shared.NewRealClock()
	}

	client := &SpaceTradersClient{
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
		rateLimiter:      rate.NewLimiter(rate.Limit(RateLimitPerSecond), RateLimitBurst), // 2 req/sec, burst 30 (SpaceTraders allows 30 req/60s burst)
		baseURL:          baseURL,
		maxRetries:       maxRetries,
		backoffBase:      backoffBase,
		clock:            clock,
		metricsCollector: nil,
	}

	// Auto-wire global metrics collector if available
	// This requires importing the metrics package, which we'll add
	// For now, callers can use SetMetricsCollector() to enable metrics

	return client
}

// SetMetricsCollector sets the metrics collector for the client
// This allows metrics to be enabled after client construction
func (c *SpaceTradersClient) SetMetricsCollector(collector APIMetricsRecorder) {
	c.metricsCollector = collector
}

// getMetricsCollector returns the metrics collector for this client.
// If no local collector is set, it checks for the global API collector.
func (c *SpaceTradersClient) getMetricsCollector() APIMetricsRecorder {
	if c.metricsCollector != nil {
		return c.metricsCollector
	}
	// Check the concrete pointer before boxing it into the interface: a
	// typed-nil *APIMetricsCollector passes callers' nil checks and then
	// crashes on first use (metrics disabled = nil global collector).
	if collector := metrics.GetGlobalAPICollector(); collector != nil {
		return collector
	}
	return nil
}

// SetBudgetTracker sets the API request-budget tracker for this client
// (sp-51ti). This allows the tracker to be enabled after client construction,
// mirroring SetMetricsCollector.
func (c *SpaceTradersClient) SetBudgetTracker(tracker *metrics.APIBudgetTracker) {
	c.budgetTracker = tracker
}

// getBudgetTracker returns the budget tracker for this client. If no local
// tracker is set, it falls back to the global tracker (sp-51ti daemon
// startup wiring). May return nil; APIBudgetTracker.Record tolerates a nil
// receiver, so callers never need an extra nil-check.
func (c *SpaceTradersClient) getBudgetTracker() *metrics.APIBudgetTracker {
	if c.budgetTracker != nil {
		return c.budgetTracker
	}
	return metrics.GetGlobalAPIBudgetTracker()
}

// GetRateLimiterTokens returns the current number of available tokens in the rate limiter
// This is useful for monitoring rate limiter saturation (max 30 tokens)
func (c *SpaceTradersClient) GetRateLimiterTokens() float64 {
	return c.rateLimiter.Tokens()
}

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

// ListShips retrieves all ships for the authenticated agent
// Uses pagination to fetch all ships (20 per page)
func (c *SpaceTradersClient) ListShips(ctx context.Context, token string) ([]*navigation.ShipData, error) {
	var allShips []*navigation.ShipData
	page := 1
	limit := 20

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

		// If no data returned, we've hit the end
		if len(response.Data) == 0 {
			break
		}

		// Convert this page's ships
		for i := range response.Data {
			allShips = append(allShips, response.Data[i].toShipData())
		}

		// Move to next page
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

	// Extract arrival time string (ISO8601 timestamp from API)
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

	// Use the existing request method with PATCH verb
	if err := c.request(ctx, "PATCH", path, token, body, nil); err != nil {
		return fmt.Errorf("failed to set flight mode: %w", err)
	}

	return nil
}

// JumpShip executes a jump through a jump gate to a different system.
// waypointSymbol must be the destination JUMP_GATE waypoint symbol (e.g.
// "X1-GQ92-I51") - not a bare system symbol. The live SpaceTraders API
// requires "waypointSymbol" in the request body and 422s with
// "waypointSymbol Required, received undefined" otherwise (sp-n0x7 round 2).
func (c *SpaceTradersClient) JumpShip(ctx context.Context, shipSymbol, waypointSymbol, token string) (*domainPorts.JumpResult, error) {
	path := fmt.Sprintf("/my/ships/%s/jump", shipSymbol)

	body := map[string]string{
		"waypointSymbol": waypointSymbol,
	}

	var response struct {
		Data struct {
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

	return &domainPorts.JumpResult{
		DestinationSystem:   response.Data.Nav.SystemSymbol,
		DestinationWaypoint: response.Data.Nav.WaypointSymbol,
		CooldownSeconds:     response.Data.Cooldown.RemainingSeconds,
		TotalPrice:          response.Data.Transaction.TotalPrice,
	}, nil
}

// GetJumpGate retrieves information about a jump gate waypoint
func (c *SpaceTradersClient) GetJumpGate(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.JumpGateData, error) {
	path := fmt.Sprintf("/systems/%s/waypoints/%s/jump-gate", systemSymbol, waypointSymbol)

	var response struct {
		Data struct {
			Symbol      string   `json:"symbol"`
			Connections []string `json:"connections"`
		} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get jump gate: %w", err)
	}

	return &domainPorts.JumpGateData{
		Symbol:      response.Data.Symbol,
		Connections: response.Data.Connections,
	}, nil
}

// GetWaypoint reads a single waypoint's detail. Only the fields the gate graph
// needs are decoded: the symbol and isUnderConstruction (whether a jump gate is
// still being built). The jump-gate connections list carries symbols only, so the
// build state of a connected gate is resolved with this per-waypoint read
// (sp-8qhu) — an unbuilt gate is a dead edge the BFS must never route through.
func (c *SpaceTradersClient) GetWaypoint(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.WaypointDetail, error) {
	path := fmt.Sprintf("/systems/%s/waypoints/%s", systemSymbol, waypointSymbol)

	var response struct {
		Data struct {
			Symbol              string `json:"symbol"`
			IsUnderConstruction bool   `json:"isUnderConstruction"`
		} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get waypoint %s: %w", waypointSymbol, err)
	}

	return &domainPorts.WaypointDetail{
		Symbol:              response.Data.Symbol,
		IsUnderConstruction: response.Data.IsUnderConstruction,
	}, nil
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
			Symbol   string                   `json:"symbol"`
			Type     string                   `json:"type"`
			X        float64                  `json:"x"`
			Y        float64                  `json:"y"`
			Traits   []map[string]interface{} `json:"traits"`
			Orbitals []map[string]string      `json:"orbitals"`
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
func (c *SpaceTradersClient) NegotiateContract(ctx context.Context, shipSymbol, token string) (*domainPorts.ContractNegotiationResult, error) {
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
	err := c.requestWithErrorParsing(ctx, "POST", path, token, emptyBody, &response)

	// Check for known error codes that caller can handle reactively
	if response.Error != nil {
		switch response.Error.Code {
		case errCodeAgentHasContract:
			return &domainPorts.ContractNegotiationResult{
				ErrorCode:          errCodeAgentHasContract,
				ExistingContractID: response.Error.Data.ContractID,
			}, nil
		case errCodeShipMustBeDocked, errCodeShipNotDocked:
			return &domainPorts.ContractNegotiationResult{
				ErrorCode: response.Error.Code,
			}, nil
		}
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

	return &domainPorts.ContractNegotiationResult{
		Contract: contractData,
	}, nil
}

// GetContract retrieves contract details
func (c *SpaceTradersClient) GetContract(ctx context.Context, contractID, token string) (*domainPorts.ContractData, error) {
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
func (c *SpaceTradersClient) AcceptContract(ctx context.Context, contractID, token string) (*domainPorts.ContractData, error) {
	path := fmt.Sprintf("/my/contracts/%s/accept", contractID)

	var response struct {
		Data struct {
			// Agent carries the post-acceptance credits in-band; the ledger
			// prefers it over a separately-fetched (stale) GetAgent snapshot.
			Agent *struct {
				Credits int `json:"credits"`
			} `json:"agent"`
			Contract map[string]interface{} `json:"contract"`
		} `json:"data"`
	}

	// Send empty body as required by API
	emptyBody := map[string]interface{}{}
	if err := c.request(ctx, "POST", path, token, emptyBody, &response); err != nil {
		return nil, fmt.Errorf("failed to accept contract: %w", err)
	}

	contractData, err := c.parseContractData(response.Data.Contract)
	if err != nil {
		return nil, err
	}
	if response.Data.Agent != nil {
		credits := response.Data.Agent.Credits
		contractData.AgentCredits = &credits
	}
	return contractData, nil
}

// DeliverContract delivers cargo to a contract
func (c *SpaceTradersClient) DeliverContract(ctx context.Context, contractID, shipSymbol, tradeSymbol string, units int, token string) (*domainPorts.ContractData, error) {
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
func (c *SpaceTradersClient) FulfillContract(ctx context.Context, contractID, token string) (*domainPorts.ContractData, error) {
	path := fmt.Sprintf("/my/contracts/%s/fulfill", contractID)

	var response struct {
		Data struct {
			// Agent carries the post-fulfillment credits in-band; the ledger
			// prefers it over a separately-fetched (stale) GetAgent snapshot.
			Agent *struct {
				Credits int `json:"credits"`
			} `json:"agent"`
			Contract map[string]interface{} `json:"contract"`
		} `json:"data"`
	}

	// Send empty body as required by API
	emptyBody := map[string]interface{}{}
	if err := c.request(ctx, "POST", path, token, emptyBody, &response); err != nil {
		return nil, fmt.Errorf("failed to fulfill contract: %w", err)
	}

	contractData, err := c.parseContractData(response.Data.Contract)
	if err != nil {
		return nil, err
	}
	if response.Data.Agent != nil {
		credits := response.Data.Agent.Credits
		contractData.AgentCredits = &credits
	}
	return contractData, nil
}

// PurchaseCargo purchases cargo at the current market
func (c *SpaceTradersClient) PurchaseCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*domainPorts.PurchaseResult, error) {
	path := fmt.Sprintf("/my/ships/%s/purchase", shipSymbol)

	body := map[string]interface{}{
		"symbol": goodSymbol,
		"units":  units,
	}

	var response struct {
		Data struct {
			// Agent is a pointer so an omitted block (nil) is distinguishable
			// from a real zero balance; the in-band credits are the ledger's
			// authoritative post-transaction balance.
			Agent *struct {
				Credits int `json:"credits"`
			} `json:"agent"`
			Transaction struct {
				TotalPrice int `json:"totalPrice"`
				Units      int `json:"units"`
			} `json:"transaction"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to purchase cargo: %w", err)
	}

	result := &domainPorts.PurchaseResult{
		TotalCost:  response.Data.Transaction.TotalPrice,
		UnitsAdded: response.Data.Transaction.Units,
	}
	if response.Data.Agent != nil {
		credits := response.Data.Agent.Credits
		result.AgentCredits = &credits
	}
	return result, nil
}

// InstallShipModule installs a module (which must already be in the ship's
// cargo) onto the ship. Mirrors PurchaseCargo's payload-bearing write shape.
// The API response carries the updated agent, the ship's post-install modules
// list and cargo (the new cargo.capacity is the whole point of a CARGO_HOLD
// upgrade), and a transaction whose totalPrice is the shipyard modification fee.
func (c *SpaceTradersClient) InstallShipModule(ctx context.Context, shipSymbol, moduleSymbol, token string) (*domainPorts.ModuleModificationResult, error) {
	return c.modifyShipModule(ctx, "install", shipSymbol, moduleSymbol, token)
}

// RemoveShipModule removes an installed module from the ship; the API places
// the module back into the ship's cargo. Mirror image of InstallShipModule.
func (c *SpaceTradersClient) RemoveShipModule(ctx context.Context, shipSymbol, moduleSymbol, token string) (*domainPorts.ModuleModificationResult, error) {
	return c.modifyShipModule(ctx, "remove", shipSymbol, moduleSymbol, token)
}

// modifyShipModule is the shared install/remove implementation. action is
// "install" or "remove"; both endpoints share the request body ({"symbol":...})
// and the 201 response shape ({agent, modules[], cargo, transaction}).
func (c *SpaceTradersClient) modifyShipModule(ctx context.Context, action, shipSymbol, moduleSymbol, token string) (*domainPorts.ModuleModificationResult, error) {
	path := fmt.Sprintf("/my/ships/%s/modules/%s", shipSymbol, action)

	body := map[string]interface{}{
		"symbol": moduleSymbol,
	}

	var response struct {
		Data struct {
			// Agent is a pointer so an omitted block is distinguishable from a
			// real zero balance; the in-band credits are the authoritative
			// post-transaction balance (mirrors PurchaseCargo).
			Agent *struct {
				Credits int `json:"credits"`
			} `json:"agent"`
			Modules []struct {
				Symbol       string `json:"symbol"`
				Name         string `json:"name"`
				Capacity     int    `json:"capacity"`
				Range        int    `json:"range"`
				Requirements struct {
					Power int `json:"power"`
					Crew  int `json:"crew"`
					Slots int `json:"slots"`
				} `json:"requirements"`
			} `json:"modules"`
			Cargo struct {
				Capacity int `json:"capacity"`
				Units    int `json:"units"`
			} `json:"cargo"`
			Transaction struct {
				TotalPrice int `json:"totalPrice"`
			} `json:"transaction"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to %s ship module: %w", action, err)
	}

	result := &domainPorts.ModuleModificationResult{
		Fee:           response.Data.Transaction.TotalPrice,
		CargoCapacity: response.Data.Cargo.Capacity,
		Modules:       make([]domainPorts.ModuleInfo, 0, len(response.Data.Modules)),
	}
	for _, m := range response.Data.Modules {
		result.Modules = append(result.Modules, domainPorts.ModuleInfo{
			Symbol:   m.Symbol,
			Name:     m.Name,
			Capacity: m.Capacity,
			Range:    m.Range,
			Power:    m.Requirements.Power,
			Crew:     m.Requirements.Crew,
			Slots:    m.Requirements.Slots,
		})
	}
	if response.Data.Agent != nil {
		credits := response.Data.Agent.Credits
		result.AgentCredits = &credits
	}
	return result, nil
}

// GetShipModules lists the modules currently installed on a ship.
func (c *SpaceTradersClient) GetShipModules(ctx context.Context, shipSymbol, token string) ([]domainPorts.ModuleInfo, error) {
	path := fmt.Sprintf("/my/ships/%s/modules", shipSymbol)

	var response struct {
		Data []struct {
			Symbol       string `json:"symbol"`
			Name         string `json:"name"`
			Capacity     int    `json:"capacity"`
			Range        int    `json:"range"`
			Requirements struct {
				Power int `json:"power"`
				Crew  int `json:"crew"`
				Slots int `json:"slots"`
			} `json:"requirements"`
		} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get ship modules: %w", err)
	}

	modules := make([]domainPorts.ModuleInfo, 0, len(response.Data))
	for _, m := range response.Data {
		modules = append(modules, domainPorts.ModuleInfo{
			Symbol:   m.Symbol,
			Name:     m.Name,
			Capacity: m.Capacity,
			Range:    m.Range,
			Power:    m.Requirements.Power,
			Crew:     m.Requirements.Crew,
			Slots:    m.Requirements.Slots,
		})
	}
	return modules, nil
}

// SellCargo sells cargo from the ship
func (c *SpaceTradersClient) SellCargo(ctx context.Context, shipSymbol, goodSymbol string, units int, token string) (*domainPorts.SellResult, error) {
	path := fmt.Sprintf("/my/ships/%s/sell", shipSymbol)

	body := map[string]interface{}{
		"symbol": goodSymbol,
		"units":  units,
	}

	var response struct {
		Data struct {
			// Agent is a pointer so an omitted block (nil) is distinguishable
			// from a real zero balance; the in-band credits are the ledger's
			// authoritative post-transaction balance.
			Agent *struct {
				Credits int `json:"credits"`
			} `json:"agent"`
			Transaction struct {
				TotalPrice int `json:"totalPrice"`
				Units      int `json:"units"`
			} `json:"transaction"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to sell cargo: %w", err)
	}

	result := &domainPorts.SellResult{
		TotalRevenue: response.Data.Transaction.TotalPrice,
		UnitsSold:    response.Data.Transaction.Units,
	}
	if response.Data.Agent != nil {
		credits := response.Data.Agent.Credits
		result.AgentCredits = &credits
	}
	return result, nil
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

// ExtractResources extracts resources from an asteroid
func (c *SpaceTradersClient) ExtractResources(ctx context.Context, shipSymbol string, token string) (*domainPorts.ExtractionResult, error) {
	path := fmt.Sprintf("/my/ships/%s/extract", shipSymbol)

	// Send empty body as required by API (survey support can be added later)
	emptyBody := map[string]interface{}{}

	var response struct {
		Data struct {
			Extraction struct {
				ShipSymbol string `json:"shipSymbol"`
				Yield      struct {
					Symbol string `json:"symbol"`
					Units  int    `json:"units"`
				} `json:"yield"`
			} `json:"extraction"`
			Cooldown struct {
				ShipSymbol       string `json:"shipSymbol"`
				TotalSeconds     int    `json:"totalSeconds"`
				RemainingSeconds int    `json:"remainingSeconds"`
				Expiration       string `json:"expiration"`
			} `json:"cooldown"`
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
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, emptyBody, &response); err != nil {
		return nil, fmt.Errorf("failed to extract resources: %w", err)
	}

	// Convert cargo inventory
	inventory := make([]shared.CargoItem, len(response.Data.Cargo.Inventory))
	for i, item := range response.Data.Cargo.Inventory {
		inventory[i] = shared.CargoItem{
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

	return &domainPorts.ExtractionResult{
		ShipSymbol:      response.Data.Extraction.ShipSymbol,
		YieldSymbol:     response.Data.Extraction.Yield.Symbol,
		YieldUnits:      response.Data.Extraction.Yield.Units,
		CooldownSeconds: response.Data.Cooldown.RemainingSeconds,
		CooldownExpires: response.Data.Cooldown.Expiration,
		Cargo:           cargo,
	}, nil
}

// SiphonResources siphons gas from a gas giant
func (c *SpaceTradersClient) SiphonResources(ctx context.Context, shipSymbol string, token string) (*domainPorts.SiphonResult, error) {
	path := fmt.Sprintf("/my/ships/%s/siphon", shipSymbol)

	// Send empty body as required by API
	emptyBody := map[string]interface{}{}

	var response struct {
		Data struct {
			Siphon struct {
				ShipSymbol string `json:"shipSymbol"`
				Yield      struct {
					Symbol string `json:"symbol"`
					Units  int    `json:"units"`
				} `json:"yield"`
			} `json:"siphon"`
			Cooldown struct {
				ShipSymbol       string `json:"shipSymbol"`
				TotalSeconds     int    `json:"totalSeconds"`
				RemainingSeconds int    `json:"remainingSeconds"`
				Expiration       string `json:"expiration"`
			} `json:"cooldown"`
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
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, emptyBody, &response); err != nil {
		return nil, fmt.Errorf("failed to siphon resources: %w", err)
	}

	// Convert cargo inventory
	inventory := make([]shared.CargoItem, len(response.Data.Cargo.Inventory))
	for i, item := range response.Data.Cargo.Inventory {
		inventory[i] = shared.CargoItem{
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

	return &domainPorts.SiphonResult{
		ShipSymbol:      response.Data.Siphon.ShipSymbol,
		YieldSymbol:     response.Data.Siphon.Yield.Symbol,
		YieldUnits:      response.Data.Siphon.Yield.Units,
		CooldownSeconds: response.Data.Cooldown.RemainingSeconds,
		CooldownExpires: response.Data.Cooldown.Expiration,
		Cargo:           cargo,
	}, nil
}

// TransferCargo transfers cargo from one ship to another at the same waypoint
func (c *SpaceTradersClient) TransferCargo(ctx context.Context, fromShipSymbol, toShipSymbol, goodSymbol string, units int, token string) (*domainPorts.TransferResult, error) {
	path := fmt.Sprintf("/my/ships/%s/transfer", fromShipSymbol)

	body := map[string]interface{}{
		"tradeSymbol": goodSymbol,
		"units":       units,
		"shipSymbol":  toShipSymbol,
	}

	var response struct {
		Data struct {
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
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to transfer cargo: %w", err)
	}

	// Convert cargo inventory (remaining cargo on source ship)
	inventory := make([]shared.CargoItem, len(response.Data.Cargo.Inventory))
	for i, item := range response.Data.Cargo.Inventory {
		inventory[i] = shared.CargoItem{
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

	return &domainPorts.TransferResult{
		FromShip:         fromShipSymbol,
		ToShip:           toShipSymbol,
		GoodSymbol:       goodSymbol,
		UnitsTransferred: units,
		RemainingCargo:   cargo,
	}, nil
}

// GetMarket retrieves market data for a waypoint
func (c *SpaceTradersClient) GetMarket(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.MarketData, error) {
	path := fmt.Sprintf("/systems/%s/waypoints/%s/market", systemSymbol, waypointSymbol)

	var response struct {
		Data struct {
			Symbol  string `json:"symbol"`
			Exports []struct {
				Symbol string `json:"symbol"`
			} `json:"exports"`
			Imports []struct {
				Symbol string `json:"symbol"`
			} `json:"imports"`
			Exchange []struct {
				Symbol string `json:"symbol"`
			} `json:"exchange"`
			TradeGoods []struct {
				Symbol        string `json:"symbol"`
				Supply        string `json:"supply"`
				Activity      string `json:"activity"`
				SellPrice     int    `json:"sellPrice"`
				PurchasePrice int    `json:"purchasePrice"`
				TradeVolume   int    `json:"tradeVolume"`
			} `json:"tradeGoods"`
		} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get market: %w", err)
	}

	// Build lookup maps for trade type classification
	exportGoods := make(map[string]bool)
	importGoods := make(map[string]bool)
	exchangeGoods := make(map[string]bool)

	for _, e := range response.Data.Exports {
		exportGoods[e.Symbol] = true
	}
	for _, i := range response.Data.Imports {
		importGoods[i.Symbol] = true
	}
	for _, x := range response.Data.Exchange {
		exchangeGoods[x.Symbol] = true
	}

	tradeGoods := make([]domainPorts.TradeGoodData, len(response.Data.TradeGoods))
	for i, good := range response.Data.TradeGoods {
		// Determine trade type based on which array contains this good
		var tradeType string
		if exportGoods[good.Symbol] {
			tradeType = "EXPORT"
		} else if importGoods[good.Symbol] {
			tradeType = "IMPORT"
		} else if exchangeGoods[good.Symbol] {
			tradeType = "EXCHANGE"
		}

		tradeGoods[i] = domainPorts.TradeGoodData{
			Symbol:        good.Symbol,
			Supply:        good.Supply,
			Activity:      good.Activity,
			SellPrice:     good.SellPrice,
			PurchasePrice: good.PurchasePrice,
			TradeVolume:   good.TradeVolume,
			TradeType:     tradeType,
		}
	}

	return &domainPorts.MarketData{
		Symbol:     response.Data.Symbol,
		TradeGoods: tradeGoods,
	}, nil
}

// GetShipyard retrieves shipyard data for a waypoint
func (c *SpaceTradersClient) GetShipyard(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.ShipyardData, error) {
	path := fmt.Sprintf("/systems/%s/waypoints/%s/shipyard", systemSymbol, waypointSymbol)

	var response struct {
		Data struct {
			Symbol    string `json:"symbol"`
			ShipTypes []struct {
				Type string `json:"type"`
			} `json:"shipTypes"`
			Ships []struct {
				Type          string                   `json:"type"`
				Name          string                   `json:"name"`
				Description   string                   `json:"description"`
				PurchasePrice int                      `json:"purchasePrice"`
				Frame         map[string]interface{}   `json:"frame"`
				Reactor       map[string]interface{}   `json:"reactor"`
				Engine        map[string]interface{}   `json:"engine"`
				Modules       []map[string]interface{} `json:"modules"`
				Mounts        []map[string]interface{} `json:"mounts"`
			} `json:"ships"`
			Transactions    []map[string]interface{} `json:"transactions"`
			ModificationFee int                      `json:"modificationsFee"`
		} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get shipyard: %w", err)
	}

	// Convert ship types
	shipTypes := make([]domainPorts.ShipTypeInfo, len(response.Data.ShipTypes))
	for i, st := range response.Data.ShipTypes {
		shipTypes[i] = domainPorts.ShipTypeInfo{
			Type: st.Type,
		}
	}

	// Convert ship listings
	ships := make([]domainPorts.ShipListingData, len(response.Data.Ships))
	for i, ship := range response.Data.Ships {
		ships[i] = domainPorts.ShipListingData{
			Type:          ship.Type,
			Name:          ship.Name,
			Description:   ship.Description,
			PurchasePrice: ship.PurchasePrice,
			Frame:         ship.Frame,
			Reactor:       ship.Reactor,
			Engine:        ship.Engine,
			Modules:       ship.Modules,
			Mounts:        ship.Mounts,
		}
	}

	return &domainPorts.ShipyardData{
		Symbol:          response.Data.Symbol,
		ShipTypes:       shipTypes,
		Ships:           ships,
		Transactions:    response.Data.Transactions,
		ModificationFee: response.Data.ModificationFee,
	}, nil
}

// PurchaseShip purchases a ship at a shipyard
func (c *SpaceTradersClient) PurchaseShip(ctx context.Context, shipType, waypointSymbol, token string) (*domainPorts.ShipPurchaseResult, error) {
	path := "/my/ships"

	body := map[string]interface{}{
		"shipType":       shipType,
		"waypointSymbol": waypointSymbol,
	}

	var response struct {
		Data struct {
			Agent struct {
				AccountID       string `json:"accountId"`
				Symbol          string `json:"symbol"`
				Headquarters    string `json:"headquarters"`
				Credits         int    `json:"credits"`
				StartingFaction string `json:"startingFaction"`
			} `json:"agent"`
			Ship        map[string]interface{} `json:"ship"`
			Transaction struct {
				WaypointSymbol string `json:"waypointSymbol"`
				ShipSymbol     string `json:"shipSymbol"`
				ShipType       string `json:"shipType"`
				Price          int    `json:"price"`
				AgentSymbol    string `json:"agentSymbol"`
				Timestamp      string `json:"timestamp"`
			} `json:"transaction"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to purchase ship: %w", err)
	}

	// Convert agent data
	agentData := &player.AgentData{
		AccountID:       response.Data.Agent.AccountID,
		Symbol:          response.Data.Agent.Symbol,
		Headquarters:    response.Data.Agent.Headquarters,
		Credits:         response.Data.Agent.Credits,
		StartingFaction: response.Data.Agent.StartingFaction,
	}

	// Convert ship data
	shipData, err := c.convertShipData(response.Data.Ship)
	if err != nil {
		return nil, fmt.Errorf("failed to convert ship data: %w", err)
	}

	// Convert transaction data
	transaction := &domainPorts.ShipPurchaseTransaction{
		WaypointSymbol: response.Data.Transaction.WaypointSymbol,
		ShipSymbol:     response.Data.Transaction.ShipSymbol,
		ShipType:       response.Data.Transaction.ShipType,
		Price:          response.Data.Transaction.Price,
		AgentSymbol:    response.Data.Transaction.AgentSymbol,
		Timestamp:      response.Data.Transaction.Timestamp,
	}

	return &domainPorts.ShipPurchaseResult{
		Agent:       agentData,
		Ship:        shipData,
		Transaction: transaction,
	}, nil
}

// convertShipData converts ship data from API response map to ShipData struct
func (c *SpaceTradersClient) convertShipData(data map[string]interface{}) (*navigation.ShipData, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ship data: %w", err)
	}

	var dto shipDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		return nil, fmt.Errorf("failed to parse ship data: %w", err)
	}

	if dto.Symbol == "" {
		return nil, fmt.Errorf("missing or invalid ship symbol")
	}

	if _, ok := data["nav"].(map[string]interface{}); !ok {
		return nil, fmt.Errorf("missing or invalid nav data")
	}
	if _, ok := data["fuel"].(map[string]interface{}); !ok {
		return nil, fmt.Errorf("missing or invalid fuel data")
	}
	if _, ok := data["cargo"].(map[string]interface{}); !ok {
		return nil, fmt.Errorf("missing or invalid cargo data")
	}
	if _, ok := data["engine"].(map[string]interface{}); !ok {
		return nil, fmt.Errorf("missing or invalid engine data")
	}

	return dto.toShipData(), nil
}

// parseContractData parses contract data from API response
func (c *SpaceTradersClient) parseContractData(data map[string]interface{}) (*domainPorts.ContractData, error) {
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
	var payment domainPorts.PaymentData
	if paymentData, ok := termsData["payment"].(map[string]interface{}); ok {
		if onAccepted, ok := paymentData["onAccepted"].(float64); ok {
			payment.OnAccepted = int(onAccepted)
		}
		if onFulfilled, ok := paymentData["onFulfilled"].(float64); ok {
			payment.OnFulfilled = int(onFulfilled)
		}
	}

	// Parse deliveries
	var deliveries []domainPorts.DeliveryData
	if deliveriesData, ok := termsData["deliver"].([]interface{}); ok {
		deliveries = make([]domainPorts.DeliveryData, len(deliveriesData))
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

				deliveries[i] = domainPorts.DeliveryData{
					TradeSymbol:       tradeSymbol,
					DestinationSymbol: destinationSymbol,
					UnitsRequired:     unitsRequired,
					UnitsFulfilled:    unitsFulfilled,
				}
			}
		}
	}

	return &domainPorts.ContractData{
		ID:            id,
		FactionSymbol: factionSymbol,
		Type:          contractType,
		Terms: domainPorts.ContractTermsData{
			DeadlineToAccept: deadlineToAccept,
			Deadline:         deadline,
			Payment:          payment,
			Deliveries:       deliveries,
		},
		Accepted:  accepted,
		Fulfilled: fulfilled,
	}, nil
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

// addJitter adds random jitter to a duration to avoid thundering herd
// Returns a duration between 50% and 150% of the original value
func addJitter(d time.Duration) time.Duration {
	// Cap the backoff at maxBackoffDuration before applying jitter
	if d > maxBackoffDuration {
		d = maxBackoffDuration
	}
	jitter := 0.5 + rand.Float64() // 0.5 to 1.5
	return time.Duration(float64(d) * jitter)
}

// request makes an HTTP request with rate limiting and exponential backoff retries
func (c *SpaceTradersClient) request(ctx context.Context, method, path, token string, body interface{}, result interface{}) error {
	return c.doWithRetry(ctx, method, path, token, body, func(statusCode int, respBody []byte) error {
		if statusCode < 200 || statusCode >= 300 {
			return fmt.Errorf("API error (status %d): %s", statusCode, string(respBody))
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("failed to unmarshal response: %w", err)
		}
		return nil
	})
}

// requestWithErrorParsing is like request() but unmarshals JSON BEFORE checking status codes
// This allows callers to inspect error details (like error code 4511) even when status is 4xx
func (c *SpaceTradersClient) requestWithErrorParsing(ctx context.Context, method, path, token string, body interface{}, result interface{}) error {
	return c.doWithRetry(ctx, method, path, token, body, func(statusCode int, respBody []byte) error {
		if result != nil {
			if err := json.Unmarshal(respBody, result); err != nil {
				return fmt.Errorf("failed to unmarshal response: %w", err)
			}
		}
		if statusCode >= 200 && statusCode < 300 {
			return nil
		}
		return fmt.Errorf("API error (status %d): %s", statusCode, string(respBody))
	})
}

// GetConstruction retrieves construction site information for a waypoint
func (c *SpaceTradersClient) GetConstruction(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.ConstructionData, error) {
	path := fmt.Sprintf("/systems/%s/waypoints/%s/construction", systemSymbol, waypointSymbol)

	var response struct {
		Data struct {
			Symbol    string `json:"symbol"`
			Materials []struct {
				TradeSymbol string `json:"tradeSymbol"`
				Required    int    `json:"required"`
				Fulfilled   int    `json:"fulfilled"`
			} `json:"materials"`
			IsComplete bool `json:"isComplete"`
		} `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get construction: %w", err)
	}

	// Convert materials
	materials := make([]domainPorts.ConstructionMaterialData, len(response.Data.Materials))
	for i, mat := range response.Data.Materials {
		materials[i] = domainPorts.ConstructionMaterialData{
			TradeSymbol: mat.TradeSymbol,
			Required:    mat.Required,
			Fulfilled:   mat.Fulfilled,
		}
	}

	return &domainPorts.ConstructionData{
		Symbol:     response.Data.Symbol,
		Materials:  materials,
		IsComplete: response.Data.IsComplete,
	}, nil
}

// SupplyConstruction delivers materials to a construction site
func (c *SpaceTradersClient) SupplyConstruction(ctx context.Context, shipSymbol, waypointSymbol, tradeSymbol string, units int, token string) (*domainPorts.ConstructionSupplyResponse, error) {
	// Construction supply is a waypoint-scoped endpoint; the ship is identified
	// via the request body, not the path. The previous ship-scoped path
	// (/my/ships/{shipSymbol}/construction/supply) does not exist and returned
	// 404 "Route not found", killing every construction delivery (sp-fi7q).
	systemSymbol := extractSystemSymbol(waypointSymbol)
	path := fmt.Sprintf("/systems/%s/waypoints/%s/construction/supply", systemSymbol, waypointSymbol)

	body := map[string]interface{}{
		"shipSymbol":  shipSymbol,
		"tradeSymbol": tradeSymbol,
		"units":       units,
	}

	var response struct {
		Data struct {
			Construction struct {
				Symbol    string `json:"symbol"`
				Materials []struct {
					TradeSymbol string `json:"tradeSymbol"`
					Required    int    `json:"required"`
					Fulfilled   int    `json:"fulfilled"`
				} `json:"materials"`
				IsComplete bool `json:"isComplete"`
			} `json:"construction"`
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
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to supply construction (system=%s): %w", systemSymbol, err)
	}

	// Convert materials
	materials := make([]domainPorts.ConstructionMaterialData, len(response.Data.Construction.Materials))
	for i, mat := range response.Data.Construction.Materials {
		materials[i] = domainPorts.ConstructionMaterialData{
			TradeSymbol: mat.TradeSymbol,
			Required:    mat.Required,
			Fulfilled:   mat.Fulfilled,
		}
	}

	// Convert cargo inventory
	inventory := make([]shared.CargoItem, len(response.Data.Cargo.Inventory))
	for i, item := range response.Data.Cargo.Inventory {
		inventory[i] = shared.CargoItem{
			Symbol:      item.Symbol,
			Name:        item.Name,
			Description: item.Description,
			Units:       item.Units,
		}
	}

	return &domainPorts.ConstructionSupplyResponse{
		Construction: &domainPorts.ConstructionData{
			Symbol:     response.Data.Construction.Symbol,
			Materials:  materials,
			IsComplete: response.Data.Construction.IsComplete,
		},
		Cargo: &navigation.CargoData{
			Capacity:  response.Data.Cargo.Capacity,
			Units:     response.Data.Cargo.Units,
			Inventory: inventory,
		},
	}, nil
}

// extractSystemSymbol extracts the system symbol from a waypoint symbol
// e.g., "X1-FB5-I61" -> "X1-FB5"
func extractSystemSymbol(waypointSymbol string) string {
	parts := strings.Split(waypointSymbol, "-")
	if len(parts) >= 2 {
		return parts[0] + "-" + parts[1]
	}
	return waypointSymbol
}

// retryableError represents an error that should trigger a retry
type retryableError struct {
	message    string
	retryAfter time.Duration
}

func (e *retryableError) Error() string {
	return e.message
}
