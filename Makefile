.PHONY: all test build-web build-server build-collector build-all install-collector clean

BIN_DIR := bin
VERSION ?= $(shell git rev-parse --short=7 HEAD 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

all: build-web build-server build-collector

test:
	go test -v ./...

build-web:
	@if [ -f web/package.json ]; then \
		cd web && npm run build && rm -rf ../internal/web/dist && cp -r dist ../internal/web/dist; \
	fi

build-server:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/ai-ledger-server ./cmd/ai-ledger-server

build-collector:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/ai-ledger-collector ./cmd/ai-ledger-collector

build-all: build-web
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/ai-ledger-server-linux-amd64 ./cmd/ai-ledger-server
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/ai-ledger-server-linux-arm64 ./cmd/ai-ledger-server
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/ai-ledger-collector-darwin-arm64 ./cmd/ai-ledger-collector
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/ai-ledger-collector-darwin-amd64 ./cmd/ai-ledger-collector
	CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/ai-ledger-collector-linux-amd64 ./cmd/ai-ledger-collector
	CGO_ENABLED=0 GOOS=linux  GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/ai-ledger-collector-linux-arm64 ./cmd/ai-ledger-collector

install-collector:
	./scripts/install-collector.sh

clean:
	rm -rf $(BIN_DIR)
