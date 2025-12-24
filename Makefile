SHELL := /usr/bin/env bash

NIX_DEVELOP ?= nix develop --command bash -lc
CACHE_ENV := XDG_CACHE_HOME="$(CURDIR)/.cache" GOPATH="$(CURDIR)/.gopath" GOMODCACHE="$(CURDIR)/.gopath/pkg/mod" GOCACHE="$(CURDIR)/.gocache"
CADDY_VERSION ?= v2.10.2

.PHONY: fmt test build tidy run clean

fmt:
	$(NIX_DEVELOP) "$(CACHE_ENV) gofmt -w *.go"

test:
	$(NIX_DEVELOP) "$(CACHE_ENV) go test ./..."

build:
	$(NIX_DEVELOP) "$(CACHE_ENV) xcaddy build $(CADDY_VERSION) --with github.com/abiosoft/caddy-json-parse=./"

tidy:
	$(NIX_DEVELOP) "$(CACHE_ENV) go mod tidy"

run:
	./caddy run --config Caddyfile.aria2.example

clean:
	rm -rf .cache .gopath .gocache
	rm -f caddy
