# JotHost Panel — developer entry points.
#
# Go is not required on the host: every Go command runs inside a Linux
# container, matching ARCHITECTURE.md section 16.

SHELL := /bin/sh

COMPOSE      := docker compose
COMPOSE_TEST := docker compose -f docker-compose.test.yml
GO_IMAGE     := golang:1.26-alpine
GO_MODULES   := shared api agent

# Credentials for the account the Phase 1 auth checks sign in with. Test-only.
INTEGRATION_ADMIN_USERNAME ?= integration_admin
INTEGRATION_ADMIN_PASSWORD ?= integration-admin-pw-9271

# Runs a command inside a throwaway Go container with the repo mounted.
GO_RUN = docker run --rm \
	-v "$(CURDIR):/src" \
	-v jothost-go-cache:/tmp/gocache \
	-e GOFLAGS=-mod=mod \
	-e GOCACHE=/tmp/gocache \
	-w /src $(GO_IMAGE) sh -euc

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------- environment

.PHONY: env
env: ## Create .env from .env.example if it does not exist
	@[ -f .env ] || (cp .env.example .env && echo "Created .env from .env.example")

.PHONY: dev
dev: env ## Build and start the development stack
	$(COMPOSE) up -d --build
	@echo ""
	@echo "Panel:    http://localhost:$${PANEL_PORT:-8081}"
	@echo "API:      http://localhost:$${API_PORT:-8080}/healthz"
	@echo "Frontend: http://localhost:$${FRONTEND_PORT:-5173}"

.PHONY: up
up: env ## Start the development stack without rebuilding
	$(COMPOSE) up -d

.PHONY: down
down: ## Stop the development stack
	$(COMPOSE) down

.PHONY: clean
clean: ## Stop the stack and delete its volumes (destroys dev data)
	$(COMPOSE) down -v

.PHONY: logs
logs: ## Follow logs for all services
	$(COMPOSE) logs -f

.PHONY: ps
ps: ## Show service status and health
	$(COMPOSE) ps

# ------------------------------------------------------------------------ Go

.PHONY: go-build
go-build: ## Compile the API and Agent binaries
	@$(GO_RUN) 'for m in $(GO_MODULES); do (cd /src/$$m && go build ./...); done'

# go-test runs through the test profile because the api suite needs a real
# PostgreSQL and Redis; the profile starts throwaway instances for it.
.PHONY: go-test
go-test: ## Run Go unit and security tests
	$(COMPOSE_TEST) run --rm go-tests

.PHONY: go-lint
go-lint: ## Run go vet and gofmt checks
	@$(GO_RUN) 'for m in $(GO_MODULES); do \
		echo "=== vet $$m ==="; (cd /src/$$m && go vet ./...); \
		unformatted=$$(cd /src/$$m && gofmt -l .); \
		if [ -n "$$unformatted" ]; then echo "gofmt required in $$m:"; echo "$$unformatted"; exit 1; fi; \
	done'

.PHONY: go-fmt
go-fmt: ## Format Go source
	@$(GO_RUN) 'for m in $(GO_MODULES); do (cd /src/$$m && gofmt -w .); done'

# --------------------------------------------------------------- distribution

# The artefacts the installer lays down.
#
# A directory rather than a tarball: `make dist` is what the Phase 23 installer
# checks are run against, and a directory can be mounted into a container
# without unpacking. Phase 25 wraps this in a release archive.
#
# Everything is built inside Linux containers, so the result is the same
# whichever machine ran the command — which is the point, since these binaries
# are copied onto a customer's server.
DIST_DIR ?= dist

.PHONY: dist
dist: dist-clean dist-binaries dist-frontend dist-support ## Build the installable artefacts into dist/
	@echo ""
	@echo "Artefacts in $(DIST_DIR):"
	@ls -1 $(DIST_DIR)
	@echo ""
	@echo "Install them with:  sudo $(DIST_DIR)/install.sh install --domain panel.example.com"

.PHONY: dist-clean
dist-clean: ## Remove the built artefacts
	rm -rf $(DIST_DIR)

.PHONY: dist-binaries
dist-binaries:
	@mkdir -p $(DIST_DIR)/bin
	# Statically linked, because the binaries are copied onto a host whose libc
	# is not known: a dynamically linked Go binary built on Alpine will not run
	# on Debian, and the failure is a bare "not found" that names nothing.
	#
	# The version is compiled in rather than read from a file, so a binary
	# always reports what it actually is.
	#
	# The commit is read from .git directly, because git is not guaranteed to
	# be in the build container. .git/HEAD holds either "ref: refs/heads/x" or,
	# when HEAD is detached, the commit itself - and detached is the normal
	# case for the two builds that matter most: a CI checkout of a pull
	# request, and a release cut by checking out a tag. Following the ref
	# blindly turned the sha into a path that does not exist, so those builds
	# were stamped "commit unknown" and release-verify rejected them as
	# development builds. A ref that has been packed is read from packed-refs,
	# which is where git puts it once the loose file is gone.
	#
	# The mode is set inside the container, by the user that wrote the files.
	# The container runs as root, so on a Linux host the binaries land owned
	# by root and a chmod afterwards - as whoever ran make - fails with EPERM.
	# Docker Desktop hands the files to the calling user instead, which is why
	# that only ever showed up on a real Linux machine.
	#
	# 0755 rather than +x because symbolic modes are filtered by the umask:
	# under a restrictive one, +x leaves the binaries executable by their
	# owner alone, and the installer copies them to a host where a service
	# user has to run them. `release` states the mode outright for the same
	# reason.
	$(GO_RUN) 'set -e; \
		version=$$(cat VERSION 2>/dev/null || echo 0.1.0-dev); \
		head=$$(cat .git/HEAD 2>/dev/null || true); \
		case "$$head" in \
			"ref: "*) \
				ref=$${head#ref: }; \
				commit=$$(cat ".git/$$ref" 2>/dev/null || true); \
				[ -n "$$commit" ] || commit=$$(sed -n "s|^\([0-9a-f][0-9a-f]*\) $$ref$$|\1|p" .git/packed-refs 2>/dev/null || true); \
				;; \
			*) commit=$$head ;; \
		esac; \
		commit=$$(printf "%s" "$$commit" | cut -c1-12); \
		[ -n "$$commit" ] || commit=unknown; \
		built=$$(date -u +%Y-%m-%dT%H:%M:%SZ); \
		flags="-s -w \
			-X github.com/jothost/panel/shared/version.Version=$$version \
			-X github.com/jothost/panel/shared/version.Commit=$$commit \
			-X github.com/jothost/panel/shared/version.BuildDate=$$built"; \
		(cd api && CGO_ENABLED=0 go build -trimpath -ldflags "$$flags" \
			-o /src/$(DIST_DIR)/bin/jothost-api ./cmd/api); \
		(cd agent && CGO_ENABLED=0 go build -trimpath -ldflags "$$flags" \
			-o /src/$(DIST_DIR)/bin/jothost-agent ./cmd/agent); \
		printf "%s\n" "$$version" > /src/$(DIST_DIR)/VERSION; \
		printf "%s\n" "$$commit" >> /src/$(DIST_DIR)/VERSION; \
		printf "%s\n" "$$built" >> /src/$(DIST_DIR)/VERSION; \
		chmod 0755 /src/$(DIST_DIR)/bin/*'

.PHONY: dist-frontend
dist-frontend:
	@mkdir -p $(DIST_DIR)
	docker run --rm -v "$(CURDIR)/frontend:/app" -v jothost-dist-node:/app/node_modules \
		-w /app node:22-alpine sh -euc 'npm ci --no-audit --no-fund && npm run build'
	rm -rf $(DIST_DIR)/frontend
	cp -r frontend/dist $(DIST_DIR)/frontend

# ---------------------------------------------------------------- release

# RELEASE_DIR holds the archives a release is published as. Separate from
# dist/, which is the unpacked tree the installer runs from: an operator
# downloads one file and a checksum, and `dist` is what comes out of it.
RELEASE_DIR ?= release

.PHONY: release
release: dist ## Build the release archive and its checksum into release/
	# The version comes from the VERSION file and is already compiled into the
	# binaries by dist-binaries, so the archive cannot be named one thing and
	# contain another. `make release-verify` checks that rather than trusting it.
	#
	# Packaged inside a Linux container, like everything else here. The
	# executable bit is part of a tar archive, and a Windows or macOS working
	# copy does not necessarily carry one - packaged from there, the archive
	# unpacks to binaries nothing can run, and the installer fails on a host
	# that is perfectly fine.
	@mkdir -p $(RELEASE_DIR)
	@docker run --rm -v "$(CURDIR):/w" -w /w $(GO_IMAGE) sh -euc '\
		version=$$(cat VERSION); \
		name=jothost-$$version-linux-amd64; \
		rm -rf $(RELEASE_DIR)/$$name $(RELEASE_DIR)/$$name.tar.gz; \
		cp -r $(DIST_DIR) $(RELEASE_DIR)/$$name; \
		chmod 0755 $(RELEASE_DIR)/$$name/install.sh $(RELEASE_DIR)/$$name/bin/*; \
		tar -C $(RELEASE_DIR) -czf $(RELEASE_DIR)/$$name.tar.gz $$name; \
		rm -rf $(RELEASE_DIR)/$$name; \
		cd $(RELEASE_DIR) && sha256sum $$name.tar.gz > $$name.tar.gz.sha256'
	@version=$$(cat VERSION); name=jothost-$$version-linux-amd64; \
		echo ""; echo "Release $$version:"; \
		ls -1 $(RELEASE_DIR)/$$name.tar.gz $(RELEASE_DIR)/$$name.tar.gz.sha256; \
		echo ""; \
		echo "Verify it with:  cd $(RELEASE_DIR) && sha256sum -c $$name.tar.gz.sha256"

.PHONY: release-images
release-images: ## Build and tag the release container images
	# The same version, commit and build date the binaries carry, passed in as
	# build args. An image tagged 0.1.0 whose binary reports something else is
	# the thing this exists to prevent, so the tag is checked against what the
	# image says about itself before either is considered built.
	@version=$$(cat VERSION); \
		commit=$$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown); \
		built=$$(date -u +%Y-%m-%dT%H:%M:%SZ); \
		for part in api agent; do \
			echo "Building jothost/$$part:$$version"; \
			docker build -f docker/$$part.Dockerfile \
				--build-arg VERSION=$$version \
				--build-arg COMMIT=$$commit \
				--build-arg BUILD_DATE=$$built \
				-t jothost/$$part:$$version -t jothost/$$part:latest . ; \
		done; \
		for part in api agent; do \
			reported=$$(docker run --rm --entrypoint /usr/local/bin/jothost-$$part \
				jothost/$$part:$$version version 2>/dev/null || true); \
			case "$$reported" in \
				*$$version*) echo "  ok   jothost/$$part:$$version reports $$reported" ;; \
				*) echo "  FAIL jothost/$$part:$$version reports '$$reported', not $$version"; exit 1 ;; \
			esac; \
		done

.PHONY: release-verify
release-verify: ## Check the release archive against what it claims to be
	# A release that says one version and ships another is the kind of thing
	# nobody notices until a bug report names a build that was never shipped.
	$(COMPOSE_TEST) run --rm release-check

.PHONY: release-clean
release-clean: ## Remove the built release archives
	rm -rf $(RELEASE_DIR)

.PHONY: dist-support
dist-support:
	@mkdir -p $(DIST_DIR)
	cp -r migrations $(DIST_DIR)/migrations
	cp scripts/jothost-installer.sh $(DIST_DIR)/install.sh
	@chmod +x $(DIST_DIR)/install.sh

# ------------------------------------------------------------------ frontend

.PHONY: fe-install
fe-install: ## Install frontend dependencies
	$(COMPOSE) run --rm --no-deps frontend npm ci

.PHONY: fe-test
fe-test: ## Run the frontend test suite
	$(COMPOSE_TEST) run --rm frontend-tests

.PHONY: fe-lint
fe-lint: ## Lint the frontend
	$(COMPOSE) run --rm --no-deps frontend npm run lint

.PHONY: fe-build
fe-build: ## Type-check and build the frontend
	$(COMPOSE) run --rm --no-deps frontend sh -euc 'npm run typecheck && npm run build'

# --------------------------------------------------------------------- tests

.PHONY: test
test: go-test fe-test ## Run all unit tests

.PHONY: lint
lint: go-lint fe-lint ## Run all linters

.PHONY: docker-test
docker-test: ## Run the full containerised test suite (unit + integration)
	# The suites are discovered, not listed. This target used to name each one
	# by hand and six of them - including four written the same week - existed
	# without it ever running them.
	$(COMPOSE_TEST) run --rm go-tests
	$(COMPOSE_TEST) run --rm frontend-tests
	$(MAKE) docker-test-integration
	$(MAKE) docker-test-suite
	# The deployment path and the drills, each needing a host of its own.
	$(MAKE) docker-test-installer
	$(MAKE) docker-test-installer-debian
	$(MAKE) docker-test-installer-ubuntu
	$(MAKE) docker-test-security-audit
	$(MAKE) docker-test-load
	$(MAKE) docker-test-recovery

.PHONY: docker-test-integration
docker-test-integration: ## Run integration tests against the running dev stack
	$(COMPOSE) up -d --build
	@echo "Waiting for the stack to become healthy..."
	@timeout=120; \
	while [ $$timeout -gt 0 ]; do \
		state=$$($(COMPOSE) ps --format '{{.Health}}' api nginx 2>/dev/null | sort -u | tr '\n' ' '); \
		case "$$state" in *unhealthy*) echo "A service is unhealthy"; $(COMPOSE) ps; exit 1;; esac; \
		if [ "$$state" = "healthy " ]; then break; fi; \
		sleep 3; timeout=$$((timeout - 3)); \
	done; \
	if [ $$timeout -le 0 ]; then echo "Timed out waiting for healthy services"; $(COMPOSE) ps; exit 1; fi
	$(COMPOSE_TEST) run --rm integration

.PHONY: docker-test-auth
docker-test-auth: create-integration-admin ## Run the Phase 1 auth integration checks
	$(COMPOSE_TEST) run --rm auth-integration

# The Phase 2 checks drive the Agent through its own socket, so they run inside
# the agent container rather than as a separate service.
.PHONY: docker-test-agent
docker-test-agent: ## Run the Phase 2 Host Agent integration checks
	$(COMPOSE) exec agent sh /tests/integration/phase2_agent.sh

.PHONY: docker-test-dashboard
docker-test-dashboard: create-integration-admin ## Run the Phase 3 dashboard integration checks
	$(COMPOSE_TEST) run --rm dashboard-integration

.PHONY: docker-test-websites
docker-test-websites: create-integration-admin ## Run the Phase 4 website integration checks
	$(COMPOSE_TEST) run --rm websites-integration

# The Phase 5 checks write PHP files into a site's document root, which only
# the managed host can do, so they run inside the agent container.
.PHONY: docker-test-php
docker-test-php: create-integration-admin ## Run the Phase 5 PHP integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase5_php.sh

# The Phase 6 checks inspect private key permissions on disk, which only the
# managed host can do, so they run inside the agent container.
.PHONY: docker-test-ssl
docker-test-ssl: create-integration-admin ## Run the Phase 6 SSL integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase6_ssl.sh

# The Phase 7 checks verify every file operation on disk after the API reports
# it, which only the managed host can see, so they run inside the agent
# container.
.PHONY: docker-test-files
docker-test-files: create-integration-admin ## Run the Phase 7 file manager integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase7_files.sh

# The Phase 7.5 checks verify what a save does to a file on disk, so they run
# inside the agent container alongside the Phase 7 checks.
.PHONY: docker-test-editor
docker-test-editor: create-integration-admin ## Run the Phase 7.5 code editor integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase75_editor.sh

# The Phase 8 checks connect to MariaDB and PostgreSQL as the accounts the panel
# created, to verify that the privileges it reports are the privileges the
# servers enforce. Only the managed host can reach those sockets, so they run
# inside the agent container.
.PHONY: docker-test-databases
docker-test-databases: create-integration-admin ## Run the Phase 8 database integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase8_databases.sh

# The Phase 9 checks run a real Express application and look at the process,
# its account, and its logs. Only the managed host can see those, so they run
# inside the agent container.
.PHONY: docker-test-firewall
docker-test-firewall: create-integration-admin ## Run the Phase 16 firewall integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase16_firewall.sh

.PHONY: docker-test-services
docker-test-services: create-integration-admin ## Run the Phase 12 service manager integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase12_services.sh

.PHONY: docker-test-logs
docker-test-logs: create-integration-admin ## Run the Phase 11 log viewer integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase11_logs.sh

.PHONY: docker-test-cron
docker-test-cron: create-integration-admin ## Run the Phase 10 scheduled job integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase10_cron.sh

.PHONY: docker-test-ssh
docker-test-ssh: create-integration-admin ## Run the Phase 17 SSH security integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase17_ssh.sh

.PHONY: docker-test-fail2ban
docker-test-fail2ban: create-integration-admin ## Run the Phase 18 intrusion prevention integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase18_fail2ban.sh

.PHONY: docker-test-ftp
docker-test-ftp: create-integration-admin ## Run the Phase 7.1 FTP integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase71_ftp.sh

.PHONY: docker-test-site-ownership
docker-test-site-ownership: create-integration-admin ## Run the site directory ownership checks
	# Deleting a website keeps its files. This proves the freed uid stops owning
	# them first, and that the next site cannot be provisioned into what is left.
	$(COMPOSE) exec -T agent sh /tests/integration/site_ownership.sh

.PHONY: docker-test-audit
docker-test-audit: create-integration-admin ## Run the audit trail integration checks
	# The trail was written from Phase 1 and readable by nothing until now.
	$(COMPOSE) exec -T agent sh /tests/integration/audit.sh

.PHONY: docker-test-database-console
docker-test-database-console: create-integration-admin ## Run the phpMyAdmin console-session checks
	# Drives the sign-in a browser performs, not just the endpoint that hands
	# out the credentials: the first version of this feature passed its own
	# tests and could not sign anybody in.
	$(COMPOSE) exec -T agent sh /tests/integration/database_console.sh

.PHONY: docker-test-database-dump
docker-test-database-dump: create-integration-admin ## Run the database export/import checks
	# Creates a table directly in MySQL, exports it through the panel, drops it,
	# and imports it back. The panel is not asked whether it worked.
	$(COMPOSE) exec -T agent sh /tests/integration/database_dump.sh

.PHONY: docker-test-dns-templates
docker-test-dns-templates: create-integration-admin ## Run the DNS template checks
	# Creates a template, makes a zone from it, and reads the zone back. A
	# template that cannot produce valid records must be refused when it is
	# written, not when somebody creates a domain.
	$(COMPOSE) exec -T agent sh /tests/integration/dns_templates.sh

.PHONY: docker-test-dns-repair
docker-test-dns-repair: create-integration-admin ## Run the DNS configuration repair checks
	# Deletes named.conf from underneath the panel and asks it to put the file
	# back, then reads the file rather than the panel's answer about it.
	$(COMPOSE) exec -T agent sh /tests/integration/dns_repair.sh

.PHONY: docker-test-domain-roots
docker-test-domain-roots: create-integration-admin ## Run the per-domain document root checks
	# Gives an alias a directory of its own and reads the generated vhost off
	# the host, then fetches both names to see which directory each is served
	# from. A config that parses and serves the wrong one looks fine otherwise.
	$(COMPOSE) exec -T agent sh /tests/integration/domain_roots.sh

.PHONY: docker-test-ssl-dns
docker-test-ssl-dns: create-integration-admin ## Run the certificate/DNS alignment checks
	# Asks for a certificate on a name this host serves and reads the zone back.
	# A name already pointing at another machine must be reported and left alone.
	$(COMPOSE) exec -T agent sh /tests/integration/ssl_dns.sh

.PHONY: docker-test-dns
docker-test-dns: create-integration-admin ## Run the Phase 13 DNS integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase13_dns.sh

.PHONY: docker-test-updates
docker-test-updates: create-integration-admin ## Run the Phase 21 system update checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase21_updates.sh

.PHONY: docker-test-monitoring
docker-test-monitoring: create-integration-admin ## Run the Phase 19 monitoring checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase19_monitoring.sh

.PHONY: docker-test-notifications
docker-test-notifications: create-integration-admin ## Run the Phase 20 notification checks
	# A real SMTP server is started for the duration, so email delivery is
	# proved against something that actually receives a message rather than
	# against a stub.
	docker compose -f docker-compose.test.yml up -d mailpit
	$(COMPOSE) exec -T agent sh /tests/integration/phase20_notifications.sh
	docker compose -f docker-compose.test.yml stop mailpit

.PHONY: docker-test-deploy
docker-test-deploy: create-integration-admin ## Run the Phase 27 deployment checks
	# The checks stand up a bare repository inside the container and deploy from
	# it over SSH, with a key the panel generated — so the deploy key, the host
	# key pinning and the authentication are all real. A local path would have
	# exercised none of them.
	$(COMPOSE) exec -T agent sh /tests/integration/phase27_deploy.sh

.PHONY: docker-test-hardening
docker-test-hardening: ## Run the whole Phase 24 hardening suite
	$(MAKE) docker-test-security-audit
	$(MAKE) docker-test-agent-boundary
	$(MAKE) docker-test-recovery
	$(MAKE) docker-test-load

.PHONY: docker-test-security-audit
docker-test-security-audit: create-integration-admin ## Run the Phase 24 adversarial suite
	# Drives the panel the way somebody trying to get in would: no token, a
	# stolen one, paths that leave the root, values that would close a command
	# line, and an account holding no permissions at all. Every section begins
	# with a control, because a refusal proves nothing unless the request
	# reached the code that refused it.
	$(COMPOSE_TEST) run --rm security-audit

.PHONY: docker-test-agent-boundary
docker-test-agent-boundary: create-integration-admin ## Run the Phase 24 Agent boundary checks
	# The privilege boundary itself, from inside the managed host: who may
	# speak to the Agent's socket, what it refuses, and what the panel leaves
	# readable on disk.
	$(COMPOSE) exec -T agent sh /tests/integration/phase24_agent.sh

.PHONY: docker-test-recovery
docker-test-recovery: create-integration-admin ## Run the Phase 24 recovery drills
	# Runs on this machine rather than in a container, because a recovery drill
	# has to be able to take services away and put them back. It stops the API,
	# the Agent and PostgreSQL in turn and checks the one claim the whole
	# architecture rests on: that a control panel outage is not a hosting
	# outage.
	sh tests/recovery/phase24_recovery.sh

.PHONY: docker-test-load
docker-test-load: create-integration-admin ## Run the Phase 24 load test
	$(COMPOSE_TEST) run --rm load-test

.PHONY: docker-test-installer
docker-test-installer: dist ## Run the Phase 23 installer checks on a clean host
	# A throwaway container with nothing on it: no nginx, no PostgreSQL, no
	# panel. The installer has to bring all of that with it, and the checks
	# afterwards ask the panel over HTTP whether it is really there.
	#
	# It depends on `dist` because an installer with nothing to install proves
	# nothing.
	$(COMPOSE_TEST) run --rm installer-test

.PHONY: docker-test-installer-debian
docker-test-installer-debian: dist ## Run the installer checks on Debian 12 with systemd
	# The other half of the installer. It picks its package manager and its
	# service manager from what it finds, and for a long time every check ran on
	# Alpine with OpenRC - so the apt and systemd branches, which is what most
	# operators will actually run, had never been executed.
	sh tests/installer/systemd-host.sh installer-host-debian

.PHONY: docker-test-installer-ubuntu
docker-test-installer-ubuntu: dist ## Run the installer checks on Ubuntu 22.04 and 24.04 with systemd
	# PRD.md names Ubuntu and Debian as the production target, and until these
	# ran only Debian had been installed onto. Both LTS releases, because they
	# ship different majors of nginx, PostgreSQL and PHP.
	sh tests/installer/systemd-host.sh installer-host-ubuntu-2204
	sh tests/installer/systemd-host.sh installer-host-ubuntu-2404

# PREVIOUS_RELEASE is the release the upgrade test upgrades from: the newest
# one before this tree. Move it forward when a release is published.
PREVIOUS_RELEASE ?= v0.1.0-rc.1
PREVIOUS_WORKTREE = .previous-release

.PHONY: dist-previous
dist-previous: ## Build the previous release's artefacts into dist-previous/
	# From its own tag, with its own Makefile, so what is installed is what that
	# release actually shipped rather than today's build recipe applied to old
	# source.
	git rev-parse -q --verify "refs/tags/$(PREVIOUS_RELEASE)" >/dev/null || 		git fetch --depth=1 origin tag "$(PREVIOUS_RELEASE)"
	rm -rf $(PREVIOUS_WORKTREE) dist-previous
	git worktree prune
	git worktree add --detach $(PREVIOUS_WORKTREE) "$(PREVIOUS_RELEASE)"
	$(MAKE) -C $(PREVIOUS_WORKTREE) dist
	cp -r $(PREVIOUS_WORKTREE)/dist dist-previous
	git worktree remove --force $(PREVIOUS_WORKTREE)

.PHONY: docker-test-upgrade
docker-test-upgrade: dist dist-previous ## Upgrade the previous release to this tree, with data, on Debian 12 and Ubuntu 24.04
	sh tests/installer/upgrade.sh upgrade-host-debian
	sh tests/installer/upgrade.sh upgrade-host-ubuntu-2404

.PHONY: docker-test-panel-restore
docker-test-panel-restore: dist ## Back up a panel on one host and restore it onto another
	sh tests/recovery/panel_restore_drill.sh

.PHONY: docker-test-suite
docker-test-suite: create-integration-admin ## Run every integration suite against the dev stack
	# Discovers the suites rather than listing them, so one added tomorrow runs
	# without anybody remembering to wire it in.
	sh tests/run-integration.sh

.PHONY: docker-test-tenancy
docker-test-tenancy: create-integration-admin ## Run the Phase 22 multi-tenancy checks
	# Every limit in here is proved by being hit. A quota that is recorded and
	# not enforced looks identical from outside to one that works, so the
	# checks create the website that is refused, measure real disk with du and
	# real bandwidth from a real access log, and read the systemd slice unit
	# off the host.
	$(COMPOSE) exec -T agent sh /tests/integration/phase22_tenancy.sh

.PHONY: docker-test-mail
docker-test-mail: create-integration-admin ## Run the Phase 26 mail server checks
	# Everything here is proved by doing it: Dovecot authenticates a real
	# mailbox, a real message is delivered into a real Maildir, and the server
	# is asked from a non-loopback address whether it will relay for a
	# stranger. None of that can be tested against a mock.
	$(COMPOSE) exec -T agent sh /tests/integration/phase26_mail.sh

.PHONY: docker-test-security
docker-test-security: create-integration-admin ## Run the Phase 15 Security Center checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase15_security.sh

.PHONY: docker-test-backup
docker-test-backup: create-integration-admin ## Run the Phase 14 backup checks
	# An S3 service is started for the duration, so the S3 destination is
	# exercised against a real implementation rather than a stub.
	docker compose -f docker-compose.test.yml up -d minio
	docker compose -f docker-compose.test.yml run --rm minio-init
	$(COMPOSE) exec -T agent sh /tests/integration/phase14_backup.sh
	docker compose -f docker-compose.test.yml stop minio

.PHONY: docker-test-hybrid
docker-test-hybrid: create-integration-admin ## Run the Phase 4.5 Apache hybrid integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase45_hybrid.sh

.PHONY: docker-test-subdomains
docker-test-subdomains: create-integration-admin ## Run the Phase 4.1 subdomain integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase41_subdomains.sh

.PHONY: docker-test-node
docker-test-node: create-integration-admin ## Run the Phase 9 Node.js integration checks
	$(COMPOSE) exec -T agent sh /tests/integration/phase9_node.sh

# create-integration-admin provisions the account the auth checks sign in with.
# Re-running is harmless: an existing username is reported and ignored.
.PHONY: create-integration-admin
create-integration-admin:
	@$(COMPOSE) exec -e JOTHOST_ADMIN_USERNAME=$(INTEGRATION_ADMIN_USERNAME) -e JOTHOST_ADMIN_PASSWORD=$(INTEGRATION_ADMIN_PASSWORD) api jothost-api create-admin >/dev/null 2>&1 || echo "integration admin already exists"

.PHONY: create-admin
create-admin: ## Create the first administrator (prompts via environment)
	@[ -n "$$JOTHOST_ADMIN_USERNAME" ] || (echo "Set JOTHOST_ADMIN_USERNAME"; exit 1)
	@[ -n "$$JOTHOST_ADMIN_PASSWORD" ] || (echo "Set JOTHOST_ADMIN_PASSWORD"; exit 1)
	$(COMPOSE) exec -e JOTHOST_ADMIN_USERNAME -e JOTHOST_ADMIN_PASSWORD -e JOTHOST_ADMIN_EMAIL api jothost-api create-admin

.PHONY: migrate
migrate: ## Apply pending database migrations
	$(COMPOSE) exec api jothost-api migrate up

.PHONY: migrate-status
migrate-status: ## Show migration state
	$(COMPOSE) exec api jothost-api migrate status

.PHONY: verify
verify: lint test docker-test-integration docker-test-auth docker-test-agent docker-test-dashboard docker-test-websites docker-test-php docker-test-ssl ## Everything CI runs
