# Phase 8 -- implementer (summary written by the lead)

The implementer's full report (paths, values, the attended sequence) was delivered to the lead and is not public: the subject is the operator's private infra repository. This file names things by role only.

## What was built, in the infra clone, on the session's working branch there

Every numbered item of the plan document's work list:

- the tunnel module, restored and trimmed to one hostname with a loopback origin, no identity provider of its own, and managed OAuth expressed in the module; and its new leaf
- the playbook, inert behind a rollout switch that defaults to off: with the switch off it only stops the two units if they exist; with it on it runs pre-flight checks, installs the release binary by pinned checksum, generates the gateway's key on the host, creates the unprivileged target account, renders the gateway's config and checks it with the gateway's own `config check` as the service user before it replaces the live file, installs the shipped unit unchanged, and installs the edge connector as its own hardened unit
- the SSH daemon template change that admits the target account from loopback only and reads its key from a root-owned file
- the group variables (placeholders for every value the operator must fill in), the one secret mapping (a name, never a value), the import into the site playbook
- a runbook, the access-guide section, the changelog entry, and dated build notes in the plan document
- a hygiene contract test for all of the above

## Checks that ran in the sandbox

- the infra repository's hygiene suite: green, with the new contract
- Terraform format check on the module and the leaf; `validate` of a copy of the module with no backend: green
- a YAML parse of every new or changed YAML file: green
- the rendered gateway config against the real `yasshm config check`: accepted with real-shaped values, refused while a placeholder remains
- `systemd-analyze verify` on the units: clean
- the SSH daemon template rendered for both switch states: unchanged with the switch off

Not run, and listed as attended in the plan document: the playbook's syntax check and dry run (they need the repository's container runner, and the sandbox has no container daemon; Ansible must not be installed locally there), anything against the remote state, the password manager, the edge, or the host.

## Review gate

Code review REQUEST CHANGES (1 blocking), security BLOCK (2 blocking), QA NEEDS WORK (the same blockers; every runnable check green; 5 of 8 mutations of the hygiene test caught). The blockers, in general words:

1. Being in no privileged group did not make the target account unprivileged on the first target: an existing setting there, outside this change, lets any local account read administrator-level material. The playbook now refuses to continue while that holds, and the plan makes an audit, run as the account, an attended step.
2. The first target is the gateway host itself, so the target account is a local user of the gateway host, which the public spec's deployment assumption excludes. The plan now records this as a decision for the operator (a separate machine, or this host with containment), and the playbook implements the containment for the second option. The same guidance was added to this repository's operator guide, threat model and spec section 8.

One fix commit in the infra clone applied both and the accepted advisories (a hygiene test that pins the vendored unit to this repository's copy and matches the key line exactly, a full uninstall list, a runnable order for the attended sequence, tighter connector unit, module input validation). The hygiene suite and the other runnable checks are green after it.

## Limits

Nothing was applied, planned against a backend, converged or synced. No secret was read or requested. No command reached the host, the edge or a password manager. Nothing from the infra repository was written into this one.
