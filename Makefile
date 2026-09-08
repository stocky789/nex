# SPDX-License-Identifier: AGPL-3.0-only
GO ?= go
PYTHON ?= python3
NPM ?= npm

.PHONY: build source binaries ui test typecheck
build: source
	rm -rf dist/build-source
	mkdir -p dist/build-source
	tar -xzf dist/nex-source.tar.gz -C dist/build-source --strip-components=1
	cd dist/build-source && $(GO) build -mod=vendor -trimpath -o ../nex-controller ./cmd/nex-controller
	cd dist/build-source && $(GO) build -mod=vendor -trimpath -o ../nex-core-agent ./cmd/nex-core-agent
	cd dist/build-source && $(GO) build -mod=vendor -trimpath -o ../nex-core-local ./cmd/nex-core-local
	cd dist/build-source/web && $(NPM) ci && $(NPM) run typecheck && $(NPM) run build
	rm -rf dist/web
	cp -r dist/build-source/web/dist dist/web
	rm -rf dist/api
	cp -r dist/build-source/docs/api dist/api

source:
	GO="$(GO)" $(PYTHON) scripts/build-source.py

binaries:
	mkdir -p dist
	$(GO) build -trimpath -o dist/nex-controller ./cmd/nex-controller
	$(GO) build -trimpath -o dist/nex-core-agent ./cmd/nex-core-agent
	$(GO) build -trimpath -o dist/nex-core-local ./cmd/nex-core-local

ui:
	cd web && $(NPM) ci && $(NPM) run build

typecheck:
	cd web && $(NPM) run typecheck

test:
	$(GO) test ./...
	$(PYTHON) -m unittest discover -s tests -p 'test_*.py'
