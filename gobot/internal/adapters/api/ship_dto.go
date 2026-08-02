package api

import (
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// requirementsDTO mirrors the SpaceTraders API's ShipRequirements schema
// (power/crew/slots). It is shared by ShipReactor, ShipModule, and ShipMount
// - every module/mount/reactor declares its own cost against the hull's
// fixed power, slot, and crew budgets.
type requirementsDTO struct {
	Power int `json:"power"`
	Crew  int `json:"crew"`
	Slots int `json:"slots"`
}

// cargoDTO mirrors the SpaceTraders API's ShipCargo schema, returned by every
// endpoint that reads or mutates a hold.
type cargoDTO struct {
	Capacity  int `json:"capacity"`
	Units     int `json:"units"`
	Inventory []struct {
		Symbol      string `json:"symbol"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Units       int    `json:"units"`
	} `json:"inventory"`
}

func (c cargoDTO) toCargoData() *navigation.CargoData {
	inventory := make([]shared.CargoItem, len(c.Inventory))
	for i, item := range c.Inventory {
		inventory[i] = shared.CargoItem{
			Symbol:      item.Symbol,
			Name:        item.Name,
			Description: item.Description,
			Units:       item.Units,
		}
	}

	return &navigation.CargoData{
		Capacity:  c.Capacity,
		Units:     c.Units,
		Inventory: inventory,
	}
}

type shipDTO struct {
	Symbol       string `json:"symbol"`
	Registration struct {
		Role string `json:"role"`
	} `json:"registration"`
	Nav struct {
		SystemSymbol   string `json:"systemSymbol"`
		WaypointSymbol string `json:"waypointSymbol"`
		Status         string `json:"status"`
		FlightMode     string `json:"flightMode"`
		Route          *struct {
			Arrival string `json:"arrival"`
			// The API's route.origin is a waypoint object (symbol + coordinates)
			// marking where the current transit began; departureTime is when it
			// began.
			DepartureTime string `json:"departureTime"`
			Origin        struct {
				Symbol string  `json:"symbol"`
				X      float64 `json:"x"`
				Y      float64 `json:"y"`
			} `json:"origin"`
		} `json:"route,omitempty"`
	} `json:"nav"`
	Fuel struct {
		Current  int `json:"current"`
		Capacity int `json:"capacity"`
	} `json:"fuel"`
	Cargo    cargoDTO `json:"cargo"`
	Cooldown *struct {
		Expiration string `json:"expiration"`
	} `json:"cooldown,omitempty"`
	Engine struct {
		Speed int `json:"speed"`
	} `json:"engine"`
	Frame struct {
		Symbol string `json:"symbol"`
		// ModuleSlots/MountingPoints are the frame's fixed budgets - frames
		// have no swap/upgrade endpoint, so these are permanent for the life
		// of the hull.
		ModuleSlots    int `json:"moduleSlots"`
		MountingPoints int `json:"mountingPoints"`
	} `json:"frame"`
	// Reactor is the hull's fixed power budget. Reactors have no
	// swap/upgrade endpoint in the SpaceTraders API - PowerOutput is
	// permanent for the life of the ship.
	Reactor struct {
		Symbol       string          `json:"symbol"`
		Name         string          `json:"name"`
		PowerOutput  int             `json:"powerOutput"`
		Requirements requirementsDTO `json:"requirements"`
	} `json:"reactor"`
	Crew struct {
		Current  int `json:"current"`
		Required int `json:"required"`
		Capacity int `json:"capacity"`
	} `json:"crew"`
	Modules []struct {
		Symbol       string          `json:"symbol"`
		Capacity     int             `json:"capacity"`
		Range        int             `json:"range"`
		Requirements requirementsDTO `json:"requirements"`
	} `json:"modules"`
	// Mounts are installed mounts (mining lasers, gas siphons, sensor
	// arrays, weapons, etc.).
	Mounts []struct {
		Symbol       string          `json:"symbol"`
		Name         string          `json:"name"`
		Strength     int             `json:"strength"`
		Deposits     []string        `json:"deposits"`
		Requirements requirementsDTO `json:"requirements"`
	} `json:"mounts"`
}

func (r requirementsDTO) toRequirementsData() navigation.RequirementsData {
	return navigation.RequirementsData{
		Power: r.Power,
		Crew:  r.Crew,
		Slots: r.Slots,
	}
}

func (d *shipDTO) toModuleData() []navigation.ModuleData {
	modules := make([]navigation.ModuleData, len(d.Modules))
	for i, module := range d.Modules {
		modules[i] = navigation.ModuleData{
			Symbol:       module.Symbol,
			Capacity:     module.Capacity,
			Range:        module.Range,
			Requirements: module.Requirements.toRequirementsData(),
		}
	}
	return modules
}

func (d *shipDTO) toMountData() []navigation.MountData {
	mounts := make([]navigation.MountData, len(d.Mounts))
	for i, mount := range d.Mounts {
		mounts[i] = navigation.MountData{
			Symbol:       mount.Symbol,
			Name:         mount.Name,
			Strength:     mount.Strength,
			Deposits:     mount.Deposits,
			Requirements: mount.Requirements.toRequirementsData(),
		}
	}
	return mounts
}

func (d *shipDTO) toShipData() *navigation.ShipData {
	data := &navigation.ShipData{
		Symbol:              d.Symbol,
		Location:            d.Nav.WaypointSymbol,
		NavStatus:           d.Nav.Status,
		FlightMode:          d.Nav.FlightMode,
		FuelCurrent:         d.Fuel.Current,
		FuelCapacity:        d.Fuel.Capacity,
		CargoCapacity:       d.Cargo.Capacity,
		CargoUnits:          d.Cargo.Units,
		EngineSpeed:         d.Engine.Speed,
		FrameSymbol:         d.Frame.Symbol,
		ModuleSlots:         d.Frame.ModuleSlots,
		MountingPoints:      d.Frame.MountingPoints,
		Role:                d.Registration.Role,
		Modules:             d.toModuleData(),
		Mounts:              d.toMountData(),
		ReactorSymbol:       d.Reactor.Symbol,
		ReactorName:         d.Reactor.Name,
		ReactorPowerOutput:  d.Reactor.PowerOutput,
		ReactorRequirements: d.Reactor.Requirements.toRequirementsData(),
		CrewCurrent:         d.Crew.Current,
		CrewRequired:        d.Crew.Required,
		CrewCapacity:        d.Crew.Capacity,
		Cargo:               d.Cargo.toCargoData(),
	}

	if d.Nav.Route != nil {
		data.ArrivalTime = d.Nav.Route.Arrival
		data.DepartureTime = d.Nav.Route.DepartureTime
		data.OriginSymbol = d.Nav.Route.Origin.Symbol
		data.OriginX = d.Nav.Route.Origin.X
		data.OriginY = d.Nav.Route.Origin.Y
	}

	if d.Cooldown != nil {
		data.CooldownExpiration = d.Cooldown.Expiration
	}

	return data
}

// convertShipData converts ship data from API response map to ShipData struct
func convertShipData(data map[string]interface{}) (*navigation.ShipData, error) {
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
