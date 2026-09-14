# Backing up the panel itself

What the panel records - its users, websites, schedules, credentials and audit
trail - was never backed up. This is the design for doing it: where the backup
runs, how it is encrypted, how the key is kept, and how a dead host is rebuilt
from it. It is the first blocker of stage 1 of the production roadmap.

---

## 1. Why this was missing, and why that reason was wrong

Phase 14 excluded the panel's own database on purpose:

> It is not on this host — it is the control plane — and backing up the thing
> that records the backups from inside itself is a circularity better solved
> by the installer.

The first half is true of the development stack, where PostgreSQL is its own
container. It is not true of an installation. The installer creates the
panel's database on the host's own PostgreSQL (`127.0.0.1:5432`), beside the
customers' databases. **Lose the host and the panel's records go with it**,
and nothing in the panel could put them back: the backups page itself lives in
the database that is gone.

The installer never picked up the half it was handed. So nothing did.

---

## 2. Decisions

| Question | Decision | Why |
|---|---|---|
| Where it runs | A new backup type, `panel`, in the Agent's existing pipeline | Phase 14 already has off-host destinations (S3, SFTP), a read-back verification, retention and schedules. An installer timer writing a local file would not survive losing the host, which is the case this exists for. |
| Encryption | The archive is encrypted | It holds every account's password hash, every session-token hash, the audit trail, and the encrypted provider, destination, mail and Cloudflare credentials. A leaked bucket should reveal nothing. |
| The key | Derived from the panel's existing `ENCRYPTION_KEY` | Losing that key already makes every stored secret unreadable, so tying the backup to it adds no new way to fail and no second secret to keep. |
| Restoring | A command on the host, not a page in the panel | A panel restoring its own database from inside itself is the circularity Phase 14 named. The restore runs with the panel stopped. |

---

## 3. Who may take one

`backup.manage` is granted to the **operator** role, which is deliberately
"not user management … or server-level configuration". A panel backup
contains every account's password hash and every stored credential, so an
operator who could take one could extract everything that role withholds.

Creating, verifying, downloading or deleting a `panel` backup therefore also
requires **`server.manage`**, which only administrators hold. The other types
are unchanged.

---

## 4. What the archive is

The same archive Phase 14 writes - a gzip tar ending in `manifest.json` - with
one database member: a plain SQL dump of the panel's database, made with the
same `pg_dump` path the Agent already uses for customers' databases.

**The database is named by the Agent's configuration, never by the request.**
The installer writes `AGENT_PANEL_DATABASE`, and a `panel` request names no
database at all. Otherwise the `panel` type would be a way to dump any database
on the host under an administrator's permission. On a host where the variable
is not set - the development stack - the type reports itself unavailable rather
than guessing.

The archive is then sealed (section 5) before it is sent. Everything Phase 14
does after that is unchanged: the stored bytes are digested, sent, read back and
compared. Verifying a `panel` backup also unseals it, which is what proves the
key opens it, not only that the bytes arrived.

---

## 5. The sealed format

`shared/seal`, standard library only, so the Agent keeps no third-party
dependencies and the API and the recovery command derive the same key.

```text
header   "JHSEAL1\n"         8 bytes, the format and its version
         salt                32 random bytes, fresh for every archive
         chunk size          4 bytes, big-endian
body     chunk 0 … chunk n   AES-256-GCM, each ciphertext + 16-byte tag
```

- **Key, in two steps.** First `seal.PanelBackupKey`: HKDF-SHA256 over the
  32-byte `ENCRYPTION_KEY` with the label `jothost panel backup master v1`.
  That derived key is what the API hands the Agent - never `ENCRYPTION_KEY`
  itself, which decrypts every credential in the database the Agent is backing
  up and has no need to read. Then, per archive, HKDF-SHA256 over the derived
  key with the archive's own salt and the label `jothost panel backup v1`.
  Every archive has its own key. The recovery command runs the same two steps
  from the escrowed `ENCRYPTION_KEY`.
- **Nonce:** an 8-byte chunk counter, three zero bytes, and a final-chunk flag.
  A fresh key per archive means the nonces never repeat under one key.
- **Associated data:** the whole header, on every chunk. Changing the version,
  salt or chunk size makes every chunk fail to open.
- **The last chunk says it is last.** A stream that ends at a chunk boundary
  without a final chunk is refused as truncated. Without the flag, an archive
  cut off after its third megabyte would open as a shorter, valid archive.
- **Reordering, duplicating or appending** chunks fails: the counter is in the
  nonce, and anything after the final chunk is refused.

It is a small construction on purpose. There is no negotiation, one version, and
no option that makes it weaker.

---

## 6. Keeping the key

A sealed backup is only as recoverable as the key.

- The installer tells the operator, in its closing summary, to export the key
  and store it **off this host**.
- `install.sh export-key --to FILE` writes it with mode `0600` and says what it
  is for. It is never printed to a terminal or a log, and an existing file is
  never overwritten: that could replace the only copy that opens older backups.
- The file holds `ENCRYPTION_KEY` itself, not the key the Agent seals with. The
  Agent's derived key does not open a backup on its own, so a compromised Agent
  cannot read the backups it writes.
- `docs/RECOVERY.md` says it plainly: without the key there is no restore, and
  no way to make one.

---

## 7. Rebuilding a dead host

```bash
sudo ./install.sh install --domain panel.example.com   # if it is not installed
sudo ./install.sh restore-panel --from panel-backup.tar.gz --key-file panel.key
```

`restore-panel` restores into an installed panel. It does not install one:
installing is its own command, already proven on every supported host.

1. **Open.** `jothost-agent -open-panel-backup` unseals the archive with the
   key, checks every member against the manifest, and refuses an archive that
   is not a panel backup. It writes the SQL dump into a 0700 directory and
   changes nothing else.
2. **Load**, while the panel keeps running: the dump is replayed into a new
   database, as the panel's own role, so what is restored belongs to the
   account the API connects as.
3. **Swap.** The API is stopped, and the two databases are renamed in one
   transaction. The database being replaced is kept as
   `jothost_before_restore_<time>`.
4. **Re-home.** If the backup came from a host with another name, its single
   server record is renamed to this host. Otherwise the API would register a
   second, empty server and every website would belong to the old one.
5. **Key.** `ENCRYPTION_KEY` is replaced with the backup's, and the previous
   `api.env` is kept.
6. **Start.** Migrations run forward, and the panel must report itself ready.

Steps 1 and 2 change nothing the panel is using, so a wrong key, a damaged
archive or a dump that will not load stops there. After step 3, a failure puts
the previous database and key back.

---

## 8. How it is proved

A two-host drill in CI, on the systemd hosts from stage 1's first item:

`tests/recovery/panel_restore_drill.sh`:

1. Install onto host A, create a website, a PostgreSQL database and a
   destination with a stored secret.
2. Take a `panel` backup through the API and export the key. Remove host A.
   Only the archive and the key file go across, which is what an operator
   would have kept.
3. Install onto a clean host B. A restore with the wrong key is refused, and
   B's own administrator still signs in.
4. `restore-panel` from the archive and key.
5. On host B: A's administrator signs in, A's website is listed, A's stored
   secret **decrypts** (the check that the key really came across), B's old
   password no longer works, and the panel is ready.

Each part has a test that fails without it: the format against truncation,
reordering, tampering and the wrong key; the permission against an operator;
the database name against a request that names one.

---

## 9. What this does not do

- **It does not back up customers' data.** Websites and databases have their
  own backup types. A host is recovered with both.
- **It is not point-in-time recovery.** A restore returns the panel to the
  moment of the backup. What happened afterwards is lost.
- **It does not protect against losing the key.** Nothing can.

---

## 10. How it lands

1. **This document and `shared/seal`**, with its tests.
2. **The `panel` backup type**: Agent configuration, the API type, the
   `server.manage` requirement, the schedules page.
3. **`restore-panel` and `export-key`**, the recovery documentation, and the
   two-host drill that closes the stage 1 gate.

Writing the drill found that the Agent had never been able to reach PostgreSQL
on an installed host (see the changelog). The unit tests used a stand-in
database, so they could not have caught it.
