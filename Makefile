# Copyright 2023 Sean Sullivan.
# SPDX-License-Identifier: MIT

GOPATH := $(shell go env GOPATH)
MYGOBIN := $(shell go env GOPATH)/bin
SHELL := /bin/bash
export PATH := $(MYGOBIN):$(PATH)

.PHONY: build
build:
	go build -o bin/ws-server cmd/server/ws-server.go
	go build -o bin/ws-client cmd/client/ws-client.go

.PHONY: install
install:
	go install cmd/server/ws-server.go
	go install cmd/client/ws-client.go

"$(MYGOBIN)/stringer":
	go install golang.org/x/tools/cmd/stringer@v0.1.10

"$(MYGOBIN)/addlicense":
	go install github.com/google/addlicense@v1.1.0

"$(MYGOBIN)/golangci-lint":
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.50.1

"$(MYGOBIN)/ginkgo":
	go install github.com/onsi/ginkgo/v2/ginkgo@v2.5.1

.PHONY: clean
clean:
	rm -f bin/ws-server
	rm -f bin/ws-client

.PHONY: test
test:
	go test -race -cover ./cmd/... ./pkg/...

.PHONY: fmt
fmt:
	go fmt ./...

.PHONY: lint
lint: "$(MYGOBIN)/golangci-lint"
	"$(MYGOBIN)/golangci-lint" run ./...

.PHONY: fix
fix:
	go fix ./...

.PHONY: generate
generate: "$(MYGOBIN)/stringer"
	go generate ./...

.PHONY: license
license: "$(MYGOBIN)/addlicense"
	"$(MYGOBIN)/addlicense" -v -y 2023 -c "Sean Sullivan." -s=only .

.PHONY: verify-license
verify-license: "$(MYGOBIN)/addlicense"
	"$(MYGOBIN)/addlicense" -check .

.PHONY: tidy
tidy:
	go mod tidy

.PHONY: vet
vet:
	go vet ./...
