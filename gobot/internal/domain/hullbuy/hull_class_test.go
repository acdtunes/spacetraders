package hullbuy

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// The class→dedicated-fleet mapping is the exclusivity contract: a bought hull is stamped for its
// fleet in the same breath so no other coordinator poaches it before it reaches its role.
//
// BOTH DIRECTIONS ARE ASSERTED, and the untagged one is not the leftover case. A light hauler IS a
// HAULER worker the moment it is bought; being adopted by a factory chain, or re-dedicated by the
// depot grower, is the INTENDED outcome, so tagging it would break the adoption the depot buy path
// depends on. A hull that lands tagged when it should be untagged — or the reverse — is a
// fleet-assignment bug that surfaces far from the buy, which is why the table pins every member.
func TestDedicatedFleet_TagsEveryExclusiveClassAndLeavesWorkersUntagged(t *testing.T) {
	cases := []struct {
		name  string
		class HullClass
		want  string
	}{
		{"light is a worker on arrival — NO tag, so the grower/factory chain can adopt it", HullClassLight, ""},
		{"heavy is stamped for the trade fleet at purchase", HullClassHeavy, "trade"},
		{"explorer is stamped before the frontier loop warps it off-gate", HullClassExplorer, "explorer"},
		{"contract delivery is stamped EXCLUSIVE to the contract fleet", HullClassContractDelivery, "contract"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DedicatedFleet(tc.class); got != tc.want {
				t.Errorf("DedicatedFleet(%q) = %q, want %q", tc.class, got, tc.want)
			}
		})
	}
}

// An unrecognised class buys a PLAIN WORKER rather than inventing a tag from the class name. The
// zero value is included deliberately: a BuyOrder built without a Class must not stamp a hull into
// some fleet nobody asked for.
func TestDedicatedFleet_UnknownClassIsUntagged(t *testing.T) {
	for _, class := range []HullClass{"", "wildcat", HullClass("HEAVY")} {
		if got := DedicatedFleet(class); got != "" {
			t.Errorf("DedicatedFleet(%q) = %q, want no tag", class, got)
		}
	}
}

// The class symbols are persisted (container config, captain events, metrics labels), so renaming
// one silently re-partitions live series and orphans an operator's tuned knob. Pinned as literals.
func TestHullClass_SymbolsAreStable(t *testing.T) {
	cases := map[HullClass]string{
		HullClassLight:            "light",
		HullClassHeavy:            "heavy",
		HullClassExplorer:         "explorer",
		HullClassContractDelivery: "contract_delivery",
	}
	for class, want := range cases {
		if string(class) != want {
			t.Errorf("class symbol = %q, want %q", string(class), want)
		}
	}
}

// AN EMPTY DECLARATION IS A SILENT FLEET-WIDE STAND-DOWN. Sensing withholds treasury toward a
// heavy only while a declared owner is live, so a list emptied by a coordinator retirement leaves
// every deployment saving nothing, with the rung that answers deliberately silent.
func TestHeavyBuyerContainers_DeclaresAnOwner(t *testing.T) {
	if len(HeavyBuyerContainers()) == 0 {
		t.Fatal("no container type declares heavy buying — the heavy reservation resolves to nothing fleet-wide")
	}
}

// EXACTLY ONE declared heavy buyer. Two would let the cap resolve off whichever container id sorts
// first rather than off the coordinator that actually spends, so the withholder and the spender
// could save toward different ceilings. When heavy buying changes hands this list is REPLACED, not
// appended to.
func TestHeavyBuyerContainers_DeclaresExactlyOneOwner(t *testing.T) {
	got := HeavyBuyerContainers()
	if len(got) != 1 {
		t.Fatalf("expected exactly one declared heavy buyer, got %v", got)
	}
	if got[0] != container.ContainerTypeFleetGrowth {
		t.Fatalf("the declared owner must be the coordinator that buys heavies, got %q", got[0])
	}
}
