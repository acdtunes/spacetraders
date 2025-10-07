# Contract Fulfillment Tests

## Overview

Comprehensive BDD tests for the contract fulfillment operation in `operations/contracts.py`.

## Test Coverage

### 12 Test Scenarios

1. **Contract with cargo already on ship - partial fulfillment**
   - Tests delivering cargo already in ship's inventory
   - Verifies partial fulfillment tracking
   - Ensures ship navigates to delivery location

2. **Contract with cargo already on ship - full fulfillment**
   - Tests completing contract with existing cargo
   - Verifies contract fulfillment and payment
   - Ensures cargo is properly delivered

3. **Multi-trip delivery when cargo capacity is less than total needed**
   - Tests contracts requiring more units than cargo capacity
   - Verifies multiple buy-deliver cycles
   - Ensures all units are eventually delivered

4. **Check existing cargo before buying**
   - Tests the critical cargo-checking logic
   - Verifies only needed units are purchased
   - Ensures existing cargo is used first

5. **Full delivery cycle - accept, buy, deliver, fulfill**
   - Tests complete contract lifecycle
   - Verifies acceptance payment
   - Verifies completion payment
   - Tests unaccepted contract flow

6. **Multiple cargo types - only deliver contract items**
   - Tests ships with mixed cargo
   - Verifies only contract items are delivered
   - Ensures other cargo remains intact

7. **Partial fulfillment then complete in second trip**
   - Tests combining existing cargo with purchases
   - Verifies multi-trip logic with mixed sources
   - Ensures proper trip counting

8. **Contract already partially fulfilled**
   - Tests resuming partially completed contracts
   - Verifies only remaining units are delivered
   - Ensures no duplicate deliveries

9. **Contract already fully fulfilled**
   - Tests handling of already-complete contracts
   - Verifies no unnecessary operations
   - Ensures early exit when nothing to do

10. **Insufficient cargo space requires multiple trips**
    - Tests cargo space constraints
    - Verifies trip planning with limited space
    - Ensures maximum cargo utilization

11. **Contract fulfillment with navigation between locations**
    - Tests navigation integration
    - Verifies fuel consumption
    - Ensures proper waypoint transitions

12. **Mixed cargo - contract item and other items**
    - Tests preserving non-contract cargo
    - Verifies selective purchasing
    - Ensures cargo integrity

## Files Created

### 1. Feature File
**Location:** `tests/features/contract_fulfillment.feature`

Gherkin-style BDD scenarios defining expected behavior for contract fulfillment operations.

### 2. Step Definitions
**Location:** `tests/test_contract_fulfillment_steps.py`

Python implementation of the BDD steps including:
- Given steps: Ship and contract setup
- When steps: Contract fulfillment actions
- Then steps: Verification assertions

### 3. Mock API Enhancements
**Location:** `tests/mock_api.py` (modified)

Added contract support to MockAPIClient:
- `add_contract()` - Create contract for testing
- `get_contract()` - Retrieve contract details
- Contract acceptance endpoint
- Contract delivery endpoint
- Contract fulfillment endpoint

## Key Testing Patterns

### 1. Cargo Checking Logic
```python
# Check current cargo first
already_have = 0
for item in current_cargo:
    if item['symbol'] == delivery['tradeSymbol']:
        already_have = item['units']
        break

still_need = remaining - already_have
```

### 2. Multi-Trip Delivery
```python
# Deliver existing cargo first
if already_have > 0:
    trip += 1
    # Navigate and deliver

# Buy and deliver remaining
while still_need > 0:
    trip += 1
    # Buy what fits in cargo
    # Navigate and deliver
```

### 3. Credit Tracking
```python
# Track initial credits
context['initial_credits'] = credits

# Verify purchase cost
expected_cost = units * 50
expected_credits = initial - cost + completion_payment
```

## Running the Tests

### Run all contract fulfillment tests
```bash
python3 -m pytest tests/test_contract_fulfillment_steps.py -v
```

### Run specific scenario
```bash
python3 -m pytest tests/test_contract_fulfillment_steps.py::test_check_existing_cargo_before_buying -v
```

### Run with coverage
```bash
python3 -m pytest tests/test_contract_fulfillment_steps.py --cov=operations.contracts --cov-report=html
```

## Test Results

All 12 scenarios pass successfully:

```
tests/test_contract_fulfillment_steps.py::test_contract_with_cargo_already_on_ship__partial_fulfillment PASSED
tests/test_contract_fulfillment_steps.py::test_contract_with_cargo_already_on_ship__full_fulfillment PASSED
tests/test_contract_fulfillment_steps.py::test_multitrip_delivery_when_cargo_capacity_is_less_than_total_needed PASSED
tests/test_contract_fulfillment_steps.py::test_check_existing_cargo_before_buying PASSED
tests/test_contract_fulfillment_steps.py::test_full_delivery_cycle__accept_buy_deliver_fulfill PASSED
tests/test_contract_fulfillment_steps.py::test_multiple_cargo_types__only_deliver_contract_items PASSED
tests/test_contract_fulfillment_steps.py::test_partial_fulfillment_then_complete_in_second_trip PASSED
tests/test_contract_fulfillment_steps.py::test_contract_already_partially_fulfilled PASSED
tests/test_contract_fulfillment_steps.py::test_contract_already_fully_fulfilled PASSED
tests/test_contract_fulfillment_steps.py::test_insufficient_cargo_space_requires_multiple_trips PASSED
tests/test_contract_fulfillment_steps.py::test_contract_fulfillment_with_navigation_between_locations PASSED
tests/test_contract_fulfillment_steps.py::test_mixed_cargo__contract_item_and_other_items PASSED

12 passed in 63.41s
```

## Integration with Existing Tests

The new tests integrate seamlessly with the existing test suite:
- Uses same MockAPIClient infrastructure
- Follows same BDD/Gherkin patterns
- Reuses helper functions for graph building
- Compatible with existing fixtures

No existing tests were broken by the additions.

## Test Coverage Areas

### Core Functionality
- ✅ Cargo checking before purchasing
- ✅ Multi-trip delivery logic
- ✅ Contract acceptance flow
- ✅ Contract fulfillment and payment
- ✅ Partial vs. full fulfillment

### Edge Cases
- ✅ Already fulfilled contracts
- ✅ Partially fulfilled contracts
- ✅ Mixed cargo types
- ✅ Insufficient cargo space
- ✅ Navigation between locations

### Integration Points
- ✅ ShipController integration
- ✅ SmartNavigator integration
- ✅ API client contract endpoints
- ✅ Credit tracking and payments

## Future Enhancements

Potential additional test scenarios:
1. Contract with mining instead of buying
2. Contract fulfillment failures (API errors)
3. Contract deadline handling
4. Multiple contracts simultaneously
5. Contract rejection/cancellation
6. Fuel emergencies during fulfillment
7. Market price variations
8. Cargo hold upgrades mid-contract
