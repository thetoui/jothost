# Phase 25 — Release

Making the panel something that can be handed to somebody else: a versioned
archive, images that say what they are, and the three documents an operator
needs when things are going badly.

---

## What a release is

```text
release/jothost-0.1.0-linux-amd64.tar.gz
release/jothost-0.1.0-linux-amd64.tar.gz.sha256
```

The archive unpacks to a single directory named for the version — so two
downloads cannot overwrite each other — containing:

```text
install.sh     the installer, executable
VERSION        version, commit, build date
bin/           jothost-api, jothost-agent, statically linked
migrations/    paired up and down SQL
frontend/      the compiled SPA
```

```bash
make release          # build it
make release-verify   # check it is what it claims
make release-images   # build and tag the container images
```

## Versioning

`VERSION` at the repository root is the single source. `dist-binaries` compiles
it into both binaries with `-ldflags`, along with the commit and build date, and
`make release` names the archive from the same file. There is no second place
to update.

Both binaries answer:

```bash
$ jothost-api version
jothost-api 0.1.0 (commit 4c63488090f0, built 2026-09-06T13:49:40Z)
```

They answer it **before loading their configuration**, deliberately. Somebody
asking a binary they have just downloaded what it is has no configuration yet,
and refusing until they write one refuses the one question worth asking before
installing anything. The Agent takes `-version` and the API a subcommand, and
both accept the bare word, because nobody should have to remember which is
which.

## Why the verification is not a formality

`tests/integration/release.sh` unpacks the archive, runs the binaries **on
Debian rather than the Alpine that built them**, and asks them their version. It
checks the checksum, the layout, the executable bits, that no `.env` or key was
swept in from the build machine, and that no binary still carries a development
stamp.

It earned its place immediately. Two things it caught on its first run:

- **Packaging from a Windows working copy produced an archive whose binaries
  had no executable bit.** Every dist-based test passed; the archive was
  unusable. Packaging now happens inside a Linux container, like everything
  else here.
- **This script's own `control()` had the shell-global bug** that had already
  been fixed once in the console checks: POSIX `sh` has no locals, so assigning
  `name="$1"` overwrote the archive name the whole script was built around, and
  every check below it looked for a file called *"the archive is present"*.

A release that names one version and ships another is the kind of thing nobody
notices until a bug report cites a build that was never published, and then
nobody can tell which code the reporter was running.

## Container images

`make release-images` builds `jothost/api` and `jothost/agent`, tagged with the
version and `latest`, passing the same version, commit and build date the
binaries carry. It then **runs each image and compares what it reports against
the tag**, and fails if they disagree.

The Agent image exists for development, where it runs in a container so that
Linux-specific behaviour is exercised on Linux. In production the Agent runs as
root on the host it manages; a containerised Agent cannot manage the host it is
isolated from.

## Documentation

| Document | What it answers |
|---|---|
| [SECURITY.md](SECURITY.md) | What protects the host, and what deliberately does not. Includes what is *not* implemented. |
| [RECOVERY.md](RECOVERY.md) | What to do when something is broken, in the order worth trying. |
| [../CHANGELOG.md](../CHANGELOG.md) | What is in this version, and its known limitations. |

`RECOVERY.md` is grounded rather than aspirational: every claim in its first
section is exercised by `tests/recovery/phase24_recovery.sh`, which stops the
API, the Agent and PostgreSQL in turn on a live host. When one of those claims
stops being true, the drill fails.

Writing `SECURITY.md` corrected a false claim before it was published: the draft
said two-factor authentication was not implemented. It is — TOTP, with the
shared secret encrypted at rest. Checking the code rather than trusting the
outline is the difference between a security document and a liability.

## Acceptance

- [x] The version lives in one file and reaches the binaries, the archive and
      the images.
- [x] A binary can be asked what it is, with no configuration present.
- [x] The archive carries a checksum an operator can verify before running
      anything as root.
- [x] The archive's contents are checked on a host that did not build them.
- [x] Images are verified against their own tags.
- [x] Security, recovery and changelog documents exist and describe the code as
      it is, limitations included.
