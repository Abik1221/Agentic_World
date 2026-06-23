.DEFAULT_GOAL := help
SHELL := /bin/bash
BIN   := bin/server
PKG   := ./...
MIGRATIONS_DIR := migrations
DATABASE_URL ?= postgres://arena:arena@localhost:5432/arena?sslmode=disable

.PHONY: help
help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: tidy
tidy: ## Resolve & pin dependencies
	go mod tidy

.PHONY: build
build: ## Build the server binary
	CGO_ENABLED=0 go build -trimpath -o $(BIN) ./cmd/server

.PHONY: run
run: ## Run the server (expects deps via `make compose-up`)
	go run ./cmd/server

.PHONY: test
test: ## Unit + integration tests with the race detector
	go test -race -count=1 $(PKG)

.PHONY: lint
lint: ## Static analysis (golangci-lint must be installed)
	golangci-lint run

.PHONY: vuln
vuln: ## Vulnerability scan
	go run golang.org/x/vuln/cmd/govulncheck@latest $(PKG)

.PHONY: gen
gen: ## Generate sqlc code (no-op until queries exist)
	sqlc generate

.PHONY: migrate
migrate: ## Apply DB migrations (golang-migrate)
	go run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest \
	  -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

.PHONY: migrate-down
migrate-down: ## Roll back the last migration
	go run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest \
	  -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down 1

.PHONY: compose-up
compose-up: ## Start local Postgres + Redis + the service
	docker compose -f deploy/docker-compose.yml up --build

.PHONY: compose-down
compose-down: ## Stop the local stack
	docker compose -f deploy/docker-compose.yml down -v

.PHONY: check
check: lint test vuln ## Run the full CI gate locally
