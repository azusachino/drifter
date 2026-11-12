GO = mise exec -- go
GOFMT = mise exec -- gofmt

.PHONY: check fmt fmt-check test build e2e e2e-install image

IMAGE_NS := azusachino.com
TAG ?= deploy-$(shell git rev-parse --short HEAD)

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

image: ## Build drifter-server and import it into the local k3s containerd store (TAG=$(TAG))
	podman build -t $(IMAGE_NS)/drifter:$(TAG) -f Containerfile .
	podman save $(IMAGE_NS)/drifter:$(TAG) | sudo k3s ctr images import --all-platforms --digests --skip-digest-for-named -

e2e:
	mise exec -- npm run e2e

e2e-probe:
	E2E_PROBE=1 E2E_PROBE_ROUTE="$(ROUTE)" mise exec -- npm run e2e

e2e-install:
	mise exec -- npm install
	mise exec -- npm run e2e:install
