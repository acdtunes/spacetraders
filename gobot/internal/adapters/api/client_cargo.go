package api

import (
	"context"
	"fmt"

	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

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
	c.invalidateAgentCache() // buy cargo spends credits -> drop the stale-high cache

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
	c.invalidateAgentCache() // module install/remove charges a shipyard fee -> drop the stale-high cache

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
	c.invalidateAgentCache() // income bonus: sell raises the balance; drop the stale-low cache so an affordable buy is visible sooner. Conservative either way — income never over-spends.

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

	// Send empty body as required by API
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
			Cargo cargoDTO `json:"cargo"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, emptyBody, &response); err != nil {
		return nil, fmt.Errorf("failed to extract resources: %w", err)
	}

	cargo := response.Data.Cargo.toCargoData()

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
			Cargo cargoDTO `json:"cargo"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, emptyBody, &response); err != nil {
		return nil, fmt.Errorf("failed to siphon resources: %w", err)
	}

	cargo := response.Data.Cargo.toCargoData()

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
			Cargo cargoDTO `json:"cargo"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", path, token, body, &response); err != nil {
		return nil, fmt.Errorf("failed to transfer cargo: %w", err)
	}

	// Convert cargo inventory (remaining cargo on source ship)
	cargo := response.Data.Cargo.toCargoData()

	return &domainPorts.TransferResult{
		FromShip:         fromShipSymbol,
		ToShip:           toShipSymbol,
		GoodSymbol:       goodSymbol,
		UnitsTransferred: units,
		RemainingCargo:   cargo,
	}, nil
}
