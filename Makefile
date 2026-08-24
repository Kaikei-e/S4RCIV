# S4rCiv test suite — single reproducible entrypoint.
#
# A third party needs only Docker (+ Node for the browser E2E). The host toolchain
# is deliberately NOT relied on: Go 1.27, protoc and Atlas all run inside containers,
# so `git clone && make test` reproduces the full suite without a bespoke setup.
#
# Layers (test pyramid, bottom-heavy):
#   make unit         Go + Rust + web unit tests (fast, hermetic, no orchestration)
#   make cdc          contract checks: proto byte-drift guard + buf breaking
#   make integration  Go drivers vs a REAL migrated Postgres (template-DB-per-test)
#   make e2e          Playwright browser journeys vs the real, deterministically seeded stack
#   make test         everything above, then teardown
#
# Every DB-touching layer uses the real Postgres from compose.yaml — the same image,
# migrations and wiring as production — so tests never diverge from the real stack.

# ISOLATION (see ADR-000016 / 2026-06-06 incident): every test command runs under a
# SEPARATE Compose project (s4rciv-test) with its own volume (s4rciv-test_db_data) and
# its own host ports. The production `s4rciv` stack and its db_data volume are never
# touched — so even `down -v` below can only remove TEST volumes. compose.test.yaml
# also sets `name: s4rciv-test`, making this safe even if -p were omitted.
TEST_PROJECT := s4rciv-test
export API_HOST_PORT ?= 18080
export WEB_HOST_PORT ?= 13000
COMPOSE := docker compose -p $(TEST_PROJECT) -f compose.yaml -f compose.test.yaml
BASELINE_REF ?= origin/main

.PHONY: test unit cdc integration e2e \
        unit-go unit-rust unit-web \
        proto-drift buf-breaking seed up down clean help

help:
	@grep -E '^[a-zA-Z0-9_-]+:.*?##' $(MAKEFILE_LIST) | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

## ---- aggregate ----------------------------------------------------------------

test: ## Run the whole suite (unit + cdc + integration + e2e), then teardown
	@$(MAKE) unit && $(MAKE) cdc && $(MAKE) integration && $(MAKE) e2e; \
	  s=$$?; $(MAKE) down; exit $$s

unit: unit-go unit-rust unit-web ## All language unit tests

## ---- unit ---------------------------------------------------------------------

unit-go: ## Go unit tests (domain/usecase/gateway/handler) — fakes only, no DB
	$(COMPOSE) build test-runner
	$(COMPOSE) run --rm test-runner go test ./...

unit-rust: ## Rust differ unit + in-process contract tests
	$(COMPOSE) run --rm --build differ-test

unit-web: ## SvelteKit component/unit tests (Vitest)
	$(COMPOSE) run --rm --build web-test

## ---- contract (CDC) -----------------------------------------------------------

cdc: proto-drift buf-breaking ## Contract checks across service boundaries

proto-drift: ## Fail if the two diff.proto trees diverge (api vs differ, synced manually)
	@./scripts/check-proto-drift.sh

buf-breaking: ## Fail on wire/source-incompatible proto changes vs $(BASELINE_REF)
	cd services/api && buf breaking --against '../../.git#ref=$(BASELINE_REF),subdir=services/api/proto'

## ---- integration (real Postgres) ----------------------------------------------

integration: ## Go drivers vs a real migrated Postgres; template-DB-per-test
	$(COMPOSE) build test-runner
	$(COMPOSE) run --rm test-runner \
	  go test -race -tags=integration -count=1 ./internal/driver/postgres/...

## ---- end-to-end (browser, real seeded stack) ----------------------------------

up: ## Bring the real stack up (db+migrate+api+web), health-gated
	$(COMPOSE) up -d --wait db migrate api web

seed: ## Deterministically seed the E2E database (fixed inputs → reproducible hashes)
	$(COMPOSE) run --rm seed

e2e: up seed ## Playwright browser journeys vs the running, seeded stack
	@# Scope the `cd web` to a subshell so the teardown `$(MAKE) down` runs from the
	@# repo root (otherwise it executes in web/, which has no Makefile, and the test
	@# stack is left running).
	( cd web && E2E_BASE_URL=http://127.0.0.1:$(WEB_HOST_PORT) pnpm exec playwright test ); \
	  s=$$?; $(MAKE) down; exit $$s

## ---- lifecycle ----------------------------------------------------------------

down: ## Stop the test stack and remove its volumes (s4rciv-test project ONLY — never prod)
	-$(COMPOSE) down -v --remove-orphans

clean: down ## Teardown + drop built test images
	-docker image rm s4rciv-test-runner s4rciv-differ-test s4rciv-web-test 2>/dev/null || true
