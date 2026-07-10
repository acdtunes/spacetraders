package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/graph"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	domainsystem "github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// NewSystemCommand creates the `system` command group. Today it exposes the
// jump-gate adjacency (sp-7gr2) so an operator (or a satellite pushing outward)
// can see the real cross-system topology the daemon routes over — the map that
// makes JP61's three-jump distance from KA42 (PA3→UQ16→JP61) visible instead of
// discovered by a laden crash at the home gate.
func NewSystemCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "system",
		Short: "Inspect system-level topology",
		Long: `Inspect system-level topology such as the cross-system jump-gate graph.

The jump-gate graph is the API's own truth about which systems are reachable
from which by a single jump. It is cached in a local store and refreshed lazily
on a miss, so 'system gates' both reveals the known map and charts a named
system live on demand.`,
	}

	cmd.AddCommand(newSystemGatesCommand())
	return cmd
}

// newSystemGatesCommand creates `system gates`: print the jump-gate adjacency for
// every charted system, or (with --system) for one system, fetching it live on a
// store miss. Output is one line per system in the shape
// `X1-ABC <-> X1-DEF,X1-GHI` — the same shape as the sp-7gr2 topology dump, so it
// stays greppable and feeds manual routing.
func newSystemGatesCommand() *cobra.Command {
	var systemSymbol string

	cmd := &cobra.Command{
		Use:   "gates",
		Short: "Print the cross-system jump-gate adjacency",
		Long: `Print the cross-system jump-gate adjacency.

Without --system, prints the adjacency for every system currently in the local
gate-graph store (era-scoped: dead-era rows are never shown). With --system,
prints just that system's connections, fetching them live from the API and
persisting them if the store has no fresh entry.

Examples:
  spacetraders system gates
  spacetraders system gates --system X1-KA42`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			cfg, err := config.LoadConfig("")
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			db, err := database.NewConnection(&cfg.Database)
			if err != nil {
				return fmt.Errorf("failed to connect to database: %w", err)
			}

			// Same dependency chain the daemon and other CLI read commands build:
			// the API client + system-graph service resolve a charted system's own
			// gate on a fetch-through miss; the gate-edge store persists the result.
			playerRepo := persistence.NewGormPlayerRepository(db)
			apiClient := api.NewSpaceTradersClient()
			waypointRepo := persistence.NewGormWaypointRepository(db)
			systemGraphRepo := persistence.NewGormSystemGraphRepository(db)
			graphBuilder := api.NewGraphBuilder(apiClient, playerRepo, waypointRepo)
			graphService := graph.NewGraphService(systemGraphRepo, waypointRepo, graphBuilder)
			gateEdgeRepo := persistence.NewGormGateEdgeRepository(db)
			gateGraph := gategraph.NewService(gateEdgeRepo, apiClient, graphService, playerRepo)

			player, err := resolveDefaultPlayer(ctx, playerRepo)
			if err != nil {
				return err
			}
			playerID := player.ID.Value()

			if systemSymbol != "" {
				edges, err := gateGraph.Connections(ctx, systemSymbol, playerID)
				if err != nil {
					return fmt.Errorf("failed to resolve gate connections for %s: %w", systemSymbol, err)
				}
				printGateAdjacency(map[string][]domainsystem.GateEdge{systemSymbol: edges})
				return nil
			}

			adjacency, err := gateGraph.Adjacency(ctx)
			if err != nil {
				return fmt.Errorf("failed to read gate adjacency: %w", err)
			}
			if len(adjacency) == 0 {
				fmt.Println("No gate edges known yet. Chart a system with: spacetraders system gates --system X1-<SYSTEM>")
				return nil
			}
			printGateAdjacency(adjacency)
			return nil
		},
	}

	cmd.Flags().StringVar(&systemSymbol, "system", "", "Print only this system's connections (fetches live on a store miss), e.g. X1-KA42")
	return cmd
}

// printGateAdjacency writes the rendered adjacency table to stdout.
func printGateAdjacency(adjacency map[string][]domainsystem.GateEdge) {
	fmt.Print(renderGateAdjacency(adjacency))
}

// renderGateAdjacency renders the adjacency as aligned `SYS <-> a,b,c` lines,
// systems sorted for stable output and neighbors sorted and comma-joined — the
// sp-7gr2 topology-dump shape. Two markers keep the chart honest (sp-8qhu): a
// VERIFIED under-construction gate is marked `*` (not routable); a STALE row —
// whose cached construction state is unverified and will be re-probed on the next
// route — is marked `?` so an invalidated row is never presented as an
// authoritative built/unbuilt verdict. Stale takes precedence over `*` (a stale
// row's construction value is exactly what we no longer trust). A legend line is
// appended for whichever markers appear. Pure (returns the text) so it is
// unit-testable without capturing stdout.
func renderGateAdjacency(adjacency map[string][]domainsystem.GateEdge) string {
	systems := make([]string, 0, len(adjacency))
	width := 0
	for sys := range adjacency {
		systems = append(systems, sys)
		if len(sys) > width {
			width = len(sys)
		}
	}
	sort.Strings(systems)

	var b strings.Builder
	anyUnderConstruction := false
	anyStale := false
	for _, sys := range systems {
		edges := adjacency[sys]
		if len(edges) == 0 {
			fmt.Fprintf(&b, "%-*s <-> (none)\n", width, sys)
			continue
		}
		sorted := make([]domainsystem.GateEdge, len(edges))
		copy(sorted, edges)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].ConnectedSystem < sorted[j].ConnectedSystem })

		labels := make([]string, 0, len(sorted))
		for _, e := range sorted {
			label := e.ConnectedSystem
			switch {
			case e.Stale:
				label += "?"
				anyStale = true
			case e.UnderConstruction:
				label += "*"
				anyUnderConstruction = true
			}
			labels = append(labels, label)
		}
		fmt.Fprintf(&b, "%-*s <-> %s\n", width, sys, strings.Join(labels, ","))
	}
	if anyUnderConstruction || anyStale {
		b.WriteString("\n")
		if anyUnderConstruction {
			b.WriteString("* = jump gate under construction — not routable (excluded from pathing)\n")
		}
		if anyStale {
			b.WriteString("? = stale cache — construction state unverified, re-probed on next route\n")
		}
	}
	return b.String()
}
