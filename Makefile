.PHONY: all format lint

all: lint

# Formatting

.PHONY: format-yamlfmt

format-yamlfmt:
	yamlfmt .

format: format-yamlfmt

# Linting

.PHONY: lint-yamlfmt lint-yamllint lint-actionlint

lint-yamlfmt:
	yamlfmt -lint .

lint-yamllint:
	yamllint --strict .

lint-actionlint:
	actionlint

lint: lint-yamlfmt lint-yamllint lint-actionlint
