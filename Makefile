GO ?= go

.DEFAULT_GOAL := help
.PHONY: help build dist install install-cli test vet fmt clean web

help:
	@printf '%s\n' \
		'make build    Build bin/team-worker and bin/team-workerd' \
		'make dist     Cross-build the team-worker CLI into bin/dist/ for every platform' \
		'make install  Install both binaries to GOBIN or GOPATH/bin' \
		'make install-cli  Install only the team-worker CLI to BINDIR (default ~/.local/bin)' \
		'make test     Run Go tests' \
		'make vet      Run go vet' \
		'make fmt      Format Go source files' \
		'make web      Rebuild the board page into internal/board/dist' \
		'make clean    Remove bin/'

build: web
	mkdir -p bin
	$(GO) build -o bin/ ./cmd/...

# The CLI for remote use on other machines; team-workerd stays Linux/macOS only.
DIST_PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
dist: web
	mkdir -p bin/dist
	@set -e; for p in $(DIST_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ "$$os" = windows ] && ext=.exe; \
		out=bin/dist/team-worker-$$os-$$arch$$ext; \
		echo "build $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -o $$out ./cmd/team-worker; \
	done

install: web
	$(GO) install ./cmd/...

# Only the CLI, e.g. for remote use. The rename replaces a binary that running
# agents or supervisors still execute without "text file busy".
BINDIR ?= $(HOME)/.local/bin
install-cli: web
	mkdir -p $(BINDIR)
	$(GO) build -trimpath -o $(BINDIR)/.team-worker.tmp ./cmd/team-worker
	mv -f $(BINDIR)/.team-worker.tmp $(BINDIR)/team-worker
	@echo "installed $(BINDIR)/team-worker"

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
