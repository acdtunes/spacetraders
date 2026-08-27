#!/usr/bin/env python3
"""What a market VISIT costs the fleet, and how much of that cost bundling can recover.

A visit spends a fixed navigate/dock/orbit bundle whatever it trades once docked, so the
requests it costs per transaction are (bundle + n)/n — worst at one good, falling with every
further good the same dock handles. That arithmetic makes "trade more goods per visit" look
like a large lever whenever most visits trade one good. Whether it IS one depends on a
quantity the arithmetic does not contain: how many profitable goods a single hop can actually
carry between two markets. This script measures both halves so the question is decided on the
board rather than on the ratio.

WHAT IT REPORTS

  1. LIVE VISIT ECONOMICS, off tour_leg_telemetry. Each telemetry row is one transaction
     request (a tradeVolume-sized chunk), so a hull's rows group into DOCK SESSIONS — runs of
     consecutive rows at one waypoint — and a session is a visit. Per session: distinct goods,
     transaction requests, and the share of the visit's whole request bill that moved the hull
     rather than goods. Split by engine, because the tour solver, the pre-jump look-back loader
     and the liquidation path make visits of very different shapes and averaging them hides
     which one is expensive.

  2. THE BUNDLING CEILING, off market_data. For every in-system directed market pair, the
     goods a single hop could pack into one hold at the live quotes — the same greedy
     best-spread-first fill the solver's stage-1 packing bound performs, at the same A-cap
     tranche depth. Its mean is the most goods a hop can bundle even with a perfect planner,
     so it bounds every per-visit bundling lever from above.

Read them together. A low goods-per-visit beside a low ceiling is a market-structure fact and
no planner change reaches it; a low goods-per-visit beside a high ceiling is a planner defect.

WHAT IT READ ON THE BOARD IT WAS BUILT AGAINST (torwind, 3h, 2,122 dock sessions, 471 fresh
markets). Re-run before quoting any of this — the whole point of a script is that the numbers
are not doctrine.

  THE CEILING IS 1.20 GOODS PER HOP. 82.7% of tradeable in-system market pairs share exactly
  ONE good a hop can carry profitably, 14.7% two, 2.6% three or more. So the requests per
  transaction a PERFECT bundler could reach is 3.75, against 4.30 at one good — a 13% saving,
  not the halving the (bundle + n)/n arithmetic suggests when n is imagined free to grow. The
  hold is not what stops it: that best-good pack already fills 71% of a 490-unit hold, and the
  live allocator's units are bounded by sink depth far more often than by hold slack.

  THE SOLVER IS ALREADY ABOVE THAT CEILING, at 1.73 goods and 2.87 transaction requests per
  dock, because a stop is sink for several earlier buys and source for several later sells at
  once — it is not limited to one hop's shared goods. 53% of its request bill is still
  movement, and that is the floor the board allows, not slack.

  THE EXPENSIVE VISITS ARE ELSEWHERE. The pre-jump look-back loader makes 43% of all dock
  sessions at 1.13 goods and 1.13 requests each — 90.7% single-good, 74.5% of its request bill
  pure movement, 3.92 requests per transaction against the solver's 2.15. The sessions where a
  look-back load and a solver leg share one dock are the cheapest visits the fleet makes
  (3.29 goods, 1.82 requests per transaction), which is where the co-location saving actually
  sits.

Usage (from gobot/services/routing-service, with the model venv):
  python visit_economics.py [--hours 3] [--hold 490] [--tranches 3] [--min-margin 1]
      [--freshness-minutes 75] [--session-gap-minutes 20]
"""
import argparse
import collections
import os
import subprocess
import sys

SEP = "\x1f"
# The movement bundle one visit spends, from the solver's own calibration against the live
# endpoint mix. Imported rather than restated so this measurement and the solver's call model
# can never disagree about what a stop costs.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from utils.tour_solver import API_CALLS_PER_VISIT  # noqa: E402


def psql(sql):
    """Read-only query against the bot database, through the same connection variables the
    model pipeline uses so a non-default host or password needs no second configuration."""
    env = dict(os.environ,
               PGPASSWORD=os.environ.get("ST_DATABASE_PASSWORD", "dev_password"))
    out = subprocess.run(
        ["psql", "-h", os.environ.get("ST_DATABASE_HOST", "localhost"),
         "-p", os.environ.get("ST_DATABASE_PORT", "5432"),
         "-U", os.environ.get("ST_DATABASE_USER", "spacetraders"),
         "-d", os.environ.get("ST_DATABASE_NAME", "spacetraders"),
         "-t", "-A", "-F", SEP, "-c", sql],
        capture_output=True, text=True, check=True, env=env).stdout
    return [line.split(SEP) for line in out.splitlines() if line.strip()]


def dock_sessions(rows, gap_seconds):
    """Group telemetry rows into dock sessions: runs of consecutive rows for one hull at one
    waypoint. Rows must arrive sorted by (ship, time).

    A session is what costs a movement bundle. Grouping by the plan's leg index instead would
    be wrong twice over: a re-plan restarts leg numbering, so one index spans many visits, and
    a look-back load carries a sentinel index that is no plan position at all. The time gap
    splits a hull that left a waypoint and came back to it without trading in between.

    Each row is one transaction REQUEST — the executor chunks a tranche at the market's own
    tradeVolume and writes a row per chunk — so a session's row count is its request count and
    its distinct goods are what that dock actually traded."""
    sessions, cur = [], None
    for ship, wp, good, at, engine in rows:
        if (cur is None or cur["ship"] != ship or cur["waypoint"] != wp
                or at - cur["last_at"] > gap_seconds):
            cur = dict(ship=ship, waypoint=wp, goods=set(), chunks=0,
                       engines=set(), last_at=at)
            sessions.append(cur)
        cur["goods"].add(good)
        cur["engines"].add(engine)
        cur["chunks"] += 1
        cur["last_at"] = at
    return sessions


def session_report(sessions):
    """Per-engine visit economics, plus the movement share of the request bill.

    A session is attributed to the engines that traded in it; a hull that fills a look-back
    manifest at a waypoint its solver plan also trades at pays ONE movement bundle for both,
    and labelling that session with both engines is what keeps the shared saving visible."""
    by_engine = collections.defaultdict(list)
    for s in sessions:
        by_engine["+".join(sorted(s["engines"]))].append(s)
    by_engine["ALL"] = sessions
    out = []
    for engine, group in sorted(by_engine.items(), key=lambda kv: -len(kv[1])):
        visits = len(group)
        chunks = sum(s["chunks"] for s in group)
        goods = sum(len(s["goods"]) for s in group)
        movement = API_CALLS_PER_VISIT * visits
        single = sum(1 for s in group if len(s["goods"]) == 1)
        out.append(dict(engine=engine, visits=visits, chunks=chunks,
                        goods_per_visit=goods / visits if visits else 0.0,
                        chunks_per_visit=chunks / visits if visits else 0.0,
                        single_share=single / visits if visits else 0.0,
                        calls_per_transaction=((movement + chunks) / chunks
                                               if chunks else 0.0),
                        movement_share=(movement / (movement + chunks)
                                        if movement + chunks else 0.0)))
    return out


def pack_ceiling(markets, hold, tranches, min_margin):
    """Goods a single directed hop can pack, over every in-system market pair.

    The fill mirrors the solver's stage-1 packing bound: every good buyable at the source and
    sellable at the sink for at least the margin floor, best undecayed spread first, each
    capped at the A-cap tranche depth the allocator can realize, until the hold is full.
    Returns (histogram of goods packed, hold-fill fractions) over the pairs that can trade at
    all — pairs with nothing profitable are not a bundling question, they are not a hop."""
    by_system = collections.defaultdict(list)
    for wp in markets:
        by_system["-".join(wp.split("-")[:2])].append(wp)
    histogram, fills = collections.Counter(), []
    for waypoints in by_system.values():
        for src in waypoints:
            for sink in waypoints:
                if src == sink:
                    continue
                spreads = []
                for good, (ask, _bid, tv_src) in markets[src].items():
                    row = markets[sink].get(good)
                    if not row or ask <= 0:
                        continue
                    _ask, bid, tv_sink = row
                    if bid - ask >= min_margin:
                        spreads.append((bid - ask,
                                        tranches * max(1, min(tv_src, tv_sink))))
                if not spreads:
                    continue
                spreads.sort(reverse=True)
                room, packed = hold, 0
                for _spread, depth in spreads:
                    if room <= 0:
                        break
                    packed += 1
                    room -= min(room, depth)
                histogram[packed] += 1
                fills.append((hold - room) / hold)
    return histogram, fills


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--hours", type=int, default=3,
                    help="telemetry window for the live visit economics")
    ap.add_argument("--hold", type=int, default=490,
                    help="hold capacity the packing ceiling fills; 490 is the live tour hull")
    ap.add_argument("--tranches", type=int, default=3,
                    help="planned tranches per market/good side (TOUR_SOLVER_MAX_PLANNED_TRANCHES)")
    ap.add_argument("--min-margin", type=int, default=1,
                    help="per-unit margin floor a good must clear to count as packable")
    ap.add_argument("--freshness-minutes", type=int, default=75,
                    help="market_data staleness cap, mirroring the solver's backstop")
    ap.add_argument("--session-gap-minutes", type=int, default=20,
                    help="idle gap at one waypoint that starts a new dock session")
    args = ap.parse_args()

    rows = psql(f"""SELECT ship_symbol, waypoint, good,
                           extract(epoch FROM planned_at)::bigint, coalesce(engine,'')
                    FROM tour_leg_telemetry
                    WHERE planned_at > now() - interval '{args.hours} hours'
                    ORDER BY ship_symbol, planned_at""")
    sessions = dock_sessions([(r[0], r[1], r[2], int(r[3]), r[4]) for r in rows],
                             args.session_gap_minutes * 60)
    print(f"=== LIVE VISIT ECONOMICS ({len(rows)} transaction requests over "
          f"{args.hours}h, {len(sessions)} dock sessions) ===")
    print(f"  {'engine':22s} {'visits':>7s} {'goods/vis':>10s} {'txn/vis':>8s} "
          f"{'1-good':>7s} {'calls/txn':>10s} {'movement':>9s}")
    for r in session_report(sessions):
        print(f"  {r['engine']:22s} {r['visits']:7d} {r['goods_per_visit']:10.2f} "
              f"{r['chunks_per_visit']:8.2f} {r['single_share']:6.1%} "
              f"{r['calls_per_transaction']:10.2f} {r['movement_share']:8.1%}")
    print(f"  (movement = the {API_CALLS_PER_VISIT} navigate/dock/orbit requests a visit "
          f"spends before it trades anything)")

    quotes = psql(f"""SELECT waypoint_symbol, good_symbol, purchase_price, sell_price,
                             trade_volume
                      FROM market_data
                      WHERE last_updated > now() - interval '{args.freshness_minutes} minutes'""")
    markets = collections.defaultdict(dict)
    for wp, good, ask, bid, volume in quotes:
        markets[wp][good] = (int(ask), int(bid), int(volume))
    histogram, fills = pack_ceiling(markets, args.hold, args.tranches, args.min_margin)
    total = sum(histogram.values())
    print(f"\n=== THE BUNDLING CEILING ({len(markets)} fresh markets, {total} in-system "
          f"directed pairs with a tradeable good) ===")
    print(f"  goods ONE hop can pack into a {args.hold}-unit hold "
          f"({args.tranches} tranches/market/good, margin floor {args.min_margin}):")
    for packed in sorted(histogram):
        print(f"    {packed:2d} goods  {histogram[packed]:6d}  "
              f"{histogram[packed] / total if total else 0:6.1%}")
    if total:
        mean = sum(k * v for k, v in histogram.items()) / total
        print(f"  MEAN goods packable per tradeable hop: {mean:.2f}")
        print(f"  mean hold fill that pack reaches: {sum(fills) / len(fills):.1%}   "
              f"hops filling under half the hold: "
              f"{sum(1 for f in fills if f < 0.5) / len(fills):.1%}")
        print(f"  requests per transaction at that ceiling: "
              f"{(API_CALLS_PER_VISIT + mean) / mean:.2f} "
              f"(against {(API_CALLS_PER_VISIT + 1):.2f} at one good)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
