# Firehose build. The frontend (web/dist) must be built before `go build`: it is embedded.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BUN ?= bun
BUNX := $(BUN) --bun

.PHONY: all web build dev test test-go test-web lint e2e docker bench-loopback clean

all: build

web:
	cd web && $(BUN) install --frozen-lockfile && $(BUNX) run build
	touch web/dist/.gitkeep

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o firehose ./cmd/firehose

# Go server in single-origin dev mode on :8080 + Vite dev server (proxying /api and the WebSocket) on :5173.
dev:
	@trap 'kill 0' INT TERM EXIT; \
	go run ./cmd/firehose serve --dev --public-url http://localhost:5173 & \
	cd web && $(BUNX) run dev

test: test-go test-web

test-go:
	go test -race ./...

test-web:
	cd web && $(BUN) test tests/unit

lint:
	go vet ./...
	staticcheck ./...
	cd web && $(BUNX) run typecheck

e2e: build
	cd web && $(BUNX) playwright test

docker:
	docker build --build-arg VERSION=$(VERSION) -t firehose:$(VERSION) .

# §13.3 row 1: loopback sanity check, no TLS. Target ≥ 20 Gbps with -n 8.
bench-loopback: build
	@./firehose serve --dev --listen 127.0.0.1:18080 --log-level warn & pid=$$!; \
	sleep 1; \
	./firehose client --server http://127.0.0.1:18080 -n 8 --no-loss --duration 6s; \
	./firehose client --server http://127.0.0.1:18080 -n 1 --no-loss --duration 6s; \
	kill $$pid

clean:
	rm -f firehose
	find web/dist -mindepth 1 ! -name .gitkeep -delete
