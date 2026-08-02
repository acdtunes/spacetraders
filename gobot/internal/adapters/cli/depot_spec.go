package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// depotTopologyFile is the on-disk shape of a `depot apply` spec file.
type depotTopologyFile struct {
	Depots []DepotDTO `json:"depots"`
}

// loadDepotSpecFile reads and parses a topology spec file into its depots. It accepts
// either the wrapped object form {"depots":[...]} or a bare array [...].
func loadDepotSpecFile(path string) ([]DepotDTO, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read depot spec %q: %w", path, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "[") {
		var depots []DepotDTO
		if err := json.Unmarshal(data, &depots); err != nil {
			return nil, fmt.Errorf("parse depot spec %q: %w", path, err)
		}
		return depots, nil
	}
	var file depotTopologyFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse depot spec %q: %w", path, err)
	}
	return file.Depots, nil
}

// selectDepotFromSpec finds the depot named id in a parsed spec's depots. It errors when
// no depot in the spec carries that id, so `depot start <name>` fails loudly rather than
// silently starting nothing when the operator names a depot the spec does not define.
func selectDepotFromSpec(depots []DepotDTO, id string) (DepotDTO, error) {
	for _, d := range depots {
		if d.ID == id {
			return d, nil
		}
	}
	available := make([]string, 0, len(depots))
	for _, d := range depots {
		available = append(available, d.ID)
	}
	return DepotDTO{}, fmt.Errorf("depot %q not found in spec (defines: %s)", id, strings.Join(available, ", "))
}

// buildDepotDTO assembles a DepotDTO from an id and the repeatable element flags.
func buildDepotDTO(id string, warehouses, stockers, deliveryHulls, sourceHubs []string) (DepotDTO, error) {
	wh, err := parseDepotElementFlags(warehouses)
	if err != nil {
		return DepotDTO{}, err
	}
	st, err := parseDepotElementFlags(stockers)
	if err != nil {
		return DepotDTO{}, err
	}
	dh, err := parseDepotElementFlags(deliveryHulls)
	if err != nil {
		return DepotDTO{}, err
	}
	sh, err := parseDepotElementFlags(sourceHubs)
	if err != nil {
		return DepotDTO{}, err
	}
	return DepotDTO{ID: id, Warehouses: wh, Stockers: st, DeliveryHulls: dh, SourceHubs: sh}, nil
}

// depotRoleNames is the set of valid --role values, mirroring the domain role names so
// a mistyped role is rejected client-side before the RPC.
var depotRoleNames = map[string]bool{"warehouse": true, "stocker": true, "delivery-hull": true, "source-hub": true}

func requireDepotRole(role string) error {
	if !depotRoleNames[role] {
		return fmt.Errorf("invalid --role %q (want one of warehouse, stocker, delivery-hull, source-hub)", role)
	}
	return nil
}

func parseDepotElementFlags(raws []string) ([]DepotElementDTO, error) {
	if len(raws) == 0 {
		return nil, nil
	}
	out := make([]DepotElementDTO, 0, len(raws))
	for _, raw := range raws {
		e, err := parseDepotElementFlag(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// parseDepotElementFlag parses one element flag "WAYPOINT" or "WAYPOINT@SHIP" into a
// DepotElementDTO. The waypoint is required; the ship is optional (an uncrewed slot).
func parseDepotElementFlag(raw string) (DepotElementDTO, error) {
	parts := strings.SplitN(raw, "@", 2)
	waypoint := strings.TrimSpace(parts[0])
	if waypoint == "" {
		return DepotElementDTO{}, fmt.Errorf("invalid element %q (want WAYPOINT or WAYPOINT@SHIP)", raw)
	}
	element := DepotElementDTO{Waypoint: waypoint}
	if len(parts) == 2 {
		element.ShipSymbol = strings.TrimSpace(parts[1])
	}
	return element, nil
}
