# Phase 8 -- security lane

The full findings (paths, lines, evidence, fixes) were delivered to the lead and are not public: the subject is the operator's private infra repository. This file names things by role only.

Verdict: **BLOCK**. 2 BLOCKING, 10 ADVISORY. Read-only review: nothing was modified, no check was run, no secret file was opened, nothing was applied.

## Blocking

1. BLOCKING -- the playbook checks which groups the target account is in, but not what an account in no group can already read on the target, and the plan document leaves that to an open question framed as "which groups to add". The code is not safe to converge until that baseline is audited. Fix: a pre-flight that refuses to continue while a known root-equivalent file is readable by others, and an attended audit step (run as the account) before the switch is turned on.
2. BLOCKING -- where a target account lives on the gateway host itself, the public spec's deployment assumption (section 8: no untrusted local users on the gateway host) does not hold, and neither the plan document nor the playbook records or contains that. Fix: record it as a decision and a risk; contain the account (no scheduled jobs, no process that outlives its session, a home it cannot write, no network egress beyond loopback), or use a separate machine as the first target.

## Advisory

3. ADVISORY -- rollback stops the units and closes the SSH daemon to the account but leaves the account, its files and the connector's credential file. Fix: add a full uninstall list to the runbook.
4. ADVISORY -- the tunnel module says a one-time bootstrap read leaves no credential in remote state; earlier state versions keep it. Fix: take the credential out of band, or rotate it after the bootstrap, and correct the comments.
5. ADVISORY -- the connector unit runs on the host with remotely managed routing, so its reach is the host's. Fix: restrict the unit's network reach to the gateway's loopback port and the public edge.
6. ADVISORY -- the privileged-group check is a list of names to refuse. Fix: make it a list of names to allow.
7. ADVISORY -- the account's SSH restrictions rest on one key line. Fix: repeat them in the SSH daemon template's block for that account.
8. ADVISORY -- the attended sequence has no dry-run diff of the SSH daemon change with the switch on, and no negative login test from a non-loopback address. Fix: add both.
9. ADVISORY -- the key line is built from a public key file the service user owns. Fix: derive it as root, or assert it is a single key line.
10. ADVISORY -- the tunnel module accepts managed OAuth with an empty redirect list and an unvalidated operator identity. Fix: add input validation; confirm a consent screen in the attended dashboard step.
11. ADVISORY -- the group variables file will hold account identifiers that the same repository keeps out of the tree elsewhere. Fix: pick one convention.
12. ADVISORY -- minor hardening: an explicit no-diff on the credential task, an unpinned package keyring, a switch that is group-wide rather than per host.

## Verified

The SSH daemon template renders unchanged with the switch off; with it on the account is admitted from loopback only, the admin entry is unchanged, the account's block is last and the config is validated before reload. The key file is root-owned, the key line is restricted with no forced command. The vendored unit matches the public one and passes no environment. The binary is checksum-pinned. The connector credential is handled only by no-log tasks and a root-only file. No secret-shaped value is in the change. The switch-off path only stops the two units. Nothing from the private repository was found in this repository.
