VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build install clean test release-snapshot

build:
	@echo "Building kube-dc $(VERSION)..."
	go build -ldflags "$(LDFLAGS)" -o kube-dc ./cmd/kube-dc

install: build
	@echo "Installing to /usr/local/bin..."
	sudo cp kube-dc /usr/local/bin/

clean:
	rm -f kube-dc
	rm -rf dist/

test:
	go test -v ./...

# Build for all platforms locally (requires goreleaser)
release-snapshot:
	goreleaser release --snapshot --clean

# Build for specific platform
build-linux-amd64:
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/kube-dc-linux-amd64 ./cmd/kube-dc

build-linux-arm64:
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/kube-dc-linux-arm64 ./cmd/kube-dc

build-darwin-amd64:
	GOOS=darwin GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/kube-dc-darwin-amd64 ./cmd/kube-dc

build-darwin-arm64:
	GOOS=darwin GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/kube-dc-darwin-arm64 ./cmd/kube-dc

build-windows-amd64:
	GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/kube-dc-windows-amd64.exe ./cmd/kube-dc

build-all: build-linux-amd64 build-linux-arm64 build-darwin-amd64 build-darwin-arm64 build-windows-amd64
	@echo "All binaries built in dist/"
