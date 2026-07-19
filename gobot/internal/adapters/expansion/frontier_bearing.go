package expansion

import (
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// bfsBranchRoots annotates every gate-reachable system with its CORRIDOR identity: the hop-1
// ancestor on the shortest BFS path from the anchor set. It mirrors bfsHops's traversal
// exactly — multi-source, hop-bounded, under-construction edges skipped, first (shortest) visit
// wins — but carries the hop-1 root forward instead of the hop count:
//   - an anchor (hop 0) has no corridor ("");
//   - the FIRST hop out of an anchor IS a corridor root (a hop-1 system is its own root);
//   - a deeper system inherits the root of the node it was first reached through.
//
// Anchors are seeded in sorted order so the traversal is fully deterministic (edge slices already
// have a stable order), which keeps a system equidistant via two corridors mapped to a stable root
// run-to-run — the depth fan-out reads these roots to spread pathfinders across distinct branches,
// so the corridor a heavy yard could hide down is never bet on twice. It is pure over its inputs
// (no store, API, or repo), unit-testable like bfsHops/growFrontierGraph.
func bfsBranchRoots(adj map[string][]system.GateEdge, anchors map[string]bool, maxHops int) map[string]string {
	roots := make(map[string]string)
	seen := make(map[string]bool, len(anchors))

	type node struct {
		system string
		depth  int
		root   string
	}
	queue := make([]node, 0, len(anchors))
	for _, anchor := range sortedAnchors(anchors) {
		roots[anchor] = ""
		seen[anchor] = true
		queue = append(queue, node{system: anchor, depth: 0, root: ""})
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.depth >= maxHops {
			continue
		}
		for _, edge := range adj[current.system] {
			if edge.UnderConstruction {
				continue
			}
			next := edge.ConnectedSystem
			if next == "" || seen[next] {
				continue
			}
			seen[next] = true
			root := branchRootFor(current.root, next)
			roots[next] = root
			queue = append(queue, node{system: next, depth: current.depth + 1, root: root})
		}
	}
	return roots
}

// branchRootFor resolves the corridor a newly-reached neighbor belongs to: the first hop out of an
// anchor (parentRoot == "") is itself a corridor root; otherwise the neighbor inherits its parent's.
func branchRootFor(parentRoot, neighbor string) string {
	if parentRoot == "" {
		return neighbor
	}
	return parentRoot
}

// sortedAnchors returns the anchor systems in a deterministic order so the BFS seeding is stable.
func sortedAnchors(anchors map[string]bool) []string {
	out := make([]string, 0, len(anchors))
	for anchor := range anchors {
		out = append(out, anchor)
	}
	sort.Strings(out)
	return out
}
