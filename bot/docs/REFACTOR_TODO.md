# Refactor TODO

1. [x] Refactor contracts operation by extracting dedicated resource acquisition and delivery strategies.
2. [x] Tidy multileg trader by separating search/optimization logic into dedicated components.
3. [ ] Modularize scout coordinator monitoring with per-ship state and clearer error handling.
4. [ ] Simplify daemon manager supervision loop via smaller command helpers.
5. [ ] Split purchasing validation/execution into dedicated functions or classes to adhere to SRP.
6. [ ] Wrap API client responses in typed success/error results to centralize error handling.
7. [ ] Introduce a MiningCycle helper in mining operations to encapsulate extraction/cooldown workflow.
