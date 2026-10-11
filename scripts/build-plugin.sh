#!/bin/sh
set -eu
mkdir -p bin
version=$(git describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null | sed 's/^v//' || echo dev)
go build -trimpath -ldflags "-s -w -X main.version=$version" -o bin/coai .
