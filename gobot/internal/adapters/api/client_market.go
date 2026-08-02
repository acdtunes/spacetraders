package api

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// classifyTradeType resolves which of the market's three symbol arrays owns a good.
// Export wins over import wins over exchange; a good in none of them is unclassified.
func classifyTradeType(symbol string, exports, imports, exchange map[string]bool) string {
	switch {
	case exports[symbol]:
		return "EXPORT"
	case imports[symbol]:
		return "IMPORT"
	case exchange[symbol]:
		return "EXCHANGE"
	}
	return ""
}

// GetMarket retrieves market data for a waypoint
func (c *SpaceTradersClient) GetMarket(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.MarketData, error) {
	path := fmt.Sprintf("/systems/%s/waypoints/%s/market", systemSymbol, waypointSymbol)

	var response struct {
		Data marketDTO `json:"data"`
	}

	if err := c.request(ctx, "GET", path, token, nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get market: %w", err)
	}

	return response.Data.toMarketData(), nil
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
				Supply        string                   `json:"supply"`
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

	shipTypes := make([]domainPorts.ShipTypeInfo, len(response.Data.ShipTypes))
	for i, st := range response.Data.ShipTypes {
		shipTypes[i] = domainPorts.ShipTypeInfo{
			Type: st.Type,
		}
	}

	ships := make([]domainPorts.ShipListingData, len(response.Data.Ships))
	for i, ship := range response.Data.Ships {
		ships[i] = domainPorts.ShipListingData{
			Type:          ship.Type,
			Name:          ship.Name,
			Description:   ship.Description,
			Supply:        ship.Supply,
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
	c.invalidateAgentCache() // buy ship spends credits (the biggest spend) -> drop the stale-high cache

	agentData := &player.AgentData{
		AccountID:       response.Data.Agent.AccountID,
		Symbol:          response.Data.Agent.Symbol,
		Headquarters:    response.Data.Agent.Headquarters,
		Credits:         response.Data.Agent.Credits,
		StartingFaction: response.Data.Agent.StartingFaction,
	}

	shipData, err := convertShipData(response.Data.Ship)
	if err != nil {
		return nil, fmt.Errorf("failed to convert ship data: %w", err)
	}

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
