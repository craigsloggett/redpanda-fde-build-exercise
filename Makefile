CONNECT_IMAGE   := docker.redpanda.com/redpandadata/connect:4.108.0
CONNECT_CONFIGS := $(addprefix /,$(wildcard connect/*.yaml))

.PHONY: all format lint up down

all: lint

# Formatting

.PHONY: format-yamlfmt

format-yamlfmt:
	yamlfmt .

format: format-yamlfmt

# Linting

.PHONY: lint-yamlfmt lint-yamllint lint-actionlint lint-compose lint-connect

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

lint: lint-yamlfmt lint-yamllint lint-actionlint lint-compose lint-connect

# Docker Compose

up:
	docker compose up --build

down:
	docker compose down
