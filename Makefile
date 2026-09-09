.PHONY: all format lint up down

all: lint

# Formatting

.PHONY: format-yamlfmt

format-yamlfmt:
	yamlfmt .

format: format-yamlfmt

# Linting

.PHONY: lint-yamlfmt lint-yamllint lint-actionlint lint-compose

lint-yamlfmt:
	yamlfmt -lint .

lint-yamllint:
	yamllint .

lint-actionlint:
	actionlint

lint-compose:
	docker compose config --quiet

lint: lint-yamlfmt lint-yamllint lint-actionlint lint-compose

# Docker Compose

up:
	docker compose up --build

down:
	docker compose down
