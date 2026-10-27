# SwarmMind 构建与发布
# 常用：make build / make test / make release / make install-hub

GOCACHE ?= $(HOME)/.cache/go-build
export GOCACHE

BINARY_NAME := swarmmind
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)

# 发布矩阵单源：清单在 scripts/release.sh（Makefile release 与 CI 共用），勿在此手写 GOOS 构建行。

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
## 任一平台构建/gzip 失败立即整体失败（set -e），产物非空校验（[ -s ]）——
## 禁止「半数失败仍算成功」的静默残缺 embed（双审 2026-10-22）。
EMBED_DIR := internal/hub/api/embedded
NODE_TARGETS := linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64

embed-nodes:
	@set -e; mkdir -p $(EMBED_DIR); \
	for t in $(NODE_TARGETS); do \
	  os=$${t%%-*}; arch=$${t##*-}; ext=""; \
	  if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
	  GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o /tmp/swarmmind-node-embed$$ext ./cmd/node || exit 1; \
	  gzip -9 -c /tmp/swarmmind-node-embed$$ext > $(EMBED_DIR)/node-$$t.gz || exit 1; \
	  [ -s $(EMBED_DIR)/node-$$t.gz ] || { echo "embed 失败：node-$$t.gz 为空" >&2; exit 1; }; \
	  rm -f /tmp/swarmmind-node-embed$$ext; \
	  echo "embedded node-$$t.gz $$(du -h $(EMBED_DIR)/node-$$t.gz | cut -f1)"; \
	done

## release: 发布矩阵交叉构建到 dist/（清单单源于 scripts/release.sh，先跑 embed-nodes 注版本）
release: embed-nodes
	@bash scripts/release.sh dist
	@ls -la dist/

clean:
	rm -f hub swarmmind-node swarmmindctl
	rm -rf dist

## deploy-hub: 自行打包部署——git bundle 经 SSH 直传服务器，源码编译+重启（不走 Release 下载）
HUB_SSH   ?= ubuntu@1.13.158.180
HUB_KEY   ?= /Users/tl.m/Downloads/Mac2026.pem
HUB_SRC   ?= /opt/swarmmind-src

deploy-hub: embed-nodes
	@$(MAKE) -s release
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
