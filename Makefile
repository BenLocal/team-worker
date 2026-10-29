GO ?= go

.DEFAULT_GOAL := help
.PHONY: help build install test vet fmt clean

help:
	@printf '%s\n' \
		'make build    Build bin/team-worker and bin/team-workerd' \
		'make install  Install both binaries to GOBIN or GOPATH/bin' \
		'make test     Run Go tests' \
		'make vet      Run go vet' \
		'make fmt      Format Go source files' \
		'make clean    Remove bin/'

build:
	mkdir -p bin
	$(GO) build -o bin/ ./cmd/...

install:
	$(GO) install ./cmd/...

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin
