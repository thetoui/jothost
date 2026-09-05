# Scripts

Operational and developer scripts.

Day-to-day development is driven through the `Makefile`. What lives here is the
production installer, which is the one thing that runs on a customer's own
machine rather than in a container.

## jothost-installer.sh

The production installer: `install`, `update`, `repair`, `uninstall`, `status`.

It is shipped as `install.sh` beside the artefacts `make dist` builds, so a
release archive unpacks to a directory the installer can run from with no path
given. Run `./install.sh --help` for the options; the design and its known
limitations are in [docs/PHASE23.md](../docs/PHASE23.md).

It is checked on a bare `alpine:3.21` container — nothing installed, no nginx,
no PostgreSQL, no panel:

```bash
make dist
make docker-test-installer
```
