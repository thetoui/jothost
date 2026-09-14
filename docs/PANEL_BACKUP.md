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

- **Key:** HKDF-SHA256 over the 32-byte `ENCRYPTION_KEY`, with the archive's
  salt and the label `jothost panel backup v1`. Every archive has its own key,
  and none of them is the key that protects the database's own secrets.
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
  is for. It is never printed to a terminal or a log.
- `docs/RECOVERY.md` gains a section: without the key there is no restore, and
  no way to make one.

---

## 7. Rebuilding a dead host

```bash
sudo ./install.sh restore-panel --from jothost-panel-….tar.gz.sealed --key-file panel.key
```

1. Install normally if the panel is not already installed.
2. Put the escrowed key in place of the one this installation generated, so
   the restored secrets can be read.
3. Stop the API.
4. `jothost-agent panel restore`: unseal, check every member against the
   manifest, and replay the dump into the panel's database, replacing what is
   there.
5. Start the API. Migrations run forward if the archive came from an older
   version.
6. Check an administrator can sign in, the same way the installer does.

An archive that fails to unseal or to match its manifest stops at step 4,
before anything is replaced.

---

## 8. How it is proved

A two-host drill in CI, on the systemd hosts from stage 1's first item:

1. Install onto host A, create an administrator, a website and a stored
   credential.
2. Take a `panel` backup to a destination both hosts can reach, and export the
   key.
3. Install onto a clean host B, then `restore-panel` from the archive and key.
4. On host B: the administrator signs in, the website is listed, and the stored
   credential **decrypts** - the check that the key really came across.

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
