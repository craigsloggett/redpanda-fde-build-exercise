CONNECT_IMAGE         := docker.redpanda.com/redpandadata/connect:4.108.0@sha256:7c6f0e53a2702fb7e3a7e8aff0f62e956025d51d131ac3631288b6eb143f22c2
CONNECT_CONFIGS       := $(addprefix /repo/,$(wildcard ingest/*.yaml transform/*.yaml serve/*.yaml))
GOLANGCI_LINT_VERSION := v2.13.2
GOLANGCI_LINT         := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GOVULNCHECK_VERSION   := v1.8.0
GO_VERSION            := $(shell awk '/^go /{print $$2}' go.mod)
BUILD_DIR             := .local/bin
COMPOSE_PROJECT       := redpanda-fde-build-exercise
PAGE_URL              := http://localhost:8080

ifeq ($(OS),Windows_NT)
OPEN := start
else ifeq ($(shell uname -s),Darwin)
OPEN := open
else
OPEN := xdg-open
endif

.PHONY: all build format lint test up down reset-sink clean clean-all

all: lint test build

# Formatting

.PHONY: format-yamlfmt format-gofumpt format-biome

format-yamlfmt:
	yamlfmt .

format-gofumpt:
	$(GOLANGCI_LINT) fmt ./...

format-biome:
	biome format --write .

format: format-yamlfmt format-gofumpt format-biome

# Linting

.PHONY: lint-yamlfmt lint-yamllint lint-actionlint lint-compose lint-connect lint-biome lint-golangci-lint lint-go-mod lint-govulncheck

lint-yamlfmt:
	yamlfmt -lint .

lint-yamllint:
	yamllint .

lint-actionlint:
	actionlint

lint-compose:
	docker compose config --quiet

lint-connect:
	docker run --rm -v "$$(pwd):/repo:ro" $(CONNECT_IMAGE) --disable-telemetry lint $(CONNECT_CONFIGS)

lint-biome:
	biome format .

lint-golangci-lint:
	$(GOLANGCI_LINT) run ./...

lint-go-mod:
	go mod tidy && git diff --exit-code -- go.mod go.sum

lint-govulncheck:
	GOTOOLCHAIN=go$(GO_VERSION) go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

lint: lint-yamlfmt lint-yamllint lint-actionlint lint-compose lint-connect lint-biome lint-golangci-lint lint-go-mod lint-govulncheck

# Building

build:
	CGO_ENABLED=0 go build -trimpath -o $(BUILD_DIR)/reason ./reason
	CGO_ENABLED=0 go build -trimpath -o $(BUILD_DIR)/serve ./serve

# Testing

test:
	go test -race -count=1 ./...

# Docker Compose

up:
	docker compose up --build --detach --wait
	$(OPEN) $(PAGE_URL) || echo "Open $(PAGE_URL) in a browser"
	docker compose logs --follow

down:
	docker compose down

reset-sink:
	docker compose stop sink
	docker compose exec postgres psql -U wiki -d wiki -c 'DROP TABLE IF EXISTS verdicts'
	docker compose exec redpanda rpk group delete postgres-sink
	docker compose start sink

# Local Data

clean:
	docker compose down
	docker volume rm --force $(COMPOSE_PROJECT)_redpanda $(COMPOSE_PROJECT)_postgres
	rm -rf .local

clean-all:
	docker compose down --volumes --rmi all
	rm -rf .local
