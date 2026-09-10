CONNECT_IMAGE         := docker.redpanda.com/redpandadata/connect:4.108.0
CONNECT_CONFIGS       := $(addprefix /,$(wildcard connect/*.yaml))
GOLANGCI_LINT_VERSION := v2.13.2
GOVULNCHECK_VERSION   := v1.8.0
GO_VERSION            := $(shell awk '/^go /{print $$2}' reasoner/go.mod)

.PHONY: all format lint up down

all: lint

# Formatting

.PHONY: format-yamlfmt format-gofmt

format-yamlfmt:
	yamlfmt .

format-gofmt:
	cd reasoner && go fmt ./...

format: format-yamlfmt format-gofmt

# Linting

.PHONY: lint-yamlfmt lint-yamllint lint-actionlint lint-compose lint-connect lint-golangci-lint lint-go-mod lint-govulncheck

lint-yamlfmt:
	yamlfmt -lint .

lint-yamllint:
	yamllint .

lint-actionlint:
	actionlint

lint-compose:
	docker compose config --quiet

lint-connect:
	docker run --rm -v "$$(pwd)/connect:/connect:ro" $(CONNECT_IMAGE) --disable-telemetry lint $(CONNECT_CONFIGS)

lint-golangci-lint:
	cd reasoner && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...

lint-go-mod:
	cd reasoner && go mod tidy && git diff --exit-code -- go.mod go.sum

lint-govulncheck:
	cd reasoner && GOTOOLCHAIN=go$(GO_VERSION) go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

lint: lint-yamlfmt lint-yamllint lint-actionlint lint-compose lint-connect lint-golangci-lint lint-go-mod lint-govulncheck

# Docker Compose

up:
	docker compose up --build

down:
	docker compose down
