.PHONY: all build controller web test vet fmt dev dev-controller dev-web clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
LDFLAGS := -s -w -X syncloud/internal/version.Version=$(VERSION)
DEV_DATA := $(CURDIR)/.data

all: build

## build: dashboard + controller binary (dashboard embedded)
build: web controller

controller:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/syncloud-controller ./cmd/controller

web: web/node_modules
	cd web && pnpm run build
	@touch internal/web/dist/.gitkeep

web/node_modules: web/package.json web/pnpm-lock.yaml
	cd web && pnpm install --frozen-lockfile
	@touch $@

test:
	go test ./...

vet:
	go vet ./...
	cd web && pnpm run typecheck

fmt:
	gofmt -w cmd internal

## dev: controller on :7070 + Vite dev server on :5173 (open http://localhost:5173)
dev: web/node_modules
	@$(MAKE) -j2 dev-controller dev-web

dev-controller:
	go run ./cmd/controller --dev --data-dir $(DEV_DATA)

dev-web:
	cd web && pnpm run dev

clean:
	rm -rf bin
	find internal/web/dist -mindepth 1 ! -name .gitkeep -delete
