package cluster

import "fmt"

// Element is one placed, sized member of a contract cluster — a destination
// warehouse hull, a background stocker, a source hub, or a pinned delivery hull —
// parked at an arbitrary Waypoint and optionally crewed by a specific ShipSymbol.
// BOTH fields are PARAMETERS (bead sp-u9xa): the mechanism hardcodes no waypoint and
// no count, so a cluster placed at a different waypoint, or sized with a different
// number of elements, needs zero code change. An empty ShipSymbol is a
// declared-but-uncrewed slot — the sizing is known before a specific hull is pinned.
type Element struct {
	Waypoint   string
	ShipSymbol string
}

// ContractCluster localizes the contract-fulfilment supply chain to a region so the
// dominant source->destination HAUL-LEG moves OFF the serialized contract critical
// path onto parallel stockers. It is the tuple
//
//	{ source hubs, destination warehouse(s), stocker(s), pinned delivery hull(s) }
//
// with every position and count a parameter. See the package doc for the economic
// model. The value is immutable after construction: the accessors hand back copies so
// a caller can never mutate a cluster's topology behind the registry's back.
type ContractCluster struct {
	id            string
	warehouses    []Element
	stockers      []Element
	deliveryHulls []Element
	sourceHubs    []Element
}

// NewContractCluster builds a cluster from an arbitrary, fully-parametrized topology.
// The only invariants are a non-empty id and at least one destination warehouse (the
// cluster's ANCHOR — a cluster with no warehouse localizes nothing and can own no
// contract's destination geometry). Counts and waypoints are otherwise unconstrained:
// the caller supplies whatever the economy-analyst policy sized, and the constructor
// bakes in none of it.
func NewContractCluster(id string, warehouses, stockers, deliveryHulls, sourceHubs []Element) (*ContractCluster, error) {
	if id == "" {
		return nil, fmt.Errorf("cluster id cannot be empty")
	}
	if len(warehouses) == 0 {
		return nil, fmt.Errorf("cluster %q must have at least one destination warehouse (the routing anchor)", id)
	}
	return &ContractCluster{
		id:            id,
		warehouses:    cloneElements(warehouses),
		stockers:      cloneElements(stockers),
		deliveryHulls: cloneElements(deliveryHulls),
		sourceHubs:    cloneElements(sourceHubs),
	}, nil
}

// ID returns the cluster's stable identifier.
func (c *ContractCluster) ID() string { return c.id }

// Warehouses returns a copy of the destination warehouse elements.
func (c *ContractCluster) Warehouses() []Element { return cloneElements(c.warehouses) }

// Stockers returns a copy of the background long-haul stocker elements.
func (c *ContractCluster) Stockers() []Element { return cloneElements(c.stockers) }

// DeliveryHulls returns a copy of the pinned local-delivery hull elements.
func (c *ContractCluster) DeliveryHulls() []Element { return cloneElements(c.deliveryHulls) }

// SourceHubs returns a copy of the source hub elements that feed the stockers.
func (c *ContractCluster) SourceHubs() []Element { return cloneElements(c.sourceHubs) }

// cloneElements returns a defensive copy so the immutable cluster never leaks its
// backing slice. A nil input round-trips to nil (an absent element class).
func cloneElements(src []Element) []Element {
	if src == nil {
		return nil
	}
	out := make([]Element, len(src))
	copy(out, src)
	return out
}
