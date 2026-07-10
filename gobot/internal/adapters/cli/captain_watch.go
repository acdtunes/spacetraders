package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// watchPolicyStore is the subset of one-shot wake-watch persistence the CLI
// needs (spec: sp-oyer). It exists so runWatchAdd/runWatchList/runWatchClear
// can be tested with a fake, without touching the filesystem.
type watchPolicyStore interface {
	Load() (watchkeeper.WatchPolicy, error)
	Save(policy watchkeeper.WatchPolicy) error
}

// fileWatchPolicyStore is the production watchPolicyStore, backed by the same
// supervisor state file the WakePolicy and RegimePolicy use (see
// internal/captain/state.go) — the wake watches are an independent owner of
// that one file, so a watch write never clobbers a wake-policy or regime
// write, or vice versa.
type fileWatchPolicyStore struct {
	path string
}

func (f fileWatchPolicyStore) Load() (watchkeeper.WatchPolicy, error) {
	return watchkeeper.LoadWatchPolicy(f.path)
}

func (f fileWatchPolicyStore) Save(policy watchkeeper.WatchPolicy) error {
	return watchkeeper.SaveWatchPolicy(f.path, policy)
}

// newCaptainWatchPolicyStore resolves the production store via the same
// state-path lookup the wake-policy and regime stores use
// (captainWakeStatePath, defined in captain_ops.go).
func newCaptainWatchPolicyStore() (watchPolicyStore, error) {
	path, err := captainWakeStatePath()
	if err != nil {
		return nil, err
	}
	return fileWatchPolicyStore{path: path}, nil
}

// defaultWatchETAMargin is the fractional margin added atop a ship's live ETA
// when deriving a ship:arrival watch's default deadline (sp-970u): a ship
// with T remaining transit time gets deadline = now + T×(1+margin) — i.e.
// arrival plus a margin×T cushion, rather than racing the honest ETA.
// Parametrized per RULINGS #5; the CLI exposes --eta-margin to override it.
const defaultWatchETAMargin = 0.25

// shipNavReader is the best-effort ship-nav lookup runWatchAdd uses to derive
// an ETA-based deadline for a ship:arrival watch with --by omitted (sp-970u).
// It exists so the derivation can be tested with a fake, without a live DB
// connection — mirroring watchPolicyStore. A returned error is never fatal to
// watch add: the caller falls back to the flat default.
type shipNavReader interface {
	// ShipNav reports whether shipSymbol is currently IN_TRANSIT and, if so,
	// its arrival timestamp (nil if not in transit or unknown).
	ShipNav(ctx context.Context, shipSymbol string) (inTransit bool, arrivalTime *time.Time, err error)
}

// dbShipNavReader is the production shipNavReader (sp-970u): a lightweight,
// read-only lookup of a ship's nav_status/arrival_time columns. This mirrors
// the direct persistence.ShipModel query internal/captain/detectors.go
// already uses for the same "is this ship IN_TRANSIT" question, rather than
// pulling in the heavier api.ShipRepository (apiClient + waypoint/graph
// wiring) that ship.go's sell/buy commands need for full domain
// reconstruction — this derivation only needs two scalar columns. The
// database is the source of truth for ship state after daemon startup (see
// navigation.ShipRepository doc comment), so this never calls the live API
// and is compatible with RULINGS #3 (single-writer: reads are unrestricted).
type dbShipNavReader struct {
	db       *gorm.DB
	playerID int
}

func (r dbShipNavReader) ShipNav(ctx context.Context, shipSymbol string) (bool, *time.Time, error) {
	var model persistence.ShipModel
	err := r.db.WithContext(ctx).
		Select("nav_status", "arrival_time").
		Where("ship_symbol = ? AND player_id = ?", shipSymbol, r.playerID).
		First(&model).Error
	if err != nil {
		return false, nil, err
	}
	return model.NavStatus == "IN_TRANSIT", model.ArrivalTime, nil
}

// newShipNavReader wires the production shipNavReader (sp-970u): connects to
// the same database the daemon uses, and resolves the effective player via
// the standard --player-id/--agent/config-default chain
// (resolvePlayerIdentifier) — watch add needs no new flag of its own. Every
// error here is returned to the caller, which treats a nil/failing reader as
// "nav unavailable" and falls back to the flat default; it never fails watch
// add.
func newShipNavReader(ctx context.Context) (shipNavReader, error) {
	playerIdent, err := resolvePlayerIdentifier()
	if err != nil {
		return nil, err
	}

	cfg, err := config.LoadConfig("")
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	db, err := database.NewConnection(&cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	playerID, err := resolveNumericPlayerID(ctx, db, playerIdent)
	if err != nil {
		return nil, err
	}
	return dbShipNavReader{db: db, playerID: playerID}, nil
}

// resolveNumericPlayerID resolves playerIdent to a numeric player ID,
// looking the agent symbol up in the database when only the symbol (not the
// numeric ID) was given — the same resolution ship.go's sell/buy commands
// perform, minus the token load those need for live API calls, which this
// read-only lookup does not.
func resolveNumericPlayerID(ctx context.Context, db *gorm.DB, playerIdent *PlayerIdentifier) (int, error) {
	if playerIdent.PlayerID > 0 {
		return playerIdent.PlayerID, nil
	}
	playerRepo := persistence.NewGormPlayerRepository(db)
	p, err := playerRepo.FindByAgentSymbol(ctx, playerIdent.AgentSymbol)
	if err != nil {
		return 0, fmt.Errorf("failed to resolve player from agent symbol: %w", err)
	}
	return p.ID.Value(), nil
}

// deriveETADeadlineForShip best-effort derives a ship:arrival watch's
// deadline from the ship's live nav (sp-970u): deadline = now +
// (ETA-now)×(1+etaMargin). ok is false — caller must fall back to the flat
// default — whenever navReader is unavailable (nil, e.g. newShipNavReader
// failed), the nav read errors, the ship isn't IN_TRANSIT, or the arrival
// timestamp isn't meaningfully in the future (already arrived, or clock
// skew/garbage data): a derived deadline must never land at or before now.
func deriveETADeadlineForShip(ctx context.Context, navReader shipNavReader, shipSymbol string, now time.Time, etaMargin float64) (deadline, eta time.Time, ok bool) {
	if navReader == nil {
		return time.Time{}, time.Time{}, false
	}
	inTransit, arrival, err := navReader.ShipNav(ctx, shipSymbol)
	if err != nil || !inTransit || arrival == nil {
		return time.Time{}, time.Time{}, false
	}
	remaining := arrival.Sub(now)
	if remaining <= 0 {
		return time.Time{}, time.Time{}, false
	}
	margined := time.Duration(float64(remaining) * (1 + etaMargin))
	return now.Add(margined), *arrival, true
}

// parseWatchDeadline parses the --by flag value, accepting either a relative
// duration applied to now ("+20m") or an absolute RFC3339 timestamp — the same
// dual-mode idiom as parseNextWakeAt, with --by-specific error text.
func parseWatchDeadline(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("--by: empty value")
	}
	if strings.HasPrefix(s, "+") {
		d, err := time.ParseDuration(s[1:])
		if err != nil {
			return time.Time{}, fmt.Errorf("--by: invalid relative duration %q: %w", s, err)
		}
		return now.Add(d), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--by: %q must be a relative duration like \"+20m\" or an RFC3339 timestamp: %w", s, err)
	}
	return t, nil
}

// runWatchAdd arms a new one-shot watch. This is additive (like `regime set`,
// unlike `wake set`'s full-replace): multiple watches coexist independently, so
// a second `watch add` must not drop the first. When --by is omitted the
// deadline defaults to now+defaultDeadline so every evented wait is deadline'd
// at the sensing layer (sp-oyer design note) — a watch whose match event is
// lost still auto-fires and disarms rather than arming forever.
//
// For a ship:arrival watch with --by omitted, sp-970u prefers a tighter,
// ETA-derived deadline over the flat default: a best-effort read of the
// ship's live nav via navReader gives now+(ETA-now)×(1+etaMargin) when the
// ship is IN_TRANSIT with a valid arrival timestamp. ANY failure to derive it
// (nav unavailable, ship not found/docked, bad timestamp) gracefully falls
// back to the flat default — a nav read never blocks or fails watch add.
// container:terminal watches are unaffected: there is no ETA concept for
// them, so they always get the flat default, exactly as before. Either way,
// one line is printed to w noting which deadline was used.
func runWatchAdd(ctx context.Context, store watchPolicyStore, navReader shipNavReader, w io.Writer, now time.Time, spec, by string, defaultDeadline time.Duration, etaMargin float64) error {
	watch, err := watchkeeper.ParseWatchSpec(spec)
	if err != nil {
		return err
	}

	deadline := now.Add(defaultDeadline)
	switch {
	case strings.TrimSpace(by) != "":
		d, err := parseWatchDeadline(by, now)
		if err != nil {
			return err
		}
		deadline = d
	case watch.Subject == watchkeeper.WatchSubjectShip:
		if derived, eta, ok := deriveETADeadlineForShip(ctx, navReader, watch.ID, now, etaMargin); ok {
			deadline = derived
			fmt.Fprintf(w, "deadline derived from ETA %s (+%.0f%%)\n", eta.Format(time.RFC3339), etaMargin*100)
		} else {
			fmt.Fprintf(w, "nav unavailable for %s, flat default %s\n", watch.ID, defaultDeadline)
		}
	}
	watch.Deadline = deadline
	watch.ArmedAt = now

	policy, err := store.Load()
	if err != nil {
		return fmt.Errorf("failed to load watch policy: %w", err)
	}
	policy.Watches = append(policy.Watches, watch)
	return store.Save(policy)
}

// runWatchList renders every currently-armed watch to w.
func runWatchList(store watchPolicyStore, w io.Writer) error {
	policy, err := store.Load()
	if err != nil {
		return fmt.Errorf("failed to load watch policy: %w", err)
	}
	if len(policy.Watches) == 0 {
		_, err := fmt.Fprintln(w, "No wake watches armed.")
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SUBJECT\tID\tPREDICATE\tDEADLINE\tARMED_AT")
	for _, watch := range policy.Watches {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			watch.Subject, watch.ID, watch.Predicate,
			watch.Deadline.Format(time.RFC3339), watch.ArmedAt.Format(time.RFC3339))
	}
	return tw.Flush()
}

// runWatchClear disarms every watch.
func runWatchClear(store watchPolicyStore) error {
	return store.Save(watchkeeper.WatchPolicy{})
}

func newCaptainWatchCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Arm, list, or clear one-shot wake watches",
		Long: `Arm one-shot wake watches that fire a single captain wake the first time a
specific ship arrives or a specific container reaches a terminal state — or at
a deadline, whichever comes first — then auto-disarm (spec: sp-oyer).

Watches are operational ephemera, SEPARATE from the standing wake policy: they
have their own store, multiple coexist independently, and "captain wake set"'s
full-replace semantics never touch them (and vice versa).

A fired watch reaches the captain as an interrupt-class wake mail tagged
"matched" (the arrival/terminal event was seen) or "deadline-fired" (the
deadline passed first — e.g. the arrival event was lost), then clears itself.`,
	}

	cmd.AddCommand(newCaptainWatchAddCommand())
	cmd.AddCommand(newCaptainWatchListCommand())
	cmd.AddCommand(newCaptainWatchClearCommand())

	return cmd
}

func newCaptainWatchAddCommand() *cobra.Command {
	var by string
	var defaultDeadline time.Duration
	var etaMargin float64

	cmd := &cobra.Command{
		Use:   "add <ship:SYMBOL:arrival | container:ID:terminal> [--by +20m|<RFC3339>]",
		Short: "Arm a one-shot wake watch (adds to, does not replace, existing watches)",
		Long: `Arm a one-shot wake watch on a ship arrival or a container terminal state.

The target is "ship:<SYMBOL>:arrival" or "container:<ID>:terminal". The watch
fires a single wake the first time a matching event is seen OR at its deadline,
whichever comes first, then auto-disarms.

--by sets the deadline: a relative duration ("+20m") applied from now, or an
absolute RFC3339 timestamp. When omitted:
  - ship:arrival watches prefer an ETA-derived deadline (sp-970u): a
    best-effort read of the ship's live nav gives now+(ETA-now)×(1+--eta-margin)
    when the ship is IN_TRANSIT with a known arrival time. Any failure to read
    or use that nav (ship not found/docked, stale data, ...) falls back to
    --default-deadline from now, so this never blocks or fails the add.
  - container:terminal watches always use --default-deadline from now — there
    is no ETA concept for a container.
Either way the watch is always deadline'd, so a watch whose match event is
lost still fires (tagged deadline-fired) and clears itself rather than arming
forever. The CLI prints which deadline was used.

Each "watch add" ADDS a watch; run "captain wake watch clear" for a clean
slate. Multiple watches coexist and fire independently.

Examples:
  spacetraders captain wake watch add ship:TORWIND-E:arrival
  spacetraders captain wake watch add ship:TORWIND-E:arrival --by +20m
  spacetraders captain wake watch add container:c-9f2a:terminal --by 2026-07-10T18:00:00Z`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newCaptainWatchPolicyStore()
			if err != nil {
				return err
			}
			ctx := context.Background()
			// Best-effort: nav unavailable is never fatal to watch add
			// (sp-970u) — a nil navReader always falls back to the flat
			// default inside runWatchAdd.
			navReader, _ := newShipNavReader(ctx)
			if err := runWatchAdd(ctx, store, navReader, os.Stdout, time.Now(), args[0], by, defaultDeadline, etaMargin); err != nil {
				return err
			}
			fmt.Println("Wake watch armed.")
			return nil
		},
	}

	cmd.Flags().StringVar(&by, "by", "", `Deadline: relative ("+20m") or an RFC3339 absolute timestamp (default: derived from ship ETA for ship:arrival, else --default-deadline from now)`)
	cmd.Flags().DurationVar(&defaultDeadline, "default-deadline", watchkeeper.DefaultWatchDeadline,
		"Deadline applied when --by is omitted and no ETA can be derived, measured from now")
	cmd.Flags().Float64Var(&etaMargin, "eta-margin", defaultWatchETAMargin,
		"Fractional margin added atop a ship's live ETA for a ship:arrival watch when --by is omitted (e.g. 0.25 = +25%)")

	return cmd
}

func newCaptainWatchListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the currently-armed one-shot wake watches",
		Long: `List every one-shot wake watch currently armed via "captain wake watch add".

Examples:
  spacetraders captain wake watch list`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newCaptainWatchPolicyStore()
			if err != nil {
				return err
			}
			return runWatchList(store, os.Stdout)
		},
	}
	return cmd
}

func newCaptainWatchClearCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Disarm all one-shot wake watches",
		Long: `Disarm every currently-armed one-shot wake watch. The standing wake policy
("captain wake set") is unaffected.

Examples:
  spacetraders captain wake watch clear`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := newCaptainWatchPolicyStore()
			if err != nil {
				return err
			}
			if err := runWatchClear(store); err != nil {
				return fmt.Errorf("failed to clear wake watches: %w", err)
			}
			fmt.Println("Wake watches cleared.")
			return nil
		},
	}
	return cmd
}
