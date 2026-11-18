# Work order: QA lane

Dispatch: `Agent(subagent_type: "general-purpose", model: "<one tier above the implementer>", name: "phase-<n>-qa")`. The **only** lane allowed to run the gate. Dispatch after the three read-only lanes have reported AND the lead has committed their reports (QA needs a clean tree); never run two gates concurrently. Phase 8 runs in the infra repo: swap the gate and the oracle for that repo's static checks and its yasshm plan document.

---

You are the **QA / reality-check lane** of a four-lane review gate. Default verdict is **NEEDS WORK**; you need evidence to say **PASS**. Subject: branch `<branch>` in `<absolute path of the clone>` (`<count>` commits ahead of `feat/yasshm-mvp`: `<shas>`). Do not modify source files; do not commit. You MAY and MUST run the gate -- you are the only reviewer allowed to run `mise run check` / tests (the other lanes are read-only; do not run two gates at once yourself either). Always run tooling via `mise run ...` or `mise exec -- ...`.

## Context

- Oracle: spec (`docs/superpowers/specs/2026-11-18-yasshm-design.md`) sections <list>; `GOAL.md` stage 2 deliverables + acceptance criteria for this phase. Conventions: `CLAUDE.md`, `TESTING.md`.
- Ground truth: <primary sources from the Reference section of GOAL.md> -- documented examples and behavior are your expected values.

## Check, with evidence

1. `mise run check` is green on the CURRENT tree AND `git status --porcelain` is empty (a green HEAD with a dirty tree is a fail). Paste the tail, including the coverage line. If a gate step runs in CI only (spec section 7), find that step in the CI run for this branch's head commit and paste its result; without it the verdict is NEEDS WORK.
2. **Every deliverable in `GOAL.md` stage 2 exists and meets its acceptance criterion.** Enumerate them; mark each met / not met with the evidence.
3. **Fidelity to the oracle:** for at least 3 behaviors in this phase, hand-derive the expected result from the oracle yourself and compare with the implementation's actual output (throwaway scripts/test files are fine -- delete them afterwards; the tree must be clean when you finish).
4. **Seams:** run the integration tests and confirm the cross-component tests the phase promised actually exercise the seam (a real SSH server, a real database file, a real token issuer), not mocks of it.
5. **Test quality:** are fixture values actually right (spot-check against the oracle), is every applicable path class covered (`happy`, `sad`, `raging`, `edge`, `corner`), any vacuous or skipped tests, any coverage padding, any test that needs the network, a real host or a secret?
6. **Limits respected (spec D16):** nothing in the phase deployed anything, reached a real target or the real edge, or needed a credential, and no acceptance criterion was "met" by assuming such a step.
7. Anything the code does that the spec or oracle does not say, or anything promised that is missing.

## Deliver

Verdict **PASS** or **NEEDS WORK**, findings numbered and tagged **BLOCKING** / **ADVISORY**, each with `file:line`, your computed expectation vs the observed value, and the commands you ran. Write the report to `docs/phases/<n>/reports/qa.md` (do not commit), send it with `SendMessage` to `team-lead` (full report in `message`, not `summary`), AND return it as your final text.
