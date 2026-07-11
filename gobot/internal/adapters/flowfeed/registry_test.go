// gobot/internal/adapters/flowfeed/registry_test.go
package flowfeed

import "testing"

func TestRegistry_PublishOverwritesByContainerID(t *testing.T) {
	r := New()
	r.Publish(Flow{ContainerID: "c1", Ship: "S1", Program: ProgramArb})
	r.Publish(Flow{ContainerID: "c1", Ship: "S1-updated", Program: ProgramArb})
	snap := r.Snapshot()
	if len(snap.Flows) != 1 {
		t.Fatalf("want 1 flow after overwrite, got %d", len(snap.Flows))
	}
	if snap.Flows[0].Ship != "S1-updated" {
		t.Errorf("want overwritten ship S1-updated, got %q", snap.Flows[0].Ship)
	}
}

func TestRegistry_RemoveDropsFlow(t *testing.T) {
	r := New()
	r.Publish(Flow{ContainerID: "c1"})
	r.Publish(Flow{ContainerID: "c2"})
	r.Remove("c1")
	snap := r.Snapshot()
	if len(snap.Flows) != 1 || snap.Flows[0].ContainerID != "c2" {
		t.Fatalf("want only c2 after remove, got %+v", snap.Flows)
	}
}

func TestRegistry_SnapshotEmptyIsNonNilSlice(t *testing.T) {
	snap := New().Snapshot()
	if snap.Flows == nil {
		t.Fatal("empty snapshot must be a non-nil slice so JSON is [], not null")
	}
	if len(snap.Flows) != 0 {
		t.Fatalf("want 0 flows, got %d", len(snap.Flows))
	}
}

func TestRegistry_SnapshotSortedByContainerID(t *testing.T) {
	r := New()
	r.Publish(Flow{ContainerID: "c3"})
	r.Publish(Flow{ContainerID: "c1"})
	r.Publish(Flow{ContainerID: "c2"})
	got := r.Snapshot().Flows
	want := []string{"c1", "c2", "c3"}
	for i, w := range want {
		if got[i].ContainerID != w {
			t.Fatalf("want sorted %v, got index %d = %q", want, i, got[i].ContainerID)
		}
	}
}

func TestRegistry_BlankContainerIDIgnored(t *testing.T) {
	r := New()
	r.Publish(Flow{ContainerID: ""})
	if len(r.Snapshot().Flows) != 0 {
		t.Fatal("blank container id must not be stored")
	}
}

func TestGlobalFreeFunctions_NilSafeAndDelegate(t *testing.T) {
	SetGlobal(nil)
	Publish(Flow{ContainerID: "x"}) // must not panic
	Remove("x")                     // must not panic

	r := New()
	SetGlobal(r)
	t.Cleanup(func() { SetGlobal(nil) })
	Publish(Flow{ContainerID: "c1"})
	if len(r.Snapshot().Flows) != 1 {
		t.Fatal("free-function Publish must delegate to the installed registry")
	}
	Remove("c1")
	if len(r.Snapshot().Flows) != 0 {
		t.Fatal("free-function Remove must delegate to the installed registry")
	}
}
