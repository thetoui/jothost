# Support

What a release of JotHost Panel is built for, how versions are numbered, and how
to move between them. Everything listed as supported here is exercised in CI on
a fresh host; nothing is listed because it ought to work.

## Supported platforms

| Operating system | Architecture | Install | Upgrade from the previous release |
|---|---|---|---|
| Ubuntu 24.04 LTS | amd64 | CI, every push | CI, every push |
| Ubuntu 22.04 LTS | amd64 | CI, every push | — see below |
| Debian 12 | amd64 | CI, every push | CI, every push |

The installer also runs on Alpine Linux with OpenRC, and that path is tested,
but it is not a production target: resource limits are recorded rather than
enforced without systemd. RHEL-family hosts are not supported. arm64 builds are
not published.

Ubuntu 22.04 has no upgrade test yet because `v0.1.0-rc.1`, the release the test
upgrades from, could not sign anybody in there. It gains one once a release that
works on 22.04 exists to upgrade from.

A published release is also installed from its public URL, signature checked,
on all three supported systems as the last step of publishing it
([release.yml](../.github/workflows/release.yml)).

## Versions

Releases follow [Semantic Versioning](https://semver.org/) from 1.0.0:

- **Patch** (1.0.x) — fixes only, including security fixes. No new settings are
  required and no migration removes data.
- **Minor** (1.x.0) — new features. May add migrations; an existing install
  keeps working after `install.sh update` with no manual steps.
- **Major** (x.0.0) — may change configuration, the API or the supported
  platforms. The release notes say what to do before updating.

A version with a suffix (`1.1.0-rc.1`) is a release candidate: published and
signed like any other, marked as a pre-release, and meant for testing.

## Upgrading

```bash
curl -fsSL https://github.com/thetoui/jothost/releases/latest/download/get-jothost.sh \
  | sudo sh -s -- update
```

`update` keeps the encryption key, the Agent token and the database password,
applies any new migrations, and restarts the services. It is tested in CI from
the previous release with real data in place: websites, a cron job, a backup
and a stored secret, all checked afterwards.

Take a panel backup and export the key first
([RECOVERY.md](RECOVERY.md)). Downgrading is not supported: migrations run
forward, and `migrate down` is refused while a panel backup is recorded,
because rolling the schema back under an encrypted archive would strand it.

## Verifying a release

Every release archive has a SHA-256 and a Sigstore signature made by this
repository's release workflow for that tag. `get-jothost.sh` checks both before
running anything; the README shows how to check them by hand.

## Security

How to report a vulnerability is in [SECURITY.md](SECURITY.md). Security fixes
are released as patch versions.
