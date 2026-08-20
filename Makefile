.DEFAULT_GOAL := verify

.PHONY: build fmt-check test test-race vet verify

build:
	go build -trimpath ./...

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
