# Lessons (max 50 — curate ruthlessly)

Format: `L<N> [evidence: decision-ids] — heuristic`

L1 [seed] — Probes are cheap: keep 1 probe per 2-3 markets for price freshness
before committing haulers to a route.
<!-- Seeded from claude-captain/strategies.md. Earned lessons append below. -->
L2 [seed] — You cannot see market prices without a ship physically at the
waypoint; scouting is the only source of price intelligence.
L3 [seed] — Deploy ALL available scouts to cover ALL markets; never leave a
scout idle and never scout only a subset of markets.
L4 [seed] — Prefer solar-powered probes as scouts: zero fuel cost means infinite
runtime and they pay for themselves quickly.
L5 [seed] — Always calculate round-trip fuel (outbound + return + 10% safety
margin). A stranded ship earns zero until rescued.
L6 [seed] — Contracts pay twice (acceptance + delivery) and are guaranteed
income; prefer them over speculative mining/trading in bootstrap.
L7 [seed] — Accept a marginal or slightly-negative contract when capital allows:
it builds reputation and unlocks the next, potentially lucrative, contract.
L8 [seed] — Source contract goods from EXPORT markets (cheapest); avoid buying
at IMPORT markets (most expensive). Mine only when no market option exists.
L9 [seed] — Buy at exporters, sell at importers: this is the most reliable way
to earn credits via arbitrage.
L10 [seed] — Survey asteroid fields before mining: surveyed high-yield deposits
give ~30-50% better yields than blind extraction.
L11 [seed] — Minimum viable mining op is 1 surveyor + 2-3 drones + 1 shuttle;
add shuttles before more drones to avoid a transport bottleneck.
L12 [seed] — Over-mining collapses asteroids (yields drop 70%+). Monitor
per-asteroid yield trends and rotate fields when yields fall >30%.
L13 [seed] — Declining credits/hour despite steady yields signals market
saturation from constantly selling the same export; diversify or reduce volume.
L14 [seed] — Monitor both sides of a supply chain: rising EXPORT prices mean an
import shortage is constraining production and will break your arbitrage.
L15 [seed] — Trade routes are not static; competition equilibrates prices. Keep
multiple routes and exit any route once its margin drops below threshold.
L16 [seed] — Scale incrementally: validate profitability before buying more
ships. Premature scaling depletes credits and leaves the fleet idle.
L17 [seed] — Respect extraction cooldowns; do not attempt to extract again until
the cooldown timer has expired or the operation is wasted.
L18 [seed] — Fuel is an exchange good with volatile, agent-driven prices;
high-demand waypoints spike and can destroy a route's margin.
L19 [d-1,d-2] — The CLI has two backends: socket commands (health, ship,
container, workflow) talk to the daemon; market/ledger/player hit Postgres
directly. On SQLSTATE/DB errors from the latter while the former works, it's a
DB outage, not a total daemon failure — keep operating on socket data.
L20 [d-1,d-2] — Confirm actuation is permitted before planning actions: in
dontAsk advisory mode only allowlisted read commands run, so mutating verbs
silently deny. A blocked Captain should record a plan-of-record and surface the
block, not spin retrying denied commands.
