#!/usr/bin/env python3
"""Classify every trade hull by whether we can SEE the market it is parked in.

Why this exists (sp-o2dzb -> sp-7hfgi). A natural query — "which systems have no
(buy A, sell B) pair where sell_price > purchase_price?" — is a LEFT JOIN whose
empty result means one of two completely different things:

  * we scanned the system and its spreads really are dead, or
  * we have never scanned it, so there are no rows to join and the answer is UNKNOWN.

Collapsing those two produced a wrong fleet diagnosis: six systems were reported as
having ZERO profitable intra-system pairs when four of them had never had a single
market scanned, and one of them (X1-TU10) holds 23 MARKETPLACE waypoints of which we
had prices for 2. Acting on that would have relocated hulls OFF potentially rich
markets on the strength of a measurement nobody took.

So this reports the buckets separately and never lets "unscanned" read as "poor".
`marketplace_wps` comes from the system graph, so a system with genuinely no market
is distinguishable from one we simply have not visited.
"""
import collections
import json
import subprocess

FRESH_MINUTES = 75      # mirrors the solver's MAX_SNAPSHOT_AGE_MINUTES_DEFAULT


def q(sql):
    out = subprocess.run(
        ["docker", "exec", "spacetraders-postgres", "psql", "-U", "spacetraders",
         "-d", "spacetraders", "-t", "-A", "-F", "\x1f", "-c", sql],
        capture_output=True, text=True, check=True).stdout
    return [line.split("\x1f") for line in out.splitlines() if line.strip()]


def system_of(waypoint):
    return "-".join(waypoint.split("-")[:2])


def main():
    fresh_wps, all_wps, best = collections.defaultdict(set), collections.defaultdict(set), {}
    rows = q(f"""SELECT waypoint_symbol, good_symbol, purchase_price, sell_price,
                        (last_updated > now() - interval '{FRESH_MINUTES} minutes') AS fresh
                 FROM market_data""")
    for wp, good, ask, bid, fresh in rows:
        sys_ = system_of(wp)
        all_wps[sys_].add(wp)
        if fresh != "t":
            continue
        fresh_wps[sys_].add(wp)
        ask, bid = int(ask), int(bid)
        lo, hi = best.get((sys_, good), (None, None))
        if ask > 0 and (lo is None or ask < lo):
            lo = ask
        if bid > 0 and (hi is None or bid > hi):
            hi = bid
        best[(sys_, good)] = (lo, hi)

    pair_systems = {s for (s, _g), (lo, hi) in best.items()
                    if lo is not None and hi is not None and hi > lo}

    # MARKETPLACE waypoints that EXIST, from the graph — the difference between
    # "no market here" and "we have not looked here".
    markets_in_system = {}
    for sys_, blob in q("SELECT system_symbol, (graph_data->'waypoints')::text "
                        "FROM system_graphs"):
        markets_in_system[sys_] = sum(
            1 for w in json.loads(blob).values()
            if "MARKETPLACE" in (w.get("traits") or []))

    hulls = q("SELECT ship_symbol, system_symbol FROM ships WHERE cargo_capacity >= 120")
    buckets = collections.defaultdict(list)
    for ship, sys_ in hulls:
        n_fresh, n_markets = len(fresh_wps.get(sys_, ())), markets_in_system.get(sys_, 0)
        if sys_ in pair_systems:
            bucket = "has profitable pairs (MEASURED)"
        elif n_fresh >= 2:
            bucket = "MEASURED zero profitable pairs"
        elif n_markets == 0:
            bucket = "no marketplace in-system (a real structural zero)"
        elif n_fresh == 1:
            bucket = "only 1 market scanned - a pair needs 2 (UNKNOWN)"
        elif all_wps.get(sys_):
            bucket = "scanned once but ALL rows stale (UNKNOWN)"
        else:
            bucket = "NEVER scanned (UNKNOWN, not zero)"
        buckets[bucket].append((ship, sys_, n_markets, n_fresh))

    print(f"\n=== {len(hulls)} trade hulls by whether we can SEE their home market ===")
    for bucket, members in sorted(buckets.items(), key=lambda kv: -len(kv[1])):
        print(f"\n  {bucket}: {len(members)} hulls")
        by_system = collections.defaultdict(list)
        for ship, sys_, n_markets, n_fresh in members:
            by_system[(sys_, n_markets, n_fresh)].append(ship)
        for (sys_, n_markets, n_fresh), ships in sorted(
                by_system.items(), key=lambda kv: (-len(kv[1]), kv[0][0])):
            print(f"    {sys_:10s} {len(ships):2d} hull(s)  "
                  f"marketplaces_in_system={n_markets:3d}  scanned_fresh={n_fresh:3d}")
    unknown = sum(len(v) for k, v in buckets.items() if "UNKNOWN" in k)
    print(f"\n  UNKNOWN (never treat as zero): {unknown} of {len(hulls)} hulls")


if __name__ == "__main__":
    main()
