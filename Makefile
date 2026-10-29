GO ?= go

.DEFAULT_GOAL := help
.PHONY: help build install test vet fmt clean web

help:
	@printf '%s\n' \
		'make build    Build bin/team-worker and bin/team-workerd' \
		'make install  Install both binaries to GOBIN or GOPATH/bin' \
		'make test     Run Go tests' \
		'make vet      Run go vet' \
		'make fmt      Format Go source files' \
		'make web      Rebuild the board page into internal/board/static' \
		'make clean    Remove bin/'

build: web
	mkdir -p bin
	$(GO) build -o bin/ ./cmd/...

install: web
	$(GO) install ./cmd/...

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin

# The board page is embedded into team-worker; npm ci reruns only when the lockfile changes.
WEB := internal/board/web
web: $(WEB)/node_modules/.package-lock.json
	cd $(WEB) && npm run build

$(WEB)/node_modules/.package-lock.json: $(WEB)/package-lock.json
	cd $(WEB) && npm ci
