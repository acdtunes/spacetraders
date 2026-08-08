package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// The fleet autosizer is DELETED. These pin the two halves of the retirement that outlive the code:
// a persisted row cannot come back, and no launch surface can make a new one.

// RULINGS #2. A still-RUNNING fleet_autosizer row persisted by a pre-cut daemon must end TERMINATED
// on the first post-retirement boot — never rebuilt into a ghost buyer, and never reported as a
// spurious recovery_failed loss, because the loud container.lost signal has to stay clean for real
// losses. The row carries a heavy_cap in its launch config, which is the shape that matters: this is
// exactly the leftover the cap resolution must also decline to read (see the heavy-buyer cap port).
func TestRecovery_RetiredFleetAutosizerFailsClosedWithoutLossAlarm(t *testing.T) {
	rec := &syncRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	s, db, playerID := newRecoveryTestServer(t)
	insertRunningContainer(t, db, "legacy-autosizer-1", "fleet_autosizer",
		string(container.ContainerTypeFleetAutosizer),
		`{"container_id":"legacy-autosizer-1","autosizer_heavy_cap":5}`, playerID, nil)

	require.NoError(t, s.RecoverRunningContainers(context.Background()))

	requireContainerState(t, db, "legacy-autosizer-1", "FAILED", "retired_command_type")
	require.Nil(t, s.registeredRunner("legacy-autosizer-1"),
		"a retired autosizer must never come back as a live runner — it would bid against fleet growth over one treasury")
	require.Empty(t, rec.lost(), "a deliberate retirement is not a loss — no container.lost alarm")
}

// PROOF THE SKIP IS THE MECHANISM, not an accident of the row's shape: the type is declared retired,
// and the registry has no builder for it. Either one alone would leave the other free to drift — a
// builder re-added without the retirement entry resurrects the coordinator, and a retirement entry
// without the missing builder is a claim nothing enforces.
func TestFleetAutosizer_IsRetiredAndUnbuildable(t *testing.T) {
	require.True(t, retiredCommandTypes["fleet_autosizer"],
		"the deleted autosizer must be declared retired, or its persisted rows alarm as unexplained losses")

	s := &DaemonServer{containerSpecs: make(map[string]ContainerSpec)}
	s.registerContainerSpecs()
	_, err := s.buildCommandForType("fleet_autosizer", map[string]interface{}{"container_id": "x"}, 1, "x")
	require.Error(t, err, "the registry must have no builder for the deleted autosizer")
	require.Contains(t, err.Error(), "unknown command type")
}
