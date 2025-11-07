# Learnings Directory

This directory contains retrospective analyses of TARS operations - documenting what worked, what didn't, and what TARS should do differently next time.

## Purpose

Learnings files are TARS's mechanism for continuous improvement:
- **Pattern Recognition:** Identify recurring problems and successes
- **Behavioral Changes:** Document concrete changes to decision-making
- **Accountability:** Track whether lessons are being applied
- **Knowledge Retention:** Preserve insights across sessions

## When Learnings Are Generated

The `learnings-analyst` agent creates these reports:
- **Every 3-5 interactions** during normal operations
- **After major operations** (multi-hour sessions, significant events)
- **After failures** requiring analysis
- **At strategic milestones** (phase transitions, major decisions)

## File Format

```
YYYY-MM-DD_HHmm_learnings.md
```

Examples:
- `2025-11-06_2100_learnings.md`
- `2025-11-07_0300_learnings.md`

## What's Inside

Each learnings report includes:

1. **Executive Summary:** Quick overview of key lessons
2. **Operational Reliability:** Lessons about executing operations correctly
3. **Strategic Decision-Making:** Lessons about when/what to do
4. **Agent Coordination:** Lessons about using specialist agents
5. **Resource Management:** Lessons about credits, ships, timing
6. **Error Handling:** Lessons about retries, fallbacks, escalation
7. **What Worked Well:** Success patterns to keep doing
8. **Metrics Comparison:** Performance trends over the period
9. **Priority Action Items:** Immediate changes needed
10. **Questions for Admiral:** Strategic uncertainties requiring human input

## How TARS Uses Learnings

TARS should:
1. **Read recent learnings** before starting major operations
2. **Apply priority actions** immediately
3. **Check success metrics** to verify lessons are being followed
4. **Reference learnings** when making similar decisions
5. **Update strategies** based on validated patterns

## Difference from Other Reports

- **Bug Reports** (`reports/bugs/`): Document specific failures that need fixing
- **Feature Proposals** (`reports/features/`): Propose new capabilities or optimizations
- **Mission Logs** (`mission_logs/`): Narrative of what happened during operations
- **Learnings** (`mission_logs/learnings/`): What TARS learned and should change

## For Developers

Learnings files provide valuable insights into:
- How TARS makes decisions under uncertainty
- Where the agent struggles or excels
- What constraints or tools are missing
- How well TARS self-corrects over time

If you see the same lesson appearing repeatedly, that indicates:
1. TARS is not applying the lesson (behavioral bug)
2. The system makes it hard to apply the lesson (design issue)
3. The lesson conflicts with other constraints (architecture issue)
