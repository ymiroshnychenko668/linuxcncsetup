GO ?= $(shell command -v go 2>/dev/null || printf '%s' /opt/remoteterminal/tools/go-1.26.5-5c2c3b16caef/bin/go)
NPM ?= npm

BUILD_DIR ?= $(CURDIR)/build
BINARY ?= $(BUILD_DIR)/websetupmanager

.PHONY: all build backend frontend frontend-deps test test-backend test-race test-frontend lint typecheck vet fmt-check quality dev-backend dev-frontend clean

all: test build

build: frontend backend

backend:
	@mkdir -p "$(BUILD_DIR)"
	CGO_ENABLED=0 $(GO) build -tags production -trimpath -ldflags '-s -w' -o "$(BINARY)" ./cmd/websetupmanager

frontend: frontend-deps
	cd web && $(NPM) run build

frontend-deps:
	cd web && $(NPM) ci --no-audit --no-fund

test: test-backend test-frontend

test-backend:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

test-frontend: frontend-deps
	cd web && $(NPM) test -- --run

lint: frontend-deps
	cd web && $(NPM) run lint

typecheck: frontend-deps
	cd web && $(NPM) run typecheck

vet:
	$(GO) vet ./...

fmt-check:
	@test -z "$$($(GO) fmt ./...)"

quality: fmt-check vet test-race lint typecheck test-frontend build

dev-backend:
	@test -n "$(LIBRARY_DIR)" || (echo "LIBRARY_DIR is required" >&2; exit 1)
	WEB_SETUP_MANAGER_LIBRARY_DIR="$(LIBRARY_DIR)" \
	WEB_SETUP_MANAGER_STATE_DIR="$(STATE_DIR)" \
	$(GO) run ./cmd/websetupmanager

dev-frontend: frontend-deps
	cd web && $(NPM) run dev

clean:
	rm -rf "$(BUILD_DIR)" web/dist
