package cli

import (
	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// newCaptainPlayerRepo connects to the database and returns a player
// repository, so captain events/report commands can resolve --player-id/
// --agent via the shared resolveDefaultPlayer helper. It opens its own
// connection independent of newCaptainEventStore/newReportEventSource,
// matching this package's established one-connection-per-factory
// convention (see ledger.go).
func newCaptainPlayerRepo() (player.PlayerRepository, error) {
	db, err := openDatabase()
	if err != nil {
		return nil, err
	}

	return persistence.NewGormPlayerRepository(db), nil
}

// NewCaptainCommand creates the captain command with subcommands.
func NewCaptainCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "captain",
		Short: "Autonomous captain operations",
		Long: `Inspect and acknowledge the strategic-event queue the autonomous
captain consumes during its wake ritual.

Player is resolved the same way everywhere: --player-id, or --agent (which
survives across era resets, unlike --player-id), or the persisted default.

Examples:
  spacetraders captain events list --player-id 1
  spacetraders captain events list --agent TORWIND --json
  spacetraders captain events ack --player-id 1 --ids 12,13,14
  spacetraders captain events ack --agent TORWIND --all`,
	}

	cmd.AddCommand(newCaptainEventsCommand())
	cmd.AddCommand(newCaptainReportCommand())
	cmd.AddCommand(newCaptainTokensCommand())
	cmd.AddCommand(newCaptainWakeCommand())
	cmd.AddCommand(newCaptainRegimeCommand())
	cmd.AddCommand(newCaptainGagCommand())

	return cmd
}
