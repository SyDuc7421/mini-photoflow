#!/usr/bin/env bash
# Regenerates microservices/pb from microservices/proto. Needs only Go: buf and the
# protoc plugins are fetched by `go run` / `go install`, plugins go to a temp dir.
set -euo pipefail
cd "$(dirname "$0")/.."
GOBIN="$(mktemp -d)"
export GOBIN
export PATH="$GOBIN:$PATH"
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.2
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.4.0
go run github.com/bufbuild/buf/cmd/buf@v1.50.0 generate --template microservices/buf.gen.yaml microservices/proto
