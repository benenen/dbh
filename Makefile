GO ?= go
GOLANGCI_LINT ?= golangci-lint

.DEFAULT_GOAL := help
.PHONY: help build install test e2e vet lint fmt clean

help:
	@printf '%s\n' \
		'make build    Build bin/dbh' \
		'make install  Install dbh to GOBIN or GOPATH/bin' \
		'make test     Run Go tests' \
		'make e2e      Build and test the CLI with Go' \
		'make vet      Run go vet' \
		'make lint     Run golangci-lint (must be installed)' \
		'make fmt      Format Go source files' \
		'make clean    Remove bin/dbh'

build:
	mkdir -p bin
	$(GO) build -o bin/dbh .

install:
	$(GO) install .

test:
	$(GO) test ./...

e2e:
	$(GO) test -tags=e2e -count=1 -v ./tests

vet:
	$(GO) vet ./...

lint:
	$(GOLANGCI_LINT) run

fmt:
	$(GO) fmt ./...

clean:
	rm -f bin/dbh
