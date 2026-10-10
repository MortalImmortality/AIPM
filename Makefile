.PHONY: build test race vet fmt fmt-check smoke

build:
	go build -o bin/aipm ./cmd/aipm

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

fmt-check:
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; exit 1)

smoke: build
	python3 -I scripts/smoke.py ./bin/aipm
