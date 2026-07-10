package api

import "strings"

var endpointNameMap = map[string]string{
	// Agent
	"/my/agent": "Get Agent",

	// Ships
	"/my/ships":                      "List Ships",
	"/my/ships/*":                    "Get Ship",
	"/my/ships/*/cargo":              "Get Cargo",
	"/my/ships/*/nav":                "Set Flight Mode",
	"/my/ships/*/navigate":           "Navigate",
	"/my/ships/*/dock":               "Dock",
	"/my/ships/*/orbit":              "Orbit",
	"/my/ships/*/refuel":             "Refuel",
	"/my/ships/*/purchase":           "Buy Cargo",
	"/my/ships/*/sell":               "Sell Cargo",
	"/my/ships/*/transfer":           "Transfer Cargo",
	"/my/ships/*/jettison":           "Jettison Cargo",
	"/my/ships/*/extract":            "Extract Resources",
	"/my/ships/*/siphon":             "Siphon Gas",
	"/my/ships/*/survey":             "Survey",
	"/my/ships/*/scan/ships":         "Scan Ships",
	"/my/ships/*/scan/waypoints":     "Scan Waypoints",
	"/my/ships/*/scan/systems":       "Scan Systems",
	"/my/ships/*/negotiate/contract": "Negotiate Contract",
	"/my/ships/*/warp":               "Warp",
	"/my/ships/*/jump":               "Jump",
	"/my/ships/*/chart":              "Chart Waypoint",
	"/my/ships/*/cooldown":           "Get Cooldown",
	"/my/ships/*/mounts":             "Get Mounts",
	"/my/ships/*/mounts/install":     "Install Mount",
	"/my/ships/*/mounts/remove":      "Remove Mount",
	"/my/ships/*/modules":            "Get Modules",
	"/my/ships/*/modules/install":    "Install Module",
	"/my/ships/*/modules/remove":     "Remove Module",

	// Contracts
	"/my/contracts":           "List Contracts",
	"/my/contracts/*":         "Get Contract",
	"/my/contracts/*/accept":  "Accept Contract",
	"/my/contracts/*/deliver": "Deliver Contract",
	"/my/contracts/*/fulfill": "Fulfill Contract",

	// Systems & Waypoints
	"/systems":                            "List Systems",
	"/systems/*":                          "Get System",
	"/systems/*/waypoints":                "List Waypoints",
	"/systems/*/waypoints/*":              "Get Waypoint",
	"/systems/*/waypoints/*/market":       "Get Market",
	"/systems/*/waypoints/*/shipyard":     "Get Shipyard",
	"/systems/*/waypoints/*/jump-gate":    "Get Jump Gate",
	"/systems/*/waypoints/*/construction": "Get Construction",

	// Factions
	"/factions":   "List Factions",
	"/factions/*": "Get Faction",

	// Register
	"/register": "Register Agent",
}

type endpointClassifier struct{}

var apiEndpointClassifier = endpointClassifier{}

func (c endpointClassifier) classify(path string) string {
	// First, strip query parameters
	cleanPath := path
	for i, ch := range path {
		if ch == '?' {
			cleanPath = path[:i]
			break
		}
	}

	// Build a normalized path pattern (replace dynamic segments)
	pattern := c.normalizePath(strings.Split(cleanPath, "/"))

	// Map patterns to human-readable names
	if name, ok := endpointNameMap[pattern]; ok {
		return name
	}

	// Fallback: return the normalized pattern
	return pattern
}

// normalizePath replaces dynamic segments with * for pattern matching
func (c endpointClassifier) normalizePath(parts []string) string {
	normalized := make([]string, 0, len(parts))

	for i, part := range parts {
		if part == "" {
			continue
		}

		// Check if this segment is dynamic based on context
		var prevPart string
		if len(normalized) > 0 {
			prevPart = normalized[len(normalized)-1]
		}

		if c.isDynamicSegment(part, prevPart, parts, i) {
			normalized = append(normalized, "*")
		} else {
			normalized = append(normalized, part)
		}
	}

	return "/" + strings.Join(normalized, "/")
}

// isDynamicSegment determines if a path segment is a dynamic value (ID, symbol)
func (c endpointClassifier) isDynamicSegment(segment, prevSegment string, parts []string, index int) bool {
	switch prevSegment {
	case "ships":
		return looksLikeShipSymbol(segment)
	case "systems":
		return looksLikeSystemSymbol(segment)
	case "waypoints":
		return looksLikeWaypointSymbol(segment)
	case "contracts":
		return looksLikeContractID(segment)
	case "factions":
		return looksLikeFactionSymbol(segment)
	}
	return false
}

// looksLikeShipSymbol checks if segment matches ship symbol pattern (e.g., TORWIND-10)
func looksLikeShipSymbol(s string) bool {
	return strings.Contains(s, "-") && !strings.HasPrefix(s, "X")
}

// extractShipSymbol returns the ship symbol embedded in a ship-scoped API
// path (e.g. "/my/ships/TORWIND-1/dock" -> "TORWIND-1"), or "" if the path
// is not scoped to a single hull (e.g. "/my/ships" list/purchase, or a
// waypoint/system/contract path). Used by the budget tracker (sp-51ti) to
// attribute request-budget consumption per hull.
func extractShipSymbol(path string) string {
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "ships" && i+1 < len(parts) && looksLikeShipSymbol(parts[i+1]) {
			return parts[i+1]
		}
	}
	return ""
}

// looksLikeSystemSymbol checks if segment matches system symbol pattern (e.g., X1-ABC)
func looksLikeSystemSymbol(s string) bool {
	return len(s) > 3 && s[0] == 'X' && s[2] == '-'
}

// looksLikeWaypointSymbol checks if segment matches waypoint symbol pattern (e.g., X1-ABC-XYZ)
func looksLikeWaypointSymbol(s string) bool {
	dashCount := strings.Count(s, "-")
	return dashCount >= 2 && strings.HasPrefix(s, "X")
}

// looksLikeContractID checks if segment matches contract ID pattern
func looksLikeContractID(s string) bool {
	return len(s) > 20
}

// looksLikeFactionSymbol checks if segment matches faction symbol pattern
func looksLikeFactionSymbol(s string) bool {
	for _, ch := range s {
		if ch < 'A' || ch > 'Z' {
			return false
		}
	}
	return len(s) > 0
}
