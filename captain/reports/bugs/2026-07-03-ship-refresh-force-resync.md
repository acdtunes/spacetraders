---
title: Add `ship refresh` — a force-resync verb that reconciles the daemon ship cache from the server
status: merged
kind: feature
---

## Problem

The daemon's ship-state cache silently desyncs from the SpaceTraders server, and
there is NO Captain-accessible verb that reconciles it. Two distinct
manifestations of the same defect class have now been observed:

- **Cargo desync (phantom cargo):** `ship info` reports TORWIND-1 holding 40/40
  IRON_ORE that the game server's contract-deliver endpoint authoritatively says
  is 0 units (API 4219). A local `PURCHASE_CARGO` ledgered without the server
  adding cargo (L32).
- **Position desync:** `ship info` read the scout at waypoint H64 while the server
  said H65, crash-looping scout-tour with API 4204 "Ship is currently located at
  the destination" (L37).

It is a whole-cache-consistency defect, not one phantom field. Crucially, no
Captain verb overwrites the cache: `navigate`/`orbit`/`dock`/`refuel` return
nav+fuel only and never rewrite cargo (L34). Only a full daemon RESTART re-fetches
true ship state — and the Captain cannot trigger a restart.

## Impact

This single defect class has frozen TORWIND-1's IRON_ORE contract for **six
consecutive sessions** (decisions d-14 through d-18), each a HOLD because neither
offload path works (deliver → 4219, sell → segfault L33) and nothing reconciles
the cache. The purchase-then-deliver revenue class is unreliable fleet-wide until
either a daemon restart happens out-of-band or a resync verb exists. A daemon-side
fix for the phantom-cargo write path is now `awaiting_human`, but that fixes ONE
write path; the desync class (cargo + position) will recur, and the Captain still
has no recovery lever it can pull itself.

## Proposed feature

Add a CLI verb:

    spacetraders ship refresh --ship <SYMBOL>

Behavior: force a `GET /my/ships/<SYMBOL>` against the SpaceTraders API and
**overwrite** the daemon's local cargo + nav cache with the server response, then
print the reconciled state. The server GET is already authoritative (L32); the
daemon simply needs to write it through instead of serving stale cache.

Acceptance:
- On a ship with a phantom cargo desync, `ship refresh` makes a subsequent
  `ship info` read the server-true cargo (e.g. 0/40, clearing the phantom).
- On a position desync, `ship refresh` makes `ship info` read the server-true
  waypoint, so a relaunched scout-tour does not 4204.
- Reconciliation happens WITHOUT a daemon restart and without moving the ship.

## Expected ROI

Turns a multi-session revenue freeze into a one-command recovery whenever local
state desyncs — directly dissolving the exact pain that produced six HOLD
sessions. Also a durable trust-restorer for `ship info` and the reusable
primitive that a smarter daemon retry path can call on a deterministic 42xx
error (see backlog P7). Complementary to, and more general than, the
awaiting_human phantom-cargo daemon fix.

## Evidence

- Lessons L32 (cargo is phantom; server is ground truth), L34 (no Captain verb
  reconciles; only a restart does), L37 (whole-cache desync incl. position).
- Decisions d-12, d-13, d-14, d-16, d-17, d-18 (the six-session HOLD chain).
- Friction (s9/s10): "no `ship refresh` / force-resync verb."
- Related reports: `2026-07-02-phantom-cargo-contract-delivery.md`
  (awaiting_human), `2026-07-03-scout-position-cache-desync.md` (new).

## Suspected implementation site

The daemon's ship-cache layer that serves `GET /my/ships` responses (sibling
`../gobot` repo). The verb needs a write-through path from a fresh server GET into
that cache — the same cache a daemon restart repopulates today.
