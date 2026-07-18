package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// negotiateStubShipRepo embeds the domain interface so only the methods the
// handler exercises need concrete implementations; any unexpected call panics
// on a nil-method deref.
type negotiateStubShipRepo struct {
	navigation.ShipRepository

	cachedShip     *navigation.Ship // stale daemon-cache state (FindBySymbol before sync)
	serverShip     *navigation.Ship // server-true state (FindBySymbol after pool sync)
	dockCalled     int
	syncAllCalled  int    // fleet-wide reconcile (SyncAllFromAPI)
	poolSynced     bool   // once the pool syncs, the DB is the source of truth
	onServerDocked func() // invoked when Dock succeeds, so the API stub can flip state
}

func (s *negotiateStubShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	// After a fleet-wide sync the DB is authoritative, so a reload returns the
	// server-true ship; before it, the stale daemon cache.
	if s.poolSynced {
		return s.serverShip, nil
	}
	return s.cachedShip, nil
}

func (s *negotiateStubShipRepo) SyncAllFromAPI(_ context.Context, _ shared.PlayerID) (int, error) {
	s.syncAllCalled++
	s.poolSynced = true
	return 1, nil
}

func (s *negotiateStubShipRepo) Dock(_ context.Context, _ *navigation.Ship, _ shared.PlayerID) error {
	s.dockCalled++
	if s.onServerDocked != nil {
		s.onServerDocked()
	}
	return nil
}

type negotiateStubAPIClient struct {
	domainPorts.APIClient

	serverDocked   bool // server-side nav state; negotiate rejects until docked
	negotiateCalls int
}

func (c *negotiateStubAPIClient) NegotiateContract(_ context.Context, _ string, _ string) (*domainPorts.ContractNegotiationResult, error) {
	c.negotiateCalls++
	if !c.serverDocked {
		// The adapter swallows game error 4214 ("ship must be docked") into a
		// nil-Contract result with err == nil.
		return &domainPorts.ContractNegotiationResult{ErrorCode: 4214}, nil
	}
	return &domainPorts.ContractNegotiationResult{
		Contract: &domainPorts.ContractData{
			ID:            "contract-1",
			FactionSymbol: "COSMIC",
			Type:          "PROCUREMENT",
			Terms: domainPorts.ContractTermsData{
				Deliveries: []domainPorts.DeliveryData{
					{TradeSymbol: "ALUMINUM", DestinationSymbol: "X1-TEST-A1", UnitsRequired: 80},
				},
			},
		},
	}, nil
}

type negotiateStubContractRepo struct {
	added []*contract.Contract
}

func (r *negotiateStubContractRepo) FindByID(_ context.Context, _ string) (*contract.Contract, error) {
	return nil, nil
}

func (r *negotiateStubContractRepo) FindActiveContracts(_ context.Context, _ int) ([]*contract.Contract, error) {
	return nil, nil
}

func (r *negotiateStubContractRepo) Add(_ context.Context, c *contract.Contract) error {
	r.added = append(r.added, c)
	return nil
}

func newNegotiateTestShip(t *testing.T, status navigation.NavStatus) *navigation.Ship {
	t.Helper()
	location, err := shared.NewWaypoint("X1-TEST-A1", 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	fuel, err := shared.NewFuel(0, 0)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	cargo, err := shared.NewCargo(80, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	ship, err := navigation.NewShip(
		"TORWIND-3",
		shared.MustNewPlayerID(1),
		location,
		fuel,
		0,
		80,
		cargo,
		9,
		"FRAME_HAULER",
		"HAULER",
		nil,
		status,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}
	return ship
}

// Reproduces the 2026-07-05 coordinator deadlock: the server rejects negotiate
// with 4214 (ship not docked, actually IN_ORBIT), but the daemon cache says
// DOCKED. A negotiate error naming ship state is a cache-desync signature, and
// desyncs strike fleet-wide — so the dock-retry path must reconcile the WHOLE
// pool from the server (one GET /my/ships) before docking. Trusting the stale
// cache makes EnsureDocked a no-op, the retry is byte-identical to the first
// attempt, and negotiation fails forever with "API returned nil result or
// contract" (cluster lesson L61).
func TestNegotiateContract_NotDockedError_ReconcilesWholePoolAndDocksForReal(t *testing.T) {
	shipRepo := &negotiateStubShipRepo{
		cachedShip: newNegotiateTestShip(t, navigation.NavStatusDocked),  // stale cache
		serverShip: newNegotiateTestShip(t, navigation.NavStatusInOrbit), // server truth
	}
	apiClient := &negotiateStubAPIClient{serverDocked: false}
	shipRepo.onServerDocked = func() { apiClient.serverDocked = true }
	contractRepo := &negotiateStubContractRepo{}

	handler := NewNegotiateContractHandler(contractRepo, shipRepo, nil, apiClient)

	ctx := auth.WithPlayerToken(context.Background(), "test-token")
	resp, err := handler.Handle(ctx, &NegotiateContractCommand{
		ShipSymbol: "TORWIND-3",
		PlayerID:   shared.MustNewPlayerID(1),
	})
	if err != nil {
		t.Fatalf("expected negotiation to self-heal after dock-retry, got: %v", err)
	}

	if shipRepo.syncAllCalled != 1 {
		t.Fatalf("expected exactly one fleet-wide pool refresh on the negotiate desync signal (SyncAllFromAPI), got %d", shipRepo.syncAllCalled)
	}
	if shipRepo.dockCalled != 1 {
		t.Fatalf("expected exactly one real Dock API call, got %d", shipRepo.dockCalled)
	}
	if apiClient.negotiateCalls != 2 {
		t.Fatalf("expected negotiate retry after docking, got %d calls", apiClient.negotiateCalls)
	}

	negotiateResp, ok := resp.(*NegotiateContractResponse)
	if !ok {
		t.Fatalf("unexpected response type: %T", resp)
	}
	if !negotiateResp.WasNegotiated || negotiateResp.Contract == nil {
		t.Fatalf("expected a freshly negotiated contract, got %+v", negotiateResp)
	}
	if len(contractRepo.added) != 1 {
		t.Fatalf("expected negotiated contract to be saved, got %d", len(contractRepo.added))
	}
}
