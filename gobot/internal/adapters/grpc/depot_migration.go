package grpc

// DepotMigrationRunbook is the operator runbook (bead sp-u9xa) for migrating a player
// from the legacy per-coordinator contract fleet to the localized contract-depot model.
//
// It is deliberately a STOP-AND-APPLY sequence, NOT a data migration: no legacy
// coordinator state is ever read or absorbed. It is surfaced as a referenceable constant
// (rather than a floating comment) so it stays with the mechanism it documents.
const DepotMigrationRunbook = `
Contract-Depot Migration Runbook (sp-u9xa) — STOP-AND-APPLY

Migration to the contract-depot model is NOT a data migration. No legacy coordinator
STATE is read, copied, or absorbed. It is a stop-then-apply sequence with one strict
ordering invariant.

1. STOP the legacy coordinators (contract-fleet, warehouse, stocker) for the player.
   Stopping a coordinator RELEASES its claimed hulls through the EXISTING claim-release
   machinery (fleet unassign / ship reserve --force / ReleaseAllActive, sp-w3yd),
   returning each hull to idle. Do this FIRST: a hull still held by a running legacy
   coordinator cannot be claimed by the depot — the domain rejects the double-claim
   (Ship.AssignToContainer -> ShipAlreadyAssignedError). The stop is what frees the hull.

2. APPLY the target depot topology declaratively:
       spacetraders depot apply <spec.json>   (-> ApplyDepotTopology, Item 2 bulk)
   REUSE the legacy warehouse hull as the central depot's warehouse element (the SAME
   ship symbol) so the stock already standing in that hull is PRESERVED across the
   migration — the applied depot adopts the hull in place rather than draining and
   re-provisioning a fresh one. Waypoints, hulls, and counts are all operator data in the
   spec; nothing is hardcoded.

3. The daemon reloads the routing registry from the durable store on the next boot (or on
   demand) via LoadDepotRegistry (Item 4). The contract engine then routes contracts
   whose destinations the depot's warehouse owns to the depot's co-located delivery
   hull instead of the serialized long-haul path.

ORDERING INVARIANT (single writer, no double-claim): STOP (release) strictly precedes
APPLY/claim. A hull is never held by both the legacy coordinator and the depot at once.
Proven by TestMigration_ReleasedShipIsClaimableByAppliedDepot.

ROLLBACK: apply an empty topology (an empty spec, or 'depot remove' each depot) to
return the registry to owns-nothing — destination warehousing OFF, the regression-safe
legacy long-haul default — then restart the legacy coordinators.
`
