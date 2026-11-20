# Installing yasshm

These are the install steps for the deployment contract of the spec (section 8): one gateway on a Linux host with systemd, one or more SSH targets, and an edge in front. They are generic: every host name, address and account below is an example (`example.net`, documentation address ranges), and nothing here is a real value. The [operator guide](../docs/operator-guide.md) is the reference for everything after the install; read its "What yasshm is not" section first.

You need root on the gateway host, root (or a way to add one account and one `authorized_keys` line) on each target, and a Cloudflare account with a domain for the edge. Nothing here is run by the project's build; the steps are for you.

## Files in this directory

| File | What it is |
|---|---|
| [yasshm.service](yasshm.service) | The hardened systemd unit. Install it as `/etc/systemd/system/yasshm.service`. |
| [config.example.yaml](config.example.yaml) | A complete example config with a comment on every key. Copy it to `/etc/yasshm/config.yaml`. |

Every release carries both files beside the binaries (as `yasshm.service` and `config.example.yaml`), and `SHA256SUMS` covers them. Install from the release's copies, not from a source tree that may have moved on: the steps below use the names the release gives them.

## 1. The user, the directories and their modes

The gateway refuses to start when a file or directory it depends on is too open, so the modes below are not decoration.

```sh
useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin yasshm

install -d -m 0750 -o root   -g yasshm /etc/yasshm
install -d -m 0700 -o yasshm -g yasshm /etc/yasshm/keys
install -d -m 0700 -o yasshm -g yasshm /var/lib/yasshm
install -d -m 0700 -o yasshm -g yasshm /var/log/yasshm
```

| Path | Owner and mode | Why |
|---|---|---|
| `/etc/yasshm/config.yaml` | root:yasshm, 0640 (or yasshm, 0600) | The service reads it. It refuses a config that group or other can write, because whoever writes it decides which host keys the gateway trusts. |
| `/etc/yasshm/keys/` | yasshm, 0700 | The gateway's private keys. |
| `/etc/yasshm/keys/<name>` | yasshm, 0600 | The service refuses a key that group or other can read, one that is not a regular file owned by the service user, one with a passphrase and one over 64 KiB. |
| `/var/lib/yasshm` | yasshm, 0700 | The job database and captured output (which can hold secrets from targets). The service refuses a directory with any bit for group or other. |
| `/var/log/yasshm` | yasshm, 0700 | The audit log, created as mode 0600. The service refuses a log whose directory group or other can write. Put it on a file system apart from `/var/lib/yasshm` if you can. |
| a `known_hosts_file` | not writable by group or other | Same reason as the config. |

The unit names `/var/lib/yasshm` and `/var/log/yasshm` in `ReadWritePaths` and does not create them, so a directory that is missing (or a mount that did not come up) stops the service at start instead of being replaced by one with another owner.

## 2. The binary, from a release, with its checksum verified

A release has five files: `yasshm_<version>_linux_amd64`, `yasshm_<version>_linux_arm64`, `yasshm.service`, `config.example.yaml` and `SHA256SUMS` (which lists the other four); the version has no leading `v` (the tag `v0.1.0` gives `yasshm_0.1.0_linux_amd64`, and `yasshm version` prints `yasshm 0.1.0`). Download the binary for your host (`uname -m` says `x86_64` for amd64 and `aarch64` for arm64), `yasshm.service`, `config.example.yaml` and `SHA256SUMS` from the same release into an empty directory, then:

```sh
sha256sum --check --ignore-missing SHA256SUMS
install -o root -g root -m 0755 yasshm_<version>_linux_amd64 /usr/local/bin/yasshm
yasshm version
```

`sha256sum` must say `OK` for each file you downloaded (the unit and the example config too: an unchecked unit is a file that runs as a service). `SHA256SUMS` comes from the same place as the binary, so the check catches a damaged or truncated download, not a tampered release; if that matters to you, build the files yourself from the tag (`mise run build` in a clone, with `VERSION` set to the version) and compare. `yasshm version` prints the version, the commit and the build date.

## 3. The gateway's SSH key

Generate the key on the gateway host, as the service user, so it never exists anywhere else. It must have no passphrase (the gateway cannot ask for one) and be a key type your target's `sshd` accepts; ed25519 is the example.

```sh
sudo -u yasshm ssh-keygen -t ed25519 -N '' -C 'yasshm gateway' -f /etc/yasshm/keys/lab-look_ed25519
chmod 0600 /etc/yasshm/keys/lab-look_ed25519
cat /etc/yasshm/keys/lab-look_ed25519.pub
```

The `.pub` line is what goes onto the target (step 4). Use one key per target, or per group of targets you would revoke together: a key on every target is a key you must remove from every target.

## 4. On each target: the unprivileged `look` account

Make an account that can do only what you are willing to let an assistant do. The gateway's guard rails (a denylist) are not a boundary; this account is.

```sh
useradd --create-home --shell /bin/bash look
usermod -p '*' look
```

`usermod -p '*'` leaves no password that can match, so there is no password login, and it does not lock the account (a locked account refuses key logins on a target whose `sshd` runs with `UsePAM no`). Keep the account out of every privileged group (`root`, `sudo`, `wheel`, `adm`, `docker`, `lxd`, `libvirt`, `kvm`, `disk`, `shadow` and the like) and give it no `sudo` rights.

**If this target is the gateway host itself,** stop here and read [When a target is the gateway host itself](../docs/operator-guide.md#when-a-target-is-the-gateway-host-itself): that account is a local user of the gateway host and needs containing. Prefer a separate machine or VM.

**The account must not be able to change which keys log in as it.** Any command the assistant runs as that account could otherwise add a key to its own `~/.ssh/authorized_keys` or remove `restrict`, and that opens a login that never passes the edge, the token check or the audit log. So keep the gateway's key in a file owned by root, outside the account's home, and tell `sshd` to read that file for that account only; the account's own `~/.ssh/authorized_keys` is then not consulted. As root on the target:

```sh
install -d -m 0755 -o root -g root /etc/ssh/authorized_keys
install -m 0644 -o root -g root /dev/null /etc/ssh/authorized_keys/look
$EDITOR /etc/ssh/authorized_keys/look        # the one line below
```

with the public key from step 3 and nothing else on the line but `restrict` before it:

```text
restrict ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXAMPLEONLYEXAMPLEONLYEXAMPLEONLYEXAMPLEONLY00 yasshm gateway
```

Then name the file in the SSH daemon's configuration, in a `Match User` block. Put the block at the end of `/etc/ssh/sshd_config` (a `Match` block runs to the next `Match` or the end of the file, so nothing may follow it that is meant for everyone), or in a drop-in file that your `sshd_config` includes at its end:

```text
Match User look
    AuthorizedKeysFile /etc/ssh/authorized_keys/%u
```

Check the configuration before you reload it, and check what it says for this account and for another:

```sh
sshd -t
sshd -T -C user=look,host=lab-1.example.net,addr=192.0.2.10 | grep -i '^authorizedkeysfile'
sshd -T -C user=nobody,host=lab-1.example.net,addr=192.0.2.10 | grep -i '^authorizedkeysfile'
systemctl reload ssh        # the unit is called sshd on some distributions
```

The first `grep` must show `/etc/ssh/authorized_keys/%u` and the second the default. (`sshd -t` prints nothing when the file is valid.) Keep the directory and the file owned by root and not writable by anyone else: `sshd` refuses a key file that others can write, and an account that could write it could undo all of this. MT-002 checks that the `look` account cannot.

- `restrict` turns off port forwarding, agent forwarding, X11 forwarding and the terminal in one word. The gateway uses none of them.
- **No forced command.** Do not add `command="..."`. The gateway signals a running command with an SSH `signal` request, and `sshd` does not deliver a signal to a session that runs under a forced command, so `kill_command` and timeouts could not stop anything.
- You may add `from="192.0.2.10"` before `restrict` to accept the key only from the gateway's address. Use the address the target sees, which is not always the one you expect.

The commands are interpreted by the account's login shell, so give it a shell that runs `sh -c` style command strings.

**Before you enable this target, audit what the account can read and reach.** Being in no privileged group is not enough. Log in as the account and check: its `sudo` rights (`sudo -l`); files that other users can read under other services' configuration and home directories (a world-readable administrator credential of another service, such as a cluster's admin configuration file, makes any local account an administrator of that service); secrets visible in process arguments (`ps -eo args`); unauthenticated services on loopback (`ss -ltn`); and sockets writable by others. Close what you find on the target. The operator guide's [Adding a server](../docs/operator-guide.md#adding-a-server-and-pinning-its-host-key) has the list, and MT-002 re-checks one file on every release.

## 5. Pin the target's host key

The gateway pins each target's host key in its config and never trusts one on first use. Get the key line from the target itself, over a channel you trust:

```sh
# on the target (the key sshd serves; use the type you want to pin)
cat /etc/ssh/ssh_host_ed25519_key.pub
ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub
```

If you cannot read the target's file, scan it from the gateway host and then compare the fingerprint with one you read on the target:

```sh
ssh-keyscan -t ed25519 -p 22 lab-1.example.net > /tmp/lab-1.pub
ssh-keygen -lf /tmp/lab-1.pub
```

`ssh-keyscan` writes the host name first (`lab-1.example.net ssh-ed25519 AAAA...`); that is a `known_hosts` line, not a `host_key` value. Once the fingerprint matches the one you read on the target, either drop the host name and paste the rest (type and base64; a trailing comment is dropped) into the server's `host_key`, or name the scanned file in `known_hosts_file`. For a key read on the target, paste the same type and base64 into `host_key`. A target that later presents another key is never spoken to: the call fails with `host_key_mismatch`.

## 6. The config

```sh
install -m 0640 -o root -g yasshm config.example.yaml /etc/yasshm/config.yaml
$EDITOR /etc/yasshm/config.yaml
```

Replace every value that starts with `REPLACE_`: the AUD tag of your Access application and the team domain (step 8 says where they come from), your email in `principals`, and each server's `host` and `host_key` (a host key placeholder is `ssh-ed25519 REPLACE_...`). The loader refuses a value that still starts with `REPLACE_`, with `replace the placeholder value`. Also set the identity's `user` and `private_key_file`, and each server's `identity`. The comments in the file give each key's meaning, default and range; the operator guide has them as a table. Then check it as the service user, the way the unit does (as root the same command fails, because the gateway refuses a private key that is not owned by the user running it; a configuration-management tool's "validate" hook runs as root and has the same problem, which the [operator guide](../docs/operator-guide.md#the-config-file) says how to avoid):

```sh
sudo -u yasshm /usr/local/bin/yasshm config check --config /etc/yasshm/config.yaml
```

It prints `config ok: 1 tenant, 1 server` and exits 0, or prints one line per problem, each `path.to.key: message (line N)`, and exits 1. It never connects anywhere, so it does not prove that a target is reachable (`get_server_info` does).

## 7. The unit

```sh
install -m 0644 yasshm.service /etc/systemd/system/yasshm.service
systemd-analyze verify /etc/systemd/system/yasshm.service
systemctl daemon-reload
systemctl enable --now yasshm
journalctl -u yasshm -o cat
```

`ExecStartPre` runs `yasshm config check`, so a bad config stops the start with the problem lines in the journal. A healthy start logs `config loaded`, `listening` and `readiness`. Until the Access side exists (step 8) the key set cannot be fetched and `readiness` says `ready: false`; that is expected. Check the gateway from the host itself:

```sh
curl -s http://127.0.0.1:8822/healthz
```

It answers `{"status":"ok","version":"..."}` once the key set is loaded and `{"status":"degraded","detail":"key set not loaded","version":"..."}` with status 503 before. A tool call needs a real Access token, so there is nothing more to try from the shell: a request to `/mcp` without one gets 401.

**The unit has not been started under a real systemd yet.** `systemd-analyze verify` checks its syntax and the build checks its directives, but nobody has run it: the first start is yours, and it is the place a hardening directive is most likely to bite (a missing `ReadWritePaths` directory, or a system call the filter denies, which shows in the journal as an `operation not permitted` error). The [operator guide](../docs/operator-guide.md#the-gateway-will-not-start) says what to look at.

The unit passes no `MCPGODEBUG` or any other environment variable, and it should stay that way: the MCP library reads `MCPGODEBUG` at start and a malformed value stops the process. The unit leaves `IPAddressDeny` out because the gateway must reach every target and the edge's key-set host; a commented example with the allow list you would need is inside the unit.

## 8. The edge

The gateway listens on `127.0.0.1` only. The edge reaches it through a tunnel connector that runs on the same host. This is Cloudflare's part of the work, and their pages are the reference for the clicks; this section says what to build and where the two values for the config come from.

1. **A tunnel connector on the gateway host** with one public hostname (for example `yasshm.example.net`) routed to `http://127.0.0.1:8822`, the port of `server.listen`. The connector makes outbound connections only; the host needs no inbound port. Start at Cloudflare's [Tunnel documentation](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/), which links the steps to create a tunnel and to download `cloudflared`.
2. **A self-hosted Access application** for that hostname, with a policy that admits your identity and nobody else. Cloudflare's page on [self-hosted applications](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/self-hosted-public-app/) is the reference.
3. **Managed OAuth switched on** for that application, so a Claude connector can sign in through Access and yasshm needs no OAuth code of its own: [Managed OAuth](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/managed-oauth/). The OAuth client must support RFC 8707. The connector's callback URL (`https://claude.ai/api/mcp/auth_callback`) is probably one of the application's allowed redirect URIs to set there (the vendor's page lists the setting and does not name Claude). Do not turn it on if you run your own OAuth server behind Access.
4. **Two values into the config.** The team domain (your `<team>.cloudflareaccess.com` host) goes into `auth.cloudflare_access.team_domain`, and the application's AUD tag goes into `auth.cloudflare_access.audience`. Cloudflare's page on [validating the Access token](https://developers.cloudflare.com/cloudflare-one/access-controls/applications/http-apps/authorization-cookie/validating-json/) describes the token and its `aud` claim, which carries the AUD tag; look for the tag in the application's settings (where the dashboard shows it was not read for this document). Then restart: `systemctl restart yasshm`. `readiness` should turn to `ready: true` and `/healthz` to `ok`.
5. **Add the connector in Claude** with the URL `https://yasshm.example.net/mcp` (Customize, Connectors) and sign in. The [operator guide](../docs/operator-guide.md#connecting-a-claude-custom-connector) says what was read in the vendor's pages and what is only inferred.

Then run the manual tests in [test/manual](../test/manual) (see [TESTING.md](../TESTING.md)): they are the check that the edge, the Access policy and a real target behave as this document says.

## Upgrade, rollback and uninstall

They are in the [operator guide](../docs/operator-guide.md#running-upgrading-and-rolling-back), which also covers the data directory, the audit log and the release process.
