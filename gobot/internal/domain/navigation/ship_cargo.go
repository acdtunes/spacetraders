package navigation

import (
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

func (s *Ship) CargoCapacity() int {
	return s.cargoCapacity
}

func (s *Ship) Cargo() *shared.Cargo {
	return s.cargo
}

func (s *Ship) CargoUnits() int {
	if s.cargo == nil {
		return 0
	}
	return s.cargo.Units
}

func (s *Ship) HasCargoSpace(units int) bool {
	if s.cargo == nil {
		return units <= s.cargoCapacity
	}
	return (s.cargo.Units + units) <= s.cargoCapacity
}

func (s *Ship) AvailableCargoSpace() int {
	if s.cargo == nil {
		return s.cargoCapacity
	}
	return s.cargo.AvailableCapacity()
}

func (s *Ship) IsCargoEmpty() bool {
	if s.cargo == nil {
		return true
	}
	return s.cargo.IsEmpty()
}

func (s *Ship) IsCargoFull() bool {
	if s.cargo == nil {
		return false
	}
	return s.cargo.Units >= s.cargoCapacity
}

// SetCargo updates the ship's cargo (used by repository for reconstruction)
func (s *Ship) SetCargo(c *shared.Cargo) {
	s.cargo = c
}

// ReceiveCargo adds cargo to the ship's hold
// Returns error if insufficient space
func (s *Ship) ReceiveCargo(item *shared.CargoItem) error {
	if item == nil || item.Units <= 0 {
		return nil
	}
	if !s.HasCargoSpace(item.Units) {
		return fmt.Errorf("insufficient cargo space: need %d, have %d available",
			item.Units, s.AvailableCargoSpace())
	}

	newInventory := inventoryWithItemMerged(s.cargo.Inventory, item)

	// Create new cargo (immutable)
	newCargo, _ := shared.NewCargo(s.cargo.Capacity, s.cargo.Units+item.Units, newInventory)
	s.cargo = newCargo
	return nil
}

func inventoryWithItemMerged(inventory []*shared.CargoItem, item *shared.CargoItem) []*shared.CargoItem {
	newInventory := make([]*shared.CargoItem, 0, len(inventory)+1)
	found := false
	for _, existing := range inventory {
		if existing.Symbol == item.Symbol {
			newInventory = append(newInventory, &shared.CargoItem{
				Symbol:      existing.Symbol,
				Name:        existing.Name,
				Description: existing.Description,
				Units:       existing.Units + item.Units,
			})
			found = true
		} else {
			newInventory = append(newInventory, existing)
		}
	}
	if !found {
		newInventory = append(newInventory, item)
	}
	return newInventory
}

// RemoveCargo removes cargo from the ship's hold
// Returns error if insufficient cargo
func (s *Ship) RemoveCargo(symbol string, units int) error {
	if units <= 0 {
		return nil
	}

	currentUnits := s.cargo.GetItemUnits(symbol)
	if currentUnits < units {
		return fmt.Errorf("insufficient cargo: have %d units of %s, need %d",
			currentUnits, symbol, units)
	}

	newInventory := inventoryWithUnitsRemoved(s.cargo.Inventory, symbol, units)

	// Create new cargo (immutable)
	newCargo, _ := shared.NewCargo(s.cargo.Capacity, s.cargo.Units-units, newInventory)
	s.cargo = newCargo
	return nil
}

func inventoryWithUnitsRemoved(inventory []*shared.CargoItem, symbol string, units int) []*shared.CargoItem {
	newInventory := make([]*shared.CargoItem, 0, len(inventory))
	for _, item := range inventory {
		if item.Symbol != symbol {
			newInventory = append(newInventory, item)
			continue
		}
		remaining := item.Units - units
		if remaining > 0 {
			newInventory = append(newInventory, &shared.CargoItem{
				Symbol:      item.Symbol,
				Name:        item.Name,
				Description: item.Description,
				Units:       remaining,
			})
		}
	}
	return newInventory
}
