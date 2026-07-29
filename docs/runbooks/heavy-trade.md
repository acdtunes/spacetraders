# Heavy trade — operator runbook (sp-fwk8z)

The fleet buys heavy freighters and puts them on trade routes, capped by an operator dial, while a
derived reservation lets treasury accumulate toward the next one without expansion draining it first.

This document exists mostly for **one question**: *"nothing is buying — is it broken?"* Usually it is
not, and the answer is one log line away.

## A note on the numbers in this document

Two kinds of figure appear below, and they age differently.

- **Constants** (`50,000`, `200,000`, `25%`, `0.5`, `T-3h`, cap `5`) are compiled defaults, each
  named beside the rule that uses it. `200,000`, `25%`, `0.5`, `T-3h` and the cap are settable in
  `config.yaml` under `fleet_autosizer` — their keys are given below, so if you changed one,
  substitute yours. The **50,000 floor has no config path at all**.
- **Prices** (a `1,565,500` heavy ask) are **live market readings**, not constants. They move. Every
  worked example built on one is marked, and the rule beside it is what survives the change.

The autosizer's decision log prints the full arithmetic with **your** live numbers in it, every tick,
for every class. When a figure here disagrees with that line, the log is right.

## The dial: `heavy_cap`

Maximum HEAVY HULLS the fleet may own. Default **5**.

It is **not** `fleet_ceiling_heavies`, and both apply:

| Dial | Counts | Caps |
|---|---|---|
| `fleet_ceiling_heavies` | hulls tagged `dedicated_fleet="trade"` | trade-pool size |
| `heavy_cap` | heavy hulls fleet-wide, any tag | capital exposure in large hulls |

A light hauler tagged `trade` counts against the first and not the second; a heavy tagged `contract`
counts against the second and not the first.

### Two keys — which gesture does what

| Gesture | Key written | Applies |
|---|---|---|
| `config.yaml` → `fleet_autosizer.heavy_cap` | `autosizer_heavy_cap` (launch) | at container build → **needs a restart** |
| `spacetraders tune --operation autosizer heavy_cap <n>` | `heavy_cap` (live) | **next tick**, no restart |

A live value **wins** over the file while it is set. `tune heavy_cap 0` **deletes** the live key —
that is the fleet-wide revert-to-default gesture, so it falls back to the file (or the default 5).

**`heavy_cap: 0` is a HOLD ("own no heavies"), not a disable — and it is only expressible in
`config.yaml` + restart.** Set it there and both spenders agree: the autosizer buys no heavy, and
the sensing buy floor reserves nothing for one. The full sequence, which has an order that matters,
is under "Releasing the reserve deliberately" below.

## "Nothing is buying" — read this before escalating

**Four** guards can hold heavy buying, and they bind at very different times. The autosizer's
decision log names every guard **per class, every tick**, with its full arithmetic, in the form
`guard_name[BLOCK: …]`. Read that first.

### 1. `heavy_cap`: the fleet already owns its heavies

The most common reason, and the normal quiescent state once the fleet is built. The cap is written
`owned >= cap`, so an over-cap fleet (a hull acquired outside this path, or the cap tuned down below
what is already owned) also blocks.

- **Log signature:** `heavy_cap[BLOCK: heavies owned 5/5 (cap)]`
- **Action:** none. Raise the cap if you want more capital in large hulls.

The same guard **fails closed** when the heavy census cannot be read — a different situation with the
same "not buying" symptom. See "Genuinely broken instead".

### 2. `affordability`, percent term: below ~6.26M treasury the heavy is not yet *legal*

`treasury_pct` and `treasury_floor` are now ONE guard, `affordability`, carrying both terms in one
bracket. Nothing about either test changed — the merge is conjunctive, so a buy either of them used
to refuse is still refused — but the log line you match on is now a single term.

A single hull must cost ≤25% of live treasury (`heavy_treasury_pct_per_purchase`, default 25). At a
**live cheapest heavy ask of 1,565,500** that means no heavy is purchasable until treasury reaches
4 × 1,565,500 = **~6,262,000**, however healthy everything else looks.

- **Log signature:** `affordability[BLOCK: price … <= 25% × treasury … = …; treasury … − floor …]` —
  the FIRST clause is the percent term.
- **Action:** none. Wait, or lower the price basis by discovering a cheaper yard.

### 3. `affordability`, floor term: the reserve holds *other* buying down — **below** a threshold, not between two

This guard is:

```
spendable := treasury − 50,000 (the immutable reserve floor) − heavy reserve
need      := hull price + margin        (purchase_margin_over_floor, default 200,000)
allowed   ⇔ spendable >= need
```

It is **monotonically increasing in treasury**: more treasury always allows more buying, never less.
So there is no upper edge to a stand-down — buying of a given hull is held while, and only while,

```
treasury < 50,000 + heavy reserve + hull price + margin
```

and it resumes permanently above that. The heavy reserve is the **full cheapest known heavy ask**:
exactly one hull is ever reserved, never cap−owned multiples.

**Worked at a 1,565,500 ask** (a live reading — recompute at yours): the part that does not depend on
which hull is being bought is 50,000 + 1,565,500 + 200,000 = **1,815,500**. So a light hauler stands
down below roughly **1.82M plus its own ask**, and buys above it. Do not memorise a single number for
this: the light ask moves with the yard, and the log line prints it.

The **heavy itself waives its own reserve** — charging it would demand roughly twice the hull's price,
the buyer reserving against itself. Its floor bound is therefore 50,000 + price + margin, but the
heavy is dominated by guard 2 at ~6.26M, which binds far higher.

**Probes stand down on the same shape but not the same number.** The sensing buy floor is
`50,000 + capex reserve (which now includes the heavy reserve) + K hours of the trading fleet's cargo
runway`, and it stops when `treasury − probe quote` falls below that. Same three ingredients — the
immutable floor, the heavy reserve, the hull's own price — with a cargo-runway term where the
autosizer has its flat margin. So probe buying stands down at its *own* threshold, near the light
one but not equal to it. The sensing heartbeat prints the floor it actually used; do not compute the
probe boundary from the light one.

So, at a given treasury:

| Treasury | What you see | Correct? |
|---|---|---|
| below ~1.82M + the hull's own ask | that hull's buying stands down; the reserve is holding. Lights and probes cross their own thresholds near here, not at the same credit | yes — **this is the stand-down**, and this section is about it |
| above that, up to ~6.26M | lights and probes buy normally; still no heavy | yes — the heavy is not legal yet (guard 2) |
| above ~6.26M | the heavy becomes purchasable; the reserve clears when it lands | yes |

- **Log signature:** the light class blocked by `affordability` with `− heavy reserve N` **inside
  its arithmetic** (the SECOND clause, after the `;`), while the heavy class is blocked by the
  percent term of its own `affordability` line. On the heavy's own line the
  same guard reads `(own reserve waived: N)` — that is the waiver, not a dropped reserve.
- **This is the distinction that matters.** The floor clause with a reserve in it = *saving*.
  The percent clause = *not yet affordable*. Both at once = the low-treasury case above, and it
  resolves itself as treasury grows. Read WHICH clause of the `affordability` bracket carries the
  failing arithmetic — the guard name alone no longer tells them apart.
- **Confirming series:** `spacetraders_daemon_autosizer_heavy_reserve_credits` non-zero, and the
  sensing heartbeat reading `buy floor, N reserved for the next heavy`.
- **Action:** none, unless you want expansion to resume sooner — see "Releasing the reserve
  deliberately".

### 4. ~~`era_payback`~~ — DELETED, and it is not coming back

There is no longer an era-clock payback guard, and no `payback_safety_factor` /
`purchase_cutoff_at_era_minus_hours` knob. It required a marginal realized $/hr it could never
actually read in production (`marginal rate unreadable/zero — cannot prove payback`), so instead of
refusing bad buys near an era boundary it refused **every** buy, forever. The `realized_rate` guard
went with it for the same class of reason (it blocked on a declining aggregate rate while its own
detail conceded the case did not apply). If you are triaging an old log that shows either name, you
are reading history — the current chain is
`demand → class_ceiling → per_tick_cap → price → heavy_cap → affordability → api_util`.

**Consequence to know:** the autosizer no longer forms any opinion on whether a heavy will EARN, or
on whether it can pay back before a universe reset. Near an era boundary it will still buy if demand,
price and treasury permit. If that matters for a given reset, stop the coordinator.

## Releasing the reserve deliberately

To stop saving for a heavy, hold the fleet at zero heavies. **The order matters**, because the
precedence table above cuts against the obvious sequence: a positive **live** key outranks the file,
so editing `config.yaml` and restarting changes nothing while one is set.

1. `spacetraders tune --operation autosizer heavy_cap 0` — this **deletes** the live key. Skipping
   this step is the failure mode: an existing live tune of, say, 3 keeps winning after the restart.
2. Set `fleet_autosizer.heavy_cap: 0` in `config.yaml`.
3. Restart the container.

The reserve reads 0 from the first tick after the restart, and probe and light buying resume at the
floor they would have had without it.

Expect a transient between steps 1 and 3: deleting the live key makes the autosizer fall back to the
**file** value immediately, which is still the old cap (or the default 5) until step 3 takes effect.
The reserve can reappear for those few minutes. That is the fallback working, not the procedure
failing.

An explicit `0` survives the launch resolve — the config field is a pointer precisely so that zero
reads as a choice rather than an unset knob. A **negative** value does not: it resolves to the
default 5, so a typo cannot quietly disable heavy buying while looking deliberate.

## The reserve went blind (degraded, not broken)

An unreadable heavy census or an unreadable yard-price read **reserves 0** and says so:

```
WARNING  Sensing heavy reserve: could not read the heavy <input> (<err>) — reserving NOTHING this
         tick, so probe buying is NOT standing down for a heavy. …
         action=sensing_heavy_reserve_blind  input=census|yard prices  error=…  player_id=…
```

It sits beside `sensing_heavy_cap_unresolved`, which is the same shape for the cap read.

**Why zero and not a hold.** A reserve that "failed closed" by holding treasury on a signal it cannot
read would starve expansion indefinitely on a database blip. Reserving nothing *releases* treasury,
and it weakens no money guard: this predicate authorises no spend of its own, and the heavy purchase
stays behind the autosizer's full fail-closed guard stack.

**What to conclude.** On a database fault **both spenders now proceed unreserved**. The fleet looks
completely healthy and simply never accumulates toward a heavy — nothing else changes, no buy is
refused, no series drops out. This WARN is the only way to see it. **Silence means healthy; the WARN
means blind, not broken.** If it persists, fix the read; the fleet will not accumulate a heavy while
it does.

Not this: **no autosizer container at all** (or a terminal one) is deliberately **silent**. There is
no heavy buyer, so there is nothing to save for, and a probe-only deployment would otherwise warn
every tick.

## A held-open reserve with no purchase — the diagnostic ladder

A non-zero reserve that never resolves into a heavy is *usually* expected accumulation. It can also
be a stale cap or a genuinely stuck buyer, and those look identical from the reserve alone. Walk this
top to bottom; each rung is cheaper than the one after it.

1. **Is the reserve being recomputed at all?**
   `spacetraders_daemon_autosizer_heavy_reserve_credits` is emitted **every tick**, whatever the
   outcome. Absent → the autosizer is not running, or metrics are off; nothing below applies. The
   value is derived per tick and never stored, so a *frozen* line means frozen inputs, not a stale
   cache.
2. **Do the two spenders agree?** The heartbeat's `buy_heavy_reserve` must equal that gauge. Both
   call the same predicate, so a disagreement means one side is on a blind read — rung 3.
3. **Is either side blind?** `sensing_heavy_reserve_blind` or `sensing_heavy_cap_unresolved` in the
   log. Either means sensing is reserving **nothing**: probe buying is proceeding unreserved and
   treasury is not accumulating. That is the degraded path above, not a stuck buyer.
4. **Is the cap already met?** Compare `spacetraders_daemon_autosizer_heavies_owned` against
   `spacetraders_daemon_autosizer_heavy_cap`. Owned **at or above** cap →
   `heavy_cap[BLOCK: heavies owned N/N (cap)]`, the reserve is correctly 0, and there is nothing to
   wait for. If `heavies_owned` reads **0**
   *alongside* `heavy_cap[BLOCK: heavy census unreadable …]`, the read failed — see "Genuinely
   broken instead".
5. **Is the heavy simply not legal yet?** Read the percent clause of the heavy class's
   `affordability` line; it prints `price P <= 25% × treasury T = C`. While C < P no other guard's
   opinion matters. At a 1,565,500 ask that clears at ~6,262,000 of treasury. Expected — wait.
6. **Only if every rung above is clean** is the buyer genuinely stuck. Escalate with the heavy
   class's full `Autosizer heavy buy-decision (…)` line, which carries every guard's arithmetic.

## Genuinely broken instead

- `spacetraders_daemon_autosizer_heavy_reserve_credits` **absent** → the autosizer is not running or
  metrics are off.
- `heavy_cap[BLOCK: heavy census unreadable (cap N) — fail closed]` → the owned-heavy census could
  not be read, so the cap guard fails closed and no heavy is bought. **`heavies_owned` will read 0 on
  the dashboard in this state, and the reserve with it** — because the reservation is only derived
  once the census is readable. Zero-and-zero here is the *unreadable* signature, **not** a fleet that
  owns no heavies; on the panel it is indistinguishable from STUCK. This log line is what tells them
  apart.
- `WARNING … sensing_heavy_cap_unresolved` → sensing cannot read the autosizer's cap, so it is
  reserving **nothing**. Treasury will not accumulate toward a heavy while this persists. Check the
  autosizer container is RUNNING and its config is readable.
- `WARNING … sensing_heavy_reserve_blind` → the census or the yard-price read failed. Same
  consequence, different input. See "The reserve went blind" above.
- `WARNING … heavy_census_unrecognised_frame` → a large hull carried a frame the known-heavy list
  does not contain. It was counted as heavy (the safe direction), and **the frame list needs that
  symbol added**. This is expected once, the first time a real heavy is seen.

  **Watch for this on the first deploy.** It is the only correction signal the inferred heavy-frame
  list has, it fires at most once per heavy acquired, and it is a single WARNING in a busy daemon
  log — easy to miss, and there is no second chance for that hull.

## Grading a heavy — read this before tuning the cap

**Early heavy-vs-light readings are a FLOOR on the true advantage, not an estimate** (bead
`sp-wl35j`).

The per-tour spend cap is a cumulative **25% of live treasury and is hull-blind** — a heavy and a
light get the same credits per tour at equal treasury. So a heavy sails partly empty for *budget*
reasons whenever `unit_price × hold_capacity > 0.25 × treasury`, which is ~2.8× easier to hit for a
225-slot hold than an 80-slot one. Worked example: at 2M treasury the tour budget is 500k — enough
for 80 light slots at 6,250/unit but only 2,222/unit across 225 heavy slots.

This systematically **understates** the heavy's advantage exactly in the thin-treasury window when
the first heavy lands — i.e. when you would first be tempted to measure it.

**Do not tune `heavy_cap` down on early readings.** Wait until treasury is comfortably above the
point where the tour budget can fill a heavy hold.

## Series

The first three are on the `Heavy reservation vs probe buying` panel in the **performance**
dashboard; the premium has its own panel beside it (`Heavy price premium`).

| Metric | Panel | Meaning |
|---|---|---|
| `spacetraders_daemon_autosizer_heavy_reserve_credits{player_id}` | Heavy reservation vs probe buying | credits held for the next heavy; non-zero = saving, not stuck |
| `spacetraders_daemon_autosizer_heavies_owned{player_id}` | Heavy reservation vs probe buying | heavy hulls owned, tag-independent. Reads 0 when the census is unreadable — check the log before believing it |
| `spacetraders_daemon_autosizer_heavy_cap{player_id}` | Heavy reservation vs probe buying | the cap in force this tick, after the live-config read |
| `spacetraders_daemon_autosizer_heavy_price_premium_percent` | Heavy price premium | per purchase: % above the cheapest KNOWN ask — the measured cost of buying at the cheapest yard *with presence*. **Query it as a mean** — see below |

The reservation panel reads **credits on the left axis and hull/placement counts on the right**; they
differ by about six orders of magnitude, so on one axis every count sits flat on zero.

### The premium is a mean, and only a mean

`autosizer_heavy_price_premium_percent` is a Prometheus **summary declared with no quantile
objectives**, so only two series exist:

```
spacetraders_daemon_autosizer_heavy_price_premium_percent_sum
spacetraders_daemon_autosizer_heavy_price_premium_percent_count
```

There is **no** `…_percent{player_id}` series and no `{quantile=…}` series. A query for the bare
metric name returns no data, permanently — it is not a broken exporter.

Read it as the ratio:

```promql
# mean premium since the daemon started — populated as soon as ONE heavy has ever been bought
spacetraders_daemon_autosizer_heavy_price_premium_percent_sum
  / spacetraders_daemon_autosizer_heavy_price_premium_percent_count

# mean premium over the dashboard's visible range (trend)
increase(spacetraders_daemon_autosizer_heavy_price_premium_percent_sum[$__range])
  / increase(spacetraders_daemon_autosizer_heavy_price_premium_percent_count[$__range])
```

Prefer a **wide** window on the second form. A heavy is bought a handful of times per era, so a short
one (`$__rate_interval`) will contain no observation and render empty almost always. Empty means "no
heavy bought in this window", not "no data collected".

Objectives are absent deliberately, and adding them would not help: quantiles over a sliding window
at that sample count are noisy and stale, and the mean is the honest read. The metric's own Help text
describes a distribution ("a persistent positive is what presence-lag costs") — read that as a
statement about the mean over time, which is all this type can tell you.
