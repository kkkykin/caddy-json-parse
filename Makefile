SHELL := /usr/bin/env bash

NIX_DEVELOP ?= nix develop --command bash -lc
CACHE_ENV := XDG_CACHE_HOME="$(CURDIR)/.cache" GOPATH="$(CURDIR)/.gopath" GOMODCACHE="$(CURDIR)/.gopath/pkg/mod" GOCACHE="$(CURDIR)/.gocache"

.PHONY: fmt test build run clean

fmt:
	$(NIX_DEVELOP) "$(CACHE_ENV) gofmt -w randomfile/*.go"

test:
	$(NIX_DEVELOP) "$(CACHE_ENV) go test ./..."

build:
	$(NIX_DEVELOP) "$(CACHE_ENV) xcaddy build --with github.com/abiosoft/caddy-json-parse=./"

tidy:
	$(NIX_DEVELOP) "$(CACHE_ENV) go mod tidy"

run:
	./caddy run --config Caddyfile.example

clean:
	rm -rf .cache .gopath .gocache
	rm -f caddy
