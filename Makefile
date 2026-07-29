# OpenFrp build targets.
#
# CGO is off everywhere on purpose: it is what produces a single static binary
# with no libc dependency, which is how one build runs on Alpine, Debian
# oldstable, CentOS and OpenWrt alike.

MODULE  := github.com/zoefix/openfrp
# The release number lives in internal/version/version.go, in x.y.z form;
# read it from there so a build stamps the same number the source declares.
# Override with `make VERSION=…` only for a prerelease experiment.
VERSION ?= $(shell awk -F'"' '/^	Version = /{print $$2}' internal/version/version.go)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT) \
	-X $(MODULE)/internal/version.Date=$(DATE)

GO      := CGO_ENABLED=0 go
BUILD   := $(GO) build -trimpath -ldflags "$(LDFLAGS)"
BIN     := bin
DIST    := dist

# Server targets. The client list is narrower because the project targets x86
# soft routers first.
SERVER_PLATFORMS := \
	linux/amd64 linux/arm64 linux/arm linux/386 \
	linux/riscv64 linux/loong64 linux/mips linux/mipsle
CLIENT_PLATFORMS := linux/amd64 linux/arm64

.PHONY: all
all: build

.PHONY: build
build: ## Build both binaries for the host platform
	@mkdir -p $(BIN)
	$(BUILD) -o $(BIN)/ ./cmd/...
	@ls -lh $(BIN)

.PHONY: test
test: ## Run the full test suite with the race detector
	go test ./... -race -count=1 -timeout 300s

.PHONY: test-linux
test-linux: ## Run the suite inside Linux, where splice(2) actually engages
	docker run --rm -v "$(CURDIR)":/src -w /src golang:1.25 \
		go test ./... -count=1 -vet=off -timeout 300s

.PHONY: vet
vet: ## Vet and format check
	go vet ./...
	@unformatted=$$(gofmt -l cmd internal pkg); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: check
check: vet test ## Everything CI runs

.PHONY: cross
cross: ## Cross-compile release artefacts for every target platform
	@mkdir -p $(DIST)
	@for platform in $(SERVER_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		echo "openfrps $$os/$$arch"; \
		env GOOS=$$os GOARCH=$$arch $${arch:+$(if $(filter mips mipsle,$(arch)),GOMIPS=softfloat,)} \
			CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" \
			-o $(DIST)/openfrps_$${os}_$${arch} ./cmd/openfrps || exit 1; \
	done
	@for platform in $(CLIENT_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		echo "openfrpc $$os/$$arch"; \
		env GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath \
			-ldflags "$(LDFLAGS)" \
			-o $(DIST)/openfrpc_$${os}_$${arch} ./cmd/openfrpc || exit 1; \
	done
	@ls -lh $(DIST)

.PHONY: bundle
bundle: ## Build the router-side release bundles the updater installs
	VERSION=$(VERSION) COMMIT=$(COMMIT) DATE=$(DATE) ./scripts/bundle.sh

.PHONY: bench
bench: ## Run the frp comparison and publish the results
	./bench/run.sh
	./bench/publish.sh

.PHONY: dev-up
dev-up: ## Bring up the local server/client/service stack
	docker compose -f deploy/dev/docker-compose.yml up --build

.PHONY: dev-down
dev-down:
	docker compose -f deploy/dev/docker-compose.yml down -v

.PHONY: clean
clean:
	rm -rf $(BIN) $(DIST)

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
