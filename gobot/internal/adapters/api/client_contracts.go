package api

import (
	"context"
	"fmt"

	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

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

	if response.Data == nil || response.Data.Contract == nil {
		return nil, fmt.Errorf("invalid response: missing contract data")
	}

	contractData, err := parseContractData(response.Data.Contract)
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

	return parseContractData(response.Data)
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

	contractData, err := parseContractData(response.Data.Contract)
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

	return parseContractData(response.Data.Contract)
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

	contractData, err := parseContractData(response.Data.Contract)
	if err != nil {
		return nil, err
	}
	if response.Data.Agent != nil {
		credits := response.Data.Agent.Credits
		contractData.AgentCredits = &credits
	}
	return contractData, nil
}

// parseContractData parses contract data from API response
func parseContractData(data map[string]interface{}) (*domainPorts.ContractData, error) {
	id, ok := data["id"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid contract id")
	}

	factionSymbol, ok := data["factionSymbol"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid factionSymbol")
	}

	contractType, ok := data["type"].(string)
	if !ok {
		return nil, fmt.Errorf("missing or invalid contract type")
	}

	accepted, ok := data["accepted"].(bool)
	if !ok {
		return nil, fmt.Errorf("missing or invalid accepted status")
	}

	fulfilled, ok := data["fulfilled"].(bool)
	if !ok {
		return nil, fmt.Errorf("missing or invalid fulfilled status")
	}

	termsData, ok := data["terms"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("missing or invalid terms")
	}

	deadlineToAccept, _ := termsData["deadline"].(string)
	deadline, _ := termsData["deadline"].(string)

	var payment domainPorts.PaymentData
	if paymentData, ok := termsData["payment"].(map[string]interface{}); ok {
		if onAccepted, ok := paymentData["onAccepted"].(float64); ok {
			payment.OnAccepted = int(onAccepted)
		}
		if onFulfilled, ok := paymentData["onFulfilled"].(float64); ok {
			payment.OnFulfilled = int(onFulfilled)
		}
	}

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
