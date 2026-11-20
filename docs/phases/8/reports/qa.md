# Phase 8 -- QA / reality-check lane

The full findings (paths, lines, commands, evidence) were delivered to the lead and are not public: the subject is the operator's private infra repository.

Verdict: **NEEDS WORK**. The blocking findings of the other lanes stand at the reviewed head; this lane adds 0 blocking and 6 advisory. Nothing in either repository was modified, committed or pushed; experiments ran in a private directory, deleted afterwards.

## Checks run

- The hygiene suite: 94 passed, 2 skipped (older tests), 0 failed; all 22 tests of the new contract pass. Tree clean before and after.
- Terraform format check: pass for the tunnel module and a copy of the leaf.
- Terraform validate of a copy of the tunnel module, no backend: pass.
- YAML parse of the 4 new or changed YAML files: pass.
- `systemd-analyze verify` on copies of the 2 units: pass.
- The SSH daemon template, rendered as Ansible renders it: 4 switch-off renders are byte-identical to the file before this phase; switch-on adds only the look account in its two loopback forms and a final block naming the root-owned key file.
- The config template, rendered with synthetic values and generated keys, checked by a gateway built from this repository as a synthetic service user with the playbook's owners and modes: accepted with real-shaped values; refused for each of 3 placeholders and in 4 negative controls.
- The plan document's nine items: 9 of 9 present.
- 8 mutations against the hygiene suite: 5 killed, 3 survived.
- The three committed reports here: no private identifier found.

## Findings

1. ADVISORY -- three mutations survive: a widened source list on the key line that still holds both loopback forms, removal of a hardening directive the test does not list, and an account-id-shaped value in the playbook.
2. ADVISORY -- the attended sequence: one check in its first step cannot succeed until a later step has run, and that step does not list the logins it needs.
3. ADVISORY -- with the switch off the playbook still gathers facts (read-only); the guard that skips absent units is not asserted.
4. ADVISORY -- the package signing key's file name could not be re-verified and may be out of date.
5. ADVISORY -- one group variable is unused; one comment carries a stale step number.
6. ADVISORY -- one phrase in the committed code-review report follows the plan document's wording closely; it carries no identifier.

## Not run

Ansible, `terragrunt`, Terraform against a backend, `sshd` (absent here), a real start of either unit, the health check. Nothing was applied, converged, synced or logged in to.
