# Detail Questions

## Q6: Should we extract the PixiJS canvas management into a custom React hook (usePixiCanvas) that can be shared between SpaceMap and GalaxyView?
**Default if unknown:** Yes (eliminates ~100 lines of duplication and follows React best practices for reusable logic)

## Q7: Should we consolidate AddAgentCard and AgentManager into a single component, since they duplicate agent-adding logic?
**Default if unknown:** Yes (reduces duplication and maintains single source of truth for agent management)

## Q8: Should we create a new directory structure (services/pixi/, utils/, hooks/, components/common/) to better organize extracted code?
**Default if unknown:** Yes (improves discoverability and follows standard React project conventions)

## Q9: Should we remove unused type definitions (ShipTrail, Cooldown, FlightMode, CargoItem) from types/spacetraders.ts?
**Default if unknown:** Yes (reduces code noise and prevents confusion about what's actually used)

## Q10: Should we split the 2011-line SpaceMap.tsx into multiple smaller files (ship rendering, position calculation, canvas management)?
**Default if unknown:** Yes (SpaceMap violates SRP and is difficult to maintain; industry best practice is < 300 lines per component)
