package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// NewClusterCommand builds the `cluster` verb family for contract-cluster management
// (bead sp-u9xa). A contract cluster localizes the contract-fulfilment supply chain to a
// region so the source->destination haul moves off the serialized contract critical path
// onto parallel stockers. Cluster topology lives in the daemon (single writer, RULINGS
// #3) and survives restarts; these verbs reach it only through the RPC.
//
// Two modes, both durable through the application Store:
//   - `cluster apply <spec.json>` DECLARATIVELY makes the persisted set exactly the spec.
//   - `cluster add|remove|list` and `cluster element add|remove|place` are GRANULAR live
//     ops, each persisted immediately (no restart needed).
//
// Nothing is hardcoded: every waypoint, ship, and count is operator data in the spec file
// or the flags.
func NewClusterCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Contract clusters: localize contract supply chains to a region",
		Long: `Manage contract clusters (sp-u9xa).

A contract cluster is { destination warehouse(s), stocker(s), pinned delivery hull(s),
source hub(s) } placed in a region, so the dominant source->destination haul runs on
parallel background stockers instead of the serialized contract critical path. Cluster
topology lives in the daemon and survives restarts.

  cluster apply <spec.json>   declaratively apply a whole topology at once
  cluster add|remove|list     granular cluster-level ops
  cluster element ...         granular element-level ops (add/remove/place a ship)`,
	}
	cmd.AddCommand(newClusterApplyCommand())
	cmd.AddCommand(newClusterAddCommand())
	cmd.AddCommand(newClusterRemoveCommand())
	cmd.AddCommand(newClusterListCommand())
	cmd.AddCommand(newClusterElementCommand())
	return cmd
}

func newClusterApplyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply <SPEC_FILE>",
		Short: "Declaratively apply a whole cluster topology from a JSON spec",
		Long: `Apply an entire cluster topology at once from a JSON spec file. The persisted
set becomes EXACTLY the spec: clusters in the spec are upserted, clusters no longer in it
are removed. This is the bulk equivalent of a series of 'cluster add'/'cluster remove'.

Spec format ({"clusters":[...]}):
  {
    "clusters": [
      {
        "id": "central",
        "warehouses":     [{"waypoint": "X1-A-1", "ship_symbol": "WH-1"}],
        "delivery_hulls": [{"waypoint": "X1-A-1", "ship_symbol": "DH-1"}],
        "stockers":       [{"waypoint": "X1-SRC-1", "ship_symbol": "ST-1"}],
        "source_hubs":    [{"waypoint": "X1-HUB-1"}]
      }
    ]
  }

Example:
  spacetraders cluster apply topology.json --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			clusters, err := loadClusterSpecFile(args[0])
			if err != nil {
				return err
			}

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			count, err := client.ApplyClusterTopology(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, clusters)
			if err != nil {
				return fmt.Errorf("failed to apply cluster topology: %w", err)
			}
			fmt.Printf("✓ Applied cluster topology: %d cluster(s) now persisted\n", count)
			return nil
		},
	}
	return cmd
}

func newClusterAddCommand() *cobra.Command {
	var (
		warehouses    []string
		stockers      []string
		deliveryHulls []string
		sourceHubs    []string
	)
	cmd := &cobra.Command{
		Use:   "add <CLUSTER_ID>",
		Short: "Add one cluster (granular) without disturbing the rest",
		Long: `Add a single cluster with the given id and elements, leaving the rest of the
topology untouched. A cluster needs at least one --warehouse (its routing anchor).

Each element flag is "WAYPOINT" or "WAYPOINT@SHIP" (the ship is optional — an
uncrewed slot declares sizing before a hull is pinned). Flags are repeatable.

Examples:
  spacetraders cluster add central --warehouse X1-A-1@WH-1 --delivery-hull X1-A-1@DH-1 --agent ENDURANCE
  spacetraders cluster add outpost --warehouse X1-B-2@WH-2 --stocker X1-SRC-1@ST-1 --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec, err := buildClusterDTO(args[0], warehouses, stockers, deliveryHulls, sourceHubs)
			if err != nil {
				return err
			}

			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if err := client.AddCluster(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, spec); err != nil {
				return fmt.Errorf("failed to add cluster: %w", err)
			}
			fmt.Printf("✓ Cluster added: %s\n", spec.ID)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&warehouses, "warehouse", nil, "Destination warehouse element: WAYPOINT or WAYPOINT@SHIP (repeatable; at least one required)")
	cmd.Flags().StringArrayVar(&stockers, "stocker", nil, "Background stocker element: WAYPOINT or WAYPOINT@SHIP (repeatable)")
	cmd.Flags().StringArrayVar(&deliveryHulls, "delivery-hull", nil, "Pinned delivery-hull element: WAYPOINT or WAYPOINT@SHIP (repeatable)")
	cmd.Flags().StringArrayVar(&sourceHubs, "source-hub", nil, "Source-hub element: WAYPOINT or WAYPOINT@SHIP (repeatable)")
	return cmd
}

func newClusterRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <CLUSTER_ID>",
		Short: "Remove one cluster by id (granular, idempotent)",
		Long: `Remove the cluster with the given id, leaving the rest of the topology intact.
Removing a cluster that does not exist is not an error.

Example:
  spacetraders cluster remove central --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if err := client.RemoveCluster(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol, args[0]); err != nil {
				return fmt.Errorf("failed to remove cluster: %w", err)
			}
			fmt.Printf("✓ Cluster removed: %s\n", args[0])
			return nil
		},
	}
	return cmd
}

func newClusterListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the player's contract clusters",
		Long: `List every contract cluster configured for a player: id and the count of each
element class (warehouses, stockers, delivery hulls, source hubs). Prints "No contract
clusters configured." when the player has none. Reads live daemon state.

Example:
  spacetraders cluster list --agent ENDURANCE`,
		RunE: func(cmd *cobra.Command, args []string) error {
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}
			client, err := connectDaemon()
			if err != nil {
				return err
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			clusters, err := client.ListClusters(ctx, playerIdent.PlayerID, playerIdent.AgentSymbol)
			if err != nil {
				return fmt.Errorf("failed to list clusters: %w", err)
			}
			if len(clusters) == 0 {
				fmt.Println("No contract clusters configured.")
				return nil
			}
			fmt.Printf("%-16s  %-10s  %-9s  %-14s  %s\n", "CLUSTER", "WAREHOUSE", "STOCKER", "DELIVERY-HULL", "SOURCE-HUB")
			for _, c := range clusters {
				fmt.Printf("%-16s  %-10d  %-9d  %-14d  %d\n", c.ID, len(c.Warehouses), len(c.Stockers), len(c.DeliveryHulls), len(c.SourceHubs))
			}
			return nil
		},
	}
	return cmd
}

func newClusterElementCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "element",
		Short: "Granular element ops: add/remove/place a ship in a cluster role",
		Long: `Add, remove, or place a single element in a cluster role. --role is one of
warehouse, stocker, delivery-hull, source-hub. Each op persists immediately (no restart).`,
	}
	cmd.AddCommand(newClusterElementAddCommand())
	cmd.AddCommand(newClusterElementRemoveCommand())
	cmd.AddCommand(newClusterElementPlaceCommand())
	return cmd
}

func newClusterElementAddCommand() *cobra.Command {
	var role, waypoint, ship string
	cmd := &cobra.Command{
		Use:   "add <CLUSTER_ID>",
		Short: "Add one element to a cluster role",
		Long: `Add one element to an existing cluster's named role.

Example:
  spacetraders cluster element add central --role stocker --waypoint X1-SRC-2 --ship ST-2 --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireClusterRole(role); err != nil {
				return err
			}
			return runClusterElementOp(func(ctx context.Context, client *DaemonClient, ident *PlayerIdentifier) error {
				return client.AddClusterElement(ctx, ident.PlayerID, ident.AgentSymbol, args[0], role, waypoint, ship)
			}, fmt.Sprintf("✓ Element added to %s (%s @ %s)\n", args[0], ship, waypoint))
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "Cluster role: warehouse | stocker | delivery-hull | source-hub (required)")
	cmd.Flags().StringVar(&waypoint, "waypoint", "", "Waypoint to place the element at (required)")
	cmd.Flags().StringVar(&ship, "ship", "", "Ship symbol crewing the element (optional for an uncrewed slot)")
	return cmd
}

func newClusterElementRemoveCommand() *cobra.Command {
	var role, ship string
	cmd := &cobra.Command{
		Use:   "remove <CLUSTER_ID>",
		Short: "Remove the element crewed by a ship from a cluster role",
		Long: `Remove the element crewed by --ship from an existing cluster's named role.

Example:
  spacetraders cluster element remove central --role stocker --ship ST-2 --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireClusterRole(role); err != nil {
				return err
			}
			return runClusterElementOp(func(ctx context.Context, client *DaemonClient, ident *PlayerIdentifier) error {
				return client.RemoveClusterElement(ctx, ident.PlayerID, ident.AgentSymbol, args[0], role, ship)
			}, fmt.Sprintf("✓ Element crewed by %s removed from %s\n", ship, args[0]))
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "Cluster role: warehouse | stocker | delivery-hull | source-hub (required)")
	cmd.Flags().StringVar(&ship, "ship", "", "Ship symbol crewing the element to remove (required)")
	return cmd
}

func newClusterElementPlaceCommand() *cobra.Command {
	var role, ship, waypoint string
	cmd := &cobra.Command{
		Use:   "place <CLUSTER_ID>",
		Short: "Reposition the element crewed by a ship to a waypoint",
		Long: `Reposition the element crewed by --ship in a cluster role to --waypoint (e.g.
park a delivery hull at its warehouse). The element must already exist.

Example:
  spacetraders cluster element place central --role delivery-hull --ship DH-1 --waypoint X1-A-1 --agent ENDURANCE`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireClusterRole(role); err != nil {
				return err
			}
			return runClusterElementOp(func(ctx context.Context, client *DaemonClient, ident *PlayerIdentifier) error {
				return client.PlaceClusterElement(ctx, ident.PlayerID, ident.AgentSymbol, args[0], role, ship, waypoint)
			}, fmt.Sprintf("✓ Element crewed by %s placed at %s in %s\n", ship, waypoint, args[0]))
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "Cluster role: warehouse | stocker | delivery-hull | source-hub (required)")
	cmd.Flags().StringVar(&ship, "ship", "", "Ship symbol crewing the element to reposition (required)")
	cmd.Flags().StringVar(&waypoint, "waypoint", "", "Target waypoint (required)")
	return cmd
}

// runClusterElementOp is the shared connect->call->report spine for the granular element
// verbs, so each verb body is just its client call and success line.
func runClusterElementOp(call func(context.Context, *DaemonClient, *PlayerIdentifier) error, successMsg string) error {
	playerIdent, err := resolvePlayerIdentifier()
	if err != nil {
		return err
	}
	client, err := connectDaemon()
	if err != nil {
		return err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := call(ctx, client, playerIdent); err != nil {
		return err
	}
	fmt.Print(successMsg)
	return nil
}

// clusterRoleNames is the set of valid --role values, mirroring the domain role names so
// a mistyped role is rejected client-side before the RPC.
var clusterRoleNames = map[string]bool{"warehouse": true, "stocker": true, "delivery-hull": true, "source-hub": true}

func requireClusterRole(role string) error {
	if !clusterRoleNames[role] {
		return fmt.Errorf("invalid --role %q (want one of warehouse, stocker, delivery-hull, source-hub)", role)
	}
	return nil
}

// clusterTopologyFile is the on-disk shape of a `cluster apply` spec file.
type clusterTopologyFile struct {
	Clusters []ClusterDTO `json:"clusters"`
}

// loadClusterSpecFile reads and parses a topology spec file into its clusters. It accepts
// either the wrapped object form {"clusters":[...]} or a bare array [...].
func loadClusterSpecFile(path string) ([]ClusterDTO, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cluster spec %q: %w", path, err)
	}
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "[") {
		var clusters []ClusterDTO
		if err := json.Unmarshal(data, &clusters); err != nil {
			return nil, fmt.Errorf("parse cluster spec %q: %w", path, err)
		}
		return clusters, nil
	}
	var file clusterTopologyFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse cluster spec %q: %w", path, err)
	}
	return file.Clusters, nil
}

// buildClusterDTO assembles a ClusterDTO from an id and the repeatable element flags.
func buildClusterDTO(id string, warehouses, stockers, deliveryHulls, sourceHubs []string) (ClusterDTO, error) {
	wh, err := parseClusterElementFlags(warehouses)
	if err != nil {
		return ClusterDTO{}, err
	}
	st, err := parseClusterElementFlags(stockers)
	if err != nil {
		return ClusterDTO{}, err
	}
	dh, err := parseClusterElementFlags(deliveryHulls)
	if err != nil {
		return ClusterDTO{}, err
	}
	sh, err := parseClusterElementFlags(sourceHubs)
	if err != nil {
		return ClusterDTO{}, err
	}
	return ClusterDTO{ID: id, Warehouses: wh, Stockers: st, DeliveryHulls: dh, SourceHubs: sh}, nil
}

func parseClusterElementFlags(raws []string) ([]ClusterElementDTO, error) {
	if len(raws) == 0 {
		return nil, nil
	}
	out := make([]ClusterElementDTO, 0, len(raws))
	for _, raw := range raws {
		e, err := parseClusterElementFlag(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// parseClusterElementFlag parses one element flag "WAYPOINT" or "WAYPOINT@SHIP" into a
// ClusterElementDTO. The waypoint is required; the ship is optional (an uncrewed slot).
func parseClusterElementFlag(raw string) (ClusterElementDTO, error) {
	parts := strings.SplitN(raw, "@", 2)
	waypoint := strings.TrimSpace(parts[0])
	if waypoint == "" {
		return ClusterElementDTO{}, fmt.Errorf("invalid element %q (want WAYPOINT or WAYPOINT@SHIP)", raw)
	}
	element := ClusterElementDTO{Waypoint: waypoint}
	if len(parts) == 2 {
		element.ShipSymbol = strings.TrimSpace(parts[1])
	}
	return element, nil
}
