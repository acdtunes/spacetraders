sp-2ehd7 — RETIRED, DO NOT RUN OR IMPORT.

Both files carry the bid/ask transposition that sp-2ehd7 fixed:
replay_hopwiden_TEMP.py built its snapshot as bid=purchase_price, ask=sell_price, so
EVERY waypoint reported bid > ask at the same waypoint (a free-money self-spread).
probe_timing_TEMP.py imported it, so it inherited the same distortion.

Everything replay_hopwiden_TEMP.py had that the tracked harness lacked is now IN
gobot/services/routing-service/replay_objective.py with the correct orientation:
  systems_within_hops / compute_allowed   (the candidate walk at a gate-hop depth)
  effective_candidate_hop_depth           (the live depth<->cap coupling)
  widen_pass / widen_verdict              (--widen --widen-caps 2,4)
plus inter_system_hop_distances, which neither TEMP file ever fed.

Archived rather than deleted only so the provenance of any number quoted from them is
recoverable. Use replay_objective.py.
