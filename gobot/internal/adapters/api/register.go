package api

import (
	"context"
	"fmt"
)

type RegisterResult struct {
	Token       string
	AgentSymbol string
	Faction     string
}

func (c *SpaceTradersClient) Register(ctx context.Context, accountToken, agentSymbol, faction string) (*RegisterResult, error) {
	body := map[string]interface{}{
		"symbol":  agentSymbol,
		"faction": faction,
	}

	var response struct {
		Data struct {
			Token string `json:"token"`
			Agent struct {
				Symbol          string `json:"symbol"`
				StartingFaction string `json:"startingFaction"`
			} `json:"agent"`
		} `json:"data"`
	}

	if err := c.request(ctx, "POST", "/register", accountToken, body, &response); err != nil {
		return nil, fmt.Errorf("failed to register agent: %w", err)
	}

	return &RegisterResult{
		Token:       response.Data.Token,
		AgentSymbol: response.Data.Agent.Symbol,
		Faction:     response.Data.Agent.StartingFaction,
	}, nil
}
