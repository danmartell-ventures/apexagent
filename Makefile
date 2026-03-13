VERSION ?= dev
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/danmartell-ventures/apex-agent/pkg/version.Version=$(VERSION) \
           -X github.com/danmartell-ventures/apex-agent/pkg/version.Commit=$(COMMIT) \
           -X github.com/danmartell-ventures/apex-agent/pkg/version.Date=$(DATE)

.PHONY: build run clean test lint icons pkg

build:
	CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o bin/apex-agent ./cmd/apex-agent

run: build
	./bin/apex-agent run --foreground

clean:
	rm -rf bin/

test:
	go test ./...

lint:
	golangci-lint run

icons:
	go run scripts/gen-icons.go

install: build
	cp bin/apex-agent /usr/local/bin/apex-agent

pkg: build
	@echo "Building PKG installer (v$(VERSION))..."
	@mkdir -p bin/pkg-root/usr/local/bin
	@cp bin/apex-agent bin/pkg-root/usr/local/bin/apex-agent
	@pkgbuild \
		--root bin/pkg-root \
		--identifier host.apex.agent \
		--version $(VERSION) \
		--scripts packaging/scripts \
		bin/apex-agent.pkg
	@productbuild \
		--distribution packaging/distribution.xml \
		--resources packaging \
		--package-path bin \
		bin/ApexAgent-$(VERSION).pkg
	@rm -f bin/apex-agent.pkg
	@echo "Built bin/ApexAgent-$(VERSION).pkg"

.DEFAULT_GOAL := build
