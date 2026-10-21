.PHONY: all build test test-race cover vet fmt lint tidy tidy-check ci example clean

all: build test

## build: compile all packages (binaries go to /dev/null — no artifacts left behind)
build:
	go build -o /dev/null .
	go build -o /dev/null ./examples/api/stdlibmux
	go build -o /dev/null ./examples/api/gin
	go build -o /dev/null ./examples/api/basic-authed-gin
	go build -o /dev/null ./examples/api/token-authed-gin
	go build -o /dev/null ./examples/requests

## test: run the unit tests
test:
	go test -count=1 -timeout 120s ./...

## test-race: run tests with the race detector
test-race:
	go test -race -count=1 -timeout 180s ./...

## cover: run tests and print a coverage summary (examples excluded)
cover:
	go test -count=1 -timeout 120s -coverprofile=coverage.out $$(go list ./... | grep -v /examples/)
	go tool cover -func=coverage.out | tail -1

## cover-ci: coverage run matching CI (race detector + atomic mode, examples excluded)
cover-ci:
	go test -race -count=1 -timeout 120s -coverprofile=coverage.out -covermode=atomic $$(go list ./... | grep -v /examples/)
	go tool cover -func=coverage.out | tail -1

## vet: run go vet
vet:
	go vet ./...

## fmt: format all Go sources
fmt:
	gofmt -w .
	goimports -w -local github.com/GoLens-Project/golens-metrics . 2>/dev/null || true

## lint: run golangci-lint (if installed)
lint:
	golangci-lint run ./...

## tidy: ensure go.mod/go.sum are clean
tidy:
	go mod tidy

## tidy-check: verify go.mod/go.sum match `go mod tidy` (matches CI gate)
tidy-check:
	go mod tidy
	@git diff --exit-code go.mod go.sum || (echo "go.mod/go.sum not tidy — run 'make tidy' and commit" && exit 1)

## ci: run the full CI pipeline locally (build → vet → tidy-check → lint → race tests)
ci: build vet tidy-check lint test-race

## example: build all example servers
example:
	go build -o bin/stdlibmux ./examples/api/stdlibmux
	go build -o bin/gin ./examples/api/gin
	go build -o bin/basic-authed-gin ./examples/api/basic-authed-gin
	go build -o bin/token-authed-gin ./examples/api/token-authed-gin

## run-stdlib: build and run the stdlib mux example
run-stdlib: example
	./bin/stdlibmux

## run-gin: build and run the gin example
run-gin: example
	./bin/gin

## run-basic-authed-gin: build and run the basic-auth gin example
run-basic-authed-gin: example
	./bin/basic-authed-gin

## run-token-authed-gin: build and run the api-token gin example
run-token-authed-gin: example
	./bin/token-authed-gin

## clean: remove build artifacts
clean:
	rm -rf bin coverage.out *.db stdlibmux gin
