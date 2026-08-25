# JotHost Panel — developer entry points.
#
# Go is not required on the host: every Go command runs inside a Linux
# container, matching ARCHITECTURE.md section 16.

SHELL := /bin/sh

COMPOSE      := docker compose
COMPOSE_TEST := docker compose -f docker-compose.test.yml
GO_IMAGE     := golang:1.23-alpine
GO_MODULES   := shared api agent

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

.PHONY: go-test
go-test: ## Run Go unit and security tests
	@$(GO_RUN) 'for m in $(GO_MODULES); do echo "=== $$m ==="; (cd /src/$$m && go test -count=1 ./...); done'

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

.PHONY: verify
verify: lint test docker-test-integration ## Everything CI runs
