.DEFAULT_GOAL := verify

.PHONY: build build-garm-derivative fmt-check garm-derivative-script test test-race vet verify

build:
	go build -trimpath ./...

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
