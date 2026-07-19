package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// ---- fakes -----------------------------------------------------------------

type fakeTransitionAPI struct {
	agent       *player.AgentData
	agentErr    error
	agentCalled bool
	agentToken  string
	status      *api.ServerStatus
	statusErr   error

	registerResult       *api.RegisterResult
	registerErr          error
	registerCalls        int
	registerAccountToken string
	registerAgent        string
	registerFaction      string
}

func (f *fakeTransitionAPI) GetAgent(ctx context.Context, token string) (*player.AgentData, error) {
	f.agentCalled = true
	f.agentToken = token
	return f.agent, f.agentErr
}

func (f *fakeTransitionAPI) GetServerStatus(ctx context.Context) (*api.ServerStatus, error) {
	return f.status, f.statusErr
}

func (f *fakeTransitionAPI) Register(ctx context.Context, accountToken, agentSymbol, faction string) (*api.RegisterResult, error) {
	f.registerCalls++
	f.registerAccountToken = accountToken
	f.registerAgent = agentSymbol
	f.registerFaction = faction
	return f.registerResult, f.registerErr
}

type fakeTransitionEraStore struct {
	openEra         *persistence.EraModel
	findErr         error
	transitionCalls int
	capturedPlayer  *persistence.PlayerModel
	capturedEra     *persistence.EraModel
	newPlayerID     int
	transitionErr   error
}

func (f *fakeTransitionEraStore) FindOpenEra(ctx context.Context) (*persistence.EraModel, error) {
	return f.openEra, f.findErr
}

func (f *fakeTransitionEraStore) TransitionEra(ctx context.Context, newPlayer *persistence.PlayerModel, newEra *persistence.EraModel) (*persistence.TransitionReport, error) {
	f.transitionCalls++
	f.capturedPlayer = newPlayer
	f.capturedEra = newEra
	if f.transitionErr != nil {
		return nil, f.transitionErr
	}
	id := f.newPlayerID
	if id == 0 {
		id = 42
	}
	newPlayer.ID = id
	newEra.PlayerID = id
	return &persistence.TransitionReport{ClosedEra: f.openEra, NewPlayerID: id, NewEra: newEra}, nil
}

type fakeDefaultSetter struct {
	called bool
	agent  string
	pid    int
}

func (f *fakeDefaultSetter) SetDefault(agentSymbol string, playerID int) error {
	f.called = true
	f.agent = agentSymbol
	f.pid = playerID
	return nil
}

type fakeCaptainCfg struct {
	called bool
	pid    int
}

func (f *fakeCaptainCfg) SetCaptainPlayerID(playerID int) (bool, string, error) {
	f.called = true
	f.pid = playerID
	return true, "/live/config.yaml", nil
}

// fakeFleet backs the lister, stopper, and reconciler off one in-memory container
// set so re-list passes converge as the drain stops/reconciles rows.
type fakeFleet struct {
	containers      []activeContainer
	notFound        map[string]bool  // IDs the daemon has no runtime handle for (orphans)
	alreadyTerminal map[string]bool  // IDs that raced to STOPPED/COMPLETED before the stop call landed
	genuineErr      map[string]error // IDs whose stop must fail with a real, non-orphan, non-terminal error
	stopOrder       []string
	reconciled      []string
}

func (f *fakeFleet) ListActiveContainers(ctx context.Context, playerID int) ([]activeContainer, error) {
	out := make([]activeContainer, 0, len(f.containers))
	for _, c := range f.containers {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeFleet) StopContainer(ctx context.Context, containerID string) error {
	f.stopOrder = append(f.stopOrder, containerID)
	if f.notFound[containerID] {
		return errors.New("rpc error: container not found")
	}
	if f.alreadyTerminal[containerID] {
		return errors.New("cannot stop container in STOPPED state")
	}
	if err, ok := f.genuineErr[containerID]; ok {
		return err
	}
	f.remove(containerID)
	return nil
}

func (f *fakeFleet) MarkStopped(ctx context.Context, containerID string, playerID int) error {
	f.reconciled = append(f.reconciled, containerID)
	f.remove(containerID)
	return nil
}

func (f *fakeFleet) remove(id string) {
	kept := f.containers[:0]
	for _, c := range f.containers {
		if c.ID != id {
			kept = append(kept, c)
		}
	}
	f.containers = kept
}

func happyDeps() (transitionDeps, *fakeTransitionAPI, *fakeTransitionEraStore, *fakeDefaultSetter, *fakeCaptainCfg, *fakeFleet) {
	priorReset := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	apiFake := &fakeTransitionAPI{
		agent:  &player.AgentData{Symbol: "TORWIND", Credits: 1000, StartingFaction: "COSMIC"},
		status: statusOn("2026-07-12"),
	}
	store := &fakeTransitionEraStore{
		openEra:     &persistence.EraModel{Name: "torwind-2026-07-05", AgentSymbol: "TORWIND", PlayerID: 2, UniverseResetDate: &priorReset},
		newPlayerID: 3,
	}
	def := &fakeDefaultSetter{}
	cap := &fakeCaptainCfg{}
	fleet := &fakeFleet{notFound: map[string]bool{}}
	deps := transitionDeps{api: apiFake, era: store, cliDefault: def, captainCfg: cap, lister: fleet, stopper: fleet, reconciler: fleet}
	return deps, apiFake, store, def, cap, fleet
}

// ---- tests -----------------------------------------------------------------

func TestTransition_InvalidTokenAbortsBeforeAnyWrite(t *testing.T) {
	deps, apiFake, store, def, cap, fleet := happyDeps()
	apiFake.agent = nil
	apiFake.agentErr = errors.New("401 invalid token (code 4104)")
	var out bytes.Buffer

	// --confirm set: proves validation gates BEFORE any destructive write.
	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", token: "corrupt", confirm: true}, &out)

	require.Error(t, err)
	require.True(t, apiFake.agentCalled)
	require.Zero(t, store.transitionCalls, "no era flip on an invalid token")
	require.False(t, def.called, "no CLI default repoint on an invalid token")
	require.False(t, cap.called, "no captain.player_id repoint on an invalid token")
	require.Empty(t, fleet.stopOrder, "no drain on an invalid token")
}

func TestTransition_TokenAgentMismatchAbortsBeforeAnyWrite(t *testing.T) {
	deps, apiFake, store, def, cap, _ := happyDeps()
	apiFake.agent = &player.AgentData{Symbol: "SOMEONE_ELSE"}
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", token: "jwt", confirm: true}, &out)

	require.Error(t, err)
	require.Zero(t, store.transitionCalls)
	require.False(t, def.called)
	require.False(t, cap.called)
}

func TestTransition_HappyPath_FlipsEraAndRepoints(t *testing.T) {
	deps, _, store, def, cap, _ := happyDeps()
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", token: "valid-jwt", confirm: true}, &out)
	require.NoError(t, err)

	// Era flipped via the non-truncating repository path with the validated token.
	require.Equal(t, 1, store.transitionCalls)
	require.Equal(t, "torwind-2026-07-12", store.capturedEra.Name)
	require.Equal(t, "valid-jwt", store.capturedPlayer.Token)
	require.NotNil(t, store.capturedEra.UniverseResetDate)
	require.Equal(t, "2026-07-12", store.capturedEra.UniverseResetDate.Format("2006-01-02"))

	// BOTH repoints land on the new player id (crit 3, closes sp-m602).
	require.True(t, def.called)
	require.Equal(t, "TORWIND", def.agent)
	require.Equal(t, 3, def.pid)
	require.True(t, cap.called)
	require.Equal(t, 3, cap.pid)

	// The one-and-only token must never be echoed to stdout.
	require.NotContains(t, out.String(), "valid-jwt")
}

func TestTransition_DrainStopsCoordinatorsBeforeWorkers(t *testing.T) {
	deps, _, _, _, _, fleet := happyDeps()
	fleet.containers = []activeContainer{
		{ID: "trade-worker-1", ContainerType: "TRADE_WORKER", CommandType: "trade_route", Status: "RUNNING"},
		{ID: "contract-fleet-coordinator-1", ContainerType: "CONTRACT_FLEET_COORDINATOR", CommandType: "contract-fleet-coordinator", Status: "RUNNING"},
		{ID: "factory-worker-1", ContainerType: "MANUFACTURING_TASK", CommandType: "manufacturing", Status: "RUNNING"},
		{ID: "goods-coordinator-1", ContainerType: "GOODS_FACTORY_COORDINATOR", CommandType: "goods_factory_coordinator", Status: "PENDING"},
	}
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", token: "valid-jwt", confirm: true}, &out)
	require.NoError(t, err)

	// Both coordinators are stopped before either worker.
	idx := map[string]int{}
	for i, id := range fleet.stopOrder {
		idx[id] = i
	}
	require.Less(t, idx["contract-fleet-coordinator-1"], idx["trade-worker-1"])
	require.Less(t, idx["contract-fleet-coordinator-1"], idx["factory-worker-1"])
	require.Less(t, idx["goods-coordinator-1"], idx["trade-worker-1"])
	require.Less(t, idx["goods-coordinator-1"], idx["factory-worker-1"])
	require.Len(t, fleet.stopOrder, 4)
}

func TestTransition_OrphanRowReconciledToStopped(t *testing.T) {
	deps, _, _, _, _, fleet := happyDeps()
	fleet.containers = []activeContainer{
		{ID: "live-worker", ContainerType: "TRADE_WORKER", Status: "RUNNING"},
		{ID: "trade-route-orphan", ContainerType: "TRADE_WORKER", Status: "PENDING"},
	}
	fleet.notFound = map[string]bool{"trade-route-orphan": true}
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", token: "valid-jwt", confirm: true}, &out)
	require.NoError(t, err)

	// The daemon-unknown orphan row is reconciled to STOPPED in the DB; the live one is not.
	require.Equal(t, []string{"trade-route-orphan"}, fleet.reconciled)
	require.Contains(t, out.String(), "orphan")
}

func TestTransition_NoCacheTruncation(t *testing.T) {
	deps, _, store, _, _, _ := happyDeps()
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", token: "valid-jwt", confirm: true}, &out)
	require.NoError(t, err)

	// The flip is routed exclusively through TransitionEra, which (per the
	// persistence TestTransitionEra...WithoutTruncatingCaches) never truncates
	// market_data / system_graphs. The command exposes no truncating close/scrub path.
	require.Equal(t, 1, store.transitionCalls)
}

func TestTransition_Idempotent_SecondRunNoop(t *testing.T) {
	deps, _, store, def, cap, fleet := happyDeps()
	// Open era already at the server reset date -> already in sync.
	inSync := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	store.openEra = &persistence.EraModel{Name: "torwind-2026-07-12", AgentSymbol: "TORWIND", PlayerID: 3, UniverseResetDate: &inSync}
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", token: "valid-jwt", confirm: true}, &out)
	require.NoError(t, err)

	require.Zero(t, store.transitionCalls, "already-current era must be a no-op")
	require.False(t, def.called)
	require.False(t, cap.called)
	require.Empty(t, fleet.stopOrder)
	require.Contains(t, out.String(), "in sync")
}

func TestTransition_DryRunNoMutation(t *testing.T) {
	deps, _, store, def, cap, fleet := happyDeps()
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", token: "valid-jwt", dryRun: true, confirm: true}, &out)
	require.NoError(t, err)

	// --dry-run wins even with --confirm: preview only, zero mutation.
	require.Zero(t, store.transitionCalls)
	require.False(t, def.called)
	require.False(t, cap.called)
	require.Empty(t, fleet.stopOrder)
	require.Contains(t, out.String(), "torwind-2026-07-12") // plan previewed
}

func TestTransition_NoConfirmPreviewsNoMutation(t *testing.T) {
	deps, _, store, def, cap, _ := happyDeps()
	var out bytes.Buffer

	// No --confirm and no --dry-run: fail-closed preview, no destructive ops.
	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", token: "valid-jwt"}, &out)
	require.NoError(t, err)

	require.Zero(t, store.transitionCalls)
	require.False(t, def.called)
	require.False(t, cap.called)
	require.Contains(t, out.String(), "--confirm")
}

// (a) No --token + ST_ACCOUNT_TOKEN present + apply: the new era's JWT is minted via
// the API, and it — not the empty --token — is what gets validated and persisted.
func TestTransition_MintsJWTFromAccountTokenOnApply(t *testing.T) {
	deps, apiFake, store, def, cap, _ := happyDeps()
	apiFake.registerResult = &api.RegisterResult{Token: "minted-jwt", AgentSymbol: "TORWIND"}
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", accountToken: "acct-tok", confirm: true}, &out)
	require.NoError(t, err)

	// Registered exactly once with (accountToken, agent, faction="") — the same
	// empty-faction default the `player register --new` path uses.
	require.Equal(t, 1, apiFake.registerCalls)
	require.Equal(t, "acct-tok", apiFake.registerAccountToken)
	require.Equal(t, "TORWIND", apiFake.registerAgent)
	require.Equal(t, "", apiFake.registerFaction)

	// The minted token is the working token: it is what GetAgent re-validates and
	// what lands in the new player row.
	require.Equal(t, "minted-jwt", apiFake.agentToken)
	require.Equal(t, "minted-jwt", store.capturedPlayer.Token)

	// The flip and both repoints proceed on the minted token.
	require.Equal(t, 1, store.transitionCalls)
	require.True(t, def.called)
	require.True(t, cap.called)

	// The one-and-only minted token must never be echoed to stdout.
	require.NotContains(t, out.String(), "minted-jwt")
}

// (b) An explicit --token wins over ST_ACCOUNT_TOKEN and must not trigger a mint —
// byte-identical to the pre-existing token path.
func TestTransition_ExplicitTokenSkipsMint(t *testing.T) {
	deps, apiFake, store, _, _, _ := happyDeps()
	apiFake.registerResult = &api.RegisterResult{Token: "should-not-be-used", AgentSymbol: "TORWIND"}
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", token: "explicit-jwt", accountToken: "acct-tok", confirm: true}, &out)
	require.NoError(t, err)

	require.Zero(t, apiFake.registerCalls, "explicit --token must not trigger a mint")
	require.Equal(t, "explicit-jwt", apiFake.agentToken)
	require.Equal(t, "explicit-jwt", store.capturedPlayer.Token)
	require.Equal(t, 1, store.transitionCalls)
}

// (c) Neither --token nor ST_ACCOUNT_TOKEN: fail closed with a clear error, touching
// no external service and writing nothing (RULINGS #4 fail-closed principle).
func TestTransition_NoTokenAndNoAccountTokenFailsClosed(t *testing.T) {
	deps, apiFake, store, def, cap, fleet := happyDeps()
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", confirm: true}, &out)

	require.Error(t, err)
	require.Contains(t, err.Error(), "ST_ACCOUNT_TOKEN")
	require.Zero(t, apiFake.registerCalls, "no registration without a token source")
	require.False(t, apiFake.agentCalled, "no API calls at all on the fail-closed path")
	require.Zero(t, store.transitionCalls)
	require.False(t, def.called)
	require.False(t, cap.called)
	require.Empty(t, fleet.stopOrder)
}

// (d) A preview (dry-run OR the absence of --confirm) must NOT register an agent —
// minting consumes an irreversible account slot — yet must tell the operator the
// mint will happen on --confirm.
func TestTransition_PreviewDoesNotMint(t *testing.T) {
	cases := []struct {
		name string
		opts transitionOpts
	}{
		{"dry-run", transitionOpts{agent: "TORWIND", accountToken: "acct-tok", dryRun: true, confirm: true}},
		{"no-confirm", transitionOpts{agent: "TORWIND", accountToken: "acct-tok"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, apiFake, store, def, cap, fleet := happyDeps()
			apiFake.registerResult = &api.RegisterResult{Token: "minted-jwt", AgentSymbol: "TORWIND"}
			var out bytes.Buffer

			err := runUniverseTransition(context.Background(), deps, tc.opts, &out)
			require.NoError(t, err)

			require.Zero(t, apiFake.registerCalls, "preview must not register an agent")
			require.Zero(t, store.transitionCalls)
			require.False(t, def.called)
			require.False(t, cap.called)
			require.Empty(t, fleet.stopOrder)

			require.Contains(t, out.String(), "ST_ACCOUNT_TOKEN")
			require.Contains(t, out.String(), "TORWIND")
		})
	}
}

// (e) If the minted token validates to a different agent than --agent, the existing
// GetAgent symbol guard must still refuse the flip (belt-and-suspenders).
func TestTransition_MintedSymbolMismatchRefused(t *testing.T) {
	deps, apiFake, store, def, cap, _ := happyDeps()
	apiFake.registerResult = &api.RegisterResult{Token: "minted-jwt", AgentSymbol: "TORWIND"}
	apiFake.agent = &player.AgentData{Symbol: "SOMEONE_ELSE"}
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", accountToken: "acct-tok", confirm: true}, &out)

	require.Error(t, err)
	require.Equal(t, 1, apiFake.registerCalls, "mint precedes validation")
	require.Zero(t, store.transitionCalls, "no era flip when the minted symbol mismatches")
	require.False(t, def.called)
	require.False(t, cap.called)
}

func TestTransition_DrainAlreadyTerminalMidList_ContinuesPastIt(t *testing.T) {
	deps, _, _, _, _, fleet := happyDeps()
	fleet.containers = []activeContainer{
		{ID: "contract-fleet-coordinator-1", ContainerType: "CONTRACT_FLEET_COORDINATOR", CommandType: "coordinator", Status: "RUNNING"},
		{ID: "scout-tour-8b", ContainerType: "SCOUT_TOUR", CommandType: "scout_tour", Status: "RUNNING"},
		{ID: "trade-worker-1", ContainerType: "TRADE_WORKER", CommandType: "trade_route", Status: "RUNNING"},
	}
	fleet.alreadyTerminal = map[string]bool{"scout-tour-8b": true}
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", token: "valid-jwt", confirm: true}, &out)

	require.NoError(t, err, "an already-terminal container mid-list must not abort the drain")
	require.Contains(t, fleet.stopOrder, "trade-worker-1", "drain must continue past the already-terminal container to reach the rest")
	require.Contains(t, out.String(), "2 stopped, 0 orphan row(s) reconciled to STOPPED, 1 already terminal")
}

func TestTransition_DrainAllAlreadyTerminal_ConvergesIdempotently(t *testing.T) {
	deps, _, _, _, _, fleet := happyDeps()
	fleet.containers = []activeContainer{
		{ID: "contract-fleet-coordinator-1", ContainerType: "CONTRACT_FLEET_COORDINATOR", CommandType: "coordinator", Status: "RUNNING"},
		{ID: "trade-worker-1", ContainerType: "TRADE_WORKER", CommandType: "trade_route", Status: "PENDING"},
	}
	fleet.alreadyTerminal = map[string]bool{"contract-fleet-coordinator-1": true, "trade-worker-1": true}
	var out bytes.Buffer

	// Simulates re-running the drain over a prior era that is already fully terminal.
	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", token: "valid-jwt", confirm: true}, &out)

	require.NoError(t, err, "re-draining an all-terminal prior era must succeed, not abort")
	require.Contains(t, out.String(), "0 stopped, 0 orphan row(s) reconciled to STOPPED, 2 already terminal")
}

func TestTransition_DrainGenuineStopError_StillAborts(t *testing.T) {
	deps, _, _, _, _, fleet := happyDeps()
	fleet.containers = []activeContainer{
		{ID: "trade-worker-1", ContainerType: "TRADE_WORKER", CommandType: "trade_route", Status: "RUNNING"},
	}
	fleet.genuineErr = map[string]error{"trade-worker-1": errors.New("rpc error: connection refused")}
	var out bytes.Buffer

	err := runUniverseTransition(context.Background(), deps, transitionOpts{agent: "TORWIND", token: "valid-jwt", confirm: true}, &out)

	require.Error(t, err, "a genuine unexpected stop error must still abort the drain")
	require.Contains(t, err.Error(), "connection refused")
}
