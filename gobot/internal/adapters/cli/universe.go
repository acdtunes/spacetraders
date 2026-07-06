package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

const eraDateLayout = "2006-01-02"

type serverStatusProvider interface {
	GetServerStatus(ctx context.Context) (*api.ServerStatus, error)
}

type openEraProvider interface {
	FindOpenEra(ctx context.Context) (*persistence.EraModel, error)
}

func NewUniverseCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "universe",
		Short: "Universe era registry and reset operations",
		Long: `Inspect and manage the universe era lifecycle.

A universe era is keyed by the server resetDate. 'universe status' compares the
live server resetDate against the open era row and signals a MISMATCH (non-zero
exit) when the universe has reset under the fleet.

Examples:
  spacetraders universe status`,
	}

	cmd.AddCommand(newUniverseStatusCommand())
	cmd.AddCommand(newUniverseCloseCommand())
	cmd.AddCommand(newUniverseScrubCommand())

	return cmd
}

type eraCloser interface {
	CloseEra(ctx context.Context, name string) (*persistence.CloseReport, error)
}

type eraScrubber interface {
	ScrubEra(ctx context.Context, name string) (*persistence.ScrubReport, error)
}

func newUniverseCloseCommand() *cobra.Command {
	var era, confirm string
	cmd := &cobra.Command{
		Use:   "close",
		Short: "Close a universe era (destructive: truncates caches, blanks the dead token)",
		Long: `Close the named era: stamp closed_at + final_credits, blank the dead player
token, truncate the market_data and system_graphs caches, and backfill
waypoints.era_id where NULL. Refuses unless --confirm echoes the era name.
Re-running on an already-closed era is an idempotent no-op. Player-scoped
history (transactions, contracts, ...) is never touched.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}
			repo := persistence.NewEraRepository(db)
			return runUniverseClose(context.Background(), repo, era, confirm, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&era, "era", "", "era name to close")
	cmd.Flags().StringVar(&confirm, "confirm", "", "must echo the era name to confirm the destructive close")
	return cmd
}

func newUniverseScrubCommand() *cobra.Command {
	var era, confirm string
	cmd := &cobra.Command{
		Use:   "scrub",
		Short: "Delete WIPE-class player-scoped junk rows for a closed era",
		Long: `Optional hygiene, any time after close: DELETE the WIPE-class player-scoped
rows (containers, container_logs, ships, manufacturing_factory_states,
gas_operations, storage_operations) for the dead era's player. Refuses on an
open era and never touches ARCHIVE-class history. Requires --confirm to echo
the era name.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}
			repo := persistence.NewEraRepository(db)
			return runUniverseScrub(context.Background(), repo, era, confirm, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&era, "era", "", "era name to scrub")
	cmd.Flags().StringVar(&confirm, "confirm", "", "must echo the era name to confirm the deletion")
	return cmd
}

func runUniverseClose(ctx context.Context, closer eraCloser, era, confirm string, out io.Writer) error {
	if era == "" {
		return fmt.Errorf("refused: --era is required")
	}
	if confirm != era {
		return fmt.Errorf("refused: --confirm must echo the era name %q", era)
	}

	report, err := closer.CloseEra(ctx, era)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Era\t%s\n", report.Era.Name)
	if report.AlreadyClosed {
		fmt.Fprintf(w, "State\talready closed (no-op)\n")
		w.Flush()
		return nil
	}
	fmt.Fprintf(w, "State\tclosed\n")
	fmt.Fprintf(w, "Final credits\t%d\n", report.FinalCredits)
	fmt.Fprintf(w, "Waypoints backfilled\t%d\n", report.WaypointsBackfilled)
	fmt.Fprintf(w, "Caches truncated\tmarket_data, system_graphs\n")
	w.Flush()
	return nil
}

func runUniverseScrub(ctx context.Context, scrubber eraScrubber, era, confirm string, out io.Writer) error {
	if era == "" {
		return fmt.Errorf("refused: --era is required")
	}
	if confirm != era {
		return fmt.Errorf("refused: --confirm must echo the era name %q", era)
	}

	report, err := scrubber.ScrubEra(ctx, era)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Era\t%s\n", report.Era.Name)
	fmt.Fprintf(w, "Rows deleted\t%d\n", report.Total)
	w.Flush()
	return nil
}

func newUniverseStatusCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Compare server resetDate against the open era",
		Long: `Print the server resetDate and next reset alongside the open era's recorded
reset date. Exits non-zero on MISMATCH so the Watchkeeper can script detection.
With no open era row it prints NO ERA and exits zero (pre-registration state).`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}

			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}

			client := api.NewSpaceTradersClient()
			eraRepo := persistence.NewEraRepository(db)

			return runUniverseStatus(context.Background(), client, eraRepo, os.Stdout)
		},
	}

	return cmd
}

func runUniverseStatus(ctx context.Context, sp serverStatusProvider, ep openEraProvider, out io.Writer) error {
	status, err := sp.GetServerStatus(ctx)
	if err != nil {
		return fmt.Errorf("failed to get server status: %w", err)
	}

	era, err := ep.FindOpenEra(ctx)
	if err != nil {
		return fmt.Errorf("failed to load open era: %w", err)
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Server resetDate\t%s\n", status.ResetDate)
	fmt.Fprintf(w, "Next reset\t%s (%s)\n", status.ServerResets.Next, status.ServerResets.Frequency)

	if era == nil {
		fmt.Fprintf(w, "Open era\tNO ERA\n")
		w.Flush()
		return nil
	}

	eraDate := ""
	if era.UniverseResetDate != nil {
		eraDate = era.UniverseResetDate.Format(eraDateLayout)
	}
	fmt.Fprintf(w, "Open era\t%s\n", era.Name)
	fmt.Fprintf(w, "Era resetDate\t%s\n", eraDate)

	if eraDate != status.ResetDate {
		fmt.Fprintf(w, "State\tMISMATCH\n")
		w.Flush()
		return fmt.Errorf("universe reset MISMATCH: server resetDate %s != open era %s resetDate %s",
			status.ResetDate, era.Name, eraDate)
	}

	fmt.Fprintf(w, "State\tin sync\n")
	w.Flush()
	return nil
}
