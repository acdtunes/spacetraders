package grpc

// THE GROWTH COORDINATOR MUST BE REACHABLE BY A REAL CALLER, not merely present as a method. It is
// the fleet's only heavy buyer AND the container the heavy cap is declared against, so one nothing
// starts is not a dormant feature — it is a fleet that can never buy a heavy and warns every tick.
// Both losses are silent: the RPC service embeds the generated Unimplemented stub, so deleting the
// handler still COMPILES and answers "unimplemented" at runtime; and the bootstrap hand-off is the
// only launch an unattended fleet performs. Each test therefore drives a PRODUCTION CALLER and
// asserts the container was persisted — asserting the method exists passes in exactly those states.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// The CLI's launch path: `workflow fleet-growth` → the daemon client → this RPC. It must reach the
// coordinator launch, not the embedded Unimplemented stub.
func TestFleetGrowthLaunch_TheRPCStartsTheCoordinator(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	// The launched runner blocks on the test mediator; a cancelable context lets it exit cleanly.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent := "REC-AGENT"
	resp, err := (&daemonServiceImpl{daemon: s}).FleetGrowthCoordinator(ctx, &pb.FleetGrowthCoordinatorRequest{
		PlayerId:    int32(playerID),
		AgentSymbol: &agent,
	})

	require.NoError(t, err, "the RPC must reach the coordinator launch — an unimplemented handler answers with an error here")
	require.NotEmpty(t, resp.ContainerId)
	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeFleetGrowth),
		"the RPC must persist exactly one growth coordinator")
}

// The unattended launch path, and the one that decides whether a deployed fleet ever buys a heavy
// without an operator typing a command: bootstrap's standing hand-off.
func TestFleetGrowthLaunch_TheBootstrapHandoffStartsTheCoordinator(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, (&bootstrapHandoffLauncher{server: s}).LaunchStandingCoordinators(ctx, playerID, "REC-AGENT"))

	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeFleetGrowth),
		"the bootstrap hand-off must launch the fleet's only heavy buyer — without it no deployment ever declares one")

	// The agent symbol must ride along, or the container re-adopts after a restart as a coordinator
	// for nobody.
	var model persistence.ContainerModel
	require.NoError(t, db.Where("player_id = ? AND container_type = ?", playerID,
		string(container.ContainerTypeFleetGrowth)).First(&model).Error)
	require.Contains(t, model.Config, "REC-AGENT")
}

// ONE HEAVY BUYER, WHICHEVER DOOR IS USED TWICE. Two growth coordinators would bid against each
// other over one treasury, and the heavy cap resolves off only ONE of them — so the loser withholds
// toward a ceiling the spender never consults. There are two doors: the RPC an operator drives with
// `workflow fleet-growth`, and the bootstrap hand-off, re-entered on every EXPANSION tick and after
// every restart. Each test below drives a REAL door twice and counts the persisted containers: a
// guard on one door only still lets the other open a second buyer, and asserting that a guard
// FUNCTION exists cannot tell the two apart.

func TestFleetGrowthLaunch_TheRPCNeverStartsASecondBuyer(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent := "REC-AGENT"
	svc := &daemonServiceImpl{daemon: s}
	req := func() *pb.FleetGrowthCoordinatorRequest {
		return &pb.FleetGrowthCoordinatorRequest{PlayerId: int32(playerID), AgentSymbol: &agent}
	}

	first, err := svc.FleetGrowthCoordinator(ctx, req())
	require.NoError(t, err)
	second, err := svc.FleetGrowthCoordinator(ctx, req())
	require.NoError(t, err, "a repeated launch is an ordinary operator action, not an error")

	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeFleetGrowth),
		"running `workflow fleet-growth` twice must not persist a second heavy buyer")
	require.Equal(t, first.ContainerId, second.ContainerId,
		"the repeat must report the LIVE coordinator's id — an empty or fresh id reads as a launch that happened")
}

func TestFleetGrowthLaunch_TheBootstrapHandoffNeverStartsASecondBuyer(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	launcher := &bootstrapHandoffLauncher{server: s}
	require.NoError(t, launcher.LaunchStandingCoordinators(ctx, playerID, "REC-AGENT"))
	require.NoError(t, launcher.LaunchStandingCoordinators(ctx, playerID, "REC-AGENT"))

	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeFleetGrowth),
		"a re-entered hand-off must not start a second heavy buyer")
}

// The cross-door case, and the one a per-call-site guard fails: the hand-off launches first (the
// unattended fleet's own path), then an operator runs the verb. This ORDER is the discriminating one
// — a guard living only in the hand-off sees nothing to stop on the RPC side.
func TestFleetGrowthLaunch_TheOperatorVerbNeverStartsARivalToTheHandoffsBuyer(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, (&bootstrapHandoffLauncher{server: s}).LaunchStandingCoordinators(ctx, playerID, "REC-AGENT"))
	// The premise, asserted rather than assumed: a hand-off that quietly stopped launching would
	// leave the RPC as the only launcher, and "exactly one buyer" would then be true for the wrong
	// reason — this test would keep passing over a fleet with no hand-off at all.
	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeFleetGrowth),
		"the hand-off must have started the buyer before the operator verb is tested against it")

	agent := "REC-AGENT"
	resp, err := (&daemonServiceImpl{daemon: s}).FleetGrowthCoordinator(ctx, &pb.FleetGrowthCoordinatorRequest{
		PlayerId: int32(playerID), AgentSymbol: &agent,
	})
	require.NoError(t, err)

	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeFleetGrowth),
		"the operator verb must adopt the hand-off's buyer, never start a rival bidding against it")

	var model persistence.ContainerModel
	require.NoError(t, db.Where("player_id = ? AND container_type = ?", playerID,
		string(container.ContainerTypeFleetGrowth)).First(&model).Error)
	require.Equal(t, model.ID, resp.ContainerId, "the verb must report the live buyer, not a phantom launch")
}

// The restart case: the buyer's row was persisted by a PREVIOUS daemon process and re-adopted by
// recovery, so neither door launched it in-process. The guard reads the fleet's state, not its own
// memory of the launch, so both doors must still refuse.
func TestFleetGrowthLaunch_ARecoveredBuyerIsNotRelaunched(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	insertRunningContainer(t, db, "growth-existing", "fleet_growth",
		string(container.ContainerTypeFleetGrowth), `{"container_id":"growth-existing"}`, playerID, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, (&bootstrapHandoffLauncher{server: s}).LaunchStandingCoordinators(ctx, playerID, "REC-AGENT"))

	agent := "REC-AGENT"
	resp, err := (&daemonServiceImpl{daemon: s}).FleetGrowthCoordinator(ctx, &pb.FleetGrowthCoordinatorRequest{
		PlayerId: int32(playerID), AgentSymbol: &agent,
	})
	require.NoError(t, err)

	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeFleetGrowth),
		"a recovered heavy buyer must not be joined by a second one")
	require.Equal(t, "growth-existing", resp.ContainerId)
}

// startingBuyerFlip promotes one container PENDING → RUNNING the first time the guard reads the
// containers table, reproducing on demand the interleaving that a launch produces naturally.
// GORM's after-query hook fires once the rows are already scanned, so the read that triggers the
// flip still returns what it saw — exactly like a real concurrent start committing a moment later.
type startingBuyerFlip struct {
	mu       sync.Mutex
	db       *gorm.DB
	targetID string
	done     bool
	err      error
}

func (f *startingBuyerFlip) afterQuery(tx *gorm.DB) {
	sql := tx.Statement.SQL.String()
	if !strings.Contains(sql, "containers") || !strings.Contains(sql, "status") {
		return
	}

	f.mu.Lock()
	if f.done {
		f.mu.Unlock()
		return
	}
	f.done = true
	f.mu.Unlock()

	err := f.db.Model(&persistence.ContainerModel{}).
		Where("id = ?", f.targetID).
		Update("status", string(container.ContainerStatusRunning)).Error

	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *startingBuyerFlip) result() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.done, f.err
}

// A BUYER THAT IS STARTING RIGHT NOW MUST STILL BE SEEN. This is the interleaving every launch
// produces: the row is written PENDING and promoted to RUNNING a moment later. Read as two queries
// — RUNNING, then PENDING — a row that crosses between them is in neither result: no longer
// PENDING when the second runs, not yet RUNNING when the first did. The guard then reports an
// empty fleet and starts a second heavy buyer against the one that was mid-start.
//
// The race suite found this by chance; the flip makes it happen every run, on the real RPC.
func TestFleetGrowthLaunch_TheGuardSeesABuyerThatIsStillStarting(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	now := time.Now()
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID:            "growth-starting",
		PlayerID:      playerID,
		ContainerType: string(container.ContainerTypeFleetGrowth),
		CommandType:   "fleet_growth",
		Status:        string(container.ContainerStatusPending),
		Config:        `{"container_id":"growth-starting"}`,
		StartedAt:     &now,
		HeartbeatAt:   &now,
	}).Error)

	flip := &startingBuyerFlip{db: db, targetID: "growth-starting"}
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("t4:starting_buyer_flip", flip.afterQuery))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent := "REC-AGENT"
	resp, err := (&daemonServiceImpl{daemon: s}).FleetGrowthCoordinator(ctx, &pb.FleetGrowthCoordinatorRequest{
		PlayerId: int32(playerID), AgentSymbol: &agent,
	})
	require.NoError(t, err)

	fired, flipErr := flip.result()
	require.NoError(t, flipErr)
	require.True(t, fired, "premise broken — the promotion never fired, so no interleaving was exercised")

	require.Equal(t, int64(1), countContainersOfType(t, db, playerID, container.ContainerTypeFleetGrowth),
		"a buyer caught mid-start must be found, not stepped over with a second one")
	require.Equal(t, "growth-starting", resp.ContainerId)
}
