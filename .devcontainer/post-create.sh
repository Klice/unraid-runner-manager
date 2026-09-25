#!/usr/bin/env bash
set -euo pipefail

sudo chown -R vscode:vscode /go/pkg/mod
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sudo sh -s -- -b /usr/local/bin
npm install -g --allow-scripts=@anthropic-ai/claude-code @anthropic-ai/claude-code
go mod download
