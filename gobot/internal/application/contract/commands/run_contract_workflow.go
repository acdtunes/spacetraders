package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
)

// Type aliases for convenience
type RunWorkflowCommand = contractTypes.RunWorkflowCommand
type RunWorkflowResponse = contractTypes.RunWorkflowResponse

// RunWorkflowHandler implements the complete contract workflow
// following the exact Python implementation pattern:
//
// 1. Check for existing active contracts (idempotency)
// 2. Negotiate new contract or resume existing (handle error 4511)
// 3. Evaluate profitability (log only, always accept)
// 4. Accept contract (skip if already accepted)
// 5. For each delivery:
//   - Reload ship state
//   - Jettison wrong cargo if needed
//   - Calculate purchase needs
//   - Execute multi-trip loop if units > cargo capacity
//   - For each trip:
//   - Navigate to seller
//   - Dock
//   - Purchase with transaction splitting (handled by PurchaseCargoHandler)
//   - Navigate to delivery
//   - Dock
//   - Deliver cargo
//
// 6. Fulfill contract
// 7. Calculate profit
// 8. Transfer ship back to coordinator (if applicable)
// 9. Signal completion via channel (if applicable)
type RunWorkflowHandler struct {
	lifecycleService *contractServices.ContractLifecycleService
	deliveryExecutor *contractServices.DeliveryExecutor
	// clock paces the continuous loop (sp-ehg9). Only consulted in loop mode;
	// the single-shot path is unaffected. Injectable so tests advance it
	// instantly (shared.MockClock).
	clock shared.Clock
}

// RunWorkflowOption configures optional collaborators on the contract workflow
// handler (and the delivery executor it builds) without breaking the positional
// constructor the existing tests use.
type RunWorkflowOption func(*runWorkflowConfig)

type runWorkflowConfig struct {
	deliveryOpts []contractServices.DeliveryExecutorOption
}

// WithInventorySourcing enables inventory-first contract sourcing (sp-dchv Lane
// D) on the delivery executor: a stocked good is withdrawn from an in-system
// warehouse at zero ask before any market buy. A nil finder is a no-op
// (market-only), so callers may forward optional wiring unconditionally.
func WithInventorySourcing(finder appContract.InventorySourceFinder, coordinator storage.StorageCoordinator, apiClient domainPorts.APIClient) RunWorkflowOption {
	return func(c *runWorkflowConfig) {
		c.deliveryOpts = append(c.deliveryOpts, contractServices.WithInventorySource(finder, coordinator, apiClient))
	}
}

// WithWithdrawalRecording wires the warehouse-withdrawal event recorder (sp-kqxe)
// onto the delivery executor: each successful warehouse→hauler buffer draw emits a
// structured event (good, units, waypoint, hauler, contract id, timestamp) so
// downstream analysis can measure warehouse ROI. A nil recorder is a no-op and a
// nil clock defaults to RealClock, so callers may forward the wiring unconditionally.
func WithWithdrawalRecording(recorder storage.WithdrawalRecorder, clock shared.Clock) RunWorkflowOption {
	return func(c *runWorkflowConfig) {
		c.deliveryOpts = append(c.deliveryOpts, contractServices.WithWithdrawalRecorder(recorder, clock))
	}
}

// NewRunWorkflowHandler creates a new contract workflow handler
func NewRunWorkflowHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	contractRepo domainContract.ContractRepository,
	clock shared.Clock,
	opts ...RunWorkflowOption,
) *RunWorkflowHandler {
	var cfg runWorkflowConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	cargoManager := contractServices.NewCargoManager(mediator, shipRepo)
	lifecycleService := contractServices.NewContractLifecycleService(mediator, contractRepo)
	deliveryExecutor := contractServices.NewDeliveryExecutor(mediator, shipRepo, cargoManager, cfg.deliveryOpts...)

	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &RunWorkflowHandler{
		lifecycleService: lifecycleService,
		deliveryExecutor: deliveryExecutor,
		clock:            clock,
	}
}

// Handle executes the contract workflow command
func (h *RunWorkflowHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunWorkflowCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// Continuous single-hull contract loop (sp-ehg9): re-negotiate + run the
	// next contract after each fulfillment, until the container is stopped. The
	// single-shot path below is left byte-identical for Loop=false.
	if cmd.Loop {
		return h.runContractLoop(ctx, cmd)
	}

	result := &RunWorkflowResponse{
		Negotiated:  false,
		Accepted:    false,
		Fulfilled:   false,
		TotalProfit: 0,
		TotalTrips:  0,
		Error:       "",
	}

	// Execute workflow
	if err := h.executeWorkflow(ctx, cmd, result); err != nil {
		// PARK, don't crash (sp-vwhi): insufficient-credits during purchase
		// is a clean recoverable exit, not a container crash. A nil Go
		// error here means the container runner does NOT count this as a
		// failure/restart - the dynamic-discovery fleet coordinator simply
		// re-picks-up this ship's unfulfilled contract on its next pass,
		// once the treasury recovers. Every other executeWorkflow error
		// keeps the existing crash-and-restart behavior unchanged.
		var insufficientErr *contractServices.ErrInsufficientCredits
		if errors.As(err, &insufficientErr) {
			result.Error = insufficientErr.Error()
			return result, nil
		}

		result.Error = err.Error()
		return result, err
	}

	// NOTE: With dynamic discovery, ships are NOT transferred back to coordinator
	// They are released by ContainerRunner and discovered dynamically in the next iteration
	// The ContainerRunner releases ship assignments on completion/failure
	// Completion is signaled via event bus (WorkerCompletedEvent published by ContainerRunner)

	return result, nil
}

// executeWorkflow handles the contract workflow execution
func (h *RunWorkflowHandler) executeWorkflow(
	ctx context.Context,
	cmd *RunWorkflowCommand,
	result *RunWorkflowResponse,
) error {
	contract, wasNegotiated, err := h.lifecycleService.FindOrNegotiateContract(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return err
	}

	if wasNegotiated {
		result.Negotiated = true
	}

	profitabilityResp, err := h.lifecycleService.EvaluateContractProfitability(ctx, cmd.ShipSymbol, cmd.PlayerID, contract)
	if err != nil {
		// Non-fatal - logged in method
	}

	var wasAccepted bool
	contract, wasAccepted, err = h.lifecycleService.AcceptContractIfNeeded(ctx, contract, cmd.PlayerID)
	if err != nil {
		return err
	}

	if wasAccepted {
		result.Accepted = true
	}

	contract, err = h.deliveryExecutor.ProcessAllDeliveries(ctx, cmd.ShipSymbol, cmd.PlayerID, contract, profitabilityResp, result, cmd.ContainerID)
	if err != nil {
		return err
	}

	// VERIFY before fulfill (sp-2ei3): the delivery leg sources+delivers every
	// unit it can and re-reads registration from each deliver response, but it
	// returns an honestly-partial contract when sourcing halts (ladder cap) or
	// the remainder can't be sourced this pass. Fulfilling that partial state is
	// the exact "deliveries not complete" crash that livelocked the chain
	// (worker crash -> coordinator re-cycle -> same partial state -> crash).
	// Park instead: a clean nil-error exit that leaves the accepted contract for
	// the coordinator to re-project and finish next pass. Never a skip
	// (RULING #1) — the contract stays accepted and owed.
	if !contract.CanFulfill() {
		msg := fmt.Sprintf("Contract %s deliveries incomplete after sourcing pass; parking for coordinator re-projection (never-skip stands)", contract.ContractID())
		common.LoggerFromContext(ctx).Log("WARNING", msg, map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "park_incomplete_deliveries",
			"contract_id": contract.ContractID(),
		})
		result.Error = msg
		return nil
	}

	if err := h.lifecycleService.FulfillContract(ctx, contract, cmd.PlayerID); err != nil {
		return err
	}

	result.Fulfilled = true

	result.TotalProfit += h.lifecycleService.CalculateTotalProfit(contract)

	// Claim this ship's NEXT contract immediately, at whatever waypoint the
	// last delivery already left it docked at - no deadhead trip back to
	// base first. Before this, a fulfilled ship had no path to claim its own
	// next contract: it released back to the fleet coordinator and waited to
	// be rediscovered, which measured fleet-wide as 74 ship-hours/day of idle
	// time between fulfillment and next acceptance (sp-qpmi). This is a
	// latency optimization on top of an already-successful fulfillment, so
	// failure here is non-fatal and never turns this result into an error -
	// it just falls back to the coordinator's normal discovery pass.
	h.negotiateNextContractBestEffort(ctx, cmd)

	return nil
}

// negotiateNextContractBestEffort reuses the same idempotent lifecycle calls
// FindOrNegotiateContract makes for a fresh worker (FindActiveContracts
// first, so it never re-negotiates a contract another path already claimed)
// to negotiate and accept this ship's next contract right after fulfillment.
// Neither negotiate nor accept require any particular ship location - only
// DOCKED state for negotiate, which already holds because DeliverCargo always
// navigates-and-docks the ship at the delivery waypoint first. Any failure is
// logged and swallowed: the coordinator's normal discovery pass remains the
// fallback path, so a transient error here cannot regress contract success
// rate.
func (h *RunWorkflowHandler) negotiateNextContractBestEffort(ctx context.Context, cmd *RunWorkflowCommand) {
	logger := common.LoggerFromContext(ctx)

	nextContract, wasNegotiated, err := h.lifecycleService.FindOrNegotiateContract(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", "Best-effort next-contract negotiation failed; falling back to coordinator discovery", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "negotiate_next_contract",
			"error":       err.Error(),
		})
		return
	}

	if _, _, err := h.lifecycleService.AcceptContractIfNeeded(ctx, nextContract, cmd.PlayerID); err != nil {
		logger.Log("WARNING", "Best-effort next-contract acceptance failed; falling back to coordinator discovery", map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"action":      "accept_next_contract",
			"contract_id": nextContract.ContractID(),
			"error":       err.Error(),
		})
		return
	}

	logger.Log("INFO", "Claimed next contract immediately after fulfillment, without returning to base", map[string]interface{}{
		"ship_symbol":    cmd.ShipSymbol,
		"action":         "negotiate_on_delivery",
		"contract_id":    nextContract.ContractID(),
		"was_negotiated": wasNegotiated,
	})
}

// ============================================================================
// Continuous single-hull contract loop (sp-ehg9)
// ============================================================================

const (
	// contractLoopSettle is the pause after a fulfilled contract before the loop
	// starts the next one. The real per-contract work (source → deliver →
	// fulfill) dominates wall time; this only keeps a degenerate/instant cycle
	// from hot-spinning.
	contractLoopSettle = 5 * time.Second

	// contractLoopBackoff is the pause after a money-guard park (insufficient
	// credits, sp-vwhi) or a transient per-contract error before the loop retries
	// — so an insolvent or stalled frigate re-checks periodically instead of
	// hammering the API. Mirrors the fleet coordinator's park-then-rediscover
	// cadence.
	contractLoopBackoff = 60 * time.Second

	// contractLoopStopChunk bounds stop latency: the paced wait is taken in
	// chunks so a container stop (ctx cancel) at the first-hauler pivot is
	// honoured within one chunk instead of the full interval. Instant under a
	// MockClock (tests).
	contractLoopStopChunk = time.Second
)

// runContractLoop runs contracts continuously on this one hull until the
// container is stopped (sp-ehg9). It wraps the SAME single-contract cycle the
// single-shot path runs (executeWorkflow), so every money guard, the
// one-active-contract idempotence (FindOrNegotiateContract finds the active
// contract before negotiating a new one), and the container runner's ship claim
// are inherited unchanged — the loop only adds "do it again, paced, until
// stopped". Exposed via `workflow batch-contract --loop <ship>`; the bootstrap
// INCOME phase starts it for the command frigate and stops it (container stop)
// at the first-hauler pivot.
func (h *RunWorkflowHandler) runContractLoop(ctx context.Context, cmd *RunWorkflowCommand) (common.Response, error) {
	return h.runContractLoopWithCycle(ctx, cmd, func(c context.Context) (*RunWorkflowResponse, error) {
		result := &RunWorkflowResponse{}
		err := h.executeWorkflow(c, cmd, result)
		return result, err
	})
}

// runContractLoopWithCycle is the loop core, decoupled from the delivery
// pipeline via the cycle seam so the orchestration (repeat, pace,
// park-not-crash, clean ctx-stop) is unit-testable. It NEVER returns on a
// per-cycle failure — a money-guard park or a transient error is logged, backed
// off, and retried, exactly as the fleet coordinator keeps working the contract
// through worker deaths (RULINGS #1). It returns ONLY when the container is
// stopped (ctx cancelled), surfacing the graceful ctx error the runner treats as
// a clean stop.
func (h *RunWorkflowHandler) runContractLoopWithCycle(
	ctx context.Context,
	cmd *RunWorkflowCommand,
	cycle func(context.Context) (*RunWorkflowResponse, error),
) (common.Response, error) {
	logger := common.LoggerFromContext(ctx)
	var last *RunWorkflowResponse

	for {
		if err := ctx.Err(); err != nil {
			return coalesceLoopResponse(last), err
		}

		result, err := cycle(ctx)
		if result != nil {
			last = result
		}

		wait := contractLoopSettle
		if err != nil {
			wait = contractLoopBackoff
			var insufficient *contractServices.ErrInsufficientCredits
			if errors.As(err, &insufficient) {
				// Money guard fired: the frigate cannot afford this contract's
				// goods. Park (don't spend, don't crash) and retry after a backoff
				// once the treasury recovers.
				logger.Log("WARNING", "Contract loop parked on insufficient credits; backing off before retry", map[string]interface{}{
					"ship_symbol": cmd.ShipSymbol,
					"action":      "contract_loop_park",
					"error":       err.Error(),
				})
			} else {
				logger.Log("WARNING", "Contract loop cycle failed; backing off before retry", map[string]interface{}{
					"ship_symbol": cmd.ShipSymbol,
					"action":      "contract_loop_retry",
					"error":       err.Error(),
				})
			}
		}

		if stopped := h.sleepWithContext(ctx, wait); stopped {
			return coalesceLoopResponse(last), ctx.Err()
		}
	}
}

// sleepWithContext paces the loop via the injected clock in stop-responsive
// chunks: it returns true the moment the container is stopped (ctx cancelled) so
// the pivot handoff is prompt, instead of blocking out the whole interval.
// Instant under a MockClock (tests advance without wall-waiting).
func (h *RunWorkflowHandler) sleepWithContext(ctx context.Context, d time.Duration) (stopped bool) {
	for remaining := d; remaining > 0; remaining -= contractLoopStopChunk {
		if ctx.Err() != nil {
			return true
		}
		step := contractLoopStopChunk
		if remaining < step {
			step = remaining
		}
		h.clock.Sleep(step)
	}
	return ctx.Err() != nil
}

// coalesceLoopResponse returns the last cycle's response, or an empty one if the
// loop was stopped before any contract ran, so the loop always returns a
// non-nil common.Response.
func coalesceLoopResponse(last *RunWorkflowResponse) common.Response {
	if last != nil {
		return last
	}
	return &RunWorkflowResponse{}
}
