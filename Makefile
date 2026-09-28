BIN      := dmux
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)

.PHONY: build install test lint vet smoke clean

build:
	go build -ldflags '$(LDFLAGS)' -o bin/$(BIN) ./cmd/dmux

install:
	go install -ldflags '$(LDFLAGS)' ./cmd/dmux

test:
	go test -race ./...

vet:
	go vet ./...

lint: vet
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed; ran go vet only"; \
	fi

# Opt-in end-to-end tests against the real Devin CLI (launches paid sessions).
smoke: build
	DMUX_SMOKE=1 go test -tags smoke -count=1 ./test/smoke/...

clean:
	rm -rf bin
