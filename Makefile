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
	go build -ldflags '$(LDFLAGS)' -o swarmmind-mcp ./cmd/swarmmind-mcp

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

## embed-nodes: 五平台 node 预编译产物 gzip 到 embedded/（供 hub 打包秒出，无需现场编译）
EMBED_DIR := internal/hub/api/embedded
NODE_TARGETS := linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64

embed-nodes:
	@mkdir -p $(EMBED_DIR)
	@for t in $(NODE_TARGETS); do \
	  os=$${t%%-*}; arch=$${t##*-}; ext=""; \
	  [ "$$os" = "windows" ] && ext=".exe"; \
	  GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /tmp/swarmmind-node-embed$$ext ./cmd/node; \
	  gzip -9 -c /tmp/swarmmind-node-embed$$ext > $(EMBED_DIR)/node-$$t.gz; \
	  rm -f /tmp/swarmmind-node-embed$$ext; \
	  echo "embedded node-$$t.gz $$(du -h $(EMBED_DIR)/node-$$t.gz | cut -f1)"; \
	done

## release: 四平台交叉构建到 dist/（linux hub/node、macOS node、Windows node/ctl）
release: embed-nodes
	@mkdir -p dist
	GOOS=linux GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/swarmmind-hub-linux-amd64 ./cmd/hub
	GOOS=linux GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/swarmmind-node-linux-amd64 ./cmd/node
	GOOS=darwin GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o dist/swarmmind-node-macos-arm64 ./cmd/node
	GOOS=windows GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/swarmmind-node-windows-amd64.exe ./cmd/node
	GOOS=windows GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/swarmmindctl-windows-amd64.exe ./cmd/swarmmindctl
	GOOS=linux GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o dist/swarmmind-mcp-linux-amd64 ./cmd/swarmmind-mcp
	GOOS=darwin GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o dist/swarmmind-mcp-macos-arm64 ./cmd/swarmmind-mcp
	@ls -la dist/

clean:
	rm -f hub swarmmind-node swarmmindctl
	rm -rf dist

## deploy-hub: 自行打包部署——git bundle 经 SSH 直传服务器，源码编译+重启（不走 Release 下载）
HUB_SSH   ?= ubuntu@1.13.158.180
HUB_KEY   ?= /Users/tl.m/Downloads/Mac2026.pem
HUB_SRC   ?= /opt/swarmmind-src

deploy-hub: embed-nodes
	@$(MAKE) -s release GOOS=linux
	git bundle create /tmp/swarmmind.bundle main
	scp -i $(HUB_KEY) /tmp/swarmmind.bundle $(HUB_SSH):/tmp/swarmmind.bundle
	ssh -i $(HUB_KEY) $(HUB_SSH) 'mkdir -p /tmp/swarmmind-embedded'
	scp -q -i $(HUB_KEY) $(EMBED_DIR)/*.gz $(HUB_SSH):/tmp/swarmmind-embedded/
	ssh -i $(HUB_KEY) $(HUB_SSH) 'set -e; \
	  export PATH=$$PATH:/usr/local/go/bin GOPROXY=https://goproxy.cn,direct; \
	  git config --global --add safe.directory $(HUB_SRC) 2>/dev/null || true; \
	  if [ -d $(HUB_SRC)/.git ]; then \
	    sudo git -C $(HUB_SRC) fetch /tmp/swarmmind.bundle main; \
	    sudo git -C $(HUB_SRC) checkout -q --detach FETCH_HEAD; \
	  else \
	    sudo git clone -q /tmp/swarmmind.bundle $(HUB_SRC); \
	  fi; \
	  cd $(HUB_SRC) && echo "构建自: $$(git log --oneline -1)"; \
	  sudo mkdir -p $(HUB_SRC)/internal/hub/api/embedded; \
	  sudo cp /tmp/swarmmind-embedded/*.gz $(HUB_SRC)/internal/hub/api/embedded/ 2>/dev/null || echo "（无预编译产物，enroll 将走现场编译兑底）"; \
	  sudo /usr/local/go/bin/go build -o /opt/swarmmind/hub.new ./cmd/hub; \
	  sudo systemctl stop swarmmind-hub; \
	  sudo mv /opt/swarmmind/hub.new /opt/swarmmind/hub && sudo chmod +x /opt/swarmmind/hub; \
	  sudo systemctl start swarmmind-hub; sleep 2; \
	  systemctl is-active swarmmind-hub; \
	  curl -s -o /dev/null -w "控制台: %{http_code}\n" http://127.0.0.1:8444/'
