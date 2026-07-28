// gobot/internal/adapters/flowfeed/registry_live_test.go
//
// sp-2uvec: the feed must reflect every RUNNING trading container at REQUEST
// time. Publishing is event-sparse (plan adoption + leg arrival only) and dies
// with the process, so a published-only feed reported 5 of 13 running tours.
package flowfeed

import "testing"

// liveSourceOf returns a LiveSource over a fixed set of runs, plus a counter so a
// test can prove the source was actually consulted.
func liveSourceOf(runs ...LiveRun) (LiveSource, *int) {
	calls := 0
	return func() []LiveRun {
		calls++
		return runs
	}, &calls
}

func containerIDs(f Feed) []string {
	ids := make([]string, 0, len(f.Flows))
	for _, fl := range f.Flows {
		ids = append(ids, fl.ContainerID)
	}
	return ids
}

// A hull that joined the fleet mid-era and has not reached a publish point yet
// (repositioning, replanning, mid-first-leg) is RUNNING and must be in the feed.
func TestSnapshot_RunningContainerAppearsBeforeItEverPublishes(t *testing.T) {
	r := New()
	src, calls := liveSourceOf(
		LiveRun{ContainerID: "tour-run-SHIP-A-aaa", Program: ProgramTour, Ship: "SHIP-A"},
		LiveRun{ContainerID: "tour-run-SHIP-B-bbb", Program: ProgramTour, Ship: "SHIP-B"},
		LiveRun{ContainerID: "tour-run-SHIP-C-ccc", Program: ProgramTour, Ship: "SHIP-C"},
	)
	r.SetLiveSource(src)

	// Only the oldest hull has ever published; the two that joined later have not.
	r.Publish(Flow{ContainerID: "tour-run-SHIP-A-aaa", Program: ProgramTour, Ship: "SHIP-A"})

	got := containerIDs(r.Snapshot())
	if len(got) != 3 {
		t.Fatalf("feed under-reports the fleet: want 3 flows for 3 RUNNING containers, got %d %v", len(got), got)
	}
	if *calls == 0 {
		t.Fatal("live source was never consulted — the feed is not derived from live state")
	}
	for _, want := range []string{"tour-run-SHIP-A-aaa", "tour-run-SHIP-B-bbb", "tour-run-SHIP-C-ccc"} {
		found := false
		for _, id := range got {
			if id == want {
				found = true
			}
		}
		if !found {
			t.Errorf("RUNNING container %s missing from feed; got %v", want, got)
		}
	}
}

// RULINGS #2: a daemon restart empties the published map (a restarted process
// starts from a fresh registry) and executors only re-publish at their NEXT plan
// adoption — minutes to tens of minutes later. The feed must not go blind in
// between.
func TestSnapshot_SurvivesDaemonRestartWithZeroPublishes(t *testing.T) {
	runs := []LiveRun{
		{ContainerID: "tour-run-SHIP-A-aaa", Program: ProgramTour, Ship: "SHIP-A"},
		{ContainerID: "tour-run-SHIP-B-bbb", Program: ProgramTour, Ship: "SHIP-B"},
	}

	before := New()
	src, _ := liveSourceOf(runs...)
	before.SetLiveSource(src)
	before.Publish(Flow{ContainerID: "tour-run-SHIP-A-aaa", Program: ProgramTour, Ship: "SHIP-A"})
	before.Publish(Flow{ContainerID: "tour-run-SHIP-B-bbb", Program: ProgramTour, Ship: "SHIP-B"})
	if n := len(before.Snapshot().Flows); n != 2 {
		t.Fatalf("pre-restart: want 2 flows, got %d", n)
	}

	// Restart: a brand-new registry (published snapshots are process memory and
	// are gone), same live containers — restart recovery rebuilt the runner map.
	afterSrc, _ := liveSourceOf(runs...)
	after := New()
	after.SetLiveSource(afterSrc)

	got := containerIDs(after.Snapshot())
	if len(got) != 2 {
		t.Fatalf("restart blinded the feed: want 2 flows for 2 RUNNING containers with no publishes, got %d %v", len(got), got)
	}
}

// A published snapshot carries the plan; the placeholder must never clobber it.
func TestSnapshot_PublishedFlowWinsOverPlaceholder(t *testing.T) {
	r := New()
	src, _ := liveSourceOf(LiveRun{ContainerID: "c1", Program: ProgramTour, Ship: "SHIP-A"})
	r.SetLiveSource(src)
	r.Publish(Flow{
		ContainerID:   "c1",
		Program:       ProgramTour,
		Ship:          "SHIP-A",
		CurrentLeg:    &Leg{From: "X1-AA-B1", To: "X1-AA-B2"},
		RemainingHops: []Hop{{Waypoint: "X1-AA-B3"}},
		Projected:     &Projection{Profit: 4242},
	})

	flows := r.Snapshot().Flows
	if len(flows) != 1 {
		t.Fatalf("want 1 flow (no duplicate for the same container), got %d", len(flows))
	}
	if flows[0].CurrentLeg == nil || flows[0].CurrentLeg.To != "X1-AA-B2" {
		t.Errorf("placeholder clobbered the published leg: %+v", flows[0].CurrentLeg)
	}
	if len(flows[0].RemainingHops) != 1 {
		t.Errorf("placeholder clobbered the published hops: %+v", flows[0].RemainingHops)
	}
	if flows[0].Projected == nil || flows[0].Projected.Profit != 4242 {
		t.Errorf("placeholder clobbered the published projection: %+v", flows[0].Projected)
	}
}

// A pending run reports its identity and claims nothing it cannot know.
func TestSnapshot_PendingFlowShape(t *testing.T) {
	r := New()
	src, _ := liveSourceOf(
		LiveRun{ContainerID: "tour-run-SHIP-A-aaa", Program: ProgramTour, Ship: "SHIP-A", Closed: true},
		LiveRun{ContainerID: "arb-run-SHIP-B-bbb", Program: ProgramArb, Ship: "SHIP-B"},
	)
	r.SetLiveSource(src)

	byID := map[string]Flow{}
	for _, f := range r.Snapshot().Flows {
		byID[f.ContainerID] = f
	}

	tour := byID["tour-run-SHIP-A-aaa"]
	if tour.Ship != "SHIP-A" || tour.Program != ProgramTour {
		t.Errorf("tour identity wrong: %+v", tour)
	}
	if !tour.Closed {
		t.Error("closed-tour mode must ride through to the pending flow")
	}
	if tour.TourID == nil || *tour.TourID != "tour-run-SHIP-A-aaa" {
		t.Errorf("a tour's id is its container id, got %v", tour.TourID)
	}
	if tour.CurrentLeg != nil || tour.Projected != nil {
		t.Error("a pending flow must not invent a leg or a projection")
	}
	if tour.Cargo == nil || tour.RemainingHops == nil {
		t.Error("cargo and hops must be non-nil slices so the JSON is [], never null")
	}
	if !tour.PlannedAt.IsZero() {
		t.Error("a pending flow has no plan snapshot: plannedAt must stay zero, not now")
	}

	if arb := byID["arb-run-SHIP-B-bbb"]; arb.TourID != nil {
		t.Errorf("a non-tour program carries no tour id, got %v", arb.TourID)
	}
}

// No live source (unit tests, any path the daemon did not wire) keeps the
// published-only shape rather than blanking the feed.
func TestSnapshot_NoLiveSourceKeepsPublishedFlows(t *testing.T) {
	r := New()
	r.Publish(Flow{ContainerID: "c1"})
	if n := len(r.Snapshot().Flows); n != 1 {
		t.Fatalf("want the published flow with no live source wired, got %d", n)
	}
}

// The placeholder path must not break the deterministic payload ordering.
func TestSnapshot_PendingAndPublishedSortTogetherByContainerID(t *testing.T) {
	r := New()
	src, _ := liveSourceOf(
		LiveRun{ContainerID: "c3", Program: ProgramTour, Ship: "S3"},
		LiveRun{ContainerID: "c1", Program: ProgramTour, Ship: "S1"},
	)
	r.SetLiveSource(src)
	r.Publish(Flow{ContainerID: "c2"})

	got := containerIDs(r.Snapshot())
	want := []string{"c1", "c2", "c3"}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("want sorted %v, got %v", want, got)
		}
	}
}
