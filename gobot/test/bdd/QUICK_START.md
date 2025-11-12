# BDD Quick Start Guide

Get started with BDD testing in under 5 minutes!

## Prerequisites

- Go 1.21+
- Godog framework (already installed via go.mod)

## Run Your First BDD Test

```bash
cd /Users/andres.camacho/Development/Personal/spacetraders/gobot

# Run all BDD tests
make test-bdd

# Or run directly with go test
go test ./test/bdd/... -v
```

## Run Specific Tests

```bash
# Ship entity tests
make test-bdd-ship

# Route entity tests
make test-bdd-route

# Container entity tests
make test-bdd-container

# Value object tests
make test-bdd-values

# Pretty colored output
make test-bdd-pretty
```

## Understanding the Output

### ✅ Passing Scenario
```
Feature: Ship Entity
  Scenario: Consume fuel reduces fuel amount
    Given a ship with 100 units of fuel     ✓
    When the ship consumes 30 units of fuel ✓
    Then the ship should have 70 units      ✓
```

### ❌ Failing Scenario
```
Feature: Ship Entity
  Scenario: Invalid fuel consumption
    Given a ship with 100 units of fuel            ✓
    When I attempt to consume 150 units of fuel    ✓
    Then the operation should fail with error "insufficient fuel"  ✗
      expected error containing 'insufficient fuel' but got 'not enough fuel'
```

## Write Your First Scenario

### 1. Add to existing feature file

Edit `test/bdd/features/domain/navigation/ship_entity.feature`:

```gherkin
Scenario: My new test
  Given a ship at "X1-A1"
  When I do something
  Then something should happen
```

### 2. Run tests to see undefined steps

```bash
make test-bdd-ship
```

Output will show:
```
Step 'I do something' is undefined
You can implement step definitions with:

func iDoSomething() error {
    return godog.ErrPending
}
```

### 3. Implement step definition

Edit `test/bdd/steps/ship_steps.go`:

```go
func (sc *shipContext) iDoSomething() error {
    // Your implementation
    return nil
}

// Register in InitializeShipScenario:
ctx.Step(`^I do something$`, sc.iDoSomething)
```

### 4. Run tests again

```bash
make test-bdd-ship
```

## Common Patterns

### Testing Success Path

```gherkin
Scenario: Successful operation
  Given a ship with 100 units of fuel
  When the ship consumes 30 units of fuel
  Then the ship should have 70 units of fuel
```

### Testing Error Cases

```gherkin
Scenario: Error handling
  Given a ship with 100 units of fuel
  When I attempt to consume 150 units of fuel
  Then the operation should fail with error "insufficient fuel"
```

### Using Tables

```gherkin
Scenario: Setup with table data
  Given test waypoints are available:
    | symbol | x   | y   |
    | X1-A1  | 0.0 | 0.0 |
    | X1-B2  | 100 | 0.0 |
  When I calculate distance from "X1-A1" to "X1-B2"
  Then the distance should be 100.0
```

### Testing State Transitions

```gherkin
Scenario: State machine
  Given a docked ship at "X1-A1"
  When the ship departs
  Then the ship should be in orbit
  And the ship should not be docked
```

## Tips

### ✅ Do

- Write scenarios in business language
- Test one behavior per scenario
- Use descriptive scenario names
- Test both happy and error paths
- Keep scenarios independent

### ❌ Don't

- Use technical implementation details
- Test multiple behaviors in one scenario
- Make scenarios depend on each other
- Over-complicate step definitions
- Skip error case testing

## Debugging

### Scenario failing?

1. **Read the error message carefully**
   ```
   expected symbol 'SHIP-1' but got 'SHIP-2'
   ```

2. **Check step implementation**
   - Is the step definition matching correctly?
   - Is the context being reset between scenarios?

3. **Add debug output**
   ```go
   func (sc *shipContext) myStep() error {
       fmt.Printf("DEBUG: ship=%+v\n", sc.ship)
       return nil
   }
   ```

4. **Run single scenario**
   ```bash
   go test ./test/bdd/... -v -godog.paths=test/bdd/features/domain/navigation/ship_entity.feature:10
   ```

### Step not matching?

Check regex pattern:
```go
// ✅ Good - matches integers
ctx.Step(`^value is (\d+)$`, func(v int) { ... })

// ❌ Bad - won't match negative numbers
ctx.Step(`^value is (\d+)$`, func(v int) { ... })

// ✅ Better - matches negative integers
ctx.Step(`^value is (-?\d+)$`, func(v int) { ... })
```

## Examples

### Ship Navigation
```gherkin
Scenario: Ship navigates from dock to orbit
  Given a docked ship at "X1-A1"
  When the ship departs
  Then the ship should be in orbit
  And the ship should be at location "X1-A1"
```

### Route Planning
```gherkin
Scenario: Create multi-segment route
  Given a route segment from "X1-A1" to "X1-B2" with distance 100.0
  And a route segment from "X1-B2" to "X1-C3" with distance 100.0
  When I create a route with 2 segments
  Then the route should have status "PLANNED"
  And the total distance should be 200.0
```

### Container Lifecycle
```gherkin
Scenario: Container completes successfully
  Given a container in "RUNNING" status
  When I complete the container
  Then the container should have status "COMPLETED"
  And the container stopped_at should be set
```

## Next Steps

1. **Read the README**: `test/bdd/README.md` - comprehensive guide
2. **Study feature files**: See examples in `test/bdd/features/`
3. **Review step definitions**: Look at `test/bdd/steps/*.go`
4. **Write your own**: Start with simple scenarios
5. **Share patterns**: Document useful patterns you discover

## Resources

- [README.md](README.md) - Comprehensive documentation
- [BDD_IMPLEMENTATION_SUMMARY.md](BDD_IMPLEMENTATION_SUMMARY.md) - Implementation details
- [Godog Documentation](https://github.com/cucumber/godog)
- [Gherkin Reference](https://cucumber.io/docs/gherkin/reference/)

## Help

Questions? Issues? Check the troubleshooting section in [README.md](README.md#troubleshooting).

---

**Happy testing! 🎯**
