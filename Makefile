# JotHost Panel — developer entry points.
#
# Go is not required on the host: every Go command runs inside a Linux
# container, matching ARCHITECTURE.md section 16.

SHELL := /bin/sh

COMPOSE      := docker compose
COMPOSE_TEST := docker compose -f docker-compose.test.yml
GO_IMAGE     := golang:1.23-alpine
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
	$(GO_RUN) 'set -e; \
		version=$$(cat VERSION 2>/dev/null || echo 0.1.0-dev); \
		commit=$$(cat .git/HEAD 2>/dev/null | sed "s|ref: ||" | xargs -I{} sh -c "cat .git/{} 2>/dev/null" | cut -c1-12); \
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
		printf "%s\n" "$$built" >> /src/$(DIST_DIR)/VERSION'
	@chmod +x $(DIST_DIR)/bin/*

.PHONY: dist-frontend
dist-frontend:
	@mkdir -p $(DIST_DIR)
	docker run --rm -v "$(CURDIR)/frontend:/app" -v jothost-dist-node:/app/node_modules \
		-w /app node:22-alpine sh -euc 'npm ci --no-audit --no-fund && npm run build'
	rm -rf $(DIST_DIR)/frontend
	cp -r frontend/dist $(DIST_DIR)/frontend

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
	$(COMPOSE_TEST) run --rm go-tests
	$(COMPOSE_TEST) run --rm frontend-tests
	$(MAKE) docker-test-integration
	$(MAKE) docker-test-auth
	$(MAKE) docker-test-agent
	$(MAKE) docker-test-dashboard
	$(MAKE) docker-test-websites
	$(MAKE) docker-test-php
	$(MAKE) docker-test-ssl
	$(MAKE) docker-test-files
	$(MAKE) docker-test-editor
	$(MAKE) docker-test-databases
	$(MAKE) docker-test-subdomains
	$(MAKE) docker-test-hybrid
	$(MAKE) docker-test-services
	$(MAKE) docker-test-logs
	$(MAKE) docker-test-cron
	$(MAKE) docker-test-ssh
	$(MAKE) docker-test-fail2ban
	$(MAKE) docker-test-ftp
	$(MAKE) docker-test-dns
	$(MAKE) docker-test-updates
	$(MAKE) docker-test-monitoring
	$(MAKE) docker-test-backup
	$(MAKE) docker-test-security
	$(MAKE) docker-test-notifications
	$(MAKE) docker-test-mail
	$(MAKE) docker-test-deploy
	$(MAKE) docker-test-tenancy
	$(MAKE) docker-test-firewall
	$(MAKE) docker-test-node

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

.PHONY: docker-test-installer
docker-test-installer: dist ## Run the Phase 23 installer checks on a clean host
	# A throwaway container with nothing on it: no nginx, no PostgreSQL, no
	# panel. The installer has to bring all of that with it, and the checks
	# afterwards ask the panel over HTTP whether it is really there.
	#
	# It depends on `dist` because an installer with nothing to install proves
	# nothing.
	$(COMPOSE_TEST) run --rm installer-test

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
