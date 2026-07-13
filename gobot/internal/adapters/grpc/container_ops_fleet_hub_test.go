package grpc

import (
	"encoding/json"
	"reflect"
	"testing"

	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
)

// These tests cover the daemon-side hub mutation + its restart-resilience
// (sp-jcke, RULINGS #2/#3). The daemon is the single writer of the persisted
// standby-station set: it reads the coordinator's container config, applies the
// add/remove, and writes it back. The core is exercised over the config MAP (the
// same shape the daemon reads/writes) so no live DB is needed; the GORM find +
// UpdateContainerConfig around it is thin plumbing.

// TestMutateStandbyStationsConfig_Add appends a hub to the config's set and marks
// it changed, leaving the set readable back as the resulting set.
func TestMutateStandbyStationsConfig_Add(t *testing.T) {
	config := map[string]interface{}{"standby_stations": []interface{}{"X1-TW-A1"}}

	result, changed := mutateStandbyStationsConfig(config, "X1-TW-B2", true)

	if !changed {
		t.Fatalf("adding a new hub must report changed=true")
	}
	if !reflect.DeepEqual(result, []string{"X1-TW-A1", "X1-TW-B2"}) {
		t.Fatalf("expected [A1 B2], got %v", result)
	}
	// The config map must carry the mutated set so the caller persists it.
	back, _ := standbyStationsFromConfigMap(config)
	if !reflect.DeepEqual(back, []string{"X1-TW-A1", "X1-TW-B2"}) {
		t.Fatalf("mutation must be written back into the config map, got %v", back)
	}
}

// TestMutateStandbyStationsConfig_RemoveNoOp: removing an absent hub reports
// changed=false so the daemon can skip a redundant DB write.
func TestMutateStandbyStationsConfig_RemoveNoOp(t *testing.T) {
	config := map[string]interface{}{"standby_stations": []interface{}{"X1-TW-A1"}}

	result, changed := mutateStandbyStationsConfig(config, "X1-TW-Z9", false)

	if changed {
		t.Fatalf("removing an absent hub must be a no-op (changed=false)")
	}
	if !reflect.DeepEqual(result, []string{"X1-TW-A1"}) {
		t.Fatalf("a no-op remove must preserve the set, got %v", result)
	}
}

// TestStandbyStationsFromConfig decodes the persisted set from a container config
// JSON string — the read side of the live provider.
func TestStandbyStationsFromConfig(t *testing.T) {
	cases := []struct {
		name string
		json string
		want []string
	}{
		{"set", `{"standby_stations":["X1-TW-A1","X1-TW-B2"]}`, []string{"X1-TW-A1", "X1-TW-B2"}},
		{"empty-config", ``, nil},
		{"absent-key", `{"dedicated_ships":["TORWIND-1"]}`, nil},
		{"empty-set", `{"standby_stations":[]}`, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := standbyStationsFromConfig(tc.json)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("want %v, got %v", tc.want, got)
				}
			}
		})
	}
}

func TestStandbyStationsFromConfig_Malformed_Errors(t *testing.T) {
	if _, err := standbyStationsFromConfig(`{not json`); err == nil {
		t.Fatalf("malformed config JSON must return an error, not silently empty")
	}
}

// TestHubChange_SurvivesRestart_NotResurrectedBySeed is the RULINGS #2 guarantee:
// a live hub change survives a daemon restart and is NOT reverted by the launch
// --standby-stations seed. It drives the full realistic cycle over the config the
// daemon persists:
//
//  1. The coordinator is created with the launch flag standby_stations=[A,B].
//  2. `fleet hub add C` mutates the persisted config in place → [A,B,C].
//  3. RESTART: the config is round-tripped through JSON (as a reload does) and the
//     coordinator command is rebuilt from it (buildContractFleetCoordinatorCommand).
//
// The rebuilt command must carry the LIVE set [A,B,C], never the original launch
// snapshot [A,B] — proving the live change survived and the launch flag did not
// resurrect the stale set on restart.
func TestHubChange_SurvivesRestart_NotResurrectedBySeed(t *testing.T) {
	const containerID = "contract_fleet_coordinator-player-2-abcd1234"

	// 1. Launch config, exactly as ContractFleetCoordinator persists it.
	launchConfig := map[string]interface{}{
		"container_id":     containerID,
		"standby_stations": []string{"X1-TW-A1", "X1-TW-B2"},
		"dedicated_ships":  []string{"TORWIND-7"},
	}

	// 2. `fleet hub add C` mutates the persisted config in place.
	_, changed := mutateStandbyStationsConfig(launchConfig, "X1-TW-C3", true)
	if !changed {
		t.Fatalf("adding a new hub must change the persisted config")
	}

	// 3. RESTART: reload the persisted config through a JSON round-trip (the
	// recovery path: numbers/slices come back as float64/[]interface{}), then
	// rebuild the coordinator command from it.
	persisted, err := json.Marshal(launchConfig)
	if err != nil {
		t.Fatalf("marshal persisted config: %v", err)
	}
	var reloaded map[string]interface{}
	if err := json.Unmarshal(persisted, &reloaded); err != nil {
		t.Fatalf("reload persisted config: %v", err)
	}

	rebuilt := buildContractFleetCoordinatorCommand(newConfigReader(reloaded), 2, containerID)
	cmd, ok := rebuilt.(*contractCmd.RunFleetCoordinatorCommand)
	if !ok {
		t.Fatalf("unexpected command type %T", rebuilt)
	}

	if !reflect.DeepEqual(cmd.StandbyStations, []string{"X1-TW-A1", "X1-TW-B2", "X1-TW-C3"}) {
		t.Fatalf("live hub change must survive restart intact (not resurrected by the launch seed), got %v", cmd.StandbyStations)
	}
}

// TestHubRemove_SurvivesRestart is the symmetric case: a live `fleet hub remove`
// of a launch-seeded hub survives the restart — the removed hub is NOT resurrected
// by the launch --standby-stations flag.
func TestHubRemove_SurvivesRestart(t *testing.T) {
	const containerID = "contract_fleet_coordinator-player-2-abcd1234"

	launchConfig := map[string]interface{}{
		"container_id":     containerID,
		"standby_stations": []string{"X1-TW-A1", "X1-TW-B2"},
	}

	_, changed := mutateStandbyStationsConfig(launchConfig, "X1-TW-A1", false)
	if !changed {
		t.Fatalf("removing a present hub must change the persisted config")
	}

	persisted, _ := json.Marshal(launchConfig)
	var reloaded map[string]interface{}
	if err := json.Unmarshal(persisted, &reloaded); err != nil {
		t.Fatalf("reload: %v", err)
	}

	rebuilt := buildContractFleetCoordinatorCommand(newConfigReader(reloaded), 2, containerID)
	cmd := rebuilt.(*contractCmd.RunFleetCoordinatorCommand)

	if !reflect.DeepEqual(cmd.StandbyStations, []string{"X1-TW-B2"}) {
		t.Fatalf("live hub removal must survive restart (removed hub not resurrected), got %v", cmd.StandbyStations)
	}
}
