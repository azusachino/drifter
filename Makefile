GO = mise exec -- go
GOFMT = mise exec -- gofmt

CONTAINER_TOOL ?= podman
IMAGE_TAG ?= azusachino.com/drifter:latest

.PHONY: check fmt fmt-check test build image-build e2e e2e-install

check: fmt-check
	$(GO) vet ./...
	$(GO) mod verify
	$(GO) test -race ./...
	$(MAKE) build

fmt:
	$(GOFMT) -w cmd internal

fmt-check:
	@files="$$($(GOFMT) -l cmd internal)" || exit $$?; if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi

test:
	$(GO) test -race ./...

build:
	mkdir -p bin
	$(GO) build -o bin/drifter ./cmd/cli
	$(GO) build -o bin/drifter-server ./cmd/server

image-build:
	$(CONTAINER_TOOL) build -t $(IMAGE_TAG) --build-arg SOURCE_REVISION="$$(git rev-parse --short HEAD 2>/dev/null || echo unknown)" -f Containerfile .

e2e:
	mise exec -- npm run e2e

e2e-probe:
	E2E_PROBE=1 E2E_PROBE_ROUTE="$(ROUTE)" mise exec -- npm run e2e

e2e-install:
	mise exec -- npm install
	mise exec -- npm run e2e:install
