GO ?= go
VERSION ?= dev

.DEFAULT_GOAL := list

.PHONY: list help run demo build check test race gif vendor release install

list: ## Show available commands
	@awk 'BEGIN {FS = ":.*## "; print "scrunch commands\n"} /^[a-zA-Z0-9_.-]+:.*## / {printf "  make %-10s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

help: list ## Show available commands

run: build ## Build and open the blank workspace
	bin/scrunch

demo: build ## Build and open the offline sample workspace
	bin/scrunch --demo

build: ## Build bin/scrunch for this machine
	CGO_ENABLED=0 $(GO) build -mod=vendor -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/scrunch ./app/scrunch

check: ## Run go vet and all tests
	$(GO) vet -mod=vendor ./...
	$(GO) test -mod=vendor ./...

test: ## Run tests (database tests use SCRUNCH_TEST_DSN when set)
	$(GO) test -mod=vendor ./...

race: ## Run tests with the race detector
	$(GO) test -mod=vendor -race ./...

gif: build ## Record docs/assets/demo.gif from docs/demo.tape (needs Charm's vhs)
	@mkdir -p .scratch/vhs-home .scratch/tweet docs/assets
	vhs docs/demo.tape

vendor: ## Refresh vendored dependencies and reapply the privacy patch
	$(GO) mod tidy
	$(GO) mod vendor
	patch -p1 < docs/vendor-privacy.patch

release: ## Build macOS arm64 and static Linux amd64/arm64 binaries
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -mod=vendor -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/scrunch-linux-amd64 ./app/scrunch
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -mod=vendor -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/scrunch-linux-arm64 ./app/scrunch
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -mod=vendor -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/scrunch-darwin-arm64 ./app/scrunch

install: ## Install scrunch into GOBIN or GOPATH/bin
	CGO_ENABLED=0 $(GO) install -mod=vendor -trimpath -ldflags="-s -w -X main.version=$(VERSION)" ./app/scrunch
