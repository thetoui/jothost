# Recovery

What to do when something is wrong, in the order worth trying it.

Every claim in section 1 is exercised by `tests/recovery/phase24_recovery.sh`,
which stops the API, the Agent and PostgreSQL in turn on a live host and checks
what survives. Run it with `make docker-test-recovery`. Claims here are not
aspirations; they fail the build when they stop being true.

---

## 1. What a panel outage is not

**A control panel outage is not a hosting outage.** The panel writes
configuration and then gets out of the way: nginx serves sites, PHP-FPM runs
them, and neither asks the panel for permission on any request.

| What is down | Websites | Panel | What happens |
|---|---|---|---|
| `jothost-api` | **served** | unreachable | Nothing queues; nothing is lost. Start it. |
| `jothost-agent` | **served** | reachable, read-only in practice | Readiness reports the Agent missing and names it. Changes are refused rather than accepted and dropped. |
| PostgreSQL | **served** | reachable, refuses work | Requests needing the database fail loudly. The failure does not disclose how the panel connects. |
| Redis | **served** | sign-in fails | Sessions live here. Start it and sign in again. |

The panel recovers on its own when the dependency returns. It does not need
restarting after PostgreSQL comes back.

**First question when a site is down: is the site actually down, or only the
panel?** Ask the site, not the panel:

```bash
curl -I https://the-site.example
systemctl status nginx      # or: rc-service nginx status
```

## 2. Is it running at all?

```bash
jothost-api version
jothost-agent version
/opt/jothost/dist/install.sh status      # or wherever install.sh lives
```

`status` reports the panel's domain, whether the API is healthy, and whether
its dependencies are ready. `readyz` is the machine-readable form:

```bash
curl -s http://127.0.0.1:8080/readyz
```

Readiness names what is missing. "Not ready" with the Agent named means the
Agent, not the database.

## 3. Repair

For a host somebody has edited by hand — a deleted vhost, a service that was
disabled, permissions that drifted:

```bash
sudo ./install.sh repair
```

Repair rewrites what the panel owns and puts back what is missing. **It does
not touch secrets**: the encryption key, the Agent token and the database
password are preserved, and the recovery drill checks that they are.

If a website's own configuration is the problem, the panel's applications are
unaffected — they run on their own nginx and their own PHP-FPM master. See
ARCHITECTURE.md.

## 4. Services that did not come back after a reboot

The Agent enables what it finds running, so a service stopped deliberately
stays stopped. A service that should be running and is not:

```bash
systemctl status nginx php-fpm8.3 mariadb   # names vary by distribution
```

The Agent's own web stack — the second nginx and PHP master that serve
phpMyAdmin — is **not** registered with the init system. The Agent starts it,
and reconciles it every time the Agent starts. If phpMyAdmin answers 502 after
a reboot, restart the Agent:

```bash
systemctl restart jothost-agent
```

Then check the Agent's log for `the panel's nginx is running`.

## 5. Restoring from a backup

Restores are deliberate: they require explicit confirmation, and the panel
verifies a new backup before it overwrites a valid one.

Through the panel: **Backups → the backup → Restore**, and confirm.

What a backup contains depends on what was asked for — a website's files, a
database dump, or both. A database backup restores over the existing database;
take a fresh one first if the current state matters.

**A website or database backup does not contain the panel's own records.**
Those are a **panel** backup, which only an administrator can take, and which is
sealed with the panel's key.

### Keep the panel's key off the host

```bash
sudo ./install.sh export-key --to /root/jothost-panel.key
```

This writes `ENCRYPTION_KEY` to a new file with mode `0600`. It never prints the
key, and it never overwrites an existing file. Move the file somewhere that is
not this host, then delete it here.

**Without this key there is no restore, and no way to make one.** It is what
panel backups are sealed with, and it is what decrypts two-factor secrets,
database passwords and destination credentials in the restored database.

### Rebuilding the panel on a new host

1. Install the panel on the new host as usual.
2. Copy the panel backup archive and the key file onto it.
3. Restore:

   ```bash
   sudo ./install.sh restore-panel --from panel-backup.tar.gz --key-file jothost-panel.key
   ```

The restore changes nothing until the Agent has opened the archive with the key
and checked every member against its manifest. It loads the backup into a new
database while the panel keeps running, and only then stops the API and swaps
the two databases. If the panel does not come back ready, the swap and the key
are undone.

Afterwards:

- Sign in with an account **from the backup**. Accounts on the new host from
  before the restore are gone.
- The database it replaced is kept as `jothost_before_restore_<time>`, and the
  previous configuration as `api.env.before-restore-<time>`. Remove them once
  the restored panel is right.
- A panel backup holds the panel's records, not the websites. Restore websites'
  files and databases from their own backups.

## 6. Locked out

**SSH.** The panel refuses to turn off password authentication when no account
has an authorised key, so this should not happen through the panel. If it did
happen another way, use your provider's console and:

```bash
rm -f /etc/ssh/sshd_config.d/*jothost*
systemctl restart sshd
```

**Firewall.** A firewall change that breaks connectivity rolls back
automatically — the panel applies rules provisionally, verifies, then commits.
If a rule was applied outside the panel, use the provider's console and reset
with your firewall tool directly (`ufw`, `firewalld`, or `nft`).

**The panel's administrator password.** Create another administrator from the
host:

```bash
JOTHOST_ADMIN_USERNAME=rescue \
JOTHOST_ADMIN_PASSWORD='a-long-password' \
  jothost-api create-admin
```

**Two-factor.** On the verification step, choose **Use a recovery code** and
enter one of the codes shown when two-factor was turned on. Each works once.
Sign in, then replace them under **Account security**.

With neither the authenticator nor a recovery code, remove two-factor from the
account on the host. Run it as the API's account, with its configuration:

```bash
su -s /bin/sh jothost-api -c 'set -a; . /etc/jothost/api.env; set +a; /opt/jothost/bin/jothost-api reset-two-factor admin'
```

The account then signs in with its password alone. Turn two-factor back on
straight away. The reset is recorded in the audit log as `user.2fa_reset`.

## 7. Migrations

The API applies pending migrations at startup when `AUTO_MIGRATE` is on, which
is how the installer configures it. Each runs in a transaction, and a failure
stops the process rather than serving against a half-migrated schema.

With auto-migration off, the API *verifies* the schema instead and refuses to
start when it does not match the code — which fails at boot with a clear reason
rather than later, at query time, with a confusing one.

To run them by hand:

```bash
jothost-api migrate up
jothost-api migrate status
```

Every migration has a paired `down`. Rolling one back is possible and is a
deliberate act — read the `down` file first, because a down migration that
drops a column drops the data in it.

## 8. Removing the panel

```bash
sudo ./install.sh uninstall          # keeps configuration and the panel database
sudo ./install.sh uninstall --purge  # removes them too
```

**Neither touches customer websites, their files, or their databases.** The
installer checks this on every run; see `tests/integration/phase23_installer.sh`
sections 10 and 11. `uninstall` leaves configuration behind so a reinstall
reuses it — including the encryption key, which is what makes the existing
control database readable again.

## 9. Reading what happened

```text
/var/log/jothost/          the panel's own logs
/var/log/jothost/web/      the panel's private nginx and PHP master
journalctl -u jothost-api -u jothost-agent
```

And the audit trail, which answers "who deleted that website" without a
database client: the panel's Audit page, or `GET /api/v1/audit`. See
[AUDIT.md](AUDIT.md).

## 10. When reporting a problem

Include:

```bash
jothost-api version
jothost-agent version
./install.sh status
curl -s http://127.0.0.1:8080/readyz
```

The version matters more than it looks: it identifies the exact build,
including the commit, and a report naming only "the latest" cannot be traced to
code.
