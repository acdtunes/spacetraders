// Package harness is a DB-seeded verification harness for the capacity
// reconciler. It is verification-FIRST: the harness itself is the deliverable.
//
// Every scenario seeds a REAL test database with a demand / contract-history /
// treasury world, constructs the reconciler from the REAL domain components
// (the adapter Sensor over the seeded DB -> HeuristicPlanner -> LadderDiffer),
// spies ONLY at the actuation boundary (Actuator, ProposalChannel) plus the
// kill switch, drives N ticks through the coordinator's SetTickObserver seam,
// and asserts convergence + the CONTRACTS.md safety invariants against the
// stream of TickOutcomes. It needs no twin and no full-daemon boot.
//
// The Governor is the one component NOT driven from main: the real capex
// governor is not merged (only NoOpGovernor is), so the harness supplies a
// faithful stand-in that applies the DOCUMENTED Govern contract (autonomous
// tiers -> Approved, capital -> Proposal) plus a deliberately-misbehaving
// variant used to prove the CONVERGE capital backstop is structural. Once the
// real governor lands, it drops in behind the same seam.
//
// All logic lives in the _test.go files of this package (test-only): nothing
// here ships in a production binary, and the harness changes no production
// behaviour.
package harness
