GO ?= $(shell command -v go 2>/dev/null || printf '%s' /opt/remoteterminal/tools/go-1.26.5-5c2c3b16caef/bin/go)
GOFMT ?= $(shell command -v gofmt 2>/dev/null || printf '%s' /opt/remoteterminal/tools/go-1.26.5-5c2c3b16caef/bin/gofmt)
NPM ?= npm
GO_TAGS ?= production pam

BUILD_DIR ?= $(CURDIR)/build
BINARY ?= $(BUILD_DIR)/websetupmanager
PROGRAM_ROOT ?= $(WEB_SETUP_MANAGER_PROGRAM_ROOT)
LINUXCNC_INI ?= $(WEB_SETUP_MANAGER_LINUXCNC_INI)
PROGRAM_ROOT_DISPLAY ?= $(WEB_SETUP_MANAGER_PROGRAM_ROOT_DISPLAY)

.PHONY: all build backend frontend frontend-deps test test-backend test-pam test-race test-frontend lint typecheck vet fmt-check quality dev-backend dev-frontend clean

all: test build

build: frontend backend

backend:
	@mkdir -p "$(BUILD_DIR)"
	CGO_ENABLED=1 $(GO) build -tags "$(GO_TAGS)" -trimpath -ldflags '-s -w' -o "$(BINARY)" ./cmd/websetupmanager

frontend: frontend-deps
	cd web && $(NPM) run build

frontend-deps:
	cd web && $(NPM) ci --no-audit --no-fund

test: test-backend test-frontend

test-backend:
	$(GO) test ./...

test-pam:
	CGO_ENABLED=1 $(GO) test -tags pam ./...

test-race:
	CGO_ENABLED=1 $(GO) test -race -tags pam ./...

test-frontend: frontend-deps
	cd web && $(NPM) test -- --run

lint: frontend-deps
	cd web && $(NPM) run lint

typecheck: frontend-deps
	cd web && $(NPM) run typecheck

vet:
	CGO_ENABLED=1 $(GO) vet -tags pam ./...

fmt-check:
	@test -z "$$(rg --files -g '*.go' | xargs "$(GOFMT)" -l)"

quality: fmt-check vet test-backend test-pam test-race lint typecheck test-frontend build

dev-backend:
	@test -n "$(LIBRARY_DIR)" || (echo "LIBRARY_DIR is required" >&2; exit 1)
	@test -n "$(PROGRAM_ROOT)" || (echo "PROGRAM_ROOT is required" >&2; exit 1)
	@test -n "$(LINUXCNC_INI)" || (echo "LINUXCNC_INI is required" >&2; exit 1)
	WEB_SETUP_MANAGER_LIBRARY_DIR="$(LIBRARY_DIR)" \
	WEB_SETUP_MANAGER_STATE_DIR="$(STATE_DIR)" \
	WEB_SETUP_MANAGER_PROGRAM_ROOT="$(PROGRAM_ROOT)" \
	WEB_SETUP_MANAGER_LINUXCNC_INI="$(LINUXCNC_INI)" \
	WEB_SETUP_MANAGER_PROGRAM_ROOT_DISPLAY="$(or $(PROGRAM_ROOT_DISPLAY),LinuxCNC programs)" \
	$(GO) run ./cmd/websetupmanager

dev-frontend: frontend-deps
	cd web && $(NPM) run dev

clean:
	rm -rf "$(BUILD_DIR)" web/dist
