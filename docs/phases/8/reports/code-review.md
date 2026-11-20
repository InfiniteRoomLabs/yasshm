# Phase 8 code review

The full findings, with paths, lines and values, were delivered to the lead only and are not public: the subject is the operator's private infra repository. This file names things by role.

Read-only review of the deployment support (the tunnel module and its leaf, the playbook with its templates and units, the group variables file, the SSH daemon template, the secret mapping, the hygiene test, the docs) against the plan document's nine items and against this repository's `deploy/` material and the code in `internal/config`, `internal/keyfile`, `internal/hostkey`, `internal/audit`, `internal/store`, `internal/cli` and `internal/sshclient`. Nothing was run but `git` and `grep`; no check of that repository, no Terraform, no Ansible.

## Verdict: REQUEST CHANGES

1 blocking, 6 advisory. All nine plan items are done; the four differences the implementer reported (root-owned key file with a `Match User` block, config checked as the service user on a staged copy, the connector's own unit, no provider version file in the module) are justified by this repository's `deploy/README.md` and code.

Checked and found correct by reading: with the switch off the SSH daemon template renders exactly as before and the playbook only stops units that exist; with it on, the look account is admitted from the two loopback forms only, the `Match` block is last and replaces the key file for that account only, and the admin entry is untouched; the key line is `restrict`, loopback-only, no forced command, in a root-owned file; owners and modes of the config, the key, the data directory and the audit directory are what the gateway accepts; the live config is never replaced by an unchecked one; the release URL uses the version without `v` under a tag with `v` and the checksum is pinned; the host key is pinned in the form the loader accepts; the token is written only by `no_log` tasks into a root-only file and never reaches a command line; the module keeps nothing of the retired applications and the token output is sensitive and empty outside the bootstrap.

## Findings

**1. BLOCKING -- the look account is not as unprivileged on the first target as the docs say.** An existing setting on that host, outside this change, lets any local account read far more than the plan document and the access guide assume, so the account stops being the boundary once the switch goes on. Fix: a pre-flight in the playbook that refuses to continue while that holds, an attended step to decide it, and corrected wording in the docs.

**2. ADVISORY -- the attended sequence is out of order in two places.** One check in its first step needs a value that a later step creates. Fix: move the later step first and the check after it; correct one stale step number in a comment.

**3. ADVISORY -- four hygiene checks are weaker than their contract.** The guard that makes the switch-off path safe on a host that never had the units is not asserted; the loopback-only check accepts extra addresses; the switch-off render is not compared with the original; one token-on-command-line form is not covered. Fix: assert each exactly.

**4. ADVISORY -- rollback leaves the connector's credential file on the host.** Fix: say so in the runbook's rollback, or remove it there.

**5. ADVISORY -- the token pre-flight's failure message is hidden by `no_log`.** Fix: carry the remedy in the task name.

**6. ADVISORY -- the package signing key file name was not re-verified by this lane.** Fix: confirm it in the attended first converge.

**7. ADVISORY -- small duplication.** One group variable is unused and three paths are written in both the playbook and the config template. Fix: use one source.

Not verified here: the provider's schema for managed OAuth and Ansible's syntax check (both need tools this lane may not run; the second is the first attended step).
