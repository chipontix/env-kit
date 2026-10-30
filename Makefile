# Everything runs in Docker: the host needs only Docker (with Compose v2) and make.
SHELL := /bin/bash
COMPOSE := docker compose --progress quiet
RUN_TESTS := $(COMPOSE) --profile test run --rm test
GOTEST := go test -race -count=1 -timeout 5m

.PHONY: help up down reset test test-broken catch-rate clean-clone logs

help:          ## list targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | sed 's/:.*## /\t/'

up:            ## build the API, start Postgres (schema + seed) and the API, wait until healthy
	$(COMPOSE) up -d --build --wait postgres api

down:          ## stop the stack and delete its volumes
	$(COMPOSE) --profile test down -v --remove-orphans

reset: down up ## fresh database with only the seed data

test:          ## unit + integration + end-to-end tests, race detector (needs `make up`)
	$(RUN_TESTS) $(GOTEST) ./...

test-broken:   ## apply each broken/*.patch, require its named tests to fail, restore the tree
	./scripts/test-broken.sh

catch-rate:    ## run the suite N times on every broken patch and on the clean tree (N=20)
	./scripts/catch-rate.sh $(or $(N),20)

clean-clone:   ## acceptance: git clone HEAD to an empty dir, run up, test, test-broken there
	./scripts/clean-clone-check.sh

logs:          ## follow API and Postgres logs
	$(COMPOSE) logs -f postgres api
