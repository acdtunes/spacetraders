package parkedsensing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// DB-BACKED tests for the cap read. The ladder tests in heavy_reserve_port_test.go drive a fake
// that takes (value, present, containerExists) as GIVENS — so they structurally cannot express the
// only question that matters here: WHICH PERSISTED KEY produces which rung. That gap is exactly how
// the bare-key-only bug shipped looking well-tested.
func capPortDB(t *testing.T) (*HeavyBuyerCapPort, *gorm.DB, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	p := &persistence.PlayerModel{AgentSymbol: "ORION", Token: "tok"}
	require.NoError(t, db.Create(p).Error)
	return NewHeavyBuyerCapPort(db), db, p.ID
}

// addHeavyBuyer seeds a container of the DECLARED heavy-buyer type — the coordinator that actually
// spends, and therefore the one whose cap the reservation must resolve off.
func addHeavyBuyer(t *testing.T, db *gorm.DB, playerID int, id, status, config string) {
	t.Helper()
	addContainer(t, db, playerID, id, string(container.ContainerTypeFleetGrowth), status, config)
}

func addContainer(t *testing.T, db *gorm.DB, playerID int, id, containerType, status, config string) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID:            id,
		PlayerID:      playerID,
		ContainerType: containerType,
		Status:        status,
		Config:        config,
	}).Error)
}

// otherCoordinator is any live coordinator that is NOT a declared heavy buyer. The tests below use
// it to express the one thing the declared-type lookup cannot: heavy buying having changed hands.
const otherCoordinator = "SOME_OTHER_COORDINATOR"

// THE RULING, PINNED AT THE PERSISTENCE LAYER. A stored cap of 0 is a HOLD — "own no heavies" —
// and must read as PRESENT with value 0, never as an absence deferring to the default. Reading it
// as absent is what would leave the buyer refusing every heavy while sensing reserved for one
// forever: permanent expansion starvation from the conservative action.
//
// This is a read contract, not a claim that a live tune can produce the row: `tune <key> 0` deletes
// the key. It is pinned here because the port is the shared reader every future writer inherits,
// and present-vs-absent is the distinction a writer cannot restore once the port has lost it.
func TestHeavyBuyerCapPort_PrefixedZeroIsAPresentHold(t *testing.T) {
	port, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "c1", string(container.ContainerStatusRunning), `{"growth_heavy_cap": 0}`)

	value, present, exists, err := port.HeavyCap(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, exists)
	require.True(t, present, "an explicit 0 is a HOLD, not an absence — reading it as absent starves expansion forever")
	require.Equal(t, 0, value)
}

// The operator's config.yaml value lands on the PREFIXED key. Reading only the bare key (which
// only `tune` ever writes) would miss it entirely.
func TestHeavyBuyerCapPort_PrefixedValueIsRead(t *testing.T) {
	port, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "c1", string(container.ContainerStatusRunning), `{"growth_heavy_cap": 2}`)

	value, present, exists, err := port.HeavyCap(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, exists)
	require.True(t, present)
	require.Equal(t, 2, value, "the operator's config.yaml cap must be read, not the compiled default")
}

// The bare key is the live-tuned override.
func TestHeavyBuyerCapPort_BareKeyIsRead(t *testing.T) {
	port, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "c1", string(container.ContainerStatusRunning), `{"heavy_cap": 3}`)

	value, present, _, err := port.HeavyCap(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, 3, value)
}

// Both set ⇒ the live tune wins, mirroring liveHeavyCap's precedence over the launch value.
func TestHeavyBuyerCapPort_BareKeyWinsOverPrefixed(t *testing.T) {
	port, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "c1", string(container.ContainerStatusRunning), `{"growth_heavy_cap": 2, "heavy_cap": 7}`)

	value, present, _, err := port.HeavyCap(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, 7, value, "a live tune must override the launch value, as it does for the coordinator")
}

// A bare 0 is the tune REVERT (the key is deleted on `tune X 0`), so it must not mask a real
// prefixed hold — it falls through to the prefixed key rather than reading as a live 0.
func TestHeavyBuyerCapPort_BareZeroFallsThroughToPrefixed(t *testing.T) {
	port, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "c1", string(container.ContainerStatusRunning), `{"heavy_cap": 0, "growth_heavy_cap": 4}`)

	value, present, _, err := port.HeavyCap(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, 4, value)
}

// Neither key set ⇒ present=false so the caller applies the same compiled default the coordinator
// resolves to — the two sides cannot then disagree about a cap.
func TestHeavyBuyerCapPort_NeitherKeySetIsAbsent(t *testing.T) {
	port, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "c1", string(container.ContainerStatusRunning), `{"growth_tick_secs": 900}`)

	_, present, exists, err := port.HeavyCap(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, exists, "the declared heavy buyer exists even though the knob is unset")
	require.False(t, present)
}

// A negative cap is a typo, not a hold: it defers to the default, matching resolveHeavyCap.
func TestHeavyBuyerCapPort_NegativeDefersToTheDefault(t *testing.T) {
	port, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "c1", string(container.ContainerStatusRunning), `{"growth_heavy_cap": -3}`)

	_, present, exists, err := port.HeavyCap(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, exists)
	require.False(t, present, "a negative must not read as an intentional hold")
}

// No declared buyer and no knob-carrying stranger ⇒ no heavy buyer ⇒ nothing to save for.
func TestHeavyBuyerCapPort_NoContainerReportsNotExists(t *testing.T) {
	port, _, pid := capPortDB(t)

	_, _, exists, err := port.HeavyCap(context.Background(), pid)
	require.NoError(t, err)
	require.False(t, exists)
}

// A TERMINATED heavy buyer buys nothing, so it must not count — holding treasury for it would
// starve expansion for a purchase that cannot happen.
func TestHeavyBuyerCapPort_TerminatedContainerDoesNotCount(t *testing.T) {
	port, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "c1", "TERMINATED", `{"growth_heavy_cap": 9}`)

	_, _, exists, err := port.HeavyCap(context.Background(), pid)
	require.NoError(t, err)
	require.False(t, exists)
}

// During a restart a PENDING replacement sits beside the RUNNING heavy buyer. The winner must be
// DETERMINISTIC (RUNNING first) or the cap flaps between ticks and the reserve with it.
func TestHeavyBuyerCapPort_RunningWinsOverPendingDeterministically(t *testing.T) {
	port, db, pid := capPortDB(t)
	// The RUNNING container sorts LAST by id and is inserted LAST, so neither id order nor
	// insertion order can be what makes it win — only its status can. With ids that happened to
	// agree with status order, dropping the CASE and keeping Order("id ASC") left this green.
	addHeavyBuyer(t, db, pid, "a1-pending", string(container.ContainerStatusPending), `{"growth_heavy_cap": 9}`)
	addHeavyBuyer(t, db, pid, "z1-running", string(container.ContainerStatusRunning), `{"growth_heavy_cap": 2}`)

	for i := 0; i < 5; i++ {
		value, present, _, err := port.HeavyCap(context.Background(), pid)
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, 2, value, "the RUNNING heavy buyer must win every read, not an arbitrary one")
	}
}

// A container carrying NO config must not shadow the authoritative one's knob.
func TestHeavyBuyerCapPort_EmptyConfigContainerDoesNotShadow(t *testing.T) {
	port, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "z1-running", string(container.ContainerStatusRunning), `{"growth_heavy_cap": 2}`)
	addHeavyBuyer(t, db, pid, "a2-pending", string(container.ContainerStatusPending), ``)

	value, present, _, err := port.HeavyCap(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, 2, value)
}

// END TO END, and the one that pins the ruling as a BEHAVIOUR: with the documented hold set in
// config.yaml, sensing must reserve EXACTLY 0 — a known priced heavy yard and zero heavies owned
// would otherwise reserve forever against a heavy the buyer will never buy.
func TestHeavyReservePort_ConfiguredHoldReservesExactlyZero(t *testing.T) {
	capPort, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "c1", string(container.ContainerStatusRunning), `{"growth_heavy_cap": 0}`)

	port := NewHeavyReservePort(
		&fakeCensus{owned: 0},
		&fakeYardPricer{price: 1_565_500, found: true},
		capPort,
	)

	got, _, err := port.Reserve(context.Background(), pid)
	require.NoError(t, err)
	require.Equal(t, common.HeavyReserveTarget(0), got, "heavy_cap: 0 is a HOLD — sensing must reserve nothing, or expansion starves permanently for a heavy that will never be bought")
}

// THE TRAP HEAVY BUYING CHANGES HANDS INTO. A live coordinator carries the cap knob, but no
// container of a declared heavy-buyer type exists. Resolving that to "no heavy buyer" zeroes the
// reservation while a real buyer runs — and the rung that answers it is deliberately silent, so
// nothing in any gauge or heartbeat would say so.
func TestHeavyBuyerCapPort_CapKnobOutsideTheDeclaredTypesStillResolves(t *testing.T) {
	port, db, pid := capPortDB(t)
	addContainer(t, db, pid, "g1", otherCoordinator, string(container.ContainerStatusRunning), `{"heavy_cap": 3}`)

	value, present, exists, err := port.HeavyCap(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, exists, "a live container carrying the heavy cap knob IS a heavy buyer — reading it as none saves nothing toward its purchases")
	require.True(t, present)
	require.Equal(t, 3, value, "the cap must follow the knob when the declared owner is gone")
}

// The prefixed LAUNCH key travels with its owner: the prefix belongs to whichever coordinator was
// launched, so the ladder matches on the shared suffix rather than on one coordinator's name.
func TestHeavyBuyerCapPort_LaunchKeyOfAnotherOwnerIsRead(t *testing.T) {
	port, db, pid := capPortDB(t)
	addContainer(t, db, pid, "g1", otherCoordinator, string(container.ContainerStatusRunning), `{"someother_heavy_cap": 0}`)

	value, present, exists, err := port.HeavyCap(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, exists)
	require.True(t, present, "an explicit 0 is the operator's HOLD whoever owns the knob")
	require.Equal(t, 0, value)
}

// The other half of the same rule: a deployment that buys no heavies at all must stay QUIET. Live
// coordinators exist, none of them owns heavy buying, and probe-only is an expected configuration
// rather than a fault.
func TestHeavyBuyerCapPort_NoHeavyBuyerAnywhereReportsNotExists(t *testing.T) {
	port, db, pid := capPortDB(t)
	addContainer(t, db, pid, "s1", otherCoordinator, string(container.ContainerStatusRunning), `{"growth_tick_secs": 900}`)

	_, _, exists, err := port.HeavyCap(context.Background(), pid)
	require.NoError(t, err)
	require.False(t, exists, "no declared owner and no cap knob anywhere ⇒ genuinely no heavy buyer")
}

// Determinism survives on the knob path too: a PENDING replacement beside a RUNNING owner must not
// make the cap flap between ticks.
func TestHeavyBuyerCapPort_KnobPathPrefersRunningDeterministically(t *testing.T) {
	port, db, pid := capPortDB(t)
	// The RUNNING container sorts LAST by id and is inserted LAST, so only its status can be what
	// makes it win.
	addContainer(t, db, pid, "a1-pending", otherCoordinator, string(container.ContainerStatusPending), `{"heavy_cap": 9}`)
	addContainer(t, db, pid, "z1-running", otherCoordinator, string(container.ContainerStatusRunning), `{"heavy_cap": 2}`)

	for i := 0; i < 5; i++ {
		value, present, _, err := port.HeavyCap(context.Background(), pid)
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, 2, value, "the RUNNING owner must win every read, not an arbitrary one")
	}
}

// A DECLARED owner outranks a stranger carrying the knob: the declaration is the authority while it
// is live, so an operator's tune landing on the wrong container cannot capture the cap.
func TestHeavyBuyerCapPort_DeclaredOwnerOutranksAKnobbedStranger(t *testing.T) {
	port, db, pid := capPortDB(t)
	addContainer(t, db, pid, "a1-other", otherCoordinator, string(container.ContainerStatusRunning), `{"heavy_cap": 9}`)
	addHeavyBuyer(t, db, pid, "z1-declared", string(container.ContainerStatusRunning), `{"growth_heavy_cap": 2}`)

	value, present, _, err := port.HeavyCap(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, 2, value)
}

// END TO END, and the loud half of the rule: the reservation must be REAL and the resolution must
// ANNOUNCE that the cap came from a container no declaration claims.
func TestHeavyReservePort_UndeclaredHeavyBuyerReservesAndWarns(t *testing.T) {
	capPort, db, pid := capPortDB(t)
	addContainer(t, db, pid, "g1", otherCoordinator, string(container.ContainerStatusRunning), `{"heavy_cap": 3}`)

	port := NewHeavyReservePort(
		&fakeCensus{owned: 0},
		&fakeYardPricer{price: 1_565_500, found: true},
		capPort,
	)
	log := &warnLogger{}
	got, _, err := port.Reserve(logging.WithLogger(context.Background(), log), pid)

	require.NoError(t, err)
	require.NotEqual(t, common.HeavyReserveTarget(0), got, "a running heavy buyer must have treasury saved toward it")
	require.NotEmpty(t, log.lines, "a cap resolved off an undeclared owner must be LOUD — it is the only signal that the declaration has gone stale")
}

// The quiet half, end to end: nothing owns heavy buying, so nothing is reserved and nothing is
// said. Every probe-only deployment sits here on every tick.
func TestHeavyReservePort_NoHeavyBuyerAnywhereIsSilent(t *testing.T) {
	capPort, db, pid := capPortDB(t)
	addContainer(t, db, pid, "s1", otherCoordinator, string(container.ContainerStatusRunning), `{"growth_tick_secs": 900}`)

	port := NewHeavyReservePort(
		&fakeCensus{owned: 0},
		&fakeYardPricer{price: 1_565_500, found: true},
		capPort,
	)
	log := &warnLogger{}
	got, _, err := port.Reserve(logging.WithLogger(context.Background(), log), pid)

	require.NoError(t, err)
	require.Equal(t, common.HeavyReserveTarget(0), got)
	require.Empty(t, log.lines, "no heavy buyer is an expected configuration, not a fault — warning here spams every probe-only deployment")
}

// The mirror of the above: an operator cap of 2 with 2 owned is at cap, so the reserve drops —
// the same class of bug without needing zero.
func TestHeavyReservePort_ConfiguredCapAtOwnedReservesZero(t *testing.T) {
	capPort, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "c1", string(container.ContainerStatusRunning), `{"growth_heavy_cap": 2}`)

	port := NewHeavyReservePort(
		&fakeCensus{owned: 2},
		&fakeYardPricer{price: 1_565_500, found: true},
		capPort,
	)

	got, _, err := port.Reserve(context.Background(), pid)
	require.NoError(t, err)
	require.Equal(t, common.HeavyReserveTarget(0), got, "at the operator's cap the buyer refuses, so sensing must not reserve")
}

// A LEFTOVER coordinator still carrying heavy_cap must not capture the cap while the declared owner
// is live. Heavy buying changing hands leaves the previous owner's launch config in place for a
// while, and resolving the cap off it would make the withholder save toward a ceiling the spender
// never consults. The scan passes it as a knob-carrying stranger and keeps going, so no
// undeclared-buyer WARN fires either.
func TestHeavyBuyerCapPort_LeftoverAutosizerDoesNotCaptureTheCap(t *testing.T) {
	port, db, pid := capPortDB(t)
	addContainer(t, db, pid, "a1", string(container.ContainerTypeFleetAutosizer), string(container.ContainerStatusRunning), `{"autosizer_heavy_cap": 5}`)
	addHeavyBuyer(t, db, pid, "g1", string(container.ContainerStatusRunning), `{"growth_heavy_cap": 9}`)

	log := &warnLogger{}
	value, present, exists, err := port.HeavyCap(logging.WithLogger(context.Background(), log), pid)

	require.NoError(t, err)
	require.True(t, exists)
	require.True(t, present)
	require.Equal(t, 9, value, "the DECLARED owner's cap must win over a leftover container's knob")
	require.Empty(t, log.lines, "a leftover knob beside a live declared owner is expected, not a fault")
}

// --- the master switch, off the SAME row as the cap ---------------------------

// growth_enabled OFF must reach the drain. Otherwise the drain evaluates a wave for a buyer that
// has stopped reading and stopped buying, and pauses probe buying forever — a deadlock no spender
// can clear.
func TestHeavyBuyerCapPort_GrowthDisabledIsReadable(t *testing.T) {
	port, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "c1", string(container.ContainerStatusRunning), `{"growth_enabled": 2}`)

	enabled, exists, err := port.GrowthEnabled(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, exists)
	require.False(t, enabled, "a switched-off buyer must be visible to the drain, or probe buying pauses toward a purchase nothing can make")
}

// Absent key is the documented default: ON. `tune <key> 0` DELETES the key, so absence IS the
// revert, and a coordinator nobody has tuned must not read as switched off.
func TestHeavyBuyerCapPort_AbsentSwitchIsOn(t *testing.T) {
	port, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "c1", string(container.ContainerStatusRunning), `{"heavy_cap": 5}`)

	enabled, exists, err := port.GrowthEnabled(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, exists)
	require.True(t, enabled, "an untuned coordinator must read as ON")
}

// A revert lands as the DELETED key, but a 0 written by any other means must read the same way: as
// the default, never as a third state.
func TestHeavyBuyerCapPort_ZeroSwitchIsOn(t *testing.T) {
	port, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "c1", string(container.ContainerStatusRunning), `{"growth_enabled": 0}`)

	enabled, exists, err := port.GrowthEnabled(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, exists)
	require.True(t, enabled, "0 is the revert value, which is the documented default ON")
}

// An empty config is an untuned live buyer: ON, and existing.
func TestHeavyBuyerCapPort_EmptyConfigBuyerIsOn(t *testing.T) {
	port, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "c1", string(container.ContainerStatusRunning), ``)

	enabled, exists, err := port.GrowthEnabled(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, exists)
	require.True(t, enabled)
}

// NO HEAVY BUYER AT ALL yields "no buyer" — a probe-only deployment. The drain turns that into the
// PROBE wave: there is nothing to save for.
func TestHeavyBuyerCapPort_NoBuyerIsNotEnabledAndNotExists(t *testing.T) {
	port, _, pid := capPortDB(t)

	enabled, exists, err := port.GrowthEnabled(context.Background(), pid)
	require.NoError(t, err)
	require.False(t, exists)
	require.False(t, enabled)
}

// A TERMINATED buyer spends nothing, so it is not a buyer — the same status filter the cap read
// applies, and it must not be possible for the two to disagree about which containers count.
func TestHeavyBuyerCapPort_TerminatedBuyerIsNotABuyerForTheSwitch(t *testing.T) {
	port, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "c1", "TERMINATED", `{"growth_enabled": 1}`)

	enabled, exists, err := port.GrowthEnabled(context.Background(), pid)
	require.NoError(t, err)
	require.False(t, exists)
	require.False(t, enabled)
}

// THE SWITCH AND THE CAP MUST COME OFF THE SAME CONTAINER. Reading them from two containers is the
// split this port was built to close, one knob later: the leftover coordinator here carries BOTH
// keys with the opposite values, so a second query that happened to select differently would be
// visible in either answer.
func TestHeavyBuyerCapPort_SwitchAndCapComeFromOneContainer(t *testing.T) {
	port, db, pid := capPortDB(t)
	addContainer(t, db, pid, "a1", otherCoordinator, string(container.ContainerStatusRunning), `{"heavy_cap": 5, "growth_enabled": 2}`)
	addHeavyBuyer(t, db, pid, "g1", string(container.ContainerStatusRunning), `{"heavy_cap": 9, "growth_enabled": 1}`)

	value, _, _, err := port.HeavyCap(context.Background(), pid)
	require.NoError(t, err)
	enabled, exists, err := port.GrowthEnabled(context.Background(), pid)
	require.NoError(t, err)

	require.True(t, exists)
	require.Equal(t, 9, value, "the cap must resolve off the declared owner")
	require.True(t, enabled, "the switch must resolve off the SAME declared owner, not off the stranger carrying the knob")
}

// THE SWITCH READ IS SILENT on the undeclared-buyer rung. Both public reads resolve the same row on
// the same tick, so warning in both would double a line whose whole value is that it is rare — and
// an operator learning to scroll past it is how the real one goes unnoticed.
func TestHeavyBuyerCapPort_SwitchDoesNotRepeatTheUndeclaredBuyerWarn(t *testing.T) {
	port, db, pid := capPortDB(t)
	addContainer(t, db, pid, "a1", otherCoordinator, string(container.ContainerStatusRunning), `{"heavy_cap": 5, "growth_enabled": 2}`)

	log := &warnLogger{}
	enabled, exists, err := port.GrowthEnabled(logging.WithLogger(context.Background(), log), pid)

	require.NoError(t, err)
	require.True(t, exists, "a knob-carrying stranger still EXISTS for the switch, so the reserve read behind it runs and the WARN below still fires")
	require.False(t, enabled)
	require.Empty(t, log.lines, "the cap read beside this one carries the undeclared-buyer WARN; two copies per tick is noise")

	// The pair: the cap read on the SAME state DOES warn, so the silence above is a scoping
	// decision rather than a warning that was lost.
	capLog := &warnLogger{}
	_, _, _, err = port.HeavyCap(logging.WithLogger(context.Background(), capLog), pid)
	require.NoError(t, err)
	require.NotEmpty(t, capLog.lines, "the undeclared-buyer WARN must still fire somewhere, or heavy buying can change hands silently")
}

// THE SHAPE THE ABSENT-KEY DEFAULT MUST NOT BE APPLIED TO: a live container carrying a cap knob and
// NO switch key, of a type no declaration claims. A coordinator that has stopped buying heavies
// leaves its launch keys behind for as long as its row lives, so this is what a leftover looks like
// — and reading its absent key as the documented ON is a heavy buyer conjured out of a number in a
// config blob, whose HEAVY wave nothing can ever clear.
//
// It still EXISTS, which is the other half: the reserve read behind the switch is what fires the
// undeclared-buyer WARN, so a genuinely stale declaration stays loud instead of going silent here.
func TestHeavyBuyerCapPort_AbsentSwitchOnAStrangerIsNotOn(t *testing.T) {
	port, db, pid := capPortDB(t)
	addContainer(t, db, pid, "a1", string(container.ContainerTypeFleetAutosizer), string(container.ContainerStatusRunning), `{"autosizer_heavy_cap": 5}`)

	enabled, exists, err := port.GrowthEnabled(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, exists, "the stranger must still exist, or the cap read that carries the undeclared-buyer WARN never runs")
	require.False(t, enabled, "a container no declaration claims cannot vouch for a switch it never had — reading absence as ON pauses probe buying for a buyer that does not exist")
}

// The CALIBRATION for the test above, and the line between the two rules: the SAME absent key on a
// DECLARED owner is still the documented ON. Without this pair the fix reads as "absence is off",
// which would switch heavy buying off for every untuned coordinator.
func TestHeavyBuyerCapPort_AbsentSwitchOnTheDeclaredOwnerIsStillOn(t *testing.T) {
	port, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "g1", string(container.ContainerStatusRunning), `{"growth_heavy_cap": 5}`)

	enabled, exists, err := port.GrowthEnabled(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, exists)
	require.True(t, enabled, "the declared owner's untuned switch is ON; absence IS the revert")
}

// An EXPLICIT on-value does not promote a stranger either. The switch's question is "can a heavy
// purchase happen", and only a declaration answers it: a value copied into a leftover launch config
// is the same number the leftover would have carried anyway.
func TestHeavyBuyerCapPort_ExplicitOnDoesNotPromoteAStranger(t *testing.T) {
	port, db, pid := capPortDB(t)
	addContainer(t, db, pid, "a1", otherCoordinator, string(container.ContainerStatusRunning), `{"heavy_cap": 3, "growth_enabled": 1}`)

	enabled, exists, err := port.GrowthEnabled(context.Background(), pid)
	require.NoError(t, err)
	require.True(t, exists)
	require.False(t, enabled, "only a declaration names a coordinator whose code buys heavies; a knob cannot")
}

// AN UNREADABLE SWITCH IS AN ERROR, NOT AN ANSWER. The drain fails the tick closed on it: a
// switched-off buyer read as ON pauses probe buying forever, and a switched-on buyer read as OFF
// releases spending on a signal nobody could read (RULINGS #4).
func TestHeavyBuyerCapPort_UnreadableSwitchErrors(t *testing.T) {
	port := NewHeavyBuyerCapPort(nil)

	enabled, exists, err := port.GrowthEnabled(context.Background(), 1)
	require.Error(t, err)
	require.False(t, exists)
	require.False(t, enabled)
}
