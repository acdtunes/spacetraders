package gategraph

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// --- sp-ry741: bounded routability (RoutableWithinJumps) ---

// RoutableWithinJumps is the routability twin of PathWithinJumps: a (bool, error) verdict at a
// CALLER-supplied jump bound. On the SAME 7-jump linear chain the strict-Path test uses (every hop
// a fresh store hit, so a nil API would panic on any fetch-through), the default Routable STILL
// caps at MaxJumpPath=5 and reports the far sink UNroutable (isolation — every existing routability
// caller is byte-identical), while RoutableWithinJumps at a large bound reports it routable. At the
// SAME MaxJumpPath bound the two agree exactly, proving the bound (not a changed resolver) is what
// admits the deeper lane. This is the resolver-level core of the long-haul Guard-0 fix: the guard's
// pre-buy delivery check must reach the 6-12 hop exotic sinks discovery ranks and the reposition flies.
func TestService_RoutableWithinJumps_HonorsCallerBoundBeyondMaxJumpPath(t *testing.T) {
	store := &freshStore{adjacency: map[string][]system.GateEdge{
		"X1-A": edgesTo("X1-B"),
		"X1-B": edgesTo("X1-C"),
		"X1-C": edgesTo("X1-D"),
		"X1-D": edgesTo("X1-E"),
		"X1-E": edgesTo("X1-F"),
		"X1-F": edgesTo("X1-G"),
		"X1-G": edgesTo("X1-H"), // H is 7 jumps from A — beyond MaxJumpPath=5
	}}
	svc := NewService(store, nil, nil, nil) // nil API: any fetch-through beyond the store would panic

	// ISOLATION: the default routability check is UNCHANGED — still capped at MaxJumpPath=5.
	ok, err := svc.Routable(context.Background(), "X1-A", "X1-H", 1)
	if err != nil {
		t.Fatalf("Routable must give a clean (false,nil) verdict at the cap, got err %v", err)
	}
	if ok {
		t.Fatal("Routable must still cap at MaxJumpPath=5 (byte-identical): a 7-jump sink is NOT routable")
	}

	// REACH: RoutableWithinJumps at a large bound reports the SAME 7-jump sink routable.
	ok, err = svc.RoutableWithinJumps(context.Background(), "X1-A", "X1-H", 1, 25)
	if err != nil {
		t.Fatalf("RoutableWithinJumps(bound=25) must not error on a reachable 7-jump sink, got %v", err)
	}
	if !ok {
		t.Fatal("RoutableWithinJumps(bound=25) must report the 7-jump sink routable — the bound must be honored")
	}

	// EQUIVALENCE at the shared bound: RoutableWithinJumps(MaxJumpPath) matches Routable exactly.
	ok, err = svc.RoutableWithinJumps(context.Background(), "X1-A", "X1-H", 1, MaxJumpPath)
	if err != nil {
		t.Fatalf("RoutableWithinJumps(MaxJumpPath) must give a clean (false,nil) verdict, got err %v", err)
	}
	if ok {
		t.Fatal("RoutableWithinJumps at MaxJumpPath must match Routable's cap (not routable)")
	}
}

// The delegation is behavior-preserving: across a routable lane, a definitively unroutable lane,
// and a store failure, Routable and RoutableWithinJumps(MaxJumpPath) — and RoutableWithinJumps(0),
// which degrades to MaxJumpPath — return IDENTICAL (bool, error) verdicts. This pins the exact error
// semantics Guard-0 depends on: a definitive no-path is (false, nil) — the clean pre-buy veto — while
// a store/fetch failure is (false, err) — fail closed, never mistaken for a clean "not routable".
func TestService_RoutableWithinJumps_MatchesRoutableAtDefaultAndZeroBound(t *testing.T) {
	boom := errors.New("db down")
	cases := []struct {
		name    string
		store   *freshStore
		wantOK  bool
		wantErr bool
	}{
		{
			name:   "routable multi-hop lane within the cap",
			store:  &freshStore{adjacency: map[string][]system.GateEdge{"X1-KA42": edgesTo("X1-PA3"), "X1-PA3": edgesTo("X1-JP61")}},
			wantOK: true,
		},
		{
			name:   "definitively unroutable lane is a clean (false,nil) veto",
			store:  &freshStore{adjacency: map[string][]system.GateEdge{"X1-KA42": edgesTo("X1-PA3"), "X1-PA3": edgesTo("X1-KA42")}}, // closed pocket
			wantOK: false,
		},
		{
			name:    "store failure fails closed (false,err)",
			store:   &freshStore{edgesErr: boom},
			wantOK:  false,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(tc.store, nil, nil, nil)

			baseOK, baseErr := svc.Routable(context.Background(), "X1-KA42", "X1-JP61", 1)
			if baseOK != tc.wantOK || (baseErr != nil) != tc.wantErr {
				t.Fatalf("Routable: got (ok=%v,err=%v), want (ok=%v,errSet=%v)", baseOK, baseErr, tc.wantOK, tc.wantErr)
			}
			// RoutableWithinJumps must match Routable at the default bound AND at 0 (which degrades to it).
			for _, bound := range []int{MaxJumpPath, 0} {
				ok, err := svc.RoutableWithinJumps(context.Background(), "X1-KA42", "X1-JP61", 1, bound)
				if ok != baseOK || (err != nil) != (baseErr != nil) {
					t.Fatalf("RoutableWithinJumps(bound=%d): got (ok=%v,err=%v), must match Routable (ok=%v,err=%v)", bound, ok, err, baseOK, baseErr)
				}
			}
		})
	}
}
