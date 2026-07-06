package api

import (
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

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
	} `json:"frame"`
	Modules []struct {
		Symbol   string `json:"symbol"`
		Capacity int    `json:"capacity"`
		Range    int    `json:"range"`
	} `json:"modules"`
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
		Role:               d.Registration.Role,
		Modules:            modules,
		Cargo:              cargo,
	}
}
