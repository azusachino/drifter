GO = mise exec -- go
GOFMT = mise exec -- gofmt

.PHONY: check fmt fmt-check test build

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
	$(GO) build -o bin/drifter ./cmd/drifter
