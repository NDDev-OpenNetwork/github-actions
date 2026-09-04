.DEFAULT_GOAL := verify

.PHONY: build build-controller controller-release build-garm-derivative fmt-check garm-derivative-script test test-controller-release-manifest test-race vet verify

# The controller, observer, cache broker and pressure observer that ship to
# the hosts. Stamped, because an unstamped binary reports `version: dev,
# commit: unknown` -- which is what the hosts reported whenever a target
# compiled without ldflags. runProviderRelease's own comment already claimed
# the Makefile stamped them; this is that claim made true.
#
# The version is the provider derivative version, read from the manifest rather
# than written here, so the binary and the manifest are one statement. The
# commit is the exact tree, and a dirty tree is refused rather than stamped with
# a commit it does not match.
CONTROLLER_VERSION = $(shell go run ./cmd/gha-fleet provider-release --field derivative_version)
CONTROLLER_COMMIT  = $(shell git rev-parse HEAD)
CONTROLLER_LDFLAGS = -buildid= -s -w -X main.version=$(CONTROLLER_VERSION) -X main.commit=$(CONTROLLER_COMMIT)

build:
	go build -trimpath ./...

build-controller:
	@test -z "$$(git status --porcelain)" || { \
		echo "refusing to stamp a dirty tree: the commit would not describe these bytes" >&2; \
		git status --short >&2; \
		exit 1; \
	}
	@test -n "$(CONTROLLER_VERSION)" || { echo "provider release manifest gave no version" >&2; exit 1; }
	mkdir -p dist
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "$(CONTROLLER_LDFLAGS)" -o dist/gha-fleet ./cmd/gha-fleet
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "$(CONTROLLER_LDFLAGS)" -o dist/gha-fleet-observer ./cmd/gha-fleet-observer
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "$(CONTROLLER_LDFLAGS)" -o dist/gha-cache-broker ./cmd/gha-cache-broker
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "$(CONTROLLER_LDFLAGS)" -o dist/gha-pressure-observer ./cmd/gha-pressure-observer
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "$(CONTROLLER_LDFLAGS)" -o dist/gha-diagnostic-exporter ./cmd/gha-diagnostic-exporter
	@./dist/gha-fleet version
	@./dist/gha-fleet-observer --version
	@./dist/gha-cache-broker -version
	@./dist/gha-pressure-observer --version
	@./dist/gha-diagnostic-exporter --version

# The only supported deployable controller build. The manifest binds every
# artifact to the exact source tree and build inputs consumed by the estate.
controller-release: build-controller
	@./scripts/write-controller-release-manifest.sh \
		"$(CONTROLLER_VERSION)" "$(CONTROLLER_COMMIT)" dist/controller-release.json dist/gha-fleet \
		dist/gha-fleet-observer dist/gha-cache-broker dist/gha-pressure-observer dist/gha-diagnostic-exporter

garm-derivative-script:
	go run ./cmd/gha-fleet render-garm-build

build-garm-derivative:
	scripts/build-garm-nddev.sh

fmt-check:
	@test -z "$$(gofmt -l cmd internal third_party)" || { \
		gofmt -d cmd internal third_party; \
		exit 1; \
	}

test:
	go test ./...

test-controller-release-manifest:
	./scripts/test-controller-release-manifest.sh

test-race:
	go test -race ./...

vet:
	go vet ./...

verify: fmt-check vet test-race test-controller-release-manifest build
