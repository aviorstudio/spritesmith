SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help
.NOTPARALLEL:
VERSION ?= dev
.PHONY: help install lint unit integration test build artifact-check vuln check dev stop clean
help:
	@echo 'make check: lint, race tests, five-platform archives, artifact verification, vulnerability scan'
install:
	mise trust .mise.toml
	mise install
	mise exec -- go mod download
lint:
	mise exec -- bash scripts/lint.sh
	mise exec -- actionlint
unit:
	mise exec -- go test -race -count=1 ./internal/token ./cmd/spritesmith
integration:
	mise exec -- go test -race -count=1 ./internal/client
test: unit integration
build:
	mise exec -- env VERSION="$(VERSION)" bash scripts/build.sh
artifact-check:
	mise exec -- python3 scripts/check-artifacts.py
vuln:
	mise exec -- govulncheck ./...
check: lint test build artifact-check vuln
dev:
	mise exec -- go run ./cmd/spritesmith $(ARGS)
stop:
	@echo 'unsupported: the CLI owns no background service'
clean:
	mise exec -- python3 -c 'import shutil; [shutil.rmtree(p, ignore_errors=True) for p in ("dist", ".artifacts")]'
