GO       ?= go
PKGS     := ./...
LDFLAGS  := -s -w
DESKLET  := internal/cli/desklet/o3ui@esivres/desklet.js
BINARY   := $(HOME)/.local/bin/o3ui

.PHONY: fmt vet lint lint-js test tidy check build install help

help:
	@echo 'targets: fmt vet lint lint-js test tidy check build install'

fmt:
	gofmt -w .
	$(GO) run golang.org/x/tools/cmd/goimports@latest -w -local github.com/esivres/openvpn3ui .

vet:
	$(GO) vet $(PKGS)

lint:
	golangci-lint run $(PKGS)

# eslint runs only if available — desklet code is optional polish.
lint-js:
	@command -v eslint >/dev/null 2>&1 \
		&& eslint $(DESKLET) \
		|| echo 'eslint not installed; run `npm i -g eslint` to lint the desklet'

test:
	$(GO) test -race -count=1 $(PKGS)

tidy:
	$(GO) mod tidy

build:
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/openvpn3ui-tui

# `make install` builds and installs the desklet in one shot.
install: build
	$(BINARY) desklet install

# Pre-commit gate. Skip linters that need extra tooling (golangci, eslint)
# — those run as part of explicit `make lint` / `make lint-js`.
check: fmt vet test
