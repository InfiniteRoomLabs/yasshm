# Work order: code-review lane

Dispatch: `Agent(subagent_type: "general-purpose", model: "<one tier above the implementer>", name: "phase-<n>-review")`. Runs in parallel with the simplification and security lanes. Read-only.

---

You are the **code-review lane** of a four-lane review gate for branch `<branch>` in `<absolute path of the clone>` (`git diff feat/yasshm-mvp...<branch>`, commits `<shas>`). **READ-ONLY:** do not modify files, do not commit, and do NOT run `mise run check`, tests, or builds -- the QA lane owns the gate and concurrent runs collide on build and coverage outputs. You may run `git`, `grep`, and read files freely.

## Context

- Spec: `docs/superpowers/specs/2026-11-18-yasshm-design.md` sections <list>. Conventions: `CLAUDE.md`, `TESTING.md`. Exemplar conventions: `<prior phase paths>`.
- Ground truth for this phase: <primary sources from the Reference section of GOAL.md> -- compare the implementation against the documented behavior, not just the spec.

## Review for

- Correctness against the spec and the oracle: <phase-specific hot spots -- boundaries, encodings, state transitions, error paths, ordering guarantees>.
- The invariants of spec section 5.2: every call below the MCP layer carries a tenant id; access is decided in one place by `required <= granted`; nothing from the config file reaches a tool result.
- Go canon: exported API shape, errors wrapped and matched with `errors.Is`/`errors.As`, contexts passed and honoured, no leaked goroutines, connections, files or sessions, no hidden global state, zero-value usability.
- Doc comments present and accurate on every exported member.
- Test smells: vacuous asserts, fixtures mirroring the implementation instead of the oracle, missing sad/edge paths, skipped tests, nondeterministic time/random, sleeps standing in for synchronisation, tests that would still pass if the behavior regressed.
- The architecture model matches the code: the elements this phase built lost `#planned`, and any new component or relationship is in `docs/architecture/model.c4` (the gate proves the model is well-formed, not that it is true).
- Convention drift from the exemplar (naming, layout, changelog entry).

Confidence-filter: report only issues you are confident matter, each with `file:line`, why, and the concrete fix.

## Deliver

Verdict **APPROVE** or **REQUEST CHANGES**, findings numbered and tagged **BLOCKING** / **ADVISORY**. Write the report to `docs/phases/<n>/reports/code-review.md` (do not commit it; the lead will), send it with `SendMessage` to `team-lead` (full report in `message`, not `summary`), AND return it as your final text.
