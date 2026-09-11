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

.PHONY: all build format lint test start-docker up down reset-sink clean clean-all

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

# Prefers the Colima profile the endpoint names, then any Colima instance, then Docker Desktop, since a stopped
# runtime leaves no socket or context to sniff.
start-docker:
	@docker info >/dev/null 2>&1 && exit 0; \
	[ "$$(uname -s)" = Darwin ] || { echo "Docker is not running. Start it and run make again." >&2; exit 1; }; \
	endpoint="$${DOCKER_HOST:-$$(docker context inspect --format '{{.Endpoints.docker.Host}}')}"; \
	case "$$endpoint" in \
	  */colima/*) profile="$${endpoint%/docker.sock}"; profile="$${profile##*/}" ;; \
	  *) profile="$$(colima list 2>/dev/null | awk 'NR > 1 { print $$1; exit }')" ;; \
	esac; \
	if [ -n "$$profile" ]; then colima start "$$profile" || exit 1; \
	elif open -g -a Docker 2>/dev/null; then echo "Starting Docker Desktop"; \
	else echo "Docker is not running. Install Docker Desktop or create a Colima VM (see README), then run make again." >&2; exit 1; fi; \
	i=0; while [ $$i -lt 60 ]; do docker info >/dev/null 2>&1 && exit 0; sleep 2; i=$$((i + 1)); done; \
	echo "Docker did not become ready within two minutes." >&2; exit 1

up: start-docker
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
