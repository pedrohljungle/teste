.PHONY: help test test-e2e coverage lint docs tidy build run-server run-worker migrate-up migrate-down migrate-status migrate-create up down logs token

MIN_COVERAGE ?= 80
KEYCLOAK_URL ?= http://localhost:8080
REALM ?= pedro-test
USERNAME ?= pedro
PASSWORD ?= pedro

# goose reads these from the environment, so every target below works the same locally and
# in the container.
export GOOSE_DRIVER ?= postgres
export GOOSE_DBSTRING ?= postgres://postgres:postgres@localhost:5432/pedro_test?sslmode=disable
export GOOSE_MIGRATION_DIR ?= migrations

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

test: ## Run the unit tests with the race detector
	go test -race -failfast -timeout=120s ./...

# The end to end suite starts Postgres, Redis, LocalStack and Keycloak with testcontainers, so
# it is behind a build tag: `make test` stays fast and needs no Docker.
#
# Colima does not publish /var/run/docker.sock, so DOCKER_HOST has to name its socket; Ryuk
# (the container reaper) does not work through it either, and the suite terminates its own
# containers anyway.
test-e2e: ## Run the end to end suite (needs Docker)
	DOCKER_HOST=$${DOCKER_HOST:-unix://$$HOME/.colima/default/docker.sock} \
	TESTCONTAINERS_RYUK_DISABLED=true \
	go test -tags e2e -count=1 -timeout 20m ./app/test/...

# Coverage is measured over services/, where the business rules live. A project-wide number
# goes up when someone tests a getter and down when someone writes a rule: it measures volume,
# not risk.
coverage: ## Measure services/ coverage and fail below MIN_COVERAGE
	go test -race -timeout=120s \
		-coverpkg=./app/src/services/... \
		-coverprofile=services.cov \
		./app/src/services/...
	@# A profile with only its header means there is no business rule to measure yet — this
	@# repository carries no domain. Failing there would be failing for the absence of code.
	@if [ "$$(grep -v '^mode:' services.cov | wc -l | tr -d ' ')" = "0" ]; then \
		echo "services coverage: nothing to measure (no domain yet)"; \
	else \
		go tool cover -func=services.cov | LC_NUMERIC=C awk -v min=$(MIN_COVERAGE) '\
			/^total:/ { gsub("%","",$$3); \
				if ($$3+0 < min) { printf "services coverage: %.1f%% (minimum %d%%)\n", $$3, min; exit 1 } \
				else { printf "services coverage: %.1f%% (minimum %d%%)\n", $$3, min } }'; \
	fi

lint: ## Run golangci-lint, which is what enforces the layer rules
	golangci-lint run ./...
	golangci-lint run --build-tags e2e ./app/test/...

tidy: ## Tidy go.mod
	go mod tidy

build: ## Build both entrypoints into bin/
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/server ./app/cmd/server
	CGO_ENABLED=0 go build -trimpath -o bin/worker ./app/cmd/worker

run-server: ## Run the HTTP server locally
	go run ./app/cmd/server

run-worker: ## Run the worker locally
	go run ./app/cmd/worker

# Migrations go through the goose CLI:
#   go install github.com/pressly/goose/v3/cmd/goose@v3.28.0
migrate-up: ## Apply pending migrations
	goose up

migrate-down: ## Roll back the last migration
	goose down

migrate-status: ## Show migration status
	goose status

migrate-create: ## Create a migration: make migrate-create NAME=add_something
	goose create $(NAME) sql

up: ## Start the whole stack
	docker compose up -d --build

down: ## Stop the stack and drop the volumes
	docker compose down -v

logs: ## Follow the server and worker logs
	docker compose logs -f server worker

token: ## Print an access token for the API (pedro/pedro)
	@curl -s -X POST "$(KEYCLOAK_URL)/realms/$(REALM)/protocol/openid-connect/token" \
		-H "Content-Type: application/x-www-form-urlencoded" \
		-d "client_id=pedro-test-api" \
		-d "grant_type=password" \
		-d "username=$(USERNAME)" \
		-d "password=$(PASSWORD)" \
		| python3 -c "import sys,json;print(json.load(sys.stdin)['access_token'])"
