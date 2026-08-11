package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

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

// FleetReadReport records what a fleet enumeration could NOT read.
//
// It is the observable half of the element-tolerant decode in
// ListShipsWithReport. Surviving one broken hull is only safe if the caller can
// tell a partial fleet from a complete one: SyncAllFromAPI prunes every row
// missing from the live list, so handing it "the fleet, minus the hull we could
// not parse" would delete exactly the hull that is already in trouble.
type FleetReadReport struct {
	// Unreadable holds one entry per element of GET /my/ships that could not be
	// turned into a usable hull, in the order encountered.
	Unreadable []UnreadableShip
}

// Partial reports whether the enumeration dropped at least one hull — i.e.
// whether the returned slice is a known-INCOMPLETE view of the fleet. Every
// destructive decision keyed on a fleet read must consult this first.
func (r FleetReadReport) Partial() bool { return len(r.Unreadable) > 0 }

// UnreadableShip identifies a dropped element as precisely as the broken
// payload allows. Symbol is best-effort: naming the hull is the difference
// between an operator diagnosing it in minutes and re-running the outage.
type UnreadableShip struct {
	Page int
	// Index is the position WITHIN Page on BOTH paths — decode failure and isolation.
	Index  int
	Symbol string
	Reason string
}

// logPartial emits the report at WARNING. The typed report is what the sync
// consumes; this is what an operator greps for. The 24h TORWIND outage was
// prolonged partly because nothing on the fleet-read path ever named the hull
// that was breaking it.
func (r FleetReadReport) logPartial(readable int) {
	if !r.Partial() {
		return
	}
	for _, u := range r.Unreadable {
		symbol := u.Symbol
		if symbol == "" {
			symbol = "<unrecoverable>"
		}
		log.Printf("WARNING [fleet_read_unreadable_ship] ship=%s page=%d index=%d: %s",
			symbol, u.Page, u.Index, u.Reason)
	}
	log.Printf("WARNING [fleet_read_partial] unreadable=%d readable=%d: GET /my/ships served at least one hull this client cannot parse; the returned fleet is INCOMPLETE and callers must not treat it as authoritative",
		len(r.Unreadable), readable)
}

// ListShips retrieves all ships for the authenticated agent, dropping any hull
// the API serves in an unusable shape. Callers that make destructive decisions
// from the result must use ListShipsWithReport instead — this form cannot tell
// them the fleet came back incomplete.
func (c *SpaceTradersClient) ListShips(ctx context.Context, token string) ([]*navigation.ShipData, error) {
	ships, _, err := c.ListShipsWithReport(ctx, token)
	return ships, err
}

// ListShipsWithReport is ListShips plus the record of what it could not read.
//
// One hull the API cannot serialise must not blind the whole fleet: agent
// TORWIND was dead for 24h because a single poisoned hull (TORWIND-5) failed to
// deserialise, and unmarshalling a page in one shot into []shipDTO turns one bad
// element into (nil, err) for all 253 hulls — SyncAllFromAPI then aborts with
// zero ships synced. Each element is therefore decoded on its own, and a failure
// costs only that hull.
//
// A decoded-but-symbol-less element counts as unreadable too. toShipData() is
// total, so garbage that unmarshals cleanly into a zero-valued shipDTO would
// otherwise be upserted as a real row under an empty ship_symbol; a corrupt row
// is worse than an absent one.
//
// A page the SERVER refuses outright (HTTP 500) is not fatal either — a hull the
// server cannot serve poisons every window containing it, so a failed page is
// re-read one hull at a time (isolatePoisonedPage). Fail-closed means "take
// no action on the ship we cannot read", not "take no action at all": one bad hull
// stopping every other hull is its blast radius being wrong, not the guard working.
// Still fatal: a page that will not split into elements, and a read that recovers
// NOTHING — a fleet 20 hulls lighter, or an empty one, returned as success is
// precisely the destructive lie this report exists to prevent.
//
// Pagination is unchanged in spirit: it pages at the API maximum of 20
// (openapi.json: limit maximum 20) and stops only when two independent signals
// agree the collection is exhausted — the page came back short, AND meta.total
// accounts for every hull already read. Either alone can lie (a server that
// under-fills; a total stale against a fleet growing mid-pagination), so when
// they disagree, or no usable total is served, the loop falls back to probing
// for an empty page. Both signals are scored against RAW elements, never
// successfully-parsed ones: they are statements about hulls the SERVER holds, so
// counting parsed hulls would make every skipped element look like a short page
// and truncate the fleet at the first unreadable hull.
func (c *SpaceTradersClient) ListShipsWithReport(ctx context.Context, token string) ([]*navigation.ShipData, FleetReadReport, error) {
	var allShips []*navigation.ShipData
	var report FleetReadReport
	rawRead := 0
	page := 1
	limit := apiPageLimitMax

	for {
		response, err := c.fetchFleetPage(ctx, token, page, limit, nil)
		if err != nil {
			sweep, sweepErr := c.isolatePoisonedPage(ctx, token, page, limit)
			if sweepErr != nil {
				return nil, FleetReadReport{}, fmt.Errorf("failed to list ships (page %d): %w", page, sweepErr)
			}
			allShips = append(allShips, sweep.ships...)
			report.Unreadable = append(report.Unreadable, sweep.unreadable...)
			rawRead += sweep.accounted
			if sweep.endOfFleet {
				break
			}
			page++
			continue
		}

		if len(response.Data) == 0 {
			break
		}

		for i, raw := range response.Data {
			ship, unreadable := decodeFleetElement(raw, page, i)
			if unreadable != nil {
				report.Unreadable = append(report.Unreadable, *unreadable)
				continue
			}
			allShips = append(allShips, ship)
		}

		// total is re-read every page, so a fleet that grows mid-pagination
		// pushes the finish line out rather than cutting the loop short.
		rawRead += len(response.Data)
		shortPage := len(response.Data) < limit
		totalAccountedFor := response.Meta.Total > 0 && rawRead >= response.Meta.Total
		if shortPage && totalAccountedFor {
			break
		}

		page++
	}

	if len(allShips) == 0 && report.Partial() {
		return nil, FleetReadReport{}, fmt.Errorf("failed to list ships: no hull of %d was readable — treating an unreadable fleet as an empty one would let every coordinator act on a stale snapshot", len(report.Unreadable))
	}

	report.logPartial(len(allShips))

	return allShips, report, nil
}

// fleetPage is one raw GET /my/ships response, elements still undecoded.
type fleetPage struct {
	// []json.RawMessage, not []shipDTO: deferring the per-ship decode is what keeps
	// one bad element from failing the entire page.
	Data []json.RawMessage `json:"data"`
	Meta struct {
		Total int `json:"total"`
		Page  int `json:"page"`
		Limit int `json:"limit"`
	} `json:"meta"`
}

func (c *SpaceTradersClient) fetchFleetPage(ctx context.Context, token string, page, limit int, serverErrorRetryCap *int) (fleetPage, error) {
	path := fmt.Sprintf("/my/ships?page=%d&limit=%d", page, limit)
	var response fleetPage
	if err := c.requestWithRetryCap(ctx, "GET", path, token, nil, &response, serverErrorRetryCap); err != nil {
		return fleetPage{}, err
	}
	return response, nil
}

// fleetPageSweep is what one hull-at-a-time re-read of a failed page recovered.
type fleetPageSweep struct {
	ships      []*navigation.ShipData
	unreadable []UnreadableShip
	// accounted is how many hull slots got a definite verdict, readable or not — the
	// page length the failed request never delivered, and like it a count of what the
	// SERVER holds, not of what we parsed.
	accounted  int
	endOfFleet bool
}

// isolatePoisonedPage re-reads a failed page's span one hull at a time, turning
// "this page is unreadable" into "THIS HULL is unreadable" — a page is 20 hulls and
// a poisoned record is one. The span is addressable because SpaceTraders pages by
// (page, limit) over a stable order: window (P, L) is indices [(P-1)L, PL), so hull
// i is exactly (page=i+1, limit=1).
//
// It errors rather than return a PARTIAL sweep when the abort streak was reached or
// nothing was recovered: hulls past an abort point would be neither readable nor
// reported — silently ABSENT, and absence is what buys a replacement hull.
func (c *SpaceTradersClient) isolatePoisonedPage(ctx context.Context, token string, page, limit int) (fleetPageSweep, error) {
	var sweep fleetPageSweep
	firstIndex := (page - 1) * limit
	abortStreak := c.isolationAbortStreak()
	probeRetries := defaultFleetIsolationProbeRetries
	streak := 0

	for offset := 0; offset < limit; offset++ {
		index := firstIndex + offset
		probe, err := c.fetchFleetPage(ctx, token, index+1, 1, &probeRetries)
		if err != nil {
			streak++
			if streak >= abortStreak {
				return fleetPageSweep{}, fmt.Errorf("fleet-read isolation abandoned at hull index %d: %d consecutive per-hull reads failed, which is the API failing rather than a poisoned record: %w", index, streak, err)
			}
			sweep.unreadable = append(sweep.unreadable, UnreadableShip{
				Page:   page,
				Index:  offset,
				Reason: fmt.Sprintf("server refused GET /my/ships?page=%d&limit=1, this hull's own record: %v", index+1, err),
			})
			sweep.accounted++
			continue
		}
		streak = 0

		if len(probe.Data) == 0 {
			sweep.endOfFleet = true
			break
		}

		ship, unreadable := decodeFleetElement(probe.Data[0], page, offset)
		if unreadable != nil {
			sweep.unreadable = append(sweep.unreadable, *unreadable)
		} else {
			sweep.ships = append(sweep.ships, ship)
		}
		sweep.accounted++
	}

	if len(sweep.ships) == 0 && !sweep.endOfFleet {
		return fleetPageSweep{}, fmt.Errorf("fleet-read isolation recovered no hull from page %d (%d unreadable): the whole page is unreadable, which is an outage rather than a poisoned record", page, len(sweep.unreadable))
	}

	log.Printf("WARNING [fleet_read_page_isolated] page=%d readable=%d unreadable=%d: GET /my/ships refused this page outright; it was re-read one hull at a time so the failure costs only the hulls that cause it",
		page, len(sweep.ships), len(sweep.unreadable))

	return sweep, nil
}

// decodeFleetElement turns one element of GET /my/ships into a hull, or explains
// why it could not. Exactly one of the two returns is non-nil.
func decodeFleetElement(raw json.RawMessage, page, index int) (*navigation.ShipData, *UnreadableShip) {
	var dto shipDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		// dto is still the best symbol source available: encoding/json records
		// the first type error and keeps decoding the remaining fields, so a hull
		// whose fuel/nav block is type-corrupt — the shape of a poisoned member —
		// usually still yields its own name here.
		return nil, &UnreadableShip{Page: page, Index: index, Symbol: dto.Symbol, Reason: err.Error()}
	}
	if dto.Symbol == "" {
		return nil, &UnreadableShip{
			Page:   page,
			Index:  index,
			Reason: "ship object decoded but carries no symbol",
		}
	}
	return dto.toShipData(), nil
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

// WarpShip warps a ship to a destination waypoint in ANOTHER system, off the
// jump-gate network. Requires a MODULE_WARP_DRIVE_I; fuel is consumed
// by inter-system distance. The wire contract mirrors NavigateShip exactly - the
// live API's POST /my/ships/{shipSymbol}/warp takes "waypointSymbol" in the body
// and returns the same fuel + nav.route envelope - so the parsing/return shape is
// identical to a navigate leg (destination, arrival time, post-warp fuel state).
func (c *SpaceTradersClient) WarpShip(ctx context.Context, symbol, destination, token string) (*navigation.Result, error) {
	path := fmt.Sprintf("/my/ships/%s/warp", symbol)

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
		return nil, fmt.Errorf("failed to warp ship: %w", err)
	}

	arrivalTimeStr := response.Data.Nav.Route.Arrival
	arrivalTime := travelSeconds(response.Data.Nav.Route.DepartureTime, arrivalTimeStr)

	return &navigation.Result{
		Destination:    response.Data.Nav.WaypointSymbol,
		ArrivalTime:    arrivalTime,
		ArrivalTimeStr: arrivalTimeStr,
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
	c.invalidateAgentCache() // refuel spends credits -> drop the stale-high cache

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

	if err := c.request(ctx, "PATCH", path, token, body, nil); err != nil {
		return fmt.Errorf("failed to set flight mode: %w", err)
	}

	return nil
}

// JumpShip executes a jump through a jump gate to a different system.
// waypointSymbol must be the destination JUMP_GATE waypoint symbol (e.g.
// "X1-GQ92-I51") - not a bare system symbol. The live SpaceTraders API
// requires "waypointSymbol" in the request body and 422s with
// "waypointSymbol Required, received undefined" otherwise.
func (c *SpaceTradersClient) JumpShip(ctx context.Context, shipSymbol, waypointSymbol, token string) (*domainPorts.JumpResult, error) {
	path := fmt.Sprintf("/my/ships/%s/jump", shipSymbol)

	body := map[string]string{
		"waypointSymbol": waypointSymbol,
	}

	var response struct {
		Data struct {
			// Agent is a pointer so an omitted block (nil) is distinguishable
			// from a real zero balance; the in-band credits are the ledger's
			// authoritative post-fee balance (mirrors RefuelShip/SellCargo).
			Agent *struct {
				Credits int `json:"credits"`
			} `json:"agent"`
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
	c.invalidateAgentCache() // jump charges a gate fee (transaction.totalPrice) -> drop the stale-high cache

	result := &domainPorts.JumpResult{
		DestinationSystem:   response.Data.Nav.SystemSymbol,
		DestinationWaypoint: response.Data.Nav.WaypointSymbol,
		CooldownSeconds:     response.Data.Cooldown.RemainingSeconds,
		TotalPrice:          response.Data.Transaction.TotalPrice,
	}
	if response.Data.Agent != nil {
		credits := response.Data.Agent.Credits
		result.AgentCredits = &credits
	}
	return result, nil
}

// CreateChart PUBLICLY charts the ship's CURRENT waypoint (POST /my/ships/{shipSymbol}/chart).
// Once a waypoint is charted, every future read of it — notably the GetJumpGate that resolves a
// jump's destination WAYPOINT — succeeds WITHOUT a ship physically present. An UNcharted frontier
// jump gate otherwise 400s that read unless a hull is sitting on it, so an our-ships-only gate
// stays uncharted-public forever and every jump-OUT re-reads it live and 400s (the ~49% of
// GetJumpGate calls that fail). Charting it once from a present hull collapses that
// re-read storm and unblocks frontier jump-outs.
//
// The ship must be AT the waypoint (the API 400s otherwise); a waypoint that is already charted
// 400s with code 4230 "waypoint already charted" — a benign no-op the gate-graph caller detects
// and swallows. Mirrors JumpShip/OrbitShip's shape: an empty-body POST through the rate-limited
// request() path, with a typed *APIError surfaced on any non-2xx so the caller can classify the
// already-charted case.
//
// Charting is NOT free: the 201 body carries a one-time reward (data.transaction.totalPrice) and
// the post-reward balance (data.agent.credits), both returned so the caller can record and re-anchor.
func (c *SpaceTradersClient) CreateChart(ctx context.Context, shipSymbol, token string) (*domainPorts.ChartResult, error) {
	path := fmt.Sprintf("/my/ships/%s/chart", shipSymbol)

	var response struct {
		Data struct {
			Chart struct {
				WaypointSymbol string `json:"waypointSymbol"`
			} `json:"chart"`
			Transaction struct {
				TotalPrice int `json:"totalPrice"`
			} `json:"transaction"`
			// Pointer: an omitted agent block must stay distinct from a real zero balance.
			Agent *struct {
				Credits int `json:"credits"`
			} `json:"agent"`
		} `json:"data"`
	}

	// Send an empty JSON object {} (not nil) to satisfy the API, exactly as OrbitShip/DockShip do.
	emptyBody := map[string]interface{}{}
	if err := c.request(ctx, "POST", path, token, emptyBody, &response); err != nil {
		return nil, fmt.Errorf("failed to chart waypoint: %w", err)
	}
	c.invalidateAgentCache() // charting pays a reward -> drop the stale-low cache

	result := &domainPorts.ChartResult{
		WaypointSymbol: response.Data.Chart.WaypointSymbol,
		Reward:         response.Data.Transaction.TotalPrice,
	}
	if response.Data.Agent != nil {
		credits := response.Data.Agent.Credits
		result.AgentCredits = &credits
	}
	return result, nil
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
