package shipyard

// ShipListing represents a ship listing in a shipyard.
// A ship listing contains all the information about a ship type available
// for purchase, including its specifications and price.
type ShipListing struct {
	ShipType      string
	Name          string
	Description   string
	PurchasePrice int
	// Optional specifications
	Frame   map[string]interface{}
	Reactor map[string]interface{}
	Engine  map[string]interface{}
	Modules []map[string]interface{}
	Mounts  []map[string]interface{}
}

// Shipyard represents a shipyard at a waypoint.
// A shipyard is where ships can be purchased. It contains information about
// available ship types, current listings, transaction history, and modification fees.
type Shipyard struct {
	Symbol          string
	ShipTypes       []string
	Listings        []ShipListing
	Transactions    []map[string]interface{}
	ModificationFee int
}

// NewShipListing creates a new ShipListing value object
func NewShipListing(
	shipType string,
	name string,
	description string,
	purchasePrice int,
) ShipListing {
	return ShipListing{
		ShipType:      shipType,
		Name:          name,
		Description:   description,
		PurchasePrice: purchasePrice,
	}
}

// NewShipyard creates a new Shipyard value object
func NewShipyard(
	symbol string,
	shipTypes []string,
	listings []ShipListing,
	modificationFee int,
) Shipyard {
	return Shipyard{
		Symbol:          symbol,
		ShipTypes:       shipTypes,
		Listings:        listings,
		Transactions:    []map[string]interface{}{},
		ModificationFee: modificationFee,
	}
}

// FindListingByType finds a ship listing by ship type
// Returns the listing if found, and a boolean indicating success
func (s *Shipyard) FindListingByType(shipType string) (*ShipListing, bool) {
	for i := range s.Listings {
		if s.Listings[i].ShipType == shipType {
			return &s.Listings[i], true
		}
	}
	return nil, false
}

// HasShipType checks if the shipyard sells a specific ship type
func (s *Shipyard) HasShipType(shipType string) bool {
	for _, st := range s.ShipTypes {
		if st == shipType {
			return true
		}
	}
	return false
}
