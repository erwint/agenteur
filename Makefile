BINARY := coai
VERSION := $(shell git describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null | sed 's/^v//')
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test lint release

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

lint:
	go vet ./...

release: test lint
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .
