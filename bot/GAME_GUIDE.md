# SpaceTraders Game Guide

Comprehensive guide to SpaceTraders game mechanics, operational strategies, and best practices.

## Table of Contents

1. [Getting Started](#getting-started)
2. [Core Mechanics](#core-mechanics)
3. [Fuel Management](#fuel-management)
4. [Mining Operations](#mining-operations)
5. [Marketplace Mechanics](#marketplace-mechanics)
6. [Contract Operations](#contract-operations)
7. [Common Mistakes](#common-mistakes)
8. [Emergency Procedures](#emergency-procedures)
9. [The 15 Commandments](#the-15-commandments)
10. [Quick Reference](#quick-reference)

---

## Getting Started

### Agent Setup

- **Callsign:** 3-14 characters
- **Starting Credits:** 175,000 credits
- **Faction Affiliation:** Choose at registration
- **Starting Fleet:**
  - Command Frigate: 40 cargo capacity, 400 fuel capacity
  - Probe: 0 cargo capacity, 0 fuel consumption (perfect scout)

### Universe Structure

```
Sector → System → Waypoints
```

**Waypoint Format:** `X1-MM38-A1`
- `X1` - Sector
- `MM38` - System
- `A1` - Waypoint location

**Waypoint Types:**
- Planets (can have orbital moons)
- Moons (orbit planets)
- Asteroids (mining locations)
- Orbital Stations (markets, shipyards, refineries)
- Jump Gates (system-to-system travel)

---

## Core Mechanics

### Flight Modes

| Mode | Fuel Consumption | Speed | Use Case |
|------|------------------|-------|----------|
| **CRUISE** | ~1 fuel/unit (configurable) | Fast (4 min/300 units) | Standard travel, fuel >75% |
| **DRIFT** | ~1 fuel/300 units (configurable) | Slow (35 min/300 units) | Fuel efficiency, long distances |
| **BURN** | ~2 fuel/unit (configurable) | Fastest | Emergency, short bursts |

> Flight mode multipliers and fuel rates are defined in `config/routing_constants.yaml` and can be hot-reloaded without restarting the bot.

**Flight Mode Selection Rules:**
- Fuel >75% → CRUISE (fast travel)
- Fuel 25-75% → DRIFT for long routes (>100 units), CRUISE for short
- Fuel <25% → DRIFT only (emergency mode)

### Ship Operations Workflow

```
1. ORBIT (leave dock/station)
   ↓
2. NAVIGATE (travel between waypoints)
   ↓
3. DOCK (at station/market)
   ↓
4. REFUEL / TRADE / REPAIR
```

**State Requirements:**
- **Extract Resources:** Must be IN_ORBIT at asteroid
- **Buy/Sell/Refuel:** Must be DOCKED at market
- **Navigate:** Must be IN_ORBIT (auto-orbits if DOCKED)
- **Scan:** Can do from any state

### Contracts

**Contract Types:**
- **PROCUREMENT:** Deliver goods for payment + reputation
- **TRANSPORT:** Move goods between locations
- **EXPLORATION:** Survey waypoints

**Contract Workflow:**
1. Negotiate new contract (requires any ship in fleet)
2. Review terms (payment, deadline, requirements)
3. Accept contract (2-day acceptance deadline)
4. Fulfill delivery requirements
5. Collect payment (on acceptance + on fulfillment)

**Partial Deliveries:**
- Allowed for contracts requiring >40 units
- Multiple trips automatically handled
- Progress tracked per delivery

### Mining

**Extraction Mechanics:**
- **Cooldown:** 80 seconds between extractions
- **Yield:** 2-7 units per extraction (RNG-based)
- **Success Rate:** 10-15% for targeted ores
- **Random Events:** Solar flares, asteroid impacts, ship damage

**Mining Workflow:**
1. Navigate to asteroid
2. Orbit
3. Extract resources (wait for cooldown)
4. Monitor cargo capacity
5. When cargo full → navigate to market
6. Dock → sell cargo → refuel
7. Repeat

### Trading

**Dynamic Economy:**
- Prices change based on supply/demand
- Import/Export goods per market
- Price fluctuations update frequently

**Profit Formula:**
```
Profit = (Sell Price - Buy Price) × Units - Fuel Cost - Time Cost
```

**Trading Workflow:**
1. Scout markets (use probe: 0 fuel cost)
2. Identify profitable routes
3. Buy goods at low price
4. Navigate to high-price market
5. Sell for profit
6. Repeat or find new route

---

## Fuel Management

### Critical Rules

1. **NEVER navigate without round-trip fuel**
2. **Maintain 50% minimum fuel reserves**
3. **DRIFT mode needs minimum 1 fuel** (0 fuel = ship permanently stranded)
4. **Always calculate round-trip + 10% buffer**

### Fuel Formula

```
Required Fuel = (Outbound + Return) × 1.1
Can Depart? = Current Fuel >= Required Fuel
```

**Example:**
```
Current Fuel: 400
Destination: 300 units away
CRUISE consumption: 300 fuel each way = 600 total
Buffer (10%): 660 total needed
Can depart in CRUISE? NO (use DRIFT: ~2 fuel total)
```

### Fuel Consumption by Mode

| Distance | CRUISE Fuel | DRIFT Fuel | Travel Time (CRUISE) | Travel Time (DRIFT) |
|----------|-------------|------------|----------------------|---------------------|
| 50 units | ~50 fuel | ~0.15 fuel | ~1 minute | ~8 minutes |
| 100 units | ~100 fuel | ~0.3 fuel | ~2 minutes | ~16 minutes |
| 200 units | ~200 fuel | ~0.6 fuel | ~4 minutes | ~32 minutes |
| 300 units | ~300 fuel | ~1 fuel | ~6 minutes | ~48 minutes |

### Emergency Protocol

**Fuel <25%:**
1. Immediately switch to DRIFT mode
2. Navigate to nearest marketplace
3. Do NOT attempt remote destinations
4. If <1 fuel → Ship is UNRECOVERABLE (lost)

**Fuel <10%:**
1. Use DRIFT mode exclusively
2. Calculate exact fuel to nearest market
3. If insufficient → jettison cargo for weight reduction (may help slightly)
4. Accept ship may be lost

### Planet-Moon Zero-Distance Navigation

**CRITICAL DISCOVERY:** Waypoints sharing orbital parent have **0 distance**.

**How it works:**
- Planet ↔ Moon orbiting same planet = 0 fuel, instant travel
- Check planet's `orbitals` field for moons
- Check moon's `orbits` field for parent planet
- Navigation between them costs 0 fuel

**Example:**
```
X1-HU87-A1 (planet) has orbitals: [X1-HU87-A2]
X1-HU87-A2 (moon) orbits: X1-HU87-A1

Navigation A1 → A2 or A2 → A1 = 0 fuel, instant
```

**Strategic Use:**
- Emergency refueling access (planet has fuel, you're at moon)
- Shipyard access without fuel cost
- Market arbitrage opportunities

### Flight Mode Optimization Table

| Current Fuel | Distance | Refuel Available? | Recommended Mode |
|--------------|----------|-------------------|------------------|
| >75% | Any | Yes | **CRUISE** |
| >75% | >200 units | No | **DRIFT** |
| 50-75% | <100 units | Yes | **CRUISE** |
| 50-75% | >100 units | No | **DRIFT** |
| 25-50% | Any | Unknown | **DRIFT** |
| <25% | Any | Any | **DRIFT (emergency)** |

**Common Mistake:** Staying in DRIFT after refueling >75% wastes hours.
- **Always switch to CRUISE after refueling to >75%**

---

## Mining Operations

### Resource Yield

- **RNG-based:** 10-15% success rate for targeted ores
- **Cooldown:** 80 seconds between extractions
- **Yield Range:** 2-7 units per successful extraction
- **Random Events:**
  - Solar flares (damage, delay)
  - Asteroid impacts (damage)
  - Equipment malfunctions

### Asteroid Selection

**SEEK (Good traits):**
- ✅ `COMMON_METAL_DEPOSITS` - Iron, Copper, Aluminum
- ✅ `PRECIOUS_METAL_DEPOSITS` - Gold, Silver, Platinum
- ✅ `RARE_METAL_DEPOSITS` - Uranium, rare elements
- ✅ `MINERAL_DEPOSITS` - Quartz, Silicon
- ✅ `MARKETPLACE` nearby (<200 fuel distance)

**AVOID (Bad traits):**
- ❌ `STRIPPED` - Depleted, very poor yields
- ❌ `RADIOACTIVE` - Ship damage risk
- ❌ `EXPLOSIVE_GASES` - Explosion risk
- ❌ `UNSTABLE_COMPOSITION` - Unpredictable

### Mining Best Practices

1. **Test nearby asteroids first** - Don't commit to one location
2. **Compare mining vs. buying** - Time cost matters
3. **Leave 50% cargo free** - Reserve space for contract resources
4. **Mine <200 fuel from market** - Ensure refuel access
5. **Relocate after 10 failed extractions** - If 0 target resource in 10 tries, move

### Mining vs. Buying Decision

**Calculate time cost:**
```
Mining Time = (Units Needed / Avg Yield per 80s) × 80s
Purchase Cost = Units × Market Price
Mining Revenue Lost = Time × (Credits/Hour from other operations)

If: Purchase Cost < Mining Revenue Lost → BUY
Else: MINE
```

**Example:**
```
Need: 50 IRON_ORE
Mining: ~10% success, 4 units/extraction → 125 extractions → 166 minutes
Purchase: 50 × 60 credits = 3,000 credits
Trading revenue: 150,000 credits/hour = 2,500 credits/minute

Mining cost: 166 min × 2,500 = 415,000 credits lost
Purchase cost: 3,000 credits

DECISION: BUY (saves 412,000 credits)
```

---

## Marketplace Mechanics

### Market Types

| Type | Buys From Ships | Sells To Ships | Notes |
|------|-----------------|----------------|-------|
| **HQ** | Finished goods | Food, medicine, equipment | No raw materials |
| **EXCHANGE** | Raw materials | Ores, crystals, water, gases | Best for mining sales |
| **Fuel Station** | Nothing | Fuel only | Refuel only |
| **Industrial** | Ores, raw materials | Refined metals, components | Contract deliveries |
| **Shipyard** | Varies | Ships, modules | Fleet expansion |

**Always verify market type with API before traveling.**

### Market Price Ranges (EXCHANGE)

| Resource | Buy Price (credits/unit) | Sell Price (credits/unit) |
|----------|--------------------------|---------------------------|
| ICE_WATER | 13-20 | 10-15 |
| SILICON_CRYSTALS | 30-35 | 25-30 |
| COPPER_ORE | 50-70 | 45-60 |
| IRON_ORE | 50-70 | 45-60 |
| ALUMINUM_ORE | 60-80 | 55-75 |
| QUARTZ_SAND | 20-30 | 15-25 |

**Prices fluctuate based on supply/demand.**

### Trade Route Evaluation

**Good Trade Route Criteria:**
- **Profit >150,000 credits/trip**
- **ROI >30%** (profit / investment)
- **Distance <300 units** (fuel efficiency)
- **Round-trip <30 minutes** (time efficiency)
- **Market stability** (prices don't crash after 1 trip)

**Example Good Route:**
```
Buy: SHIP_PARTS at X1-HU87-D42 (10,000 credits/unit)
Sell: SHIP_PARTS at X1-HU87-A2 (14,000 credits/unit)
Cargo: 40 units
Profit: (14,000 - 10,000) × 40 = 160,000 credits
Distance: 120 units (240 fuel total, ~5 min round-trip)
ROI: 160,000 / 400,000 = 40%
```

---

## Contract Operations

### Negotiating New Contracts

**API Endpoint:** `POST /my/ships/{shipSymbol}/negotiate/contract`

**When to Negotiate:**
- All current contracts completed
- Looking for new revenue opportunities
- Fleet has idle capacity

**How to Negotiate:**

```bash
# Using bot
python3 spacetraders_bot.py negotiate --token TOKEN --ship SHIP-1
```

**Response Fields:**
```json
{
  "id": "contract_id",
  "factionSymbol": "COSMIC",
  "type": "PROCUREMENT",
  "terms": {
    "deadline": "2025-10-11T01:01:15.291Z",
    "payment": {
      "onAccepted": 2657,
      "onFulfilled": 9993
    },
    "deliver": [{
      "tradeSymbol": "IRON",
      "destinationSymbol": "X1-JB59-G52",
      "unitsRequired": 55,
      "unitsFulfilled": 0
    }]
  },
  "accepted": false,
  "deadlineToAccept": "2025-10-05T01:01:15.291Z"
}
```

### Contract Acceptance Strategy

**Evaluate Before Accepting:**

1. **Check cargo capacity** - 40 units standard
   - Units required ≤40 → Single trip
   - Units required >40 → Multiple trips needed
   - Trips needed = ceil(units / 40)

2. **Verify resource availability**
   - Can you mine it nearby?
   - Can you buy it at reasonable price?
   - Time cost vs. payment

3. **Calculate profit**
   ```
   Total Payment = onAccepted + onFulfilled
   Total Cost = (Fuel + Purchase/Mining) × Trips
   Net Profit = Total Payment - Total Cost
   ```

4. **Check delivery distance**
   - Is delivery waypoint within fuel range?
   - Refuel stops needed?

5. **Calculate ROI**
   ```
   ROI = Net Profit / Total Cost × 100%
   ```

### Accept If:

- ✅ **Net Profit >5,000 credits**
- ✅ **ROI >5%**
- ✅ **Resource available** (can mine or buy)
- ✅ **Delivery within fuel range**
- ✅ **Time commitment reasonable** (<2 hours)

### Reject If:

- ❌ **Net profit <5,000 credits** (not worth effort)
- ❌ **Resource unavailable** (can't mine, can't buy)
- ❌ **Delivery too far** (fuel emergency risk)
- ❌ **Deadline too tight** (<1 hour to complete)

### Contract Fulfillment Workflow

```bash
# 1. Accept contract
python3 spacetraders_bot.py contract \
  --token TOKEN --ship SHIP-1 --contract-id ID

# Bot automatically:
# - Navigates to resource source (mine or buy)
# - Acquires required resources
# - Navigates to delivery waypoint
# - Docks (auto-delivers on dock)
# - Collects payment
# - Reports completion
```

**Partial Deliveries:**
- Allowed when units required >40
- Each dock at delivery waypoint delivers all matching cargo
- Multiple trips automatically handled

---

## Common Mistakes

### 1. Fuel Starvation

**Mistake:** Navigating without checking round-trip fuel.

**Consequence:** Ship stranded, 0 fuel = permanently lost.

**Fix:** Always calculate:
```
Required = (Outbound + Return) × 1.1
```

### 2. DRIFT Over-Reliance

**Mistake:** Using DRIFT when fuel is abundant (>75%).

**Consequence:** Wasting hours on travel that could be minutes.

**Fix:** Use CRUISE when fuel >75%.

### 3. Remote Mining Without Refuel Access

**Mistake:** Mining at asteroid >200 units from nearest market.

**Consequence:** Long refuel trips waste time and fuel.

**Fix:** Only mine <200 fuel from market.

### 4. Wrong Market Type

**Mistake:** Navigating to market that doesn't buy/sell needed goods.

**Consequence:** Wasted fuel and time.

**Fix:** Verify market type with API before departing.

### 5. Cargo Miscalculation

**Mistake:** Accepting contract without checking cargo capacity.

**Consequence:** Unexpected multi-trip requirement.

**Fix:** Always calculate trips needed: `ceil(units / 40)`.

### 6. Mining vs. Buying Ignored

**Mistake:** Mining for hours when buying is cheaper.

**Consequence:** Massive opportunity cost.

**Fix:** Compare time cost vs. purchase cost.

### 7. Cargo Full During Mining

**Mistake:** Mining with cargo full of non-target resources.

**Consequence:** No space for target resources, wasted extractions.

**Fix:** Leave 50% cargo free for target resources.

### 8. STRIPPED Asteroids

**Mistake:** Mining at asteroids with STRIPPED trait.

**Consequence:** 0-1% yield, complete waste of time.

**Fix:** Avoid STRIPPED asteroids completely.

---

## Emergency Procedures

### Fuel Emergency (<10% fuel)

**Immediate Actions:**
1. Switch to DRIFT mode immediately
2. Calculate fuel to nearest marketplace
3. Navigate using DRIFT only
4. If fuel <1 → Ship may be lost

**Recovery:**
- If you reach marketplace with >0 fuel → dock, refuel, continue
- If fuel reaches 0 → Ship is permanently stranded (lost)

### Ship Damage

**Damage Levels:**
- **99-100%:** Normal, no action needed
- **95-99%:** Minor damage, monitor
- **90-95%:** Moderate damage, consider repair
- **<90%:** Major damage, repair at shipyard immediately

**Repair Workflow:**
1. Navigate to shipyard
2. Dock
3. POST `/my/ships/{shipSymbol}/repair`
4. Pay repair cost
5. Continue operations

### Ship Lost (0 Fuel)

**If ship reaches 0 fuel:**
- Ship is permanently stranded
- Cannot recover (no towing in SpaceTraders)
- Write off ship value
- File captain's log entry documenting mistake
- Learn lesson: always check fuel

### Routing Validation Deviation (>5%)

- Run manual validation: `python3 spacetraders_bot.py validate-routing --player-id <ID> --ship <SHIP> --destination <WAYPOINT>`
- If time or fuel deviation exceeds configured threshold (default 5%), routing operations automatically pause (`var/routing_pause.json`)
- Tune `config/routing_constants.yaml` multipliers/fuel rates and re-run validation to clear the pause
- While paused, SmartNavigator and routing CLIs will reject new routes until validation passes

---

## The 15 Commandments

1. **Calculate round-trip fuel before navigating**
2. **Use CRUISE when fuel >75%, DRIFT only for emergencies or efficiency**
3. **Verify waypoint services before departing**
4. **Keep 50% minimum fuel reserves**
5. **Compare mining vs. buying economics**
6. **Test nearby asteroids first**
7. **Avoid STRIPPED asteroids completely**
8. **Check market type matches needs**
9. **Plan multi-trip deliveries for large contracts**
10. **Jettison non-essential cargo when needed**
11. **Never navigate to remote locations without marketplace access**
12. **Switch to CRUISE after refueling >75%**
13. **Leave 50% cargo free for contract resources**
14. **Relocate after 10 failed extractions with 0 target resource**
15. **Don't mine when buying is faster and cheaper**

---

## Quick Reference

### Fuel Planning Formula

```
Required = (Outbound + Return + 10% buffer)

Example:
- Current: 400 fuel
- Destination: 300 units (CRUISE = 300 fuel each way)
- Total needed: (300 + 300) × 1.1 = 660 fuel
- Can depart CRUISE? NO (400 < 660)
- Can depart DRIFT? YES (~2 fuel total)
```

### Flight Mode Decision Tree

```
Is fuel >75%?
├─ YES: Use CRUISE (fast)
└─ NO: Is distance >100 units?
    ├─ YES: Use DRIFT (efficient)
    └─ NO: Use CRUISE (acceptable fuel cost)
```

### Contract Quick Evaluation

```
1. Units ≤40? → Single trip
2. Resource available? → Can fulfill
3. Profit >5,000? → Worth it
4. ROI >5%? → Good deal
5. All YES? → ACCEPT
```

### Market Type Quick Reference

| Need to... | Go to... |
|------------|----------|
| Sell mining ores | EXCHANGE |
| Buy fuel | FUEL_STATION or marketplace with fuel |
| Buy equipment | HQ or shipyard |
| Deliver contract (raw) | INDUSTRIAL |
| Deliver contract (finished) | HQ |
| Buy ship | SHIPYARD |

### Emergency Contact

```
Fuel <25%: DRIFT to nearest market immediately
Fuel <10%: Emergency mode, DRIFT only
Fuel <1: Ship at risk of permanent loss
Damage <90%: Navigate to shipyard for repair
```

---

## Resources

- **API Documentation:** https://docs.spacetraders.io
- **Website:** https://spacetraders.io
- **Discord Community:** https://discord.gg/UpEfRRjjsCT
- **Game Wiki:** https://spacetraders.fandom.com (community-maintained)

---

*"Learning from mistakes. Flying smart. Building an empire." - Commander o7*
