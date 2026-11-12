SHELL := /bin/bash

# Keep Go caches inside the workspace (the sandbox only allows workspace writes).
export GOPATH := $(CURDIR)/.gopath
export GOMODCACHE := $(CURDIR)/.gomodcache
export GOCACHE := $(CURDIR)/.gocache

.PHONY: test run serve build fmt

test:
	go test ./...

run:
	go run ./cmd/hornedviper -db $(CURDIR)/hornedviper.db -mode ctf -runtime fake

serve:
	go run ./cmd/hornedviper -serve :8787 -db $(CURDIR)/hornedviper.db

build:
	mkdir -p bin
	go build -o bin/hornedviper ./cmd/hornedviper

fmt:
	gofmt -w cmd internal
