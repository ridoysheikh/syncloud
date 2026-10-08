.PHONY: all build controller agent synctl web proto test vet fmt dev dev-controller dev-agent dev-web clean release e2e postgres-image

# The version without the tag's "v" (v0.1.0 → 0.1.0).
VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev))
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X github.com/ridoysheikh/syncloud/internal/version.Version=$(VERSION) -X github.com/ridoysheikh/syncloud/internal/version.Commit=$(COMMIT)
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

## postgres-image: build the managed PostgreSQL images (Patroni, WAL-G, etcd, extensions) locally, one per major version, as syncloud-postgres:<tag> (tags pinned in internal/system)
POSTGRES_MAJORS := $(shell sed -n 's/.*PostgresTag\([0-9]*\) *= *".*/\1/p' internal/system/manifest.go)
pg_tag = $(shell sed -n 's/.*PostgresTag$(1) *= *"\(.*\)"/\1/p' internal/system/manifest.go)
postgres-image: $(addprefix postgres-image-,$(POSTGRES_MAJORS))
postgres-image-%:
	docker build --build-arg PG_MAJOR=$* -t syncloud-postgres:$(call pg_tag,$*) images/postgres

web: web/node_modules
	cd web && pnpm run build
	@touch internal/web/dist/.gitkeep

web/node_modules: web/package.json web/pnpm-lock.yaml
	cd web && pnpm install --frozen-lockfile
	@touch $@

## release: build a release locally into dist/ (make release VERSION=0.1.0):
##   raw binaries (what install.sh and upgrades fetch), tar.gz bundles per
##   platform, the PostgreSQL image archives, install.sh and SHA256SUMS.
##   RELEASE_IMAGES=0 skips the images (they need make postgres-image first).
RELEASE_IMAGES ?= 1
release: web
	rm -rf dist && mkdir -p dist
	@echo "== binaries $(VERSION)"
	for arch in amd64 arm64; do \
	  for cmd in controller agent; do \
	    CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/syncloud-$$cmd-linux-$$arch ./cmd/$$cmd || exit 1; \
	  done; \
	  for os in linux darwin; do \
	    CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/synctl-$$os-$$arch ./cmd/synctl || exit 1; \
	  done; \
	done
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/synctl-windows-amd64.exe ./cmd/synctl
	cp scripts/install.sh dist/
	@echo "== bundles"
	set -e; for arch in amd64 arm64; do \
	  b=syncloud_$(VERSION)_linux_$$arch; mkdir -p dist/.pkg/$$b; \
	  cp dist/syncloud-controller-linux-$$arch dist/.pkg/$$b/syncloud-controller; \
	  cp dist/syncloud-agent-linux-$$arch dist/.pkg/$$b/syncloud-agent; \
	  cp dist/synctl-linux-$$arch dist/.pkg/$$b/synctl; \
	  cp scripts/install.sh LICENSE README.md dist/.pkg/$$b/; \
	  tar -C dist/.pkg --owner=0 --group=0 -czf dist/$$b.tar.gz $$b; \
	done
	set -e; for t in darwin_amd64 darwin_arm64 windows_amd64; do \
	  b=synctl_$(VERSION)_$$t; mkdir -p dist/.pkg/$$b; \
	  src=dist/synctl-$$(echo $$t | tr _ -); [ -f $$src ] || src=$$src.exe; \
	  cp $$src dist/.pkg/$$b/$$(basename $$src | sed 's/-.*\.exe$$/.exe/; s/-.*//'); \
	  cp LICENSE dist/.pkg/$$b/; \
	  tar -C dist/.pkg --owner=0 --group=0 -czf dist/$$b.tar.gz $$b; \
	done
	rm -rf dist/.pkg
ifeq ($(RELEASE_IMAGES),1)
	@echo "== PostgreSQL images (linux/amd64)"
	set -e; for major in $(POSTGRES_MAJORS); do \
	  tag=$$(sed -n "s/.*PostgresTag$$major *= *\"\(.*\)\"/\1/p" internal/system/manifest.go); \
	  docker image inspect syncloud-postgres:$$tag >/dev/null 2>&1 || $(MAKE) postgres-image-$$major; \
	  go run ./tools/imagepack syncloud-postgres:$$tag $$tag dist/syncloud-postgres-$$tag-linux-amd64.tar.gz; \
	done
endif
	cd dist && sha256sum * > SHA256SUMS
	@echo "== dist/"; ls -lh dist

## e2e: multi-node tests in Docker-in-Docker containers (needs Docker, privileged containers)
e2e:
	test/e2e/mesh.sh
	test/e2e/services.sh
	test/e2e/deploy.sh
	test/e2e/registry.sh
	test/e2e/lifecycle.sh
	test/e2e/builds.sh
	test/e2e/metrics.sh
	test/e2e/traffic.sh
	test/e2e/autoscale.sh
	test/e2e/alerts.sh
	test/e2e/secgroups.sh
	test/e2e/routing.sh
	test/e2e/iam.sh
	test/e2e/pools.sh
	test/e2e/upgrade.sh
	test/e2e/storage.sh
	test/e2e/integrations.sh
	test/e2e/gitserver.sh
	test/e2e/restore.sh
	test/e2e/chaos.sh

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
	gofmt -w cmd internal sdk tools

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
