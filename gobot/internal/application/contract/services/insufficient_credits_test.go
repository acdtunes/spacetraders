package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractQueries "github.com/andrescamacho/spacetraders-go/internal/application/contract/queries"
	playerQueries "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipTypes "github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// capturedLogEntry/capturingLogger mirror the fake used by
// run_manufacturing_task_worker_test.go: the container-log renderer prints
// only level+message and DROPS the metadata map, so a cause hidden in
// metadata never reaches an operator. Assertions below check the rendered
// MESSAGE TEXT, not the metadata, to prove the numbers actually surface.
type capturedLogEntry struct {
	level   string
	message string
}

type capturingLogger struct {
	entries []capturedLogEntry
}

func (l *capturingLogger) Log(level, message string, _ map[string]interface{}) {
	l.entries = append(l.entries, capturedLogEntry{level: level, message: message})
}

func (l *capturingLogger) warnings() []string {
	var out []string
	for _, e := range l.entries {
		if e.level == "WARNING" {
			out = append(out, e.message)
		}
	}
	return out
}

// insufficientCreditsFakeMediator drives DeliveryExecutor's purchase path
// (navigate -> dock -> purchase) plus the live-credits lookup used to
// enrich the park WARNING. Navigate/dock always succeed; purchaseErr and
// liveCredits are set per test to control the purchase outcome and the
// treasury snapshot returned to lookupLiveCredits.
type insufficientCreditsFakeMediator struct {
	common.Mediator

	navShip     *navigation.Ship
	purchaseErr error
	liveCredits int

	purchaseCalls int
}

func (m *insufficientCreditsFakeMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	switch request.(type) {
	case *shipNav.NavigateRouteCommand:
		return &shipNav.NavigateRouteResponse{Status: "completed", Ship: m.navShip}, nil

	case *shipTypes.DockShipCommand:
		return nil, nil

	case *shipCargo.PurchaseCargoCommand:
		m.purchaseCalls++
		if m.purchaseErr != nil {
			return nil, m.purchaseErr
		}
		return nil, nil

	case *playerQueries.GetPlayerQuery:
		return &playerQueries.GetPlayerResponse{Player: &player.Player{Credits: m.liveCredits}}, nil

	default:
		return nil, fmt.Errorf("unexpected mediator command in test: %T", request)
	}
}

// insufficientCreditsWireError reproduces the exact API error text a 4600
// "insufficient funds" purchase failure surfaces as, unmodified through
// every %w-wrapping layer between the API client and this package.
func insufficientCreditsWireError() error {
	return fmt.Errorf(`API error (status 400): {"error":{"message":"Purchase failed. Agent has insufficient funds.","code":4600}}`)
}

func TestIsInsufficientCreditsError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"4600 wire format", insufficientCreditsWireError(), true},
		{"wrapped 4600", fmt.Errorf("failed to purchase cargo: %w", insufficientCreditsWireError()), true},
		{"unrelated error", errors.New("server error (500)"), false},
		{"different code", fmt.Errorf(`API error (status 400): {"error":{"message":"x","code":4219}}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsInsufficientCreditsError(tc.err); got != tc.want {
				t.Errorf("IsInsufficientCreditsError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestProcessSingleDelivery_ParksOnInsufficientCredits(t *testing.T) {
	ship := buildShipWithIronOre(t, 0)
	shipRepo := &reconcileFakeShipRepo{cached: ship, server: ship}
	mediator := &insufficientCreditsFakeMediator{
		navShip:     ship,
		purchaseErr: insufficientCreditsWireError(),
		liveCredits: 85517,
	}
	cargoManager := NewCargoManager(mediator, shipRepo)
	executor := NewDeliveryExecutor(mediator, shipRepo, cargoManager)

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	delivery := domainContract.Delivery{
		TradeSymbol:       "IRON_ORE",
		DestinationSymbol: "X1-TEST-A1",
		UnitsRequired:     18,
		UnitsFulfilled:    0,
	}
	profitResult := &contractQueries.ProfitabilityResult{
		PurchaseCost:           100188,
		CheapestMarketWaypoint: "X1-TEST-M1",
	}
	result := &RunWorkflowResponse{}

	_, err := executor.ProcessSingleDelivery(ctx, "TORWIND-1", shared.MustNewPlayerID(1), nil, delivery, profitResult, result, nil)

	if err == nil {
		t.Fatalf("expected an error (park signal), got nil")
	}

	var insufficientErr *ErrInsufficientCredits
	if !errors.As(err, &insufficientErr) {
		t.Fatalf("expected *ErrInsufficientCredits, got %T: %v", err, err)
	}
	if insufficientErr.UnitsAttempted != 18 {
		t.Errorf("expected UnitsAttempted=18, got %d", insufficientErr.UnitsAttempted)
	}
	if insufficientErr.CreditsNeeded != 100188 {
		t.Errorf("expected CreditsNeeded=100188, got %d", insufficientErr.CreditsNeeded)
	}
	if insufficientErr.CreditsAvailable != 85517 {
		t.Errorf("expected CreditsAvailable=85517, got %d", insufficientErr.CreditsAvailable)
	}

	warnings := logger.warnings()
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one WARNING log entry, got %d: %v", len(warnings), warnings)
	}
	msg := warnings[0]
	for _, substr := range []string{"credits_needed=100188", "credits_available=85517", "action=parked", "reason=insufficient_credits"} {
		if !strings.Contains(msg, substr) {
			t.Errorf("expected WARNING message to contain %q (renderer drops metadata map, so numbers must be in the text), got: %s", substr, msg)
		}
	}
}

func TestProcessSingleDelivery_RecoversWhenCreditsSufficient(t *testing.T) {
	ship := buildShipWithIronOre(t, 0)
	shipRepo := &reconcileFakeShipRepo{cached: ship, server: ship}
	mediator := &insufficientCreditsFakeMediator{navShip: ship}
	cargoManager := NewCargoManager(mediator, shipRepo)
	executor := NewDeliveryExecutor(mediator, shipRepo, cargoManager)

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	delivery := domainContract.Delivery{
		TradeSymbol:       "IRON_ORE",
		DestinationSymbol: "X1-TEST-A1",
		UnitsRequired:     18,
		UnitsFulfilled:    0,
	}
	profitResult := &contractQueries.ProfitabilityResult{
		PurchaseCost:           100188,
		CheapestMarketWaypoint: "X1-TEST-M1",
	}
	result := &RunWorkflowResponse{}

	_, err := executor.ProcessSingleDelivery(ctx, "TORWIND-1", shared.MustNewPlayerID(1), nil, delivery, profitResult, result, nil)

	if err != nil {
		t.Fatalf("expected purchase to succeed once treasury is sufficient, got: %v", err)
	}
	if mediator.purchaseCalls != 1 {
		t.Errorf("expected exactly one purchase call, got %d", mediator.purchaseCalls)
	}
	if warnings := logger.warnings(); len(warnings) != 0 {
		t.Errorf("expected no WARNING log on successful recovery, got: %v", warnings)
	}
}

func TestProcessSingleDelivery_NonCreditsErrorStaysUnchanged(t *testing.T) {
	ship := buildShipWithIronOre(t, 0)
	shipRepo := &reconcileFakeShipRepo{cached: ship, server: ship}
	unrelatedErr := errors.New("server error (500): market unavailable")
	mediator := &insufficientCreditsFakeMediator{navShip: ship, purchaseErr: unrelatedErr}
	cargoManager := NewCargoManager(mediator, shipRepo)
	executor := NewDeliveryExecutor(mediator, shipRepo, cargoManager)

	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	delivery := domainContract.Delivery{
		TradeSymbol:       "IRON_ORE",
		DestinationSymbol: "X1-TEST-A1",
		UnitsRequired:     18,
		UnitsFulfilled:    0,
	}
	profitResult := &contractQueries.ProfitabilityResult{
		PurchaseCost:           100188,
		CheapestMarketWaypoint: "X1-TEST-M1",
	}
	result := &RunWorkflowResponse{}

	_, err := executor.ProcessSingleDelivery(ctx, "TORWIND-1", shared.MustNewPlayerID(1), nil, delivery, profitResult, result, nil)

	if err == nil {
		t.Fatalf("expected an error to propagate for a non-credits failure")
	}
	var insufficientErr *ErrInsufficientCredits
	if errors.As(err, &insufficientErr) {
		t.Fatalf("expected a plain (non-park) error for a non-4600 failure, got *ErrInsufficientCredits: %v", err)
	}
	if !strings.Contains(err.Error(), "server error (500)") {
		t.Errorf("expected the original error text to survive, got: %v", err)
	}
	if warnings := logger.warnings(); len(warnings) != 0 {
		t.Errorf("expected no park WARNING for a non-credits error, got: %v", warnings)
	}
}
