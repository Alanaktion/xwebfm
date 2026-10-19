BIN := xwebfm

.PHONY: all build web go dev test lint clean

all: build

build: web go

web:
	cd web && npm ci && npm run build

go:
	go build -o $(BIN) ./cmd/xwebfm

# Run the Go API on :8080 and the Vite dev server (with /api proxied) together.
dev:
	cd web && npm install
	trap 'kill 0' EXIT; go run ./cmd/xwebfm -listen 127.0.0.1:8080 & (cd web && npm run dev)

test:
	go test ./...

lint:
	go vet ./...
	test -z "$$(gofmt -l .)"
	cd web && npm run check

clean:
	rm -f $(BIN)
	find web/dist -mindepth 1 ! -name .gitkeep -delete
