package api

import (
	"context"
	"fmt"
)

type ServerResets struct {
	Next      string
	Frequency string
}

type ServerStatus struct {
	ResetDate    string
	ServerResets ServerResets
}

func (c *SpaceTradersClient) GetServerStatus(ctx context.Context) (*ServerStatus, error) {
	var response struct {
		ResetDate    string `json:"resetDate"`
		ServerResets struct {
			Next      string `json:"next"`
			Frequency string `json:"frequency"`
		} `json:"serverResets"`
	}

	if err := c.request(ctx, "GET", "/", "", nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get server status: %w", err)
	}

	return &ServerStatus{
		ResetDate: response.ResetDate,
		ServerResets: ServerResets{
			Next:      response.ServerResets.Next,
			Frequency: response.ServerResets.Frequency,
		},
	}, nil
}
