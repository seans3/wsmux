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
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

"$(MYGOBIN)/ginkgo":
	go install github.com/onsi/ginkgo/v2/ginkgo@v2.5.1

.PHONY: clean
clean:
	rm -rf bin
	rm -f coverage.out

.PHONY: test
test:
	go test -race -cover ./pkg/...

.PHONY: test-long
test-long:
	go test -v -race -tags=long ./pkg/multiplex/...

.PHONY: test-e2e
test-e2e:
	@echo "No e2e tests implemented yet."

.PHONY: stress
stress:
	go test -v -race -timeout 5m -tags=stress ./pkg/multiplex/...

.PHONY: test-all
test-all: test test-long stress test-e2e

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
