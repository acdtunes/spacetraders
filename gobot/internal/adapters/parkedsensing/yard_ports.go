package parkedsensing

// yard_ports.go is the sensing engine's one outbound seam for reading a
// SHIPYARD: what a counter sells, and — when a hull of ours happens to be
// standing at it — what it charges.
//
// It adds no scanning logic of its own. Fetching the shipyard, unioning the
// availability list with the priced listings, persisting the row set and firing
// the once-per-era heavy-yard milestone are all the fleet's existing shipyard
// scanner's job, and duplicating any of it here would give the sensing engine a
// second, divergent definition of what a yard reading records — the same
// argument that keeps ScanRunnerPort a thin wrapper over the market scanner.
//
// What this file contributes is the ATTRIBUTION, and that is why one type is
// constructed twice. The two callers spend from different halves of the API
// budget and must be billed accordingly (see the constructors).

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Compile-time proof both wirings still satisfy the port the engine drives them
// through.
var _ appSensing.YardCatalogReader = (*YardReadPort)(nil)

// shipyardScanAPI is the single verb the sensing engine needs from the fleet's
// shipyard scanner. Narrowed to one method so this adapter can be exercised
// without an API client and cannot grow a second responsibility by accident.
// *ship.ShipyardScanner satisfies it.
//
// PRESENCE IS NOT A PRECONDITION of the call. ScanAndSaveShipyard reads the
// waypoint's SHIPYARD trait from the local cache first (so a non-shipyard is a
// free no-op), honours its own re-read window, and persists whatever the API
// returns — the availability list always, the prices only when a hull is there.
// That is exactly the shape both callers below need, and neither of them checks
// for a hull.
type shipyardScanAPI interface {
	ScanAndSaveShipyard(ctx context.Context, playerID uint, waypointSymbol string) error
}

// YardReadPort reads one shipyard and persists what it found.
type YardReadPort struct {
	scanner shipyardScanAPI
	tokens  playerTokenReader
	// tag bills the call to the right half of the API budget. See the constructors.
	tag func(context.Context) context.Context
}

// NewYardCatalogPort wires the FREE catalogue sweep's reader — the one that reads
// yards no hull has ever visited.
//
// CHARTING-tagged, like the screen's remote market fetch beside it and for the
// identical reason: it is discovery work that reveals a charted waypoint's
// contents without sending a ship, it is bounded per tick, and the budget
// arithmetic already concedes charting's measured rate out of the pacer's share.
func NewYardCatalogPort(scanner shipyardScanAPI, tokens playerTokenReader) *YardReadPort {
	return &YardReadPort{scanner: scanner, tokens: tokens, tag: sensingCtx}
}

// NewYardScanPort wires the SCAN ROTATION's reader — the yard read a parked probe
// takes on its own turn, which is what finally prices the yards we occupy.
//
// SCANNING-tagged, matching the market scan it rides. The pacer's rate is the
// residual left after every other source has taken its share, so billing a scan's
// companion read to charting would count it twice: once as non-sensing spend that
// shrinks the residual, and again as the sensing spend that residual funds. The
// budget would chase its own tail downward.
func NewYardScanPort(scanner shipyardScanAPI, tokens playerTokenReader) *YardReadPort {
	return &YardReadPort{scanner: scanner, tokens: tokens, tag: scanCtx}
}

// ReadCatalog reads the shipyard at waypoint and persists what it sells.
//
// The token is resolved from the player store and injected explicitly rather than
// assumed to be on the context, mirroring RemoteMarketPort. Both callers reach
// this from a coordinator tick, and one of them from a pacer goroutine several
// hops removed from the mediator middleware that would otherwise have supplied
// it — so the port carries its own credentials rather than depending on where it
// was called from.
func (p *YardReadPort) ReadCatalog(ctx context.Context, playerID int, waypoint string) error {
	token, err := playerToken(ctx, p.tokens, playerID)
	if err != nil {
		return err
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	return p.scanner.ScanAndSaveShipyard(common.WithPlayerToken(p.tag(ctx), token), uint(pid.Value()), waypoint)
}
