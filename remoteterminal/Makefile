GO ?= go
NPM ?= npm
GO_TAGS ?= pam

BUILD_DIR ?= $(CURDIR)/build
BINARY ?= $(BUILD_DIR)/remoteterminal

.PHONY: all build backend frontend test test-backend test-frontend clean

all: test build

build: frontend backend

backend:
	@mkdir -p "$(BUILD_DIR)"
	CGO_ENABLED=1 $(GO) build -tags "$(GO_TAGS)" -trimpath -o "$(BINARY)" ./cmd/remoteterminal

frontend:
	cd web && $(NPM) ci
	cd web && $(NPM) run build

test: test-backend test-frontend

test-backend:
	CGO_ENABLED=1 $(GO) test -tags "$(GO_TAGS)" ./...

test-frontend:
	cd web && $(NPM) ci
	cd web && $(NPM) test -- --run

clean:
	rm -rf "$(BUILD_DIR)" web/dist
