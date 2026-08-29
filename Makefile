.DEFAULT_GOAL := verify

.PHONY: build build-controller build-garm-derivative fmt-check garm-derivative-script test test-race vet verify

# The controller and observer that ship to the hosts. Stamped, because an
# unstamped binary reports `version: dev, commit: unknown` -- which is what all
# five hosts reported, since `build` compiles without ldflags and no other
# target ever produced these. runProviderRelease's own comment already claimed
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
	@./dist/gha-fleet version

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

test-race:
	go test -race ./...

vet:
	go vet ./...

verify: fmt-check vet test-race build
