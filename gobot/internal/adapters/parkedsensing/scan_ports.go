package parkedsensing

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// scan_ports.go is the scan pacer's one outbound seam: the adapter that turns
// "scan this waypoint" into a market read the rest of the fleet has already
// paid for the machinery of.
//
// It is separate from the placement adapters in ship_ports.go because it is
// tagged differently, and that difference is the whole point of the file — see
// scanCtx.

// Compile-time proof the runner still satisfies the port the pacer drives it
// through.
var _ appSensing.MarketScanRunner = (*ScanRunnerPort)(nil)

// marketScanAPI is the single verb the pacer needs from the fleet's market
// scanner. Narrowed to one method so the runner can be exercised without an API
// client, and so this adapter cannot grow a second responsibility by accident.
// *ship.MarketScanner satisfies it.
// It is the OUTCOME-reporting variant deliberately. The plain ScanAndSaveMarket
// returns nil both when it wrote market data and when the fleet's scan budget
// declined the request, and the pacer keeps a freshness ledger off this return —
// so the narrow error-only form is precisely what made 78.5% of the sensing
// ledger's stamps false (sp-zml2u).
type marketScanAPI interface {
	ScanAndSaveMarketWithOutcome(ctx context.Context, playerID uint, waypointSymbol string) (bool, error)
}

// ScanRunnerPort issues one parked-probe market scan.
//
// It adds no scanning logic of its own — fetching the market, upserting the
// prices and recording the price history are all the existing scanner's job,
// and duplicating any of it here would give parked probes a second, divergent
// definition of what a scan writes. What this type contributes is the
// attribution the budget arithmetic depends on.
type ScanRunnerPort struct{ scanner marketScanAPI }

// NewScanRunnerPort wires the parked-probe scan.
func NewScanRunnerPort(scanner marketScanAPI) *ScanRunnerPort {
	return &ScanRunnerPort{scanner: scanner}
}

// Run scans one waypoint and persists what it found, reporting whether it wrote
// anything: scanned is false when the fleet's market-scan budget declined the
// request, which is a success for the caller but not an event any freshness
// ledger may record.
func (p *ScanRunnerPort) Run(ctx context.Context, playerID int, waypoint string) (bool, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return false, err
	}
	return p.scanner.ScanAndSaveMarketWithOutcome(scanCtx(ctx), uint(pid.Value()), waypoint)
}

// scanCtx tags a parked-probe scan on both of the API client's axes, and is the
// only place in the sensing engine that sets either one to these values.
//
// SourceScanning is the accounting half. The pacer's rate is computed as the
// residual left under the utilization target after every OTHER source has taken
// its bounded share, so a scan billed to any other source — or to nothing —
// would be double-counted: once as non-sensing spend that shrinks the residual,
// and again as the sensing spend that residual is meant to fund. The budget
// would then chase its own tail downward.
//
// PriorityLow is the scheduling half, and sensing is the one class of call that
// takes it. Priority never changes the rate, only who gets a CONTENDED token
// first, and a market price that arrives a second late costs nothing while a
// trade-critical call delayed behind it can cost a filled order. Scanning has
// unbounded appetite and no deadline, so it yields to everyone; the scheduler's
// bounded aging is what keeps yielding from becoming starvation.
func scanCtx(ctx context.Context) context.Context {
	return api.WithPriority(api.WithSource(ctx, apibudget.SourceScanning), api.PriorityLow)
}
