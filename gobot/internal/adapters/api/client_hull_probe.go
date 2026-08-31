package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// ShipReadVerdict classifies one read of a hull's composite record.
type ShipReadVerdict int

const (
	// ShipReadOK — the record served.
	ShipReadOK ShipReadVerdict = iota
	// ShipReadServerRefused — a 5xx: the server holds the hull but cannot render it.
	ShipReadServerRefused
	// ShipReadClientRefused — a 4xx the API rendered on the request's own merits: the hull
	// is gone, or not ours. Not a corrupt record.
	ShipReadClientRefused
	// ShipReadUnavailable — rate limiting or a transport failure. Nothing about the hull
	// has been established at all.
	ShipReadUnavailable
)

// shipPartPaths are the sub-resources a composite read can be bisected against, /nav first
// because a repair needs the live position and would otherwise pay for it twice.
var shipPartPaths = []struct{ name, suffix string }{
	{"nav", "/nav"},
	{"cargo", "/cargo"},
	{"cooldown", "/cooldown"},
	{"mounts", "/mounts"},
	{"modules", "/modules"},
}

// ShipNavReading is the live position from GET /my/ships/<symbol>/nav.
type ShipNavReading struct {
	SystemSymbol   string
	WaypointSymbol string
	Status         string
	ArrivalAt      time.Time
}

// ShipPartsReading is what a bisect of one hull's sub-resources established.
type ShipPartsReading struct {
	Nav      *ShipNavReading
	Answered []string
	Refused  []string
}

// ReadShipRecord reads a hull's composite record and reports only whether it serves. The
// ladder is capped: a record the full retry ladder already failed to fetch will not come
// back within another one, and an API-wide failure must not cost the fleet minutes of
// backoff per hull.
func (c *SpaceTradersClient) ReadShipRecord(ctx context.Context, symbol, token string) (ShipReadVerdict, error) {
	retries := defaultFleetIsolationProbeRetries
	var response struct {
		Data shipDTO `json:"data"`
	}
	err := c.requestWithRetryCap(ctx, "GET", fmt.Sprintf("/my/ships/%s", symbol), token, nil, &response, &retries)
	if err == nil {
		return ShipReadOK, nil
	}
	return classifyShipRead(err), err
}

// classifyShipRead separates a server-side render failure from a refusal on the request's
// merits and from having learned nothing. A 5xx and a 429 both arrive as the same
// exhausted-ladder failure rather than the typed error, so only the status tells them apart.
func classifyShipRead(err error) ShipReadVerdict {
	var retryErr *retryableError
	if errors.As(err, &retryErr) {
		if retryErr.statusCode >= 500 {
			return ShipReadServerRefused
		}
		return ShipReadUnavailable
	}
	var apiErr *domainPorts.APIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode >= 500 {
			return ShipReadServerRefused
		}
		return ShipReadClientRefused
	}
	return ShipReadUnavailable
}

// ProbeShipParts reads a hull's sub-resources until one answers, and reports which did.
//
// This is the confirmation a repair is spent on: parts that serve while the composite does
// not means exactly one field the parts do not cover will not render, and that is a fault
// this client can write over. Parts that also refuse means the API is failing, and nothing
// about this hull has been established at all.
//
// It stops at the first success because that is all the confirmation needs; the full walk
// is only paid when the answer is going to be "the API is down".
func (c *SpaceTradersClient) ProbeShipParts(ctx context.Context, symbol, token string) (ShipPartsReading, error) {
	var reading ShipPartsReading
	for _, part := range shipPartPaths {
		if err := ctx.Err(); err != nil {
			return reading, err
		}
		retries := defaultFleetIsolationProbeRetries
		path := fmt.Sprintf("/my/ships/%s%s", symbol, part.suffix)

		if part.name == "nav" {
			nav, err := c.readShipNav(ctx, path, token, &retries)
			if err != nil {
				reading.Refused = append(reading.Refused, part.name)
				continue
			}
			reading.Nav = nav
			reading.Answered = append(reading.Answered, part.name)
			return reading, nil
		}

		// result nil: a part is probed for liveness only, and /cooldown answers an
		// un-cooled hull with an empty body that no decode would survive.
		if err := c.requestWithRetryCap(ctx, "GET", path, token, nil, nil, &retries); err != nil {
			reading.Refused = append(reading.Refused, part.name)
			continue
		}
		reading.Answered = append(reading.Answered, part.name)
		return reading, nil
	}
	return reading, nil
}

func (c *SpaceTradersClient) readShipNav(ctx context.Context, path, token string, retries *int) (*ShipNavReading, error) {
	var response struct {
		Data struct {
			SystemSymbol   string `json:"systemSymbol"`
			WaypointSymbol string `json:"waypointSymbol"`
			Status         string `json:"status"`
			Route          struct {
				Arrival string `json:"arrival"`
			} `json:"route"`
		} `json:"data"`
	}
	if err := c.requestWithRetryCap(ctx, "GET", path, token, nil, &response, retries); err != nil {
		return nil, err
	}
	nav := &ShipNavReading{
		SystemSymbol:   response.Data.SystemSymbol,
		WaypointSymbol: response.Data.WaypointSymbol,
		Status:         response.Data.Status,
	}
	if arrival, err := time.Parse(time.RFC3339, response.Data.Route.Arrival); err == nil {
		nav.ArrivalAt = arrival
	}
	return nav, nil
}
