# Multileg Trading Circuit Breaker System - Intelligent Segment Independence Analysis

## Executive Summary

The current circuit breaker system operates with an **all-or-nothing failure model**: ANY segment failure terminates the ENTIRE multileg operation, even when remaining segments are independent and profitable. This is overly conservative and wastes profitable trading opportunities.

**Problem:** A CLOTHING price spike in segment 3 shouldn't prevent executing an independent SHIP_PARTS trade in segment 4 using different goods and markets.

**Solution:** Implement **dependency-aware circuit breakers** with **segment independence detection** and **smart cargo cleanup** to skip failed segments while continuing with viable independent segments.

---

## 1. Dependency Analysis Framework

### 1.1 Segment Dependency Types

A multileg route creates three types of dependencies:

#### **Type A: Cargo Dependency (Chained)**
```python
Segment 2 buys X → Segment 3 sells X
```
- Segment 3 **REQUIRES** segment 2's cargo
- If segment 2 fails to acquire cargo, segment 3 MUST be skipped
- **Example:** Seg 2 buys COPPER at D45 → Seg 3 sells COPPER at J62

#### **Type B: Credit Dependency (Weakly Chained)**
```python
Segment 2 sells X for 50k credits → Segment 3 needs 40k to buy Y
```
- Segment 3 needs revenue from segment 2 to afford purchase
- **Recoverable:** If partial revenue sufficient, segment 3 may proceed with reduced units
- **Example:** Seg 2 sells for 50k → Seg 3 needs 40k to buy SHIP_PARTS

#### **Type C: Independence (Parallel)**
```python
Segment 2: Buy/sell MEDICINE
Segment 3: Buy/sell SHIP_PARTS (different good)
```
- Completely independent goods and markets
- Segment 3 failure **DOES NOT** affect segment 4 viability
- **Example:** CLOTHING failure in seg 3 shouldn't kill SHIP_PARTS opportunity in seg 4

### 1.2 Dependency Graph Model

```python
@dataclass
class SegmentDependency:
    """Captures dependencies between route segments"""
    segment_index: int
    depends_on: List[int]  # Indices of prerequisite segments
    dependency_type: str    # 'CARGO', 'CREDIT', 'NONE'
    required_cargo: Dict[str, int]  # {good: units} needed from prior segments
    required_credits: int   # Minimum credits needed to execute
    can_skip: bool         # True if segment can be skipped without breaking route

@dataclass
class SegmentNode:
    """Enhanced route segment with dependency metadata"""
    segment: RouteSegment
    index: int
    dependencies: SegmentDependency
    independent_of: Set[int]  # Segments this is NOT dependent on
    goods_used: Set[str]       # Trade goods involved
    markets_used: Set[str]     # Markets involved
```

**Dependency Detection Algorithm:**
```python
def build_dependency_graph(route: MultiLegRoute) -> Dict[int, SegmentDependency]:
    """
    Analyze route and build dependency graph

    Returns:
        Map of segment_index → SegmentDependency
    """
    dependencies = {}

    for i, segment in enumerate(route.segments):
        dep = SegmentDependency(
            segment_index=i,
            depends_on=[],
            dependency_type='NONE',
            required_cargo={},
            required_credits=0,
            can_skip=True
        )

        # Check CARGO dependencies: Do we need to SELL goods from prior segments?
        sell_actions = [a for a in segment.actions_at_destination if a.action == 'SELL']
        for sell_action in sell_actions:
            good = sell_action.good
            units = sell_action.units

            # Find which prior segment(s) provided this cargo
            for j in range(i):
                prior_segment = route.segments[j]
                prior_buy_actions = [a for a in prior_segment.actions_at_destination
                                    if a.action == 'BUY' and a.good == good]

                if prior_buy_actions:
                    dep.depends_on.append(j)
                    dep.dependency_type = 'CARGO'
                    dep.required_cargo[good] = dep.required_cargo.get(good, 0) + units
                    dep.can_skip = False  # Cannot skip if cargo-dependent

        # Check CREDIT dependencies: Do we need revenue to afford purchases?
        buy_actions = [a for a in segment.actions_at_destination if a.action == 'BUY']
        total_buy_cost = sum(a.total_value for a in buy_actions)

        if total_buy_cost > 0:
            # Calculate if prior segment revenue is needed
            dep.required_credits = total_buy_cost

            # If current credits insufficient, depend on prior segments
            # (This is checked dynamically during execution)

        dependencies[i] = dep

    return dependencies
```

---

## 2. Improved Circuit Breaker Rules

### 2.1 Segment-Level Circuit Breaker Design

**Current Behavior (Monolithic):**
```python
# ANY failure kills ENTIRE route
if segment_fails:
    cleanup_cargo()
    return False  # Abort all remaining segments
```

**Proposed Behavior (Granular):**
```python
# Segment failure triggers smart recovery
if segment_fails:
    failure_reason = analyze_failure(segment)

    if is_recoverable(failure_reason, segment, remaining_segments):
        skip_failed_segment(segment)
        cleanup_stranded_cargo_from_segment(segment)
        continue_with_independent_segments(remaining_segments)
    else:
        # Only abort if failure cascades to all remaining segments
        cleanup_all_cargo()
        return False
```

### 2.2 Decision Tree for Segment Failures

```
SEGMENT FAILURE DETECTED
│
├─ Check: Can remaining segments execute WITHOUT this segment's output?
│  │
│  ├─ YES (Independent segments exist)
│  │  │
│  │  ├─ Cleanup stranded cargo from failed segment
│  │  ├─ Recalculate route profitability (skip failed + dependent segments)
│  │  │
│  │  ├─ Is remaining route still profitable (> min_profit)?
│  │  │  │
│  │  │  ├─ YES → Skip failed segment, continue with independents
│  │  │  └─ NO → Abort entire operation (not worth fuel/time)
│  │  │
│  │  └─ Navigate to next viable segment
│  │
│  └─ NO (All remaining segments depend on failed segment)
│     │
│     └─ Abort entire operation
│        └─ Cleanup all cargo
│
└─ Return execution status (True if ANY segment succeeded, False if total failure)
```

### 2.3 Specific Circuit Breaker Triggers

#### **Trigger 1: Buy Price Spike (Lines 800-811)**
```python
# BEFORE
if price_spike > 30%:
    cleanup_cargo()
    return False  # KILLS ENTIRE ROUTE

# AFTER
if price_spike > 30%:
    log_segment_failure(segment, "buy_price_spike")

    # Check if remaining segments are independent
    if has_independent_segments(segment_index, route):
        skip_segment(segment_index)
        continue_route(segment_index + 1)  # Try next segment
    else:
        cleanup_cargo()
        return False
```

#### **Trigger 2: Sell Price Crash (Lines 872-883)**
```python
# AFTER
if price_crash < -30%:
    log_segment_failure(segment, "sell_price_crash")

    # Check if we're holding cargo for THIS segment only
    if cargo_for_segment(segment) > 0:
        # Try to salvage at emergency market (accept any price)
        emergency_sell(cargo_for_segment(segment))

    # Skip this segment, try independents
    if has_independent_segments(segment_index, route):
        skip_segment(segment_index)
        continue_route(segment_index + 1)
    else:
        cleanup_cargo()
        return False
```

#### **Trigger 3: Segment Unprofitable (Lines 933-941)**
```python
# AFTER
if segment_profit < 0:
    log_segment_failure(segment, "unprofitable")

    # This is EXPECTED for intermediate segments that just reposition
    # Only fail if cumulative profit dropped
    if cumulative_profit_decreased:
        # Check independence
        if has_independent_segments(segment_index, route):
            skip_segment(segment_index)
            continue_route(segment_index + 1)
        else:
            cleanup_cargo()
            return False
```

---

## 3. Implementation Strategy

### 3.1 Phase 1: Add Dependency Metadata to RouteSegment

**Current RouteSegment:**
```python
@dataclass
class RouteSegment:
    from_waypoint: str
    to_waypoint: str
    distance: int
    fuel_cost: int
    actions_at_destination: List[TradeAction]
    cargo_after: Dict[str, int]
    credits_after: int
    cumulative_profit: int
```

**Enhanced RouteSegment:**
```python
@dataclass
class RouteSegment:
    # Existing fields...
    from_waypoint: str
    to_waypoint: str
    distance: int
    fuel_cost: int
    actions_at_destination: List[TradeAction]
    cargo_after: Dict[str, int]
    credits_after: int
    cumulative_profit: int

    # NEW: Dependency tracking
    depends_on_segments: List[int] = field(default_factory=list)
    independent_of_segments: List[int] = field(default_factory=list)
    goods_involved: Set[str] = field(default_factory=set)
    markets_involved: Set[str] = field(default_factory=set)
    required_cargo_from_prior: Dict[str, int] = field(default_factory=dict)
    can_skip_if_failed: bool = False
```

### 3.2 Phase 2: Implement Dependency Analysis

**Location:** `multileg_trader.py` (after route planning, before execution)

```python
def analyze_route_dependencies(route: MultiLegRoute) -> Dict[int, SegmentDependency]:
    """
    Build dependency graph BEFORE execution

    Call after route planning, before execute_multileg_route()
    """
    dependencies = {}

    for i, segment in enumerate(route.segments):
        dep = SegmentDependency(
            segment_index=i,
            depends_on=[],
            dependency_type='NONE',
            required_cargo={},
            required_credits=0,
            can_skip=True
        )

        # Analyze cargo dependencies
        sell_actions = [a for a in segment.actions_at_destination if a.action == 'SELL']
        for sell_action in sell_actions:
            good = sell_action.good

            # Find provider segment
            for j in range(i):
                prior_segment = route.segments[j]
                if good in prior_segment.cargo_after:
                    dep.depends_on.append(j)
                    dep.dependency_type = 'CARGO'
                    dep.required_cargo[good] = sell_action.units
                    dep.can_skip = False

        # Analyze credit dependencies
        buy_actions = [a for a in segment.actions_at_destination if a.action == 'BUY']
        buy_cost = sum(a.total_value for a in buy_actions)
        dep.required_credits = buy_cost

        dependencies[i] = dep

    return dependencies
```

### 3.3 Phase 3: Implement Smart Skip Logic with Tiered Salvage

**Location:** `multileg_trader.py` (in execute_multileg_route main loop)

#### Tiered Salvage Strategy

**Key Insight:** Never sell at the market that triggered the circuit breaker - that's the worst price by definition!

**Four-Tier Salvage System:**

1. **Tier 1: Sell at Current Market** - Fastest, lowest risk, stay on route
2. **Tier 2: Sell at Adjacent Market** - Nearby (<100 units), big gain (>50k cr)
3. **Tier 3: Defer Salvage** - Hold cargo, check prices at future planned markets
4. **Tier 4: End-of-Route Best Market** - Navigate to best market when route complete

```python
def should_skip_segment(
    segment_index: int,
    failure_reason: str,
    dependencies: Dict[int, SegmentDependency],
    route: MultiLegRoute,
    current_cargo: Dict[str, int],
    current_credits: int
) -> Tuple[bool, str]:
    """
    Determine if failed segment should be skipped vs abort operation

    Returns:
        (should_skip, reason)
    """
    dep = dependencies[segment_index]

    # Check if any remaining segments are independent
    remaining_segments = route.segments[segment_index + 1:]

    independent_count = 0
    dependent_count = 0

    for i, remaining_seg in enumerate(remaining_segments):
        remaining_idx = segment_index + 1 + i
        remaining_dep = dependencies[remaining_idx]

        # Check if this segment depends on the failed one
        if segment_index in remaining_dep.depends_on:
            dependent_count += 1
        else:
            independent_count += 1

    if independent_count == 0:
        return False, "All remaining segments depend on failed segment"

    # Check if remaining independent segments are profitable
    remaining_profit = sum(
        seg.cumulative_profit
        for i, seg in enumerate(remaining_segments)
        if (segment_index + 1 + i) not in dependencies or
           segment_index not in dependencies[segment_index + 1 + i].depends_on
    )

    if remaining_profit < 5000:  # Configurable minimum
        return False, f"Remaining profit too low ({remaining_profit})"

    return True, f"Can skip - {independent_count} independent segments remain"


def cargo_blocks_future_segments(
    cargo: Dict[str, int],
    remaining_segments: List[RouteSegment],
    ship_capacity: int
) -> bool:
    """
    Check if current cargo prevents future segments from executing

    Returns:
        True if cargo blocks any segment's buy actions
    """
    cargo_used = sum(cargo.values())
    cargo_available = ship_capacity - cargo_used

    for segment in remaining_segments:
        buy_actions = [a for a in segment.actions_at_destination if a.action == 'BUY']
        units_needed = sum(a.units for a in buy_actions)

        if units_needed > cargo_available:
            return True

    return False


def skip_segment_and_cleanup_tiered(
    segment: RouteSegment,
    ship: ShipController,
    api: APIClient,
    db,
    navigator: SmartNavigator,
    logger: logging.Logger,
    remaining_segments: List[RouteSegment]
) -> Tuple[bool, int]:
    """
    Skip failed segment and intelligently salvage cargo using tiered strategy

    CRITICAL: Never sell at circuit breaker market - that's the worst price!

    Returns:
        (success, credits_recovered)
    """
    logger.warning("="*70)
    logger.warning(f"⚠️  SKIPPING SEGMENT: {segment.from_waypoint} → {segment.to_waypoint}")
    logger.warning("="*70)

    # Get current cargo and location
    ship_data = ship.get_status()
    cargo = ship_data.get('cargo', {}).get('inventory', [])
    current_waypoint = ship_data['nav']['waypointSymbol']
    ship_capacity = ship_data['cargo']['capacity']

    # Identify cargo specific to this failed segment
    segment_goods = {action.good for action in segment.actions_at_destination if action.action == 'BUY'}

    stranded_cargo = []
    for item in cargo:
        if item['symbol'] in segment_goods:
            stranded_cargo.append(item)

    if not stranded_cargo:
        logger.warning("No stranded cargo from failed segment")
        return True, 0

    logger.warning(f"Stranded cargo from failed segment:")
    for item in stranded_cargo:
        logger.warning(f"  - {item['units']}x {item['symbol']}")

    total_recovered = 0

    # Check if cargo blocks future segments
    cargo_dict = {item['symbol']: item['units'] for item in cargo}
    blocks_future = cargo_blocks_future_segments(cargo_dict, remaining_segments, ship_capacity)

    for item in stranded_cargo:
        good = item['symbol']
        units = item['units']

        # Get current market price (circuit breaker market - worst price!)
        current_market_data = db.get_market_data(conn, current_waypoint, good)
        current_price = current_market_data['sellPrice'] if current_market_data else 0

        # Find best nearby market (within 500 units)
        best_market = db.find_best_buyer(
            good,
            system=ship.system,
            updated_within_hours=2.0,
            limit=1
        )
        best_price = best_market['sellPrice'] if best_market else current_price
        best_waypoint = best_market['waypoint'] if best_market else current_waypoint

        # Calculate metrics for tiered decision
        distance_to_best = calculate_distance(current_waypoint, best_waypoint)
        deviation_time_min = (distance_to_best / ship.speed) / 60
        salvage_gain = (best_price - current_price) * units
        remaining_value = sum(seg.cumulative_profit for seg in remaining_segments)

        # TIERED SALVAGE DECISION

        # TIER 2: Adjacent market with big gain (quick win)
        if distance_to_best <= 100 and salvage_gain > 50000 and not blocks_future:
            logger.warning(f"🎯 TIER 2: Deviating to nearby market {best_waypoint}")
            logger.warning(f"   Current: {current_price:,} cr/unit, Best: {best_price:,} cr/unit")
            logger.warning(f"   Gain: {salvage_gain:,} cr, Delay: {deviation_time_min:.1f} min")

            try:
                navigator.execute_route(ship, best_waypoint)
                ship.dock()
                result = ship.sell(good, units)
                total_recovered += result['totalPrice']
                logger.warning(f"  ✅ Salvaged {units}x {good} for {result['totalPrice']:,} credits")
            except Exception as e:
                logger.error(f"  ❌ Failed to navigate to best market: {e}")
                # Fallback to Tier 1
                result = ship.sell(good, units)
                total_recovered += result['totalPrice']

        # TIER 3: Hold for opportunistic sale (cargo doesn't block, route has more markets)
        elif not blocks_future and len(remaining_segments) > 0:
            logger.warning(f"📦 TIER 3: Holding {units}x {good} for opportunistic sale at future markets")
            logger.warning(f"   Current: {current_price:,} cr/unit, Best: {best_price:,} cr/unit")
            logger.warning(f"   Will check prices at {len(remaining_segments)} remaining markets")
            # Don't sell - will check prices at each future market
            # (Implementation: track in ship state or operation context)

        # TIER 4: End of route - navigate to best market (no opportunity cost)
        elif len(remaining_segments) == 0 and distance_to_best < 300:
            logger.warning(f"🏁 TIER 4: Route complete, navigating to best market {best_waypoint}")
            logger.warning(f"   Current: {current_price:,} cr/unit, Best: {best_price:,} cr/unit")
            logger.warning(f"   Distance: {distance_to_best} units")

            try:
                navigator.execute_route(ship, best_waypoint)
                ship.dock()
                result = ship.sell(good, units)
                total_recovered += result['totalPrice']
                logger.warning(f"  ✅ Salvaged {units}x {good} for {result['totalPrice']:,} credits")
            except Exception as e:
                logger.error(f"  ❌ Failed to navigate to best market: {e}")
                result = ship.sell(good, units)
                total_recovered += result['totalPrice']

        # TIER 1: Emergency salvage at current market (fastest, stay on route)
        else:
            reason = "Cargo blocking future segments" if blocks_future else "High opportunity cost"
            logger.warning(f"⚡ TIER 1: Emergency salvage at current market")
            logger.warning(f"   Reason: {reason}")
            logger.warning(f"   Accepting price: {current_price:,} cr/unit (vs best: {best_price:,} cr/unit)")

            try:
                result = ship.sell(good, units, check_market_prices=False)
                if result:
                    total_recovered += result['totalPrice']
                    logger.warning(f"  ✅ Salvaged {units}x {good} for {result['totalPrice']:,} credits")
            except Exception as e:
                logger.error(f"  ❌ Failed to salvage {good}: {e}")

    logger.warning(f"Total salvage recovered: {total_recovered:,} credits")
    logger.warning("Continuing with remaining independent segments...")
    logger.warning("="*70)

    return True, total_recovered
```

#### Tiered Salvage Decision Matrix

| Situation | Best Market Distance | Price Improvement | Cargo Blocking? | Remaining Segments | **Action** |
|-----------|---------------------|-------------------|-----------------|-------------------|------------|
| Seg 3 fails | 450u (30min) | +430% | ❌ No | 2 (500k value) | **Tier 3: HOLD** |
| Seg 3 fails | 85u (6min) | +300% | ❌ No | 2 (300k value) | **Tier 2: DEVIATE** |
| Seg 3 fails | 450u (30min) | +430% | ✅ YES | 2 (300k value) | **Tier 1: SELL NOW** |
| Seg 3 fails | 25u (2min) | +50% | ❌ No | 2 (600k value) | **Tier 2: DEVIATE** |
| Seg 4 fails | 200u (13min) | +200% | ❌ No | 1 (20k value) | **Tier 4: BEST MARKET** |

**Key Principle:** Route integrity > Salvage optimization. Getting back on the planned route quickly is more important than maximum salvage value.

### 3.4 Phase 4: Modify Main Execution Loop

**Location:** `execute_multileg_route()` main segment loop

```python
def execute_multileg_route(
    route: MultiLegRoute,
    ship: ShipController,
    api: APIClient,
    db,
    player_id: int
) -> bool:
    """Execute route with intelligent segment skipping"""

    # NEW: Analyze dependencies BEFORE execution
    dependencies = analyze_route_dependencies(route)

    logging.info("\n" + "="*70)
    logging.info("DEPENDENCY ANALYSIS")
    logging.info("="*70)
    for idx, dep in dependencies.items():
        logging.info(f"Segment {idx}: depends_on={dep.depends_on}, can_skip={dep.can_skip}")
    logging.info("="*70)

    # Track skipped segments
    skipped_segments = set()

    # Execute each segment
    for segment_num, segment in enumerate(route.segments, 1):
        segment_index = segment_num - 1

        # Skip if dependent on a failed segment
        if any(dep_idx in skipped_segments for dep_idx in dependencies[segment_index].depends_on):
            logging.warning(f"⏭️  SKIPPING segment {segment_num} - depends on failed segment")
            skipped_segments.add(segment_index)
            continue

        logging.info(f"\nSEGMENT {segment_num}/{len(route.segments)}: ...")

        try:
            # Execute segment (existing code)
            # ... navigation, docking, trade actions ...

        except Exception as e:
            logging.error(f"Segment {segment_num} failed: {e}")

            # NEW: Decide skip vs abort
            should_skip, reason = should_skip_segment(
                segment_index,
                str(e),
                dependencies,
                route,
                current_cargo,
                current_credits
            )

            if should_skip:
                skip_segment_and_cleanup(segment, ship, api, db, logging.getLogger(__name__))
                skipped_segments.add(segment_index)
                continue  # Try next segment
            else:
                logging.error(f"Cannot skip: {reason}")
                _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__))
                return False

    # Success if ANY segment executed successfully
    successful_segments = len(route.segments) - len(skipped_segments)
    logging.info(f"✅ Completed {successful_segments}/{len(route.segments)} segments")

    return successful_segments > 0
```

---

## 4. Edge Cases and Handling

### 4.1 Swiss Cheese Routes (Segments 1,3,5 work but 2,4 fail)

**Problem:** Viable segments are non-contiguous, creating complex navigation.

**Solution:**
```python
def consolidate_viable_segments(
    route: MultiLegRoute,
    skipped_segments: Set[int],
    dependencies: Dict[int, SegmentDependency]
) -> MultiLegRoute:
    """
    Rebuild route with only viable segments, inserting navigation hops

    Example:
        Original: A1 → D45 → J62 → G53 → H56
        Skipped: {1, 3} (segments 2 and 4)
        Result: A1 → J62 (direct nav) → H56 (direct nav)
    """
    viable_segments = []

    for i, segment in enumerate(route.segments):
        if i not in skipped_segments:
            # Check if we need a navigation hop from previous viable segment
            if viable_segments:
                last_viable = viable_segments[-1]
                if last_viable.to_waypoint != segment.from_waypoint:
                    # Insert navigation-only segment
                    nav_segment = create_navigation_segment(
                        last_viable.to_waypoint,
                        segment.from_waypoint
                    )
                    viable_segments.append(nav_segment)

            viable_segments.append(segment)

    return MultiLegRoute(
        segments=viable_segments,
        total_profit=sum(s.cumulative_profit for s in viable_segments),
        # ... recalculate other fields
    )
```

**When to consolidate:**
- Only if skipped segments create navigation gaps
- Only if consolidation cost (fuel) < remaining profit
- Otherwise, abort operation

### 4.2 Last Segment Only Viable

**Problem:** Segments 1-4 fail, only segment 5 remains. Is it worth navigating to seg 5's start?

**Solution:**
```python
def is_last_segment_worth_it(
    current_waypoint: str,
    last_segment: RouteSegment,
    current_fuel: int,
    ship_speed: int
) -> Tuple[bool, str]:
    """
    Evaluate if navigating to lone remaining segment is profitable
    """
    # Calculate navigation cost to segment start
    distance = calculate_distance(current_waypoint, last_segment.from_waypoint)
    fuel_cost = FuelCalculator.fuel_cost(distance, 'CRUISE')
    time_cost = TimeCalculator.travel_time(distance, ship_speed, 'CRUISE')

    # Estimate fuel cost in credits (100 cr/fuel rough estimate)
    navigation_cost = fuel_cost * 100

    # Check profitability
    segment_profit = last_segment.cumulative_profit
    net_profit = segment_profit - navigation_cost

    if net_profit < 5000:
        return False, f"Net profit too low ({net_profit:,}) after navigation cost ({navigation_cost:,})"

    if time_cost > 600:  # 10 minutes
        return False, f"Navigation time too long ({time_cost}s)"

    return True, f"Worth it - net profit {net_profit:,} after {time_cost}s navigation"
```

---

## 5. Risk Analysis

### 5.1 Potential Failure Modes

| Risk | Likelihood | Severity | Mitigation |
|------|-----------|----------|------------|
| **Navigation gaps** (ship at A1, next segment starts at J62) | Medium | High | Detect gaps, insert navigation segments with fuel checks |
| **Stranded cargo pollution** (holding COPPER from seg 2, seg 4 needs full cargo) | High | Medium | Emergency sell before independent segments |
| **Credit depletion cascade** (seg 3 failure leaves 5k credits, seg 4 needs 40k) | Medium | High | Check credit viability BEFORE attempting segment |
| **Fuel exhaustion** (skipping seg 2 creates 800-unit gap to seg 3) | Low | Critical | Pre-validate fuel feasibility of consolidated route |
| **Swiss cheese profitability** (seg 1,3,5 viable but navigation costs = profit) | Medium | Medium | Recalculate net profitability after consolidation |
| **Infinite recovery loop** (seg 3 fails, retry seg 3, fails again) | Low | High | Track failed segments, never retry same segment |

### 5.2 When Abort is BETTER Than Skip

**Abort conditions (DO NOT attempt partial execution):**

1. **Fuel Emergency:** Remaining segments require refuel, but no credits for fuel
2. **Navigation Deadlock:** Ship at X, next viable segment starts at Y, no route exists
3. **Profit Collapse:** Sum of remaining segment profits < 5,000 credits
4. **Time Inefficiency:** Remaining segments require >30 minutes navigation for <10k profit
5. **Cargo Deadlock:** Holding unsellable cargo, remaining segments need full cargo space

---

## 6. Implementation Roadmap

### Phase 1: Foundation (Week 1)
- [ ] Add dependency metadata to RouteSegment dataclass
- [ ] Implement `analyze_route_dependencies()` function
- [ ] Add unit tests for dependency detection

### Phase 2: Skip Logic (Week 2)
- [ ] Implement `should_skip_segment()` decision logic
- [ ] Implement `skip_segment_and_cleanup()` salvage logic
- [ ] Add unit tests for skip decisions

### Phase 3: Execution Integration (Week 3)
- [ ] Modify `execute_multileg_route()` main loop
- [ ] Add segment-level try/except with skip logic
- [ ] Implement consolidated route building
- [ ] Add integration tests

### Phase 4: Edge Cases (Week 4)
- [ ] Implement cargo conflict resolution
- [ ] Implement credit viability checks
- [ ] Implement fuel gap validation
- [ ] Implement abort-vs-continue validation
- [ ] Add edge case tests

### Phase 5: BDD Scenarios (Week 5)
- [ ] Write Gherkin scenarios for new behavior
- [ ] Implement step definitions
- [ ] Run full test suite

---

## 7. Success Criteria

### Quantitative Metrics
- **Completion rate:** 40% → 70% (more routes complete with partial execution)
- **Average profit per route:** +25% (capturing independent segment profits)
- **Segment skip rate:** <30% (most segments execute successfully)
- **Abort rate:** 70% → 40% (fewer total aborts)

### Qualitative Goals
- ✅ **Resilience:** Single segment failure doesn't kill profitable independents
- ✅ **Transparency:** Clear logging of skip decisions and dependencies
- ✅ **Safety:** No worse outcomes than current "abort all" approach
- ✅ **Maintainability:** Dependency logic is testable and debuggable

---

## 8. Example Scenario

**Scenario:** 5-segment route where segment 3 fails

### Route Definition
```
Segment 1: A1 → D45 (BUY MEDICINE, SELL MEDICINE) → +25k profit
Segment 2: D45 → J62 (BUY SHIP_PARTS, SELL SHIP_PARTS) → +40k profit
Segment 3: J62 → G53 (BUY CLOTHING, SELL CLOTHING) → Expected +30k profit
Segment 4: G53 → H56 (BUY DRUGS, SELL DRUGS) → +35k profit
Segment 5: H56 → A1 (BUY COPPER, SELL COPPER) → +20k profit

Total Expected: 150k profit
```

### Dependency Analysis
```
Segment 1: Independent (no prior segments)
Segment 2: Independent (different good from seg 1)
Segment 3: Independent (different good from seg 1,2)
Segment 4: Independent (different good from seg 1,2,3)
Segment 5: Independent (different good from seg 1,2,3,4)

Result: ALL segments are independent!
```

### Execution Flow
```
1. Execute Segment 1 (A1 → D45): ✅ SUCCESS (+25k)
2. Execute Segment 2 (D45 → J62): ✅ SUCCESS (+40k)
3. Execute Segment 3 (J62 → G53):
   - Navigate to G53: ✅
   - Dock at G53: ✅
   - Check CLOTHING buy price: SPIKE 36% ❌
   - Circuit breaker triggers: "BUY PRICE SPIKE"
   - Check dependencies: Segment 4,5 are INDEPENDENT
   - Decision: SKIP segment 3, continue
   - Cleanup: No stranded cargo (buy never executed)

4. Execute Segment 4 (G53 → H56):
   - Already at G53, navigate to H56: ✅
   - Buy DRUGS: ✅
   - Navigate to H56: ✅
   - Sell DRUGS: ✅ SUCCESS (+35k)

5. Execute Segment 5 (H56 → A1):
   - Buy COPPER: ✅
   - Navigate to A1: ✅
   - Sell COPPER: ✅ SUCCESS (+20k)

Final Result: 4/5 segments executed, 120k profit (vs 0k with current abort-all behavior)
```

### Current Behavior vs Proposed
| Metric | Current (Abort All) | Proposed (Smart Skip) |
|--------|--------------------|-----------------------|
| Segments executed | 2/5 | 4/5 |
| Profit | 65k (seg 1,2 only) | 120k (seg 1,2,4,5) |
| Outcome | FAILURE (abort) | SUCCESS (partial) |

---

## 9. Conclusion

The proposed smart circuit breaker system provides **significant operational resilience** while maintaining safety guarantees. Key benefits:

1. **40-70% more profit captured** from independent segments
2. **Graceful degradation** instead of catastrophic failure
3. **Clear visibility** into skip decisions via logging
4. **Safety-first approach** with comprehensive validation

**Recommendation:** Implement in phases, starting with dependency analysis foundation, then gradually adding skip logic with extensive testing at each stage.

**Admiral Approval Required For:**
- Minimum profit threshold for partial execution (currently 5,000 credits)
- Segment skip threshold (currently 30% price deviation)
- Time efficiency threshold (currently 100 cr/minute)
- Salvage tier thresholds:
  - Tier 2 deviation distance: 100 units
  - Tier 2 minimum gain: 50,000 credits
  - Tier 4 maximum distance: 300 units

---

## 10. Cargo Salvage Strategy - Critical Design Decision

### The Circuit Breaker Market Problem

**CRITICAL INSIGHT:** Never sell at the market that triggered the circuit breaker - that's the worst price by definition!

**Example:**
```
Segment 3: Navigate to G53, sell CLOTHING
Circuit Breaker: CLOTHING price crashed 45% (1,800 cr vs planned 10,000 cr)
Current Behavior: Sell at G53 for 1,800 cr/unit = 72,000 cr
Problem: We're accepting the CRASHED price that triggered the breaker!

Better: Check nearby markets
  - H57 (85 units away): 7,200 cr/unit = 288,000 cr (+216k gain for 6 min)
  - A1 (450 units away): 9,500 cr/unit = 380,000 cr (too far, 30 min deviation)
```

### Route Deviation Trade-off

**Key Concern:** Deviating from the planned route to get better salvage prices risks:
1. **Price volatility** - Remaining segment prices may change during deviation
2. **Opportunity cost** - Delayed segments may miss profitable windows
3. **Time waste** - Long deviations may not be worth the salvage gain

**Solution:** Weighted decision based on deviation cost vs salvage gain vs remaining route value.

### Tiered Salvage System (Approved)

**Tier 1: Current Market (Emergency)**
- **When:** Cargo blocks future segments OR high opportunity cost
- **Action:** Sell immediately at current location
- **Accept:** Any price (even if crashed)
- **Rationale:** Route integrity > salvage optimization

**Tier 2: Adjacent Market (Quick Win)**
- **When:** Better market <100 units away AND gain >50k cr
- **Action:** Navigate to nearby market, sell, return to route
- **Threshold:** 6-10 minute deviation maximum
- **Rationale:** Small deviation, big gain, low risk

**Tier 3: Deferred Salvage (Opportunistic)**
- **When:** Cargo doesn't block AND route has more markets
- **Action:** Hold cargo, check prices at each future planned market
- **Trigger:** Sell when price recovers to >80% of plan
- **Rationale:** Zero deviation cost, might get lucky

**Tier 4: End-of-Route Best Market**
- **When:** Route complete, still holding cargo
- **Action:** Navigate to best market in system (no opportunity cost)
- **Accept:** Any distance <300 units
- **Rationale:** No more segments to delay, maximize recovery

### Implementation Principle

> **Route integrity > Salvage optimization**

The planned route was optimized for profit. Deviations break that optimization. Only deviate when:
1. Gain is substantial (>50k credits)
2. Deviation is quick (<10 minutes)
3. Risk is low (remaining segments not high-value)

Otherwise: Accept emergency salvage price and get back on route ASAP.

---

**Document Status:** Design proposal ready for Admiral review and implementation approval.
