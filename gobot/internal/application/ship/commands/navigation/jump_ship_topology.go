package navigation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// JumpTopologyStore answers a jump's two topology questions from the persisted gate
// graph the router already maintains: which gate waypoint a hop leaves for, and whether
// a gate has finished building. Both are era-scoped and freshness-bounded by the store,
// and both report a miss for anything uncertain, so the handler falls through to the
// live read rather than acting on a guess.
type JumpTopologyStore interface {
	StoredGateWaypoint(ctx context.Context, fromSystem, toSystem string) (string, bool, error)
	RecordedBuiltGate(ctx context.Context, gateWaypoint string) (bool, error)
	// PruneContradictedEdges reconciles systemSymbol's stored edges against the server's
	// authoritative connection set, deleting the ones it contradicts and returning how many
	// went. It is REMOVAL-ONLY by contract — an edge the set names but we do not hold is
	// never created — so it can only ever shrink the routable graph (RULINGS #4).
	PruneContradictedEdges(ctx context.Context, systemSymbol string, authoritativeConnections []string) (int, error)
}

// isDestinationGateUnderConstructionError reports whether the API rejected a
// jump because the destination system's jump gate is still under
// construction (error 4262). Mirrors isAlreadyAtDestinationError's
// string-matching approach in navigate_direct.go.
func isDestinationGateUnderConstructionError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "4262") || strings.Contains(msg, "under construction")
}

// notConnectedCode is the SpaceTraders verdict that the waypoint we asked to jump to is not
// adjacent to the hull's ACTUAL current location. Its payload carries data.connections — the
// complete gate set of the system the hull is really standing on — which is the only first-hand
// evidence we ever get that our believed position is wrong.
const notConnectedCode = 4255

// notConnectedTruth reports whether err is a not-connected refusal (4255) and, if so, returns
// whatever authoritative connection set the server attached to it.
//
// The two results are deliberately independent. The VERDICT alone is position evidence — it
// says the gate we asked for is not adjacent to where the hull IS — and that is what drives the
// re-anchor, with or without a connection list. The list is the separate, optional evidence
// that drives the adjacency reconcile; an absent one simply means there is nothing to reconcile
// against (the jump-gate endpoint is known to return empty reads).
//
// It reads the TYPED *ports.APIError body rather than string-matching the wrapped message, so
// it cannot be fooled by an unrelated error that happens to mention a code, and it survives the
// adapter's %w wrapping. The code — not the payload SHAPE — is the verdict: another refusal
// carrying a connections blob is not a position correction.
func notConnectedTruth(err error) ([]string, bool) {
	var apiErr *ports.APIError
	if !errors.As(err, &apiErr) {
		return nil, false
	}
	var payload struct {
		Error struct {
			Code int `json:"code"`
			Data struct {
				Connections []string `json:"connections"`
			} `json:"data"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(apiErr.Body), &payload) != nil {
		return nil, false
	}
	if payload.Error.Code != notConnectedCode {
		return nil, false
	}
	return payload.Error.Data.Connections, true
}

// reAnchorAfterNotConnected is the root-cause fix for the live TORWIND-41 incident. The
// planner had routed a hull standing on X1-KC84's gate as though it were at X1-GF41: the
// persisted row said GF41, so candidate discovery, the BFS and finally
// StoredGateWaypoint("X1-GF41","X1-KP23") all resolved faithfully off that one stale fact and
// posted X1-KP23-I53, a gate KC84 does not reach. Nothing reconciled that belief against the
// server, so the next tick re-derived the SAME impossible jump — four crashes in 27 minutes.
//
// A 4255 refusal settles it: the server is telling us where the hull is NOT. So re-read the
// hull and write it through to durable state, which is what the next tick re-derives from
// (RULINGS #2 — the correction lives in the durable row, no routing state is carried across
// ticks). Then, knowing at last which system the hull actually occupies, reconcile THAT
// system's stored edges against the connection set the refusal handed us.
//
// Attribution is the whole subtlety, and getting it wrong is worse than doing nothing: the
// connection set describes the gate the hull is STANDING ON, not the system we mistakenly
// planned from. In the incident GF41's stored edges were perfectly correct; charging them with
// this evidence would have corrupted healthy topology on every crash. So if the re-anchor
// cannot tell us where the hull really is, the evidence is unattributable and the adjacency is
// left completely alone — fail closed.
//
// It ALWAYS returns an error (the jump did fail), wrapping the original cause so the existing
// failure path, retries and operator triage are unchanged.
func (h *JumpShipHandler) reAnchorAfterNotConnected(
	ctx context.Context,
	cmd *JumpShipCommand,
	playerID shared.PlayerID,
	connections []string,
	cause error,
) error {
	logger := common.LoggerFromContext(ctx)

	fresh, err := h.shipRepo.SyncShipFromAPI(ctx, cmd.ShipSymbol, playerID)
	if err != nil || fresh == nil || fresh.CurrentLocation() == nil {
		logger.Log("WARNING", "Jump refused as not-connected but the hull could not be re-anchored — the server's connection set is unattributable, so the stored adjacency is left untouched (fail closed)", map[string]interface{}{
			"action":             "jump_not_connected_reanchor_failed",
			"ship_symbol":        cmd.ShipSymbol,
			"destination_system": cmd.DestinationSystem,
			"connections":        connections,
		})
		return fmt.Errorf("failed to execute jump: %w", cause)
	}

	trueSystem := fresh.CurrentLocation().SystemSymbol
	logger.Log("WARNING", fmt.Sprintf("Jump refused as not-connected — the hull is really in %s, not where routing believed; re-anchored on the server so the next tick re-plans from its true position", trueSystem), map[string]interface{}{
		"action":             "jump_not_connected_reanchored",
		"ship_symbol":        cmd.ShipSymbol,
		"destination_system": cmd.DestinationSystem,
		"true_system":        trueSystem,
		"true_waypoint":      fresh.CurrentLocation().Symbol,
		"connections":        connections,
	})

	h.reconcileAdjacencyAgainstTruth(ctx, trueSystem, connections, logger)

	return fmt.Errorf("jump of %s to %s refused: the hull is actually in %s (re-anchored on the server; next tick re-plans from there): %w",
		cmd.ShipSymbol, cmd.DestinationSystem, trueSystem, cause)
}

// reconcileAdjacencyAgainstTruth deletes any stored edge for trueSystem that the server's
// connection set contradicts, so the phantom hop can never be replanned. REMOVAL-ONLY: an
// unheld connection is never created (it carries no verified build state), so this can only
// ever shrink the routable graph and never authorise a jump that would otherwise be refused
// (RULINGS #4). Best-effort — the jump has already failed and a reconcile problem must not
// change that outcome, so a store failure or an unwired store is logged and swallowed.
func (h *JumpShipHandler) reconcileAdjacencyAgainstTruth(ctx context.Context, trueSystem string, connections []string, logger common.ContainerLogger) {
	// No list means no evidence: the re-anchor above still stands, but there is nothing to
	// reconcile against and an absent set must never be read as "connects nowhere".
	if h.topologyStore == nil || trueSystem == "" || len(connections) == 0 {
		return
	}
	removed, err := h.topologyStore.PruneContradictedEdges(ctx, trueSystem, connections)
	if err != nil {
		logger.Log("WARNING", "Could not reconcile the stored adjacency against the server's connection set (non-fatal)", map[string]interface{}{
			"action": "jump_adjacency_reconcile_failed",
			"system": trueSystem,
			"error":  err.Error(),
		})
		return
	}
	if removed > 0 {
		logger.Log("WARNING", fmt.Sprintf("Reconciled %s against the server's gate connections — %d contradicted edge(s) removed so the refused hop cannot be replanned", trueSystem, removed), map[string]interface{}{
			"action":        "jump_adjacency_reconciled",
			"system":        trueSystem,
			"removed_edges": removed,
			"authoritative": connections,
		})
	}
}

// maxJumpGateReadAttempts bounds how many times a cross-system jump re-reads the ORIGIN
// gate's live connections when the intended destination is missing from the response.
// The SpaceTraders jump-gate endpoint intermittently returns a 200 OK with an
// incomplete/empty connections list — a transient, eventually-consistent read, DISTINCT
// from a 429 (which the API client already retries on status code, so it never reaches
// here as an empty list). Treating that one bad read as a permanent "no connection" is
// what bounced hulls forever between two systems: the gate_edges cache is a faithful
// snapshot that MATCHES the live API, so the charted connection is real and reappears on
// the very next read. A few bounded re-reads recover the hop; a destination absent from
// ALL attempts fails cleanly (no infinite bounce, no cache poisoning).
const maxJumpGateReadAttempts = 3

// jumpGateReadRetryBackoff is the short settle between bounded re-reads of a gate whose
// live connections came back missing the destination — enough for an eventually-consistent
// backend to converge without stalling the jump. Applied ONLY between re-reads (never after
// the final attempt, never on the happy path), and via the handler clock so tests advance
// it instantly. Combined with the API client's own rate limiter, the re-read never spams:
// the extra reads happen only on the (rare) missing-destination path.
const jumpGateReadRetryBackoff = 750 * time.Millisecond

// resolveDestinationGateWaypoint resolves the destination system's gate WAYPOINT from the
// origin gate's LIVE connections (the jump request body requires the waypoint, not the
// bare system), re-reading a bounded number of times when the
// destination is missing. A single live read is NOT trusted for a NEGATIVE
// verdict: the jump-gate endpoint occasionally returns an incomplete/empty 200, and a
// charted connection reappears on the next read. The happy path (destination present on
// the first read) returns immediately with exactly one read — zero overhead, no spam. A
// destination genuinely absent from every bounded read yields a clean, terminal error (no
// infinite bounce). A hard GetJumpGate error (429-exhausted, 5xx, network) is surfaced
// immediately — the client already retried it, so re-reading here would not help.
func (h *JumpShipHandler) resolveDestinationGateWaypoint(ctx context.Context, originGateSymbol, destinationSystem, token string) (string, error) {
	originSystem := shared.ExtractSystemSymbol(originGateSymbol)
	logger := common.LoggerFromContext(ctx)
	// The router already recorded this hop's destination gate, written from this very
	// origin gate's connections list — the same string the live read returns, for a
	// symbol that does not change within an era. A miss, a stale row, or any store
	// failure falls through to the live reads below unchanged.
	if h.topologyStore != nil {
		if waypoint, ok, err := h.topologyStore.StoredGateWaypoint(ctx, originSystem, destinationSystem); err == nil && ok && waypoint != "" {
			return waypoint, nil
		}
	}
	for attempt := 0; attempt < maxJumpGateReadAttempts; attempt++ {
		gateData, err := h.apiClient.GetJumpGate(ctx, originSystem, originGateSymbol, token)
		if err != nil {
			return "", fmt.Errorf("failed to resolve jump gate connections for %s: %w", originGateSymbol, err)
		}
		if waypoint, ok := findDestinationGateWaypoint(gateData.Connections, destinationSystem); ok {
			return waypoint, nil
		}
		// Destination missing from THIS read. A charted gate momentarily returning an
		// incomplete/empty connection set is a transient live read, not a permanent
		// topology change — re-read (bounded) before concluding there is no connection.
		if attempt < maxJumpGateReadAttempts-1 {
			logger.Log("INFO", "Destination missing from live gate connections — re-reading (transient incomplete read, sp-hguq3)", map[string]interface{}{
				"action":             "jump_gate_reread",
				"origin_gate":        originGateSymbol,
				"destination_system": destinationSystem,
				"connections_seen":   len(gateData.Connections),
				"attempt":            attempt + 1,
			})
			h.clock.Sleep(jumpGateReadRetryBackoff)
		}
	}
	return "", fmt.Errorf("no jump gate connection from origin gate %s to system %s after %d live reads", originGateSymbol, destinationSystem, maxJumpGateReadAttempts)
}

// findDestinationGateWaypoint returns the connection in a jump gate's connections list
// whose system matches destinationSystem, and whether one was found. The live SpaceTraders
// jump API requires this full WAYPOINT (e.g. "X1-GQ92-I51"), not the bare system symbol, as
// waypointSymbol in the request body. Returning ok=false (rather than an
// error) lets the caller distinguish "missing from THIS read" — worth a bounded re-read
// — from a hard fetch failure.
func findDestinationGateWaypoint(connections []string, destinationSystem string) (string, bool) {
	for _, conn := range connections {
		if shared.ExtractSystemSymbol(conn) == destinationSystem {
			return conn, true
		}
	}
	return "", false
}

// sourceGateComplete reports whether the jump gate at waypointSymbol has
// finished construction, i.e. is a valid SOURCE gate for a driveless jump.
// Returns an error if construction status could not be determined (no
// repository configured, or the lookup itself failed) - callers should fail
// open on error rather than block an otherwise-legal jump.
func (h *JumpShipHandler) sourceGateComplete(ctx context.Context, waypointSymbol string, playerID int) (bool, error) {
	// A gate the router already recorded as finished cannot have gone back to being
	// built, so re-reading it costs a request to learn nothing. Only that verdict is
	// taken from the record; anything else — not recorded, still building, stale, or a
	// store failure — is verified live below.
	if h.topologyStore != nil {
		if built, err := h.topologyStore.RecordedBuiltGate(ctx, waypointSymbol); err == nil && built {
			return true, nil
		}
	}
	if h.constructionRepo == nil {
		return false, fmt.Errorf("construction repository not configured")
	}
	site, err := h.constructionRepo.FindByWaypoint(ctx, waypointSymbol, playerID)
	if err != nil {
		return false, err
	}
	return site.IsComplete(), nil
}
