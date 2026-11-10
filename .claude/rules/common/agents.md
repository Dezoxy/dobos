# Agent Orchestration

## Available Agents

Agents ship in the repository's own `.claude/agents/` and are invoked through
the Agent tool by name. List that folder to see which exist; the repository's
`CLAUDE.md` may say when to use each. There is no plugin namespace: a name
such as `ecc:planner` resolves to nothing.

```text
Agent(subagent_type: "security-reviewer", prompt: "...")
```

## Immediate Agent Usage

No user prompt needed:
1. Code just written/modified - Use the repository's reviewer for that language or area
2. Bug fix or new feature - Use **tdd-guide** agent, when the repository ships it
3. Security-sensitive change - Use **security-reviewer** agent
4. Architectural decision - Use the **architecture-docs** skill (record an ADR)

## Parallel Task Execution

ALWAYS use parallel Task execution for independent operations:

```markdown
# GOOD: Parallel execution
Launch 3 agents in parallel:
1. Agent 1: Security analysis of auth module
2. Agent 2: Performance review of cache system
3. Agent 3: Type checking of utilities

# BAD: Sequential when unnecessary
First agent 1, then agent 2, then agent 3
```

## Delegation Completion Contract

Applies to every agent at every depth (parent, child, grandchild):

1. **Your final message IS the deliverable.** Never end your turn with "waiting for background agents" — a spawned task is not a completed task. Ending your turn while children are running orphans their results (completed children cannot notify a parent whose turn has ended).
2. **If you delegate, you own collection.** Wait for results, integrate them, then return. Fire-and-forget delegation is forbidden.
3. **Decompose only when the work cannot fit in one context.** Do not re-delegate a task already sized for a single agent — depth is an outcome, not a plan.

> Rationale: observed failure mode — research agents followed "Parallel Task Execution" above, spawned children, and returned "waiting" as their final answer. All children completed successfully but their results were orphaned. The parallel rule without a completion contract produces zombie tasks.

## Multi-Perspective Analysis

For complex problems, use split role sub-agents:
- Factual reviewer
- Senior engineer
- Security expert
- Consistency reviewer
- Redundancy checker
