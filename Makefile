BIN      := dmux
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)

.PHONY: build install test lint vet smoke clean

build:
	go build -ldflags '$(LDFLAGS)' -o bin/$(BIN) ./cmd/dmux

install:
	go install -ldflags '$(LDFLAGS)' ./cmd/dmux
	@bindir="$$(go env GOBIN)"; [ -n "$$bindir" ] || bindir="$$(go env GOPATH)/bin"; \
	echo "installed $$bindir/dmux"; \
	if command -v dmux >/dev/null 2>&1 && [ "$$(command -v dmux)" = "$$bindir/dmux" ]; then \
		echo "dmux is on PATH; run: dmux init && dmux doctor"; \
	elif command -v dmux >/dev/null 2>&1; then \
		echo "WARNING: 'dmux' on PATH is $$(command -v dmux), not the one just installed. Remove the stale copy or reorder PATH."; \
	else \
		echo "WARNING: $$bindir is not on PATH. Add it, then open a new terminal:"; \
		case "$$(basename "$${SHELL:-sh}")" in \
			zsh)  echo "  echo 'export PATH=\"$$bindir:\$$PATH\"' >> ~/.zshrc" ;; \
			fish) echo "  fish_add_path $$bindir" ;; \
			*)    echo "  echo 'export PATH=\"$$bindir:\$$PATH\"' >> ~/.bash_profile   # login shells read this, not ~/.bashrc" ;; \
		esac; \
	fi

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
