# Discovery Questions

## Q1: Should OR-Tools completely replace the existing routing system (routing.py)?
**Default if unknown:** No (keep existing system as fallback for edge cases and gradual migration)

**Rationale:** The existing system has been tested extensively and handles many edge cases. A complete replacement is risky. Better to run both systems in parallel initially, compare results, and gradually transition.

---

## Q2: Should the validation layer run automatically on every route calculation?
**Default if unknown:** No (run periodically via scheduled task or manual trigger to avoid performance overhead)

**Rationale:** Running validation on every route would add significant latency (requires actual API navigation). Better to validate periodically (e.g., hourly, daily) or manually when debugging.

---

## Q3: Should the configuration constants (flight mode multipliers, fuel rates) be stored in a database?
**Default if unknown:** No (store in config file like YAML/JSON for easier version control and deployment)

**Rationale:** Constants rarely change and need version control. File-based config (e.g., config/routing_constants.yaml) is easier to manage, review, and deploy than database entries.

---

## Q4: Should the system automatically switch to OR-Tools routing when it detects the current system would choose DRIFT for long distances?
**Default if unknown:** Yes (use OR-Tools as intelligent fallback when current system shows poor performance)

**Rationale:** This provides immediate value without breaking existing functionality. Current system calculates route → if it chooses DRIFT for >500 units → recalculate with OR-Tools.

---

## Q5: Should validation failures (>5% deviation from actual API behavior) automatically disable OR-Tools and revert to the legacy system?
**Default if unknown:** Yes (fail-safe mechanism to prevent incorrect routing if constants become outdated)

**Rationale:** If SpaceTraders changes their formulas and validation detects high deviation, automatically falling back to the legacy system prevents ships from getting stranded due to incorrect fuel calculations.
