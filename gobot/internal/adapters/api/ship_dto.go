package api

import (
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// requirementsDTO mirrors the SpaceTraders API's ShipRequirements schema
// (power/crew/slots). It is shared by ShipReactor, ShipModule, and ShipMount
// - every module/mount/reactor declares its own cost against the hull's
// fixed power, slot, and crew budgets (sp-el60).
type requirementsDTO struct {
	Power int `json:"power"`
	Crew  int `json:"crew"`
	Slots int `json:"slots"`
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
		} `json:"route,omitempty"`
	} `json:"nav"`
	Fuel struct {
		Current  int `json:"current"`
		Capacity int `json:"capacity"`
	} `json:"fuel"`
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
		// of the hull (sp-el60).
		ModuleSlots    int `json:"moduleSlots"`
		MountingPoints int `json:"mountingPoints"`
	} `json:"frame"`
	// Reactor is the hull's fixed power budget. Reactors have no
	// swap/upgrade endpoint in the SpaceTraders API - PowerOutput is
	// permanent for the life of the ship (sp-el60).
	Reactor struct {
		Symbol       string           `json:"symbol"`
		Name         string           `json:"name"`
		PowerOutput  int              `json:"powerOutput"`
		Requirements requirementsDTO  `json:"requirements"`
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
	// arrays, weapons, etc.) - sp-el60.
	Mounts []struct {
		Symbol       string          `json:"symbol"`
		Name         string          `json:"name"`
		Strength     int             `json:"strength"`
		Deposits     []string        `json:"deposits"`
		Requirements requirementsDTO `json:"requirements"`
	} `json:"mounts"`
}

func (d *shipDTO) toShipData() *navigation.ShipData {
	inventory := make([]shared.CargoItem, len(d.Cargo.Inventory))
	for i, item := range d.Cargo.Inventory {
		inventory[i] = shared.CargoItem{
			Symbol:      item.Symbol,
			Name:        item.Name,
			Description: item.Description,
			Units:       item.Units,
		}
	}

	cargo := &navigation.CargoData{
		Capacity:  d.Cargo.Capacity,
		Units:     d.Cargo.Units,
		Inventory: inventory,
	}

	modules := make([]navigation.ModuleData, len(d.Modules))
	for i, module := range d.Modules {
		modules[i] = navigation.ModuleData{
			Symbol:   module.Symbol,
			Capacity: module.Capacity,
			Range:    module.Range,
			Requirements: navigation.RequirementsData{
				Power: module.Requirements.Power,
				Crew:  module.Requirements.Crew,
				Slots: module.Requirements.Slots,
			},
		}
	}

	mounts := make([]navigation.MountData, len(d.Mounts))
	for i, mount := range d.Mounts {
		mounts[i] = navigation.MountData{
			Symbol:   mount.Symbol,
			Name:     mount.Name,
			Strength: mount.Strength,
			Deposits: mount.Deposits,
			Requirements: navigation.RequirementsData{
				Power: mount.Requirements.Power,
				Crew:  mount.Requirements.Crew,
				Slots: mount.Requirements.Slots,
			},
		}
	}

	arrivalTime := ""
	if d.Nav.Route != nil {
		arrivalTime = d.Nav.Route.Arrival
	}

	cooldownExpiration := ""
	if d.Cooldown != nil {
		cooldownExpiration = d.Cooldown.Expiration
	}

	return &navigation.ShipData{
		Symbol:             d.Symbol,
		Location:           d.Nav.WaypointSymbol,
		NavStatus:          d.Nav.Status,
		FlightMode:         d.Nav.FlightMode,
		ArrivalTime:        arrivalTime,
		CooldownExpiration: cooldownExpiration,
		FuelCurrent:        d.Fuel.Current,
		FuelCapacity:       d.Fuel.Capacity,
		CargoCapacity:      d.Cargo.Capacity,
		CargoUnits:         d.Cargo.Units,
		EngineSpeed:        d.Engine.Speed,
		FrameSymbol:        d.Frame.Symbol,
		ModuleSlots:        d.Frame.ModuleSlots,
		MountingPoints:     d.Frame.MountingPoints,
		Role:               d.Registration.Role,
		Modules:            modules,
		Mounts:             mounts,
		ReactorSymbol:      d.Reactor.Symbol,
		ReactorName:        d.Reactor.Name,
		ReactorPowerOutput: d.Reactor.PowerOutput,
		ReactorRequirements: navigation.RequirementsData{
			Power: d.Reactor.Requirements.Power,
			Crew:  d.Reactor.Requirements.Crew,
			Slots: d.Reactor.Requirements.Slots,
		},
		CrewCurrent:  d.Crew.Current,
		CrewRequired: d.Crew.Required,
		CrewCapacity: d.Crew.Capacity,
		Cargo:        cargo,
	}
}
