package api

import (
	"context"
	"fmt"

	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// scrapEnvelope is the shared GET/POST body. Agent is a pointer so an omitted
// block stays distinguishable from a real zero balance.
type scrapEnvelope struct {
	Data struct {
		Agent *struct {
			Credits int `json:"credits"`
		} `json:"agent"`
		Transaction struct {
			WaypointSymbol string `json:"waypointSymbol"`
			ShipSymbol     string `json:"shipSymbol"`
			TotalPrice     int    `json:"totalPrice"`
			Timestamp      string `json:"timestamp"`
		} `json:"transaction"`
	} `json:"data"`
}

func scrapPath(shipSymbol string) string {
	return fmt.Sprintf("/my/ships/%s/scrap", shipSymbol)
}

// GetScrapQuote prices the hull WITHOUT selling it. The API refuses with 4263
// unless the hull is docked at a waypoint carrying the SHIPYARD trait.
func (c *SpaceTradersClient) GetScrapQuote(ctx context.Context, shipSymbol, token string) (*domainPorts.ScrapQuote, error) {
	var response scrapEnvelope
	if err := c.request(ctx, "GET", scrapPath(shipSymbol), token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to read scrap quote: %w", err)
	}

	return &domainPorts.ScrapQuote{
		ShipSymbol:     response.Data.Transaction.ShipSymbol,
		WaypointSymbol: response.Data.Transaction.WaypointSymbol,
		TotalPrice:     response.Data.Transaction.TotalPrice,
		Timestamp:      response.Data.Transaction.Timestamp,
	}, nil
}

// ScrapShip sells the hull for credits and removes it from the fleet. Irreversible,
// and cargo aboard is destroyed rather than compensated.
func (c *SpaceTradersClient) ScrapShip(ctx context.Context, shipSymbol, token string) (*domainPorts.ScrapResult, error) {
	var response scrapEnvelope
	if err := c.request(ctx, "POST", scrapPath(shipSymbol), token, map[string]interface{}{}, &response); err != nil {
		return nil, fmt.Errorf("failed to scrap ship: %w", err)
	}
	c.invalidateAgentCache() // income: drop the stale-low cache so the proceeds are visible sooner

	result := &domainPorts.ScrapResult{
		ShipSymbol:     response.Data.Transaction.ShipSymbol,
		WaypointSymbol: response.Data.Transaction.WaypointSymbol,
		TotalPrice:     response.Data.Transaction.TotalPrice,
		Timestamp:      response.Data.Transaction.Timestamp,
	}
	if response.Data.Agent != nil {
		credits := response.Data.Agent.Credits
		result.AgentCredits = &credits
	}
	return result, nil
}
