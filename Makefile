.PHONY: all build controller agent synctl web proto test vet fmt dev dev-controller dev-agent dev-web clean release

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
LDFLAGS := -s -w -X syncloud/internal/version.Version=$(VERSION)
DEV_DATA := $(CURDIR)/.data

all: build

## build: dashboard + controller binary (dashboard embedded)
build: web controller agent synctl

controller:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/syncloud-controller ./cmd/controller

agent:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/syncloud-agent ./cmd/agent

synctl:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/synctl ./cmd/synctl

web: web/node_modules
	cd web && pnpm run build
	@touch internal/web/dist/.gitkeep

web/node_modules: web/package.json web/pnpm-lock.yaml
	cd web && pnpm install --frozen-lockfile
	@touch $@

## release: linux binaries for amd64/arm64 plus SHA256SUMS in dist/ (what install.sh downloads)
release: web
	rm -rf dist && mkdir -p dist
	for arch in amd64 arm64; do \
	  for cmd in controller agent; do \
	    CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/syncloud-$$cmd-linux-$$arch ./cmd/$$cmd || exit 1; \
	  done; \
	  CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/synctl-linux-$$arch ./cmd/synctl || exit 1; \
	done
	cp scripts/install.sh dist/
	cd dist && sha256sum syncloud-* synctl-* > SHA256SUMS

## proto: regenerate gRPC code (tools are installed into .tools/ on first use)
proto: .tools/buf .tools/protoc-gen-go .tools/protoc-gen-go-grpc
	PATH=$(CURDIR)/.tools:$$PATH .tools/buf lint
	PATH=$(CURDIR)/.tools:$$PATH .tools/buf generate

.tools/buf:
	GOBIN=$(CURDIR)/.tools go install github.com/bufbuild/buf/cmd/buf@latest
.tools/protoc-gen-go:
	GOBIN=$(CURDIR)/.tools go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
.tools/protoc-gen-go-grpc:
	GOBIN=$(CURDIR)/.tools go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

test:
	go test -race -timeout 5m ./...

vet:
	go vet ./...
	cd web && pnpm run typecheck

fmt:
	gofmt -w cmd internal

## dev: controller on :7070, its local agent (ctl-0), and Vite on :5173 (open http://localhost:5173)
dev: web/node_modules
	@$(MAKE) -j3 dev-controller dev-agent dev-web

dev-controller:
	go run ./cmd/controller --dev --data-dir $(DEV_DATA)

# The local agent joins as ctl-0 with the token the controller writes on first start (§6.4).
dev-agent:
	@until [ -f $(DEV_DATA)/agent/agent.json ] || [ -f $(DEV_DATA)/local-join.token ]; do sleep 0.5; done
	@[ -f $(DEV_DATA)/agent/agent.json ] || go run ./cmd/agent join --data-dir $(DEV_DATA)/agent \
		--controller http://127.0.0.1:7070 --token-file $(DEV_DATA)/local-join.token --name ctl-0
	go run ./cmd/agent run --data-dir $(DEV_DATA)/agent

dev-web:
	cd web && pnpm run dev

clean:
	rm -rf bin
	find internal/web/dist -mindepth 1 ! -name .gitkeep -delete
