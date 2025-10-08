# Refactor TODO

1. [x] Refactor contracts operation by extracting dedicated resource acquisition and delivery strategies.
2. [x] Tidy multileg trader by separating search/optimization logic into dedicated components.
3. [x] Modularize scout coordinator monitoring with per-ship state and clearer error handling.
4. [x] Simplify daemon manager supervision loop via smaller command helpers.
5. [x] Split purchasing validation/execution into dedicated functions or classes to adhere to SRP.
6. [x] Wrap API client responses in typed success/error results to centralize error handling.
7. [x] Introduce a MiningCycle helper in mining operations to encapsulate extraction/cooldown workflow.
8. [x] Identify the next high-complexity operation module for conditional-logic refactor (strategy pattern candidates).
9. [x] Extract a trade evaluation strategy from GreedyRoutePlanner to separate market simulation logic.
10. [x] Update MultiLegTradeOptimizer to accept pluggable strategies and cover new components with unit tests.
11. [x] Reassess remaining operations modules for further conditional refactors after strategy integration.
12. [x] Extract contract resource availability logic into a dedicated `ResourceAcquisitionStrategy` to shrink branching in `contract_operation`.
13. [x] Update the contract delivery loop to consume the new strategy and add focused unit tests for resource procurement and retry paths.
