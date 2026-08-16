.DEFAULT_GOAL := verify

.PHONY: actionlint build build-benchmark build-controller build-diagnostic-exporter build-garm-derivative build-gateway build-observer build-provider build-stage-transaction cache-plan fmt fmt-check garm-derivative-script image-plan image-plan-integration reproducible-binaries rustfs-cache-plan shellcheck staticcheck telemetry-plan test test-race test-umask vet validate verify vulncheck

BENCHMARK_VERSION := v0.1.0
BENCHMARK_COMMIT ?= $(shell git rev-parse HEAD)
CONTROLLER_VERSION := v0.1.0
CONTROLLER_COMMIT ?= $(shell git rev-parse HEAD)
# Derived from config/provider-derivative.yaml so the stamp the binary carries
# and the version the manifest declares are one statement. Lazy (=) so only
# the targets that stamp a provider pay for reading it.
PROVIDER_VERSION = $(shell go run ./cmd/gha-fleet provider-release --field derivative_version)
PROVIDER_COMMIT ?= $(shell git rev-parse HEAD)
GATEWAY_VERSION := v0.1.0
GATEWAY_COMMIT ?= $(shell git rev-parse HEAD)
OBSERVER_VERSION := v0.6.2
OBSERVER_COMMIT ?= $(shell git rev-parse HEAD)
DIAGNOSTIC_EXPORTER_VERSION := v0.1.3
DIAGNOSTIC_EXPORTER_COMMIT ?= $(shell git rev-parse HEAD)
ACTIONLINT_VERSION := v1.7.12
GOVULNCHECK_VERSION := v1.6.0
STATICCHECK_VERSION := v0.7.0

build:
	go build -trimpath ./...

build-benchmark:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
		-ldflags '-buildid= -s -w -X main.version=$(BENCHMARK_VERSION) -X main.commit=$(BENCHMARK_COMMIT)' \
		-o dist/gha-benchmark-linux-amd64 ./cmd/gha-benchmark

build-controller:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
		-ldflags '-buildid= -s -w -X main.version=$(CONTROLLER_VERSION) -X main.commit=$(CONTROLLER_COMMIT)' \
		-o dist/gha-fleet-linux-amd64 ./cmd/gha-fleet

build-provider:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
		-ldflags '-buildid= -s -w -X main.version=$(PROVIDER_VERSION) -X main.commit=$(PROVIDER_COMMIT)' \
		-o dist/garm-provider-incus-nddev-linux-amd64 ./cmd/garm-provider-incus-nddev

build-gateway:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
		-ldflags '-buildid= -s -w -X main.version=$(GATEWAY_VERSION) -X main.commit=$(GATEWAY_COMMIT)' \
		-o dist/gha-fleet-gateway-linux-amd64 ./cmd/gha-fleet-gateway

build-observer:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
		-ldflags '-buildid= -s -w -X main.version=$(OBSERVER_VERSION) -X main.commit=$(OBSERVER_COMMIT)' \
		-o dist/gha-fleet-observer-linux-amd64 ./cmd/gha-fleet-observer

build-diagnostic-exporter:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
		-ldflags '-buildid= -s -w -X main.version=$(DIAGNOSTIC_EXPORTER_VERSION) -X main.commit=$(DIAGNOSTIC_EXPORTER_COMMIT)' \
		-o dist/gha-diagnostic-exporter-linux-amd64 ./cmd/gha-diagnostic-exporter

garm-derivative-script:
	go run ./cmd/gha-fleet render-garm-build

build-stage-transaction:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
		-ldflags '-buildid= -s -w' \
		-o dist/gha-stage-transaction-linux-amd64 ./cmd/gha-stage-transaction

build-garm-derivative:
	scripts/build-garm-nddev.sh

fmt:
	gofmt -w $$(find . -name '*.go' -type f)

fmt-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

test:
	go test ./...

test-race:
	go test -race ./...

# A worker runs the suite under umask 077. That masks two different things: the
# mode argument of every file a test creates, and the mode actions/checkout
# gives every file it writes. Reproduce both here instead of finding them on the
# fleet, by running the suite over a copy of the tracked tree whose group and
# other bits have been stripped exactly as that umask would strip them.
#
# The copy comes from the working tree, not HEAD, and includes files that are
# new but not ignored, so a package added in the current change is covered
# rather than producing a confusing missing-module failure. The test cache is
# disabled because it does not key on umask.
test-umask:
	@set -eu; \
		tree="$$(mktemp -d)"; \
		trap 'chmod -R u+rwX "$${tree}" 2>/dev/null || true; rm -rf "$${tree}"' EXIT; \
		git ls-files -z --cached --others --exclude-standard | xargs -0 cp --parents -t "$${tree}"; \
		chmod -R go-rwx "$${tree}"; \
		cd "$${tree}" && umask 077 && go test -count=1 ./...

vet:
	go vet ./...

# Every host the fleet declares, not just the first one. Two dedicated hosts
# were added with their own reserve mode and pool weights, and nothing checked
# them: a config that fails closed on one host and not another is exactly the
# kind of difference this target exists to catch.
HOST_CONFIGS := \
	config/server-gha-runner-1.yaml \
	config/server-gha-runner-2.yaml \
	config/server-gha-runner-3.yaml

validate:
	@set -eu; for host_config in $(HOST_CONFIGS); do \
		printf '==> %s\n' "$${host_config}"; \
		go run ./cmd/gha-fleet validate --config "$${host_config}"; \
	done

image-plan:
	go run ./cmd/gha-fleet reconcile-image --config config/server-gha-runner-1.yaml --manifest config/golden-image.yaml

image-plan-integration:
	go run ./cmd/gha-fleet reconcile-image --config config/server-gha-runner-1.yaml --manifest config/golden-image-integration.yaml --profile nddev-linux-integration

cache-plan:
	go run ./cmd/gha-fleet validate-cache --manifest config/cache-artifacts.yaml

telemetry-plan:
	go run ./cmd/gha-fleet validate-telemetry --manifest config/telemetry-artifacts.yaml

rustfs-cache-plan:
	go run ./cmd/gha-fleet validate-rustfs-cache --config config/rustfs-cache-identities.yaml

shellcheck:
	shellcheck internal/imagebuild/assets/*.sh scripts/*.sh

actionlint:
	GOWORK=off go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

staticcheck:
	GOWORK=off go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

vulncheck:
	GOWORK=off go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

reproducible-binaries:
	@set -eu; \
		repro_dir="$$(mktemp -d)"; \
		trap 'rm -rf "$${repro_dir}"' EXIT; \
		$(MAKE) --no-print-directory build-benchmark build-controller build-provider build-gateway build-observer build-diagnostic-exporter build-stage-transaction; \
		sha256sum dist/gha-benchmark-linux-amd64 > "$${repro_dir}/benchmark.sha256"; \
		sha256sum dist/gha-fleet-linux-amd64 > "$${repro_dir}/controller.sha256"; \
		sha256sum dist/garm-provider-incus-nddev-linux-amd64 > "$${repro_dir}/provider.sha256"; \
		sha256sum dist/gha-fleet-gateway-linux-amd64 > "$${repro_dir}/gateway.sha256"; \
		sha256sum dist/gha-fleet-observer-linux-amd64 > "$${repro_dir}/observer.sha256"; \
		sha256sum dist/gha-diagnostic-exporter-linux-amd64 > "$${repro_dir}/diagnostic-exporter.sha256"; \
		sha256sum dist/gha-stage-transaction-linux-amd64 > "$${repro_dir}/stage-transaction.sha256"; \
		rm -f dist/gha-benchmark-linux-amd64 dist/gha-fleet-linux-amd64 dist/garm-provider-incus-nddev-linux-amd64 dist/gha-fleet-gateway-linux-amd64 dist/gha-fleet-observer-linux-amd64 dist/gha-diagnostic-exporter-linux-amd64 dist/gha-stage-transaction-linux-amd64; \
		$(MAKE) --no-print-directory build-benchmark build-controller build-provider build-gateway build-observer build-diagnostic-exporter build-stage-transaction; \
		sha256sum --check "$${repro_dir}/benchmark.sha256"; \
		sha256sum --check "$${repro_dir}/controller.sha256"; \
		sha256sum --check "$${repro_dir}/provider.sha256"; \
		sha256sum --check "$${repro_dir}/gateway.sha256"; \
		sha256sum --check "$${repro_dir}/observer.sha256"; \
		sha256sum --check "$${repro_dir}/diagnostic-exporter.sha256"; \
		sha256sum --check "$${repro_dir}/stage-transaction.sha256"

verify: fmt-check vet staticcheck test-race test-umask build validate image-plan image-plan-integration cache-plan telemetry-plan rustfs-cache-plan shellcheck actionlint reproducible-binaries vulncheck
