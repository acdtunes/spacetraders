package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// ---- seams -----------------------------------------------------------------

type transitionAPI interface {
	GetAgent(ctx context.Context, token string) (*player.AgentData, error)
	GetServerStatus(ctx context.Context) (*api.ServerStatus, error)
	Register(ctx context.Context, accountToken, agentSymbol, faction string) (*api.RegisterResult, error)
}

type transitionEraStore interface {
	FindOpenEra(ctx context.Context) (*persistence.EraModel, error)
	TransitionEra(ctx context.Context, newPlayer *persistence.PlayerModel, newEra *persistence.EraModel) (*persistence.TransitionReport, error)
}

type playerDefaultSetter interface {
	SetDefault(agentSymbol string, playerID int) error
}

type captainConfigSetter interface {
	// SetCaptainPlayerID repoints captain.player_id, returning whether the file
	// changed and the path written (for reporting).
	SetCaptainPlayerID(playerID int) (changed bool, path string, err error)
}

// activeContainer is the drain-relevant view of a container row.
type activeContainer struct {
	ID            string
	ContainerType string
	CommandType   string
	Status        string
}

type containerLister interface {
	ListActiveContainers(ctx context.Context, playerID int) ([]activeContainer, error)
}

type containerStopper interface {
	// StopContainer asks the daemon to stop a live container. A wrapped error whose
	// message contains "not found" signals an orphan (no runtime handle) and is
	// handled by reconciling the DB row directly, not by failing the drain.
	StopContainer(ctx context.Context, containerID string) error
}

type orphanReconciler interface {
	// MarkStopped writes STOPPED directly to a daemon-unknown orphan row. This is a
	// sanctioned container-status write (not ship state), for rows the daemon will
	// never update because it has no runtime handle for them.
	MarkStopped(ctx context.Context, containerID string, playerID int) error
}

type transitionDeps struct {
	api        transitionAPI
	era        transitionEraStore
	cliDefault playerDefaultSetter
	captainCfg captainConfigSetter
	lister     containerLister
	stopper    containerStopper
	reconciler orphanReconciler
}

type transitionOpts struct {
	agent        string
	token        string
	accountToken string
	dryRun       bool
	confirm      bool
}

// ---- orchestration ---------------------------------------------------------

// runUniverseTransition performs the full era rollover as one idempotent, guarded
// operation (sp-nax3): validate the token against the API BEFORE any DB write
// (fail-closed), flip the era table without truncating player-partitioned caches,
// repoint both the CLI default AND captain.player_id (closes sp-m602), and drain
// the prior era's containers coordinators-first (reconciling daemon-unknown
// orphans). --dry-run and the absence of --confirm both stop before any mutation.
func runUniverseTransition(ctx context.Context, deps transitionDeps, opts transitionOpts, out io.Writer) error {
	if opts.agent == "" {
		return fmt.Errorf("--agent is required")
	}
	apply := opts.confirm && !opts.dryRun

	// Resolve the working token. An explicit --token (manual/recovery override) wins
	// and is validated exactly as before. Absent it, the new era's JWT is minted from
	// ST_ACCOUNT_TOKEN. Registering an agent is an irreversible external effect that
	// consumes an account slot, so the mint runs ONLY on a confirmed apply — a
	// preview/dry-run states the mint and registers nothing. With neither token source
	// we fail closed before touching the API or the DB.
	if opts.token == "" && opts.accountToken == "" {
		return fmt.Errorf("provide --token or set ST_ACCOUNT_TOKEN")
	}
	if opts.token == "" && !apply {
		return previewPendingMint(ctx, deps, opts, out)
	}
	token := opts.token
	if token == "" {
		minted, err := deps.api.Register(ctx, opts.accountToken, opts.agent, "")
		if err != nil {
			return fmt.Errorf("failed to mint new-era JWT for %q — no changes made: %w", opts.agent, err)
		}
		token = minted.Token
	}

	// 1. Validate the (provided or freshly-minted) token FIRST via the API. This is
	//    the root-cause fix for the silent-corruption era: nothing is written unless
	//    the token authenticates. On the mint path it also re-asserts the minted
	//    agent's symbol matches --agent.
	agentData, err := deps.api.GetAgent(ctx, token)
	if err != nil {
		return fmt.Errorf("token validation failed (GetAgent) — no changes made: %w", err)
	}
	if !strings.EqualFold(agentData.Symbol, opts.agent) {
		return fmt.Errorf("token belongs to agent %q but --agent is %q — refusing (no changes made)",
			agentData.Symbol, opts.agent)
	}
	symbol := agentData.Symbol

	// 2. Resolve the server reset date; refuse before any write if it won't parse.
	status, err := deps.api.GetServerStatus(ctx)
	if err != nil {
		return fmt.Errorf("failed to get server status: %w", err)
	}
	resetDate, err := time.Parse(eraDateLayout, status.ResetDate)
	if err != nil {
		return fmt.Errorf("failed to parse server reset date %q: %w", status.ResetDate, err)
	}
	newEraName := strings.ToLower(symbol) + "-" + resetDate.Format(eraDateLayout)

	// 3. Idempotency: if the open era already matches the server reset date the
	//    universe is in sync (same comparison as `universe status`) — no-op, exit 0.
	openEra, err := deps.era.FindOpenEra(ctx)
	if err != nil {
		return fmt.Errorf("failed to load open era: %w", err)
	}
	if openEra != nil && openEra.UniverseResetDate != nil &&
		openEra.UniverseResetDate.Format(eraDateLayout) == status.ResetDate {
		fmt.Fprintf(out, "Universe already in sync (open era %s, player %d, resetDate %s). No changes made.\n",
			openEra.Name, openEra.PlayerID, status.ResetDate)
		return nil
	}

	printTransitionPlan(out, symbol, agentData, status.ResetDate, newEraName, openEra)

	// 4. Preview gate: --dry-run OR the absence of --confirm stops here, unmutated.
	if !apply {
		if opts.dryRun {
			fmt.Fprintln(out, "\n--dry-run: no changes made.")
		} else {
			fmt.Fprintln(out, "\nPreview only — re-run with --confirm to apply the era rollover.")
		}
		return nil
	}

	// 5. Era flip through the non-truncating repository path (crit 1, 5). The new
	//    player row is created here with the validated token.
	now := time.Now().UTC()
	newPlayer := &persistence.PlayerModel{
		AgentSymbol: symbol,
		Token:       token,
		CreatedAt:   now,
		Metadata:    agentIdentityMetadata(agentData),
	}
	newEra := &persistence.EraModel{
		Name:              newEraName,
		AgentSymbol:       symbol,
		RegisteredAt:      &now,
		UniverseResetDate: &resetDate,
	}
	if agentData.StartingFaction != "" {
		f := agentData.StartingFaction
		newEra.Faction = &f
	}
	report, err := deps.era.TransitionEra(ctx, newPlayer, newEra)
	if err != nil {
		return fmt.Errorf("era flip failed: %w", err)
	}
	newPlayerID := report.NewPlayerID
	fmt.Fprintf(out, "\n✓ Era flipped: opened %s (player %d)", newEra.Name, newPlayerID)
	if report.ClosedEra != nil {
		fmt.Fprintf(out, "; closed %s (final_credits %d)", report.ClosedEra.Name, report.ClosedCredits)
	}
	fmt.Fprintln(out)

	// 6. Repoint the CLI default player.
	if err := deps.cliDefault.SetDefault(symbol, newPlayerID); err != nil {
		return fmt.Errorf("era flipped but failed to set CLI default player: %w", err)
	}
	fmt.Fprintf(out, "✓ CLI default player → %s (id %d)\n", symbol, newPlayerID)

	// 7. Repoint captain.player_id so the supervisor does not wake as the dead
	//    prior-era player (closes sp-m602). Fail loud if the file can't be located.
	changed, cfgPath, err := deps.captainCfg.SetCaptainPlayerID(newPlayerID)
	if err != nil {
		return fmt.Errorf("era flipped but failed to repoint captain.player_id: %w", err)
	}
	if changed {
		fmt.Fprintf(out, "✓ captain.player_id → %d (%s)\n", newPlayerID, cfgPath)
	} else {
		fmt.Fprintf(out, "• captain.player_id already %d (%s)\n", newPlayerID, cfgPath)
	}

	// 8. Drain the prior era's containers coordinators-first + reconcile orphans.
	if openEra != nil {
		dr, err := drainPriorEra(ctx, deps.lister, deps.stopper, deps.reconciler, openEra.PlayerID, out)
		if err != nil {
			return fmt.Errorf("drain failed: %w", err)
		}
		fmt.Fprintf(out, "✓ Drained prior player %d: %d stopped, %d orphan row(s) reconciled to STOPPED, %d already terminal\n",
			openEra.PlayerID, dr.Stopped, dr.OrphansReconciled, dr.AlreadyTerminal)
	}

	fmt.Fprintln(out, "\nNOTE: the daemon's in-memory 'Active Containers' gauge may stay high until an")
	fmt.Fprintln(out, "Admiral daemon restart clears it — verify the drain against DB truth, not the gauge.")
	return nil
}

func printTransitionPlan(out io.Writer, symbol string, agentData *player.AgentData, resetDate, newEraName string, openEra *persistence.EraModel) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Agent (validated)\t%s (credits %d, faction %s)\n", symbol, agentData.Credits, agentData.StartingFaction)
	fmt.Fprintf(w, "Server resetDate\t%s\n", resetDate)
	if openEra != nil {
		fmt.Fprintf(w, "Prior era\t%s (player %d) → will be CLOSED (no cache truncation)\n", openEra.Name, openEra.PlayerID)
		fmt.Fprintf(w, "Drain\tprior player %d containers, coordinators-first\n", openEra.PlayerID)
	} else {
		fmt.Fprintf(w, "Prior era\t(none open)\n")
	}
	fmt.Fprintf(w, "New era\t%s (new player) → will be OPENED for resetDate %s\n", newEraName, resetDate)
	fmt.Fprintf(w, "Repoint\tCLI default + captain.player_id → new player\n")
	w.Flush()
}

// previewPendingMint prints the rollover plan for the mint path (no --token, not yet
// applied) using only read-only calls — it registers no agent. The new era's JWT is
// minted from ST_ACCOUNT_TOKEN only when the command is re-run with --confirm.
func previewPendingMint(ctx context.Context, deps transitionDeps, opts transitionOpts, out io.Writer) error {
	status, err := deps.api.GetServerStatus(ctx)
	if err != nil {
		return fmt.Errorf("failed to get server status: %w", err)
	}
	resetDate, err := time.Parse(eraDateLayout, status.ResetDate)
	if err != nil {
		return fmt.Errorf("failed to parse server reset date %q: %w", status.ResetDate, err)
	}
	newEraName := strings.ToLower(opts.agent) + "-" + resetDate.Format(eraDateLayout)

	openEra, err := deps.era.FindOpenEra(ctx)
	if err != nil {
		return fmt.Errorf("failed to load open era: %w", err)
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Agent\t%s — a fresh JWT will be minted from ST_ACCOUNT_TOKEN on --confirm\n", opts.agent)
	fmt.Fprintf(w, "Server resetDate\t%s\n", status.ResetDate)
	if openEra != nil {
		fmt.Fprintf(w, "Prior era\t%s (player %d) → will be CLOSED (no cache truncation)\n", openEra.Name, openEra.PlayerID)
	} else {
		fmt.Fprintf(w, "Prior era\t(none open)\n")
	}
	fmt.Fprintf(w, "New era\t%s (new player) → will be OPENED for resetDate %s\n", newEraName, status.ResetDate)
	w.Flush()

	if opts.dryRun {
		fmt.Fprintln(out, "\n--dry-run: no changes made, no agent registered.")
	} else {
		fmt.Fprintln(out, "\nPreview only — re-run with --confirm to mint the JWT and apply the era rollover.")
	}
	return nil
}

// agentIdentityMetadata builds the new era row's metadata column from the agent payload the
// rollover already fetched to validate the token.
//
// It replaces the former factionMetadata, which persisted starting_faction and dropped everything
// else — including headquarters, which was sitting right there in the same struct. That omission
// is what left every players row without the key: three engines read it, nothing wrote it, and
// the parked-sensing cutover therefore refused every 30s and took all frontier expansion down
// with it.
//
// The merge is the shared player.MergeAgentIdentity, so "which keys are identity" has ONE
// definition rather than one here and another in the sync handler. Empty fields are omitted
// rather than written blank (the merge's fail-closed rule), so an agent payload with nothing
// usable yields an empty column exactly as before — no downstream reader sees a new shape.
func agentIdentityMetadata(agent *player.AgentData) string {
	metadata, changed := player.MergeAgentIdentity(nil, agent)
	if !changed || len(metadata) == 0 {
		return "" // nothing knowable — persist no metadata at all, as the faction-only path did
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return ""
	}
	return string(raw)
}

// ---- drain -----------------------------------------------------------------

type drainReport struct {
	Stopped           int
	OrphansReconciled int
	AlreadyTerminal   int
	StopOrder         []string
	Passes            int
}

// drainPriorEra stops every RUNNING/PENDING container of the prior player,
// coordinators FIRST (they run iterations=-1 reconcile loops that relaunch
// workers, so stopping a worker before its coordinator just thrashes;
// restart_policy=on-failure makes an explicit stop terminal). A daemon-unknown
// orphan (StopContainer → "not found") is reconciled straight to STOPPED in the DB.
// A container that raced to STOPPED/COMPLETED between the list and the stop call
// (or a re-run over an already-drained era) is a no-op, not an abort — this is
// what makes the whole drain idempotent.
//
// It re-lists across passes (skipping already-handled IDs) so any worker a
// coordinator spawned in the enumerate→stop window is still caught, converging
// once a pass finds nothing new. maxPasses guards against pathological churn.
func drainPriorEra(ctx context.Context, lister containerLister, stopper containerStopper, reconciler orphanReconciler, priorPlayerID int, out io.Writer) (*drainReport, error) {
	report := &drainReport{}
	seen := map[string]bool{}
	const maxPasses = 6

	for pass := 1; pass <= maxPasses; pass++ {
		active, err := lister.ListActiveContainers(ctx, priorPlayerID)
		if err != nil {
			return nil, err
		}

		todo := make([]activeContainer, 0, len(active))
		for _, c := range active {
			if !isActiveStatus(c.Status) || seen[c.ID] {
				continue
			}
			todo = append(todo, c)
		}
		if len(todo) == 0 {
			report.Passes = pass
			return report, nil
		}

		for _, c := range orderCoordinatorsFirst(todo) {
			seen[c.ID] = true
			report.StopOrder = append(report.StopOrder, c.ID)

			if err := stopper.StopContainer(ctx, c.ID); err != nil {
				if isNotFoundErr(err) {
					if rerr := reconciler.MarkStopped(ctx, c.ID, priorPlayerID); rerr != nil {
						return nil, fmt.Errorf("failed to reconcile orphan container %s: %w", c.ID, rerr)
					}
					report.OrphansReconciled++
					fmt.Fprintf(out, "  · reconciled orphan %s → STOPPED (daemon had no handle)\n", c.ID)
					continue
				}
				if isAlreadyTerminalErr(err) {
					report.AlreadyTerminal++
					fmt.Fprintf(out, "  · already terminal %s (no-op)\n", c.ID)
					continue
				}
				return nil, fmt.Errorf("failed to stop container %s: %w", c.ID, err)
			}
			report.Stopped++
		}
	}

	// Convergence guard: after the pass budget, nothing new must remain.
	active, err := lister.ListActiveContainers(ctx, priorPlayerID)
	if err != nil {
		return nil, err
	}
	remaining := 0
	for _, c := range active {
		if isActiveStatus(c.Status) && !seen[c.ID] {
			remaining++
		}
	}
	if remaining > 0 {
		return nil, fmt.Errorf("drain did not converge after %d passes: %d prior-era container(s) still active", maxPasses, remaining)
	}
	report.Passes = maxPasses
	return report, nil
}

func isActiveStatus(status string) bool {
	return status == string(container.ContainerStatusPending) || status == string(container.ContainerStatusRunning)
}

func isCoordinator(c activeContainer) bool {
	return strings.Contains(strings.ToLower(c.ContainerType), "coordinator") ||
		strings.Contains(strings.ToLower(c.CommandType), "coordinator")
}

// orderCoordinatorsFirst is a stable partition: coordinators in listed order,
// then workers in listed order.
func orderCoordinatorsFirst(cs []activeContainer) []activeContainer {
	ordered := make([]activeContainer, 0, len(cs))
	for _, c := range cs {
		if isCoordinator(c) {
			ordered = append(ordered, c)
		}
	}
	for _, c := range cs {
		if !isCoordinator(c) {
			ordered = append(ordered, c)
		}
	}
	return ordered
}

func isNotFoundErr(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

// isAlreadyTerminalErr matches the domain guard's rejection of a stop on a
// container already STOPPED/COMPLETED (container.go Stop). That guard emits
// this exact prefix only for terminal states, so it's a safe, stable match.
func isAlreadyTerminalErr(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "cannot stop container in")
}

// ---- production adapters ---------------------------------------------------

type userConfigDefaultSetter struct {
	handler *config.UserConfigHandler
}

func (s *userConfigDefaultSetter) SetDefault(agentSymbol string, playerID int) error {
	if err := s.handler.SetDefaultAgent(agentSymbol); err != nil {
		return err
	}
	return s.handler.SetDefaultPlayer(playerID)
}

type fileCaptainConfigSetter struct{}

func (fileCaptainConfigSetter) SetCaptainPlayerID(playerID int) (bool, string, error) {
	path := config.ResolveConfigFilePath()
	if path == "" {
		return false, "", fmt.Errorf("could not locate config.yaml to repoint captain.player_id")
	}
	changed, err := config.SetCaptainPlayerID(path, playerID)
	return changed, path, err
}

type dbContainerLister struct {
	repo *persistence.ContainerRepositoryGORM
}

func (l *dbContainerLister) ListActiveContainers(ctx context.Context, playerID int) ([]activeContainer, error) {
	models, err := l.repo.ListAll(ctx, &playerID)
	if err != nil {
		return nil, err
	}
	out := make([]activeContainer, 0, len(models))
	for _, m := range models {
		out = append(out, activeContainer{
			ID:            m.ID,
			ContainerType: m.ContainerType,
			CommandType:   m.CommandType,
			Status:        m.Status,
		})
	}
	return out, nil
}

type daemonContainerStopper struct {
	client *DaemonClient
}

func (s *daemonContainerStopper) StopContainer(ctx context.Context, containerID string) error {
	_, err := s.client.StopContainer(ctx, containerID)
	return err
}

type dbOrphanReconciler struct {
	repo *persistence.ContainerRepositoryGORM
}

func (r *dbOrphanReconciler) MarkStopped(ctx context.Context, containerID string, playerID int) error {
	now := time.Now().UTC()
	return r.repo.UpdateStatus(ctx, containerID, playerID, container.ContainerStatusStopped, &now, nil,
		"universe transition: reconciled daemon-unknown orphan (sp-nax3)")
}

// ---- cobra wiring ----------------------------------------------------------

func newUniverseTransitionCommand() *cobra.Command {
	var (
		agent   string
		token   string
		dryRun  bool
		confirm bool
	)

	cmd := &cobra.Command{
		Use:   "transition",
		Short: "One-command era rollover: adopt a token, flip the era, repoint, and drain",
		Long: `Perform the full universe-era rollover as one idempotent, guarded command.

With --token it validates that token against the API (GetAgent) BEFORE writing
anything, so a corrupt token is rejected with zero partial state. Without --token it
mints the new era's JWT by registering --agent via ST_ACCOUNT_TOKEN (only on
--confirm), then validates and uses it. On --confirm it flips the era table WITHOUT
truncating the player-partitioned market_data / system_graphs caches, repoints both
the CLI default player AND captain.player_id, and drains the prior era's containers
coordinators-first (reconciling daemon-unknown orphan rows to STOPPED).

Idempotent: re-running once the universe is in sync is a no-op. --dry-run (or the
absence of --confirm) previews the plan and mutates nothing (and mints nothing).

Examples:
  spacetraders universe transition --agent TORWIND --confirm            # mints from ST_ACCOUNT_TOKEN
  spacetraders universe transition --agent TORWIND --token eyJ... --dry-run
  spacetraders universe transition --agent TORWIND --token eyJ... --confirm`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUniverseTransitionCommand(agent, token, dryRun, confirm)
		},
	}

	cmd.Flags().StringVar(&agent, "agent", "", "agent symbol for the new era (must match the token's agent)")
	cmd.Flags().StringVar(&token, "token", "", "JWT for the new era's agent; if omitted, one is minted from ST_ACCOUNT_TOKEN (validated via API before any write)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview the rollover plan without mutating anything")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "apply the destructive rollover (era flip, repoint, drain)")
	return cmd
}

func runUniverseTransitionCommand(agent, token string, dryRun, confirm bool) error {
	cfg, err := config.LoadConfig("")
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	userConfigHandler, err := config.NewUserConfigHandler()
	if err != nil {
		return fmt.Errorf("failed to create user config handler: %w", err)
	}

	containerRepo := persistence.NewContainerRepository(db)
	deps := transitionDeps{
		api:        api.NewSpaceTradersClient(),
		era:        persistence.NewEraRepository(db),
		cliDefault: &userConfigDefaultSetter{handler: userConfigHandler},
		captainCfg: fileCaptainConfigSetter{},
		lister:     &dbContainerLister{repo: containerRepo},
		reconciler: &dbOrphanReconciler{repo: containerRepo},
	}

	// The daemon is only needed to stop LIVE containers during an --confirm drain.
	// Preview/dry-run must not require a running daemon.
	apply := confirm && !dryRun
	if apply {
		client, err := connectDaemon()
		if err != nil {
			return err
		}
		defer client.Close()
		deps.stopper = &daemonContainerStopper{client: client}
	}

	return runUniverseTransition(context.Background(), deps, transitionOpts{
		agent:        agent,
		token:        token,
		accountToken: os.Getenv("ST_ACCOUNT_TOKEN"),
		dryRun:       dryRun,
		confirm:      confirm,
	}, os.Stdout)
}
