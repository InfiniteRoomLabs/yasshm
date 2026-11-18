# Work order: implementer

Dispatch: `Agent(subagent_type: "general-purpose", model: "<per the Retarget table>", name: "phase-<n>-impl")`. Fill every `<...>`. Pointers, not pasted text. Phase 8 runs in the infra repo: swap the yasshm-specific lines (spec sections, gate, coverage, architecture) for that repo's rules, taken from its `CLAUDE.md`, `CONTRIBUTING.md` and its yasshm plan document.

---

You are implementing **Phase <n> (<name>)** of `yasshm`. Work ONLY inside `<absolute path of the clone>` on branch `<branch>` (already created from `feat/yasshm-mvp`, clean). Do not touch other branches, other worktrees or the other repository.

## Read first (pointers, not pasted)

1. The oracle: `docs/superpowers/specs/2026-11-18-yasshm-design.md` sections <list>. Section 2 is locked; do not redesign.
2. Conventions: `CLAUDE.md`, `TESTING.md`, and `GOAL.md` stage 2 for this phase (the deliverable list and acceptance criteria).
3. **Ground truth before coding:** <the primary sources from the Reference section of GOAL.md that this phase is built against, with URLs or paths>. Read them before writing code; they carry details the spec summarizes.
4. Exemplar to mirror in shape and style: `<path to the most recently shipped phase's code + tests>`.

## Deliverables

- <file or package> -- <exactly what it must contain>
- Tests: <unit expectations>; <integration expectations for every cross-package seam this phase introduces>. Subtest names start with the path class (`happy/`, `sad/`, `raging/`, `edge/`, `corner/`). Total coverage >= 85%. `go test -race -shuffle=on -count=1 ./...` clean. No test reaches the network or a real host.
- Doc comments on every exported member; changelog `[Unreleased]` entry in the existing style.
- Architecture: remove `#planned` from the elements this phase builds (<FQNs>), run `mise run arch:gen`, and commit the model and the regenerated diagrams with the code.
- `mise run check` green on a clean tree. Always run tooling via `mise run ...` or `mise exec -- ...`; never a bare `go`.
- Commit on `<branch>` in TDD-sized conventional commits (`feat(<scope>): ...`), each green. **Stage and commit in separate tool calls.** Do NOT push, do NOT merge -- the lead runs the gate and ships.

## Gotchas (these cost prior runs time)

- <project-specific traps from CLAUDE.md Gotchas + GOAL.md Lessons relevant to this phase>
- If the spec is wrong about something the oracle does, implement what the oracle does, add a `> **STATE AS OF <date>**` callout in the affected spec section in the same commit, and list the discrepancy in your report.
- A new dependency (spec D14) is named and justified in your report, and `mise run check -- vuln` stays clean.
- This repo is public. Fixture hosts, users, keys, tokens and ids are synthetic (`example.net`, documentation address ranges, keys generated inside the test). Never paste a real id, token, host name or key, and never copy anything out of the infra repo.
- You have no secrets and need none. If a step seems to need a credential, a real host or the real edge, stop and report it: that work is the operator's (spec D16).

## Reporting (both channels)

When done (gate green, committed, `git status --porcelain` empty): write the report to `docs/phases/<n>/reports/implementer.md` (commit it), send the same report with `SendMessage` to `team-lead` (report in `message`, not `summary`), AND return it as your final text. Report: files created/changed, test counts, coverage, the exact gate tail, `git log --oneline feat/yasshm-mvp..<branch>`, `git status --porcelain` output, every new dependency with its justification, and every spec discrepancy or ambiguity you hit and how you resolved it. If you are genuinely blocked, report the blocker the same way instead of guessing.
