# SwarmMind 构建与发布
# 常用：make build / make test / make release / make install-hub

GOCACHE ?= $(HOME)/.cache/go-build
export GOCACHE

BINARY_NAME := swarmmind
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)

PLATFORMS := linux/amd64 linux/arm64 darwin/arm64 windows/amd64

.PHONY: build test race vet fmt release clean

build:
	go build -ldflags '$(LDFLAGS)' -o hub ./cmd/hub
	go build -ldflags '$(LDFLAGS)' -o swarmmind-node ./cmd/node
	go build -ldflags '$(LDFLAGS)' -o swarmmindctl ./cmd/swarmmindctl

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

## release: 四平台交叉构建到 dist/（linux hub/node、macOS node、Windows node/ctl）
release:
	@mkdir -p dist
	GOOS=linux GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/swarmmind-hub-linux-amd64 ./cmd/hub
	GOOS=linux GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/swarmmind-node-linux-amd64 ./cmd/node
	GOOS=darwin GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o dist/swarmmind-node-macos-arm64 ./cmd/node
	GOOS=windows GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/swarmmind-node-windows-amd64.exe ./cmd/node
	GOOS=windows GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/swarmmindctl-windows-amd64.exe ./cmd/swarmmindctl
	@ls -la dist/

clean:
	rm -f hub swarmmind-node swarmmindctl
	rm -rf dist
