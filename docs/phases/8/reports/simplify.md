# Phase 8 -- simplification lane

The full proposals (paths, lines, before/after sketches) were delivered to the lead and are not public: the subject is the operator's private infra repository. This file names things by role only.

Verdict: little to cut. The work follows that repository's conventions, and most of its repetition is deliberate (pinned by the hygiene test, or mirroring the public install steps one section per step). Nothing was modified and no check was run by this lane.

## Proposals

1. APPLY-RECOMMENDED -- the hygiene test: replace overlapping substring assertions on the look account's key line with one exact match of its options field. Shorter and strictly stronger. About 3 lines saved, low risk.
2. APPLY-RECOMMENDED -- the hygiene test: a format string that interpolates nothing becomes a plain literal. No lines saved, no risk.
3. OPTIONAL -- the playbook: two near-identical ownership tasks for the gateway's key pair become one looped task. About 4 lines saved, low risk.
4. OPTIONAL -- the config template: take the private key path from the play variable that already holds it, not from a second literal. Changes quoting only in the rendered file.
5. OPTIONAL -- the hygiene test: pin the vendored unit to the public repository's copy with a digest of its body, compared offline. Adds about 6 lines; the body is identical to the public copy today.
6. OPTIONAL -- the playbook: drop three comments that restate the task or block name beneath them.
7. OPTIONAL -- the tunnel module: the token bootstrap procedure is explained in four places; keep one and point to it. About 9 lines saved, comments only.
8. OPTIONAL -- docs: shorten the changelog entry to a pointer at the plan's build notes; state where in the attended sequence the tracked-file edits land; trim one step the playbook already checks; move one stray sentence in the runbook.

## Do not apply

9. Reading the host key from gathered facts: facts are cached, so a changed key could be missed.
10. Rendering templates with a template engine in the hygiene test: needs a new test dependency and a lock change.
11. Merging the two guards in the sshd template: saves 2 lines, reads worse, or forces a test change.
12. Fetching the unit at converge time: adds a network step and a second pin.
13. Folding directories or units into wider loops, dropping the reload in handlers, removing the placeholder check, unused module outputs, the managed-OAuth toggle or the unused hostname variable: each changes ordering, the plan's contract, plan output or house style.
