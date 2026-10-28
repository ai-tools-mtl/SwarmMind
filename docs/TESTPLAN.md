# SwarmMind 功能测试文档

## Logo 接入专项（2026-10-26）

测试使用本轮重新构建的 hub/node 二进制、独立临时配置和空闲端口。独立执行者按下表验证，记录到 `docs/archive/TESTREPORT-logo.md`。用户既有通知界面等改动保留；不提交、不推送、不操作现有部署。

### 双端形态对照

| 位置 | hub | node | 一致要求 |
|---|---|---|---|
| 顶部品牌位 | 图标 + SwarmMind + 中心端 | 图标 + SwarmMind + 本机 | 同图、30×30、同间距；端角色保留 |
| 登录页 | 64×64 图标 + 管理员登录 | 64×64 图标 + 本机令牌登录 | 同图、同尺寸；不改表单字段/逻辑 |
| 侧栏 | 24×24 图标 + 中心端名称 | 24×24 图标 + 节点名称 | 同组件形态和位置，名称按角色 |
| 页签 | `/favicon.svg` | `/favicon.svg` | 相同 SVG；免登录可达 |

| ID | 操作 | 通过条件 |
|---|---|---|
| LOGO-01 | 检查 assets/brand SVG/PNG、字标路径及脚本 | 四模块图形/配色延续已批准方案；SVG 无外部引用/字体依赖，PNG 透明版含 alpha；文件未被 Git 忽略 |
| LOGO-02 | `python3 scripts/build-brand.py` 前后比较 SVG 内容；核对 README picture 与文件 | 导出确定性；明暗横幅可预览；GitHub 版与系统同一图形 |
| LOGO-03 | 实际请求重建后两端 `/logo.svg`、`/favicon.svg` | 均200、image/svg+xml、同字节、无认证、无配置/凭据；未知资源404 |
| LOGO-04 | Playwright 等登录视图就绪后检查登录与登录后页面 | 顶部/登录/侧栏图标 naturalWidth>0，favicon可取；双端对比截图；32/24像素图标可辨，窄屏无横向溢出 |
| LOGO-05 | 验证登录/导航及共享展示测试 | 登录与页面跳转仍可用；`go test ./internal/node/localapi/ -run 'TestWebMdRender|TestWebDrawer'` 通过；不改既有通知行为 |
| LOGO-06 | `go build ./...`、`go test ./...`、回环演示 | 构建和测试通过；DoD1–11逐项列出证据与未执行边界；不以静态截图代替运行接入 |

只做资源整理和界面接入，不在本轮更改任务协议、权限、通知配置或部署状态。PNG 是由 SVG 渲染的交付文件，原概念提案板不当作运行 Logo。

## 文档内容修订专项（2026-10-26）

此专项验证文档修订；基础用例中的工具发现清单随当前接口同步更新。独立执行者从当前文件读取需求，逐条记录到 `docs/archive/TESTREPORT-docs-20261026.md`，不宣称外部双机或 Windows 真机已验收。

| ID | 操作 | 通过条件 |
|---|---|---|
| DOC-01 | 检查 README、ARCHITECTURE、OPERATIONS、docs/README 中本地 Markdown 链接和图片路径 | 文件存在；架构 SVG 和 Mermaid 均非被忽略文件 |
| DOC-02 | 对照 cmd 入口、configs、Makefile 检查 README 构建、最小配置和 CLI 命令 | 输出名、参数、TLS/URL、独立 data_dir 和令牌用途一致；隔离实例手动派发 echo 返回 hello |
| DOC-03 | 构建 MCP 到临时目录，向 stdio 发送 tools/list，分别设 SWARM_TARGET=hub / node | hub 19 工具（含 Work 五个与新建议工具）、node 六个，与 README 一致 |
| DOC-04 | XML 解析 SVG，实际渲染检查文字、连线、节点和方向；对照 architecture.mmd 与架构详解 | 三节点主动连 hub，入口分离，无截断/重叠；Mermaid 可解析 |
| DOC-05 | 对照 protocol/constants.go、鉴权、inputs 与 Work 实现核对文档 | 容量、保留期、凭据、产物存放与 P5 开发边界有代码依据 |
| DOC-06 | `go build -mod=readonly ./...` 与 `go test ./...` | 均退出 0；保留命令与日志证据 |
| DOC-07 | `./scripts/demo-loopback.sh` | 退出 0，DoD 3–10 输出 PASS；说明 DoD 6/8 注入子项由单测补充 |
| DOC-08 | 逐条对照 SPEC 第七节 11 项 | 逐项记录证据；DoD 11 未执行须明示，不用历史报告冒充通过 |
| DOC-09 | 对照源码与报告核对 Work、D1、node 分页/清理、群聊、旧 Prime 模板 | 明确实现/验证/发布边界；无虚构的本地部署、概率校准或自动清理保证 |
| DOC-10 | 检查本轮主要文档术语、历史记录与代码差异 | 当前说明无口号式表述；历史结论不冒充新验证；除配置注释和文档外不改运行代码；Prime 行为节同步锁通过 |

本轮验证仅针对文档改动与当前本机回归。D1 真实模型、W4 远程故障、Windows 服务真机及观察期不在本轮执行；报告必须逐项列为未执行或引用带边界的历史事实。

所有验证使用隔离临时实例与空闲端口，不触碰已有部署。手动首次派单用临时目录替代示例 data_dir，其余保持 README 最小配置语义。

## 执行说明

### 测试范围与判定原则

以下为基础用例与后续增补。Work、任务可见性和 D1 建议服务还需各自专项，不能把本文件基础回归当作全部功能验收。覆盖：

- 中心端管理面 REST API、账户、密码登录、PAT、任务、产物、核对记录、控制批次、目录与节点。
- Web 控制台：hub 八页（总览、机器、任务、派发、工作单、群聊、策略容量、设置），node 四页（总览、服务、任务、连接）。登录作为独立交互入口检查。
- MCP stdio 基础执行工具与 hub 当前工具发现；新增工具行为见专项。
- node 本地 API、跨节点派单、离线信箱、幂等、断线恢复、`needs_review`。
- SPEC DoD 十一项、工作目录互斥、持久化与协议边界。

依据为 `SPEC.md` 第六、六.五、六.六及第七节 DoD，结合 `docs/INTERACTION.md`、`docs/PLAN.md`、`README.md`。接口路径、配置字段和页面路由按当前代码确定。

**本文是测试规程，不代表已执行或已通过。** 规范与实现不一致时记录失败，不能修改断言使其通过。

### 环境要求

全量测试包含账户初始化、节点终止、hub 重启和信箱压测，须使用**专用验收实例**。管理面固定：

```text
http://127.0.0.1:8444
```

验收实例要求：

1. 使用当前仓库构建的 hub，启动配置为 `hub.yaml`。
2. `admin_token` 在 `hub.yaml` 中显式配置且非空。
3. `nodes` 至少登记两个专用测试身份；本文默认选配置中的前两个。它们不能同时被其他 node 使用。
4. 初始 hub 数据目录为空，账户尚未初始化。
5. 本机 `8445`、`8446` 端口空闲。
6. Unix/macOS 环境；具备 Go、bash、curl、jq、python3、PyYAML、Node.js、npm、lsof。
7. 跨云用例另需两台真实机器、两种真实 coding agent，以及可用的 SSH 接入。

进入仓库并在同一个 **bash** 会话执行后续命令：

```bash
bash
set -Eeuo pipefail
umask 077

export REPO="$PWD"
export HUB="http://127.0.0.1:8444"
export HUB_CONFIG="${HUB_CONFIG:-$REPO/hub.yaml}"
export T="$(mktemp -d /tmp/swarmmind-test.XXXXXX)"
export PYTHON="$(command -v python3)"
export ID="PREP"

command -v go curl jq node npm lsof >/dev/null
"$PYTHON" -c 'import yaml'
test -f "$HUB_CONFIG"

export ADMIN_TOKEN="$("$PYTHON" - "$HUB_CONFIG" <<'PY'
import sys, yaml
c = yaml.safe_load(open(sys.argv[1]))
assert isinstance(c.get("admin_token"), str) and c["admin_token"]
print(c["admin_token"])
PY
)"

"$PYTHON" - "$HUB_CONFIG" "$T" <<'PY'
import json, pathlib, sys, yaml
cfg = yaml.safe_load(open(sys.argv[1]))
pairs = list(cfg["nodes"].items())
assert len(pairs) >= 2, "hub.yaml 至少需要两个专用测试节点"
root = pathlib.Path(sys.argv[2])
for tag, (nid, token), port in zip(("a", "b"), pairs[:2], (8445, 8446)):
    d = root / tag
    for sub in ("data", "w", "other", "home"):
        (d / sub).mkdir(parents=True, exist_ok=True)
    def profile(name, command, args=True, timeout=120, workdir="w", s2a=False):
        return {
            "name": name, "kind": "cli", "command": command,
            "workdir": str(d / workdir), "config_home": str(d / "home"),
            "agent_args": [], "timeout_sec": timeout,
            "allow_args": args, "allow_env": ["TMPDIR"],
            "stdin_to_arg": s2a,
            "description": "SwarmMind 功能验收专用服务",
            "permission_mode": "test-fixture", "exposed": True,
        }
    insecure = cfg.get("tls", {}).get("insecure_loopback", False)
    nc = {
        "node_id": nid,
        "register_token": token,
        "hub_url": ("ws" if insecure else "wss") + "://127.0.0.1:8443/ws/node",
        "hub_pubkey_fp": "",
        "local_listen": f"127.0.0.1:{port}",
        "local_token": "",
        "concurrency": 2, "data_dir": str(d / "data"),
        "base_env": {"PATH": "/usr/bin:/bin", "TMPDIR": "/tmp", "LANG": "C"},
        "agents": [
            profile("qa-echo", "/bin/echo"),
            profile("qa-sh", "/bin/sh"),
            profile("qa-cat", "/bin/cat", args=False),
            profile("qa-timeout", "/bin/sh", timeout=1),
            profile("qa-other", "/bin/sh", workdir="other"),
            profile("qa-conv", "/bin/echo", s2a=True),
        ],
    }
    (d / "node.yaml").write_text(yaml.safe_dump(nc))
    (root / f"{tag}.id").write_text(nid)
(root / "meta.json").write_text(json.dumps({"config": str(pathlib.Path(sys.argv[1]).resolve())}))
PY

export N1="$(cat "$T/a.id")"
export N2="$(cat "$T/b.id")"
export NODE_A="http://127.0.0.1:8445"
export NODE_B="http://127.0.0.1:8446"
export COOKIE="$T/hub.cookies"
export SWARM_TEST_SECRET="SHOULD_NOT_REACH_RUNNER"

go build -mod=readonly -o "$T/hub" ./cmd/hub
go build -mod=readonly -o "$T/node" ./cmd/node
go build -mod=readonly -o "$T/mcp" ./cmd/swarmmind-mcp
go build -mod=readonly -o "$T/ctl" ./cmd/swarmmindctl

export HUB_PID="$(lsof -nP -t -iTCP:8444 -sTCP:LISTEN | sort -u)"
test -n "$HUB_PID"
test "$(printf '%s\n' "$HUB_PID" | wc -l | tr -d ' ')" = 1
```

若验收 hub 使用非默认 node 入口地址，应在启动测试节点前将两份临时 `node.yaml` 的 `hub_url` 调整为实际 WSS 地址；管理面仍固定为 `127.0.0.1:8444`。不得把 `api_listen` 当作 node 本地监听字段，node 当前字段为 `local_listen`。

### 公共执行函数

以下函数将确切 HTTP 状态码、响应头和响应体保存到 `$T`。`api` 的最后一个参数指定认证方式：`cookie`、`none`、`pat`、`bad`、`root`、`local-a`、`local-b`。

```bash
begin() {
  export ID="$1"
  printf 'RUN %s\n' "$ID"
}
pass() {
  printf 'PASS %s\n' "$ID" | tee -a "$T/results.log"
}
trap 'printf "FAIL %s line=%s\n" "$ID" "$LINENO" | tee -a "$T/results.log"' ERR

api() {
  local expected="$1" method="$2" path="$3" body="${4:-}"
  local auth="${5:-cookie}" base="$HUB" code
  local opts=()
  case "$auth" in
    cookie) opts+=(-b "$COOKIE" -c "$COOKIE");;
    none) ;;
    pat) opts+=(-H "Authorization: Bearer $PAT");;
    bad) opts+=(-H "Authorization: Bearer definitely-invalid");;
    root) opts+=(-H "Authorization: Bearer $ADMIN_TOKEN");;
    local-a) base="$NODE_A"; opts+=(-H "Authorization: Bearer $LOCAL_A");;
    local-b) base="$NODE_B"; opts+=(-H "Authorization: Bearer $LOCAL_B");;
    *) return 2;;
  esac
  if test -n "$body"; then
    opts+=(-H 'Content-Type: application/json')
    if [[ "$body" == @* ]]; then
      opts+=(--data-binary "$body")
    else
      opts+=(--data-binary "$body")
    fi
  fi
  code="$(curl --silent --show-error --max-time 40 \
    -X "$method" "${opts[@]}" \
    -D "$T/$ID.headers" -o "$T/$ID.body" -w '%{http_code}' "$base$path")"
  printf '%s\n' "$code" > "$T/$ID.code"
  test "$code" = "$expected"
}
check() {
  jq -e "$1" "$T/$ID.body"
}
login() {
  api 200 POST /api/v1/login \
    "$(jq -nc --arg token "$ADMIN_TOKEN" '{token:$token}')" none
  # api 的 none 模式不保存 Cookie，显式保存。
  curl -fsS --max-time 10 -c "$COOKIE" \
    -H 'Content-Type: application/json' \
    --data "$(jq -nc --arg token "$ADMIN_TOKEN" '{token:$token}')" \
    "$HUB/api/v1/login" > "$T/login.json"
}
request() {
  local node="$1" agent="$2" stdin="${3:-}" args="${4:-[]}" env="${5:-}"
  [[ -n "$env" ]] || env='{}'   # 默认空对象须走空串+显式赋值（bash 会把 ${5:-{}} 解析为默认值 '{' + 字面 '}'：显式传 {} 时实得 '{}}'，jq 拒绝）
  local key="${6:-}"
  jq -nc --arg n "$node" --arg a "$agent" --arg s "$stdin" \
    --argjson args "$args" --argjson env "$env" --arg k "$key" \
    '{target_node:$n,target_agent:$a,input:{stdin:$s,args:$args,env:$env},
      submitted_by:"forged-label",idem_key:$k}'
}
submit() {
  api 202 POST /api/v1/tasks "$(request "$@")"
  jq -er '.task_id' "$T/$ID.body"
}
wait_task() {
  # limit 与 stop 必须分两条 local 声明：同一命令的全部词先于 local 赋值展开，
  # 同行引用 limit 在 set -u 下报 unbound variable。
  local tid="$1" wanted="$2" limit="${3:-30}"
  local stop=$((SECONDS+limit))
  while (( SECONDS < stop )); do
    api 200 GET "/api/v1/tasks/$tid"
    cp "$T/$ID.body" "$T/task.$tid.json"
    if jq -e --arg s "$wanted" '.status==$s' "$T/$ID.body" >/dev/null; then
      return 0
    fi
    if jq -e '.status=="completed" or .status=="failed" or .status=="needs_review"' \
      "$T/$ID.body" >/dev/null; then
      return 1
    fi
    sleep 0.2
  done
  return 1
}
wait_node() {
  local nid="$1" online="$2" stop=$((SECONDS+90))
  while (( SECONDS < stop )); do
    if curl -fsS --max-time 3 -b "$COOKIE" "$HUB/api/v1/nodes" \
      > "$T/nodes.poll.json"; then
      if jq -e --arg n "$nid" --argjson o "$online" \
        'any(.nodes[]; .node_id==$n and .online==$o)' \
        "$T/nodes.poll.json" >/dev/null; then return 0; fi
    fi
    sleep 0.5
  done
  return 1
}
wait_file() {
  local file="$1" stop=$((SECONDS+15))
  while (( SECONDS < stop )); do
    if test -s "$file"; then return 0; fi
    sleep 0.1
  done
  return 1
}
start_a() {
  "$T/node" --config "$T/a/node.yaml" >>"$T/a.log" 2>&1 &
  export A_PID=$!
}
start_b() {
  "$T/node" --config "$T/b/node.yaml" >>"$T/b.log" 2>&1 &
  export B_PID=$!
}
stop_a() { kill -TERM "$A_PID"; wait "$A_PID"; }
stop_b() { kill -TERM "$B_PID"; wait "$B_PID"; }
start_hub() {
  "$T/hub" --config "$HUB_CONFIG" >>"$T/hub.log" 2>&1 &
  export HUB_PID=$!
  local stop=$((SECONDS+20))
  while (( SECONDS < stop )); do
    if curl -fsS --max-time 2 "$HUB/" >/dev/null; then return 0; fi
    sleep 0.2
  done
  return 1
}
stop_hub() {
  kill -TERM "$HUB_PID"
  local stop=$((SECONDS+15))
  while kill -0 "$HUB_PID" 2>/dev/null; do
    (( SECONDS < stop )) || return 1
    sleep 0.2
  done
}
mcp() {
  local method="$1" params="${2:-}"
  [[ -n "$params" ]] || params='{}'   # 默认空对象写法同 request（${2:-{}} 显式传参会得 '{...}}'）
  {
    printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"functional-test","version":"1"}}}'
    printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized"}'
    jq -nc --arg m "$method" --argjson p "$params" \
      '{jsonrpc:"2.0",id:2,method:$m,params:$p}'
  } | SWARM_HUB_URL="$HUB" SWARM_HUB_TOKEN="${MCP_TOKEN:-$PAT}" \
      "$T/mcp" > "$T/$ID.rpc"
  jq -ce 'select(.id==2)' "$T/$ID.rpc" > "$T/$ID.mcp"
}
tool() {
  local args="${2:-}"
  [[ -n "$args" ]] || args='{}'
  mcp tools/call "$(jq -nc --arg n "$1" --argjson a "$args" \
    '{name:$n,arguments:$a}')"
}
mcheck() { jq -e "$1" "$T/$ID.mcp"; }
```

运行 TC-02 后启动测试节点：

```bash
start_a
start_b
wait_node "$N1" true
wait_node "$N2" true
export LOCAL_A="$(cat "$T/a/data/local_token")"
export LOCAL_B="$(cat "$T/b/data/local_token")"
```

### 浏览器执行器

Web 用例操作真实页面、真实 DOM 与真实后端。故障注入只拦截指定请求的响应，不伪造成功数据。

安装浏览器依赖到临时目录：

```bash
npm install --prefix "$T/browser" --no-audit --no-fund @playwright/test
"$T/browser/node_modules/.bin/playwright" install chromium
```

创建执行器：

```bash
cat > "$T/browser/ui.cjs" <<'JS'
const { chromium, expect } = require('@playwright/test');
const fs = require('node:fs');
const assert = require('node:assert/strict');

(async () => {
  const [side, route, code] = process.argv.slice(2);
  const E = process.env;
  const browser = await chromium.launch({ headless: true });
  const base = side === 'node' ? E.NODE_A : E.HUB;
  const context = await browser.newContext({
    permissions: ['clipboard-read', 'clipboard-write'],
    acceptDownloads: true
  });
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  const errors = [];
  page.on('pageerror', e => errors.push(e.message));
  await context.tracing.start({ screenshots: true, snapshots: true });

  try {
    if (side === 'node') {
      await context.addInitScript(token =>
        localStorage.setItem('swarmmind_node_token', token), E.LOCAL_A);
    } else if (side === 'hub') {
      const r = await context.request.post(E.HUB + '/api/v1/login', {
        data: { token: E.ADMIN_TOKEN }
      });
      assert.equal(r.status(), 200);
    }
    await page.goto(base + '/#/' + route);
    if (side !== 'guest') await expect(page.locator('h1')).toBeVisible();

    const run = new (Object.getPrototypeOf(async function(){}).constructor)(
      'page','context','expect','assert','E','fs', code
    );
    await run(page, context, expect, assert, E, fs);
    assert.deepEqual(errors, []);
    console.log('UI_OK ' + E.ID);
  } catch (e) {
    await page.screenshot({ path: E.T + '/' + E.ID + '.png', fullPage: true });
    throw e;
  } finally {
    await context.tracing.stop({ path: E.T + '/' + E.ID + '.trace.zip' });
    await browser.close();
  }
})().catch(e => { console.error(e); process.exit(1); });
JS

ui() {
  node "$T/browser/ui.cjs" "$1" "$2" "$3" \
    > "$T/$ID.ui.log" 2>&1
  grep -Fx "UI_OK $ID" "$T/$ID.ui.log"
}
```

### 执行顺序、全绿与失败报告

按编号顺序执行。每条步骤须包在对应 `begin` 与 `pass` 之间，例如：

```bash
begin TC-10
# 执行 TC-10 步骤
pass
```

用例使用同一 bash 会话中的变量。不得在未完成前置用例时单独执行依赖用例。

- **PASS**：命令全部退出 0，HTTP 状态、JSON、内容或页面断言全部满足。
- **FAIL**：任一断言失败、超时、异常退出、重复执行或语义不符。
- **BLOCKED**：环境、真实云节点或真实 agent 未就绪。不得记作 PASS。
- **全绿**：55 个不同编号全部 PASS，且无 FAIL/BLOCKED；跨云验收必须有真实双机证据。

最终核验：

```bash
test "$(awk '$1=="PASS"{print $2}' "$T/results.log" | sort -u | wc -l | tr -d ' ')" = 55
! grep -Eq '^(FAIL|BLOCKED) ' "$T/results.log"
printf 'ALL_GREEN 55/55\n'
```

失败报告使用以下模板，令牌、Cookie、密码必须脱敏：

```markdown
- 用例编号：
- 分级：必须修 / 建议修 / 可不修
- Git commit：
- 环境：OS、Go 版本、hub/node 版本、节点 ID
- 失败命令：
- 期望：
- 实际：HTTP 状态、error/status、断言错误
- 证据：响应文件、task_id、trace_id、batch_id、日志、截图/trace
- 定位：仓库文件:行号
- 复现次数：
```

## 一、构建、鉴权与账户

表中步骤中的 `api`、`submit`、`tool`、`ui` 均使用上方完整定义；每条执行前后分别调用 `begin 编号`、`pass`。

| 编号 | 模块 | 前置条件 | 步骤 | 期望结果 | 优先级 |
|---|---|---|---|---|---|
| TC-01 | 构建与全量测试 | 仓库依赖缓存可用 | `go build -mod=readonly ./... >"$T/build.log" 2>&1`；`go test -mod=readonly -count=1 -timeout=15m ./... >"$T/test.log" 2>&1`；`test -s go.sum`；`grep -Eq '^ok[[:space:]]' "$T/test.log"`；`! grep -Eq '^FAIL' "$T/test.log"` | build/test 均退出 0；测试确实执行，不能只有 `[no test files]`；不以历史声明代替复跑 | P0 冒烟 |
| TC-02 | 管理令牌登录 | 真实 hub 已启动；令牌从 `hub.yaml` 读取 | `login`；`check '.ok==true and .expires_in==86400'`；`grep -Ei '^Set-Cookie: swarmmind_session=.*HttpOnly' "$T/$ID.headers"`；`api 200 GET /api/v1/nodes`；随后执行上方“启动测试节点”命令 | 返回有效 HttpOnly 会话 Cookie；Cookie 能访问受保护接口；两节点上线 | P0 冒烟 |
| TC-03 | 登录与鉴权反向 | TC-02 | `api 401 POST /api/v1/login '{"token":"wrong"}' none`；`check '.error=="auth_failed"'`；`api 401 GET /api/v1/tasks '' none`；`check '.error=="unauthorized"'`；`api 401 GET /api/v1/nodes '' bad`；`check '.error=="unauthorized"'` | 错误登录令牌、缺失认证、错误 Bearer 均拒绝；不能返回业务数据 | P0 冒烟 |
| TC-04 | 首次账户初始化 | 专用 hub 尚无账户 | `api 200 GET /api/v1/auth/account`；`check '.initialized==false'`；`api 400 POST /api/v1/auth/setup '{"username":"x","password":"short"}' none`；`check '.error=="invalid_request"'`；`api 201 POST /api/v1/auth/setup '{"username":"qa_admin","password":"Qa_test_2026!"}' none`；`check '.ok==true'`；`api 200 GET /api/v1/auth/account`；`check '.initialized==true and .username=="qa_admin" and (has("password")\|not) and (has("salt")\|not)'` | 非法用户名/短密码拒绝；有效账户创建成功；查询不泄漏凭据 | P1 核心 |
| TC-05 | 密码登录与重复初始化 | TC-04 | `api 401 POST /api/v1/auth/login-password '{"username":"qa_admin","password":"wrong"}' none`；`check '.error=="auth_failed"'`；`api 200 POST /api/v1/auth/login-password '{"username":"qa_admin","password":"Qa_test_2026!"}' none`；`check '.ok==true'`；`grep -Ei '^Set-Cookie: swarmmind_session=.*HttpOnly' "$T/$ID.headers"`；`api 409 POST /api/v1/auth/setup '{"username":"other_admin","password":"Other_pass_123!"}' none`；`check '.error=="already_initialized"'` | 密码登录发放会话；错误密码失败；已有账户不能被无 root 凭据覆盖 | P0 冒烟 |
| TC-06 | 修改账户与 root 恢复 | TC-05 | 依次执行下方“账户修改与恢复命令” | 旧密码验证生效；新凭据生效；root 可恢复；最终恢复为基线用户名/密码 | P1 核心 |
| TC-07 | PAT 创建、使用与脱敏 | 已登录 | `api 201 POST /api/v1/auth/tokens '{"name":"functional-test-main"}'`；`export PAT="$(jq -er .token "$T/$ID.body")"`；`export PAT_ID="$(jq -er .id "$T/$ID.body")"`；`printf '%s' "$PAT" \| grep -Eq '^smsw_[A-Za-z0-9_-]{43}$'`；`api 200 GET /api/v1/nodes '' pat`；`api 200 GET /api/v1/auth/tokens`；`check --arg` 不适用，改执行：`jq -e --arg id "$PAT_ID" 'any(.tokens[];.id==$id and .revoked==false and .hash=="") and all(.tokens[];has("token")\|not)' "$T/$ID.body"`；`! grep -Fq "$PAT" "$T/$ID.body"` | PAT 明文仅创建时返回；可作 Bearer；列表不回明文或有效哈希；PAT 为完整管理权限，名称不构成只读授权 | P0 冒烟 |
| TC-08 | PAT 撤销及非法创建 | TC-07；保留主 PAT | `api 400 POST /api/v1/auth/tokens '{"name":" "}'`；`api 201 POST /api/v1/auth/tokens '{"name":"to-revoke"}'`；`R_ID="$(jq -er .id "$T/$ID.body")"`；`R_TOKEN="$(jq -er .token "$T/$ID.body")"`；`api 200 DELETE "/api/v1/auth/tokens/$R_ID"`；`SAVED_PAT="$PAT"`；`PAT="$R_TOKEN"`；`api 401 GET /api/v1/nodes '' pat`；`PAT="$SAVED_PAT"`；`api 404 DELETE "/api/v1/auth/tokens/$R_ID"`；`check '.error=="token_not_found"'` | 空名称拒绝；撤销立即生效；重复撤销 404；原会话及其他 PAT 仍可用 | P1 核心 |

账户修改与恢复命令：

```bash
api 400 POST /api/v1/auth/account/update \
  '{"current_password":"wrong","new_username":"qa_changed"}'
check '.error=="invalid_request"'

api 400 POST /api/v1/auth/account/update \
  '{"current_password":"Qa_test_2026!"}'
check '.error=="invalid_request"'

api 200 POST /api/v1/auth/account/update \
  '{"current_password":"Qa_test_2026!","new_username":"qa_changed","new_password":"Qa_changed_2026!"}'
check '.ok==true and .account.username=="qa_changed"'

api 401 POST /api/v1/auth/login-password \
  '{"username":"qa_admin","password":"Qa_test_2026!"}' none
api 200 POST /api/v1/auth/login-password \
  '{"username":"qa_changed","password":"Qa_changed_2026!"}' none

code="$(curl -sS --max-time 10 -o "$T/$ID.body" -w '%{http_code}' \
  -H 'Content-Type: application/json' -H "X-Admin-Token: $ADMIN_TOKEN" \
  --data '{"username":"qa_admin","password":"Qa_test_2026!"}' \
  "$HUB/api/v1/auth/setup")"
test "$code" = 200
check '.ok==true and .reset_by=="admin_token"'

api 200 POST /api/v1/auth/login-password \
  '{"username":"qa_admin","password":"Qa_test_2026!"}' none
login
```

## 二、目录、任务、执行与产物

| 编号 | 模块 | 前置条件 | 步骤 | 期望结果 | 优先级 |
|---|---|---|---|---|---|
| TC-09 | 节点、能力与目录 | 两测试节点在线 | `api 200 GET /api/v1/nodes`；`jq -e --arg a "$N1" --arg b "$N2" 'any(.nodes[];.node_id==$a and .online and .epoch>=1) and any(.nodes[];.node_id==$b and .online) and all(.nodes[];.inflight>=0 and .inflight<=8)' "$T/$ID.body"`；`api 200 GET /api/v1/agents`；`check '.agents\|length>0'`；`! grep -Eq '"(command\|workdir\|agent_args\|timeout_sec\|allow_env\|config_home)"[[:space:]]*:' "$T/$ID.body"`；`check 'all(.agents[];(.card.allow_args\|type=="boolean") and (.card.stdin_to_arg\|type=="boolean"))'`；`api 200 GET /api/v1/directory`；`check 'all(.directory[];.card.exposed==true and .card.input_mode=="prompt_text" and .card.output_mode=="answer+artifacts" and .card.interactive==false and (.card.allow_args\|type=="boolean") and (.card.stdin_to_arg\|type=="boolean"))'` | 节点在线/epoch/窗口字段正确；目录只含已暴露 AgentCard；本机执行细节不出本机（禁键集=command/workdir/agent_args/timeout_sec/allow_env/config_home，与 internal/node/service/card_input_semantics_test.go 泄漏名单一致；输入语义声明 allow_args/stdin_to_arg 必须在场且为布尔，SPEC 六.五投影口径） | P0 冒烟 |
| TC-10 | REST 派单完整链路 | TC-09 | `export ECHO="$(submit "$N1" qa-echo '' '["SWARM_ECHO_OK"]' '{}' 'echo-main')"`；`wait_task "$ECHO" completed 5`；`check '.result.status=="completed" and .result.exit_code==0 and .result.final_answer=="SWARM_ECHO_OK\n" and .submitted_by=="admin" and (.trace_id\|length)>0'` | 202 后 5 秒内完成；FinalAnswer 正确；自报身份被覆盖为 admin；可获得 trace_id | P0 冒烟 |
| TC-11 | 任务幂等与冲突 | TC-10 | `api 200 POST /api/v1/tasks "$(request "$N1" qa-echo '' '["SWARM_ECHO_OK"]' '{}' echo-main)"`；`jq -e --arg id "$ECHO" '.dup==true and .task_id==$id' "$T/$ID.body"`；`api 409 POST /api/v1/tasks "$(request "$N1" qa-echo '' '["CHANGED"]' '{}' echo-main)"`；`check '.error=="conflict"'` | 同键同内容返回原 ID；同键不同内容 409；不新建执行 | P0 冒烟 |
| TC-12 | 任务列表与不存在 ID | TC-10 | `api 200 GET /api/v1/tasks`；`jq -e --arg id "$ECHO" 'any(.tasks[];.id==$id) and ([.tasks[].created_at]==([.tasks[].created_at]\|sort\|reverse))' "$T/$ID.body"`；`api 404 GET /api/v1/tasks/t-not-exist`；`check '.error=="task_not_found"'` | 全量列表含任务，按创建时间倒序；不存在任务准确返回 404 | P1 核心 |
| TC-13 | 提交校验与目标越界 | 两节点在线 | `api 400 POST /api/v1/tasks '{'`；`check '.error=="invalid_request"'`；`api 400 POST /api/v1/tasks '{}'`；`api 404 POST /api/v1/tasks "$(request no-such-node qa-echo)"`；`check '.error=="agent_not_exposed"'`；`api 404 POST /api/v1/tasks "$(request "$N1" no-such-agent)"`；`check '.error=="agent_not_exposed"'` | 无效 JSON/必填缺失拒绝；不可调用目标拒绝；不能落入无限重推 | P1 核心 |
| TC-14 | 参数及环境白名单 | qa-cat 禁止任务参数；所有服务仅允许 TMPDIR | `for ENV_JSON in '{"PATH":"/evil"}' '{"HOME":"/evil"}' '{"SWARM_TASK_ID":"forged"}' '{"SECRET":"x"}'; do D="$(submit "$N1" qa-cat SWARM_DENIED '[]' "$ENV_JSON")"; wait_task "$D" failed; check '.result.error=="policy_denied" and .result.exit_code==-1'; done`；`D="$(submit "$N1" qa-cat '' '["unexpected"]')"`；`wait_task "$D" failed`；`check '.result.error=="policy_denied"'` | 越权 env、系统变量覆盖、未授权 args 均 failed/policy_denied，进程不执行 | P0 冒烟 |
| TC-15 | stdin、干净环境与系统变量 | TC-14 | `D="$(submit "$N1" qa-cat SWARM_STDIN_OK)"`；`wait_task "$D" completed`；`check '.result.final_answer=="SWARM_STDIN_OK"'`；`D="$(submit "$N1" qa-sh 'printf "SECRET=%s\nTMPDIR=%s\nHOME=%s\nID=%s\nTRACE=%s\nOUT=%s\n" "${SWARM_TEST_SECRET-unset}" "$TMPDIR" "$HOME" "$SWARM_TASK_ID" "$SWARM_TRACE_ID" "$SWARM_OUTPUTS_DIR"' '[]' '{"TMPDIR":"/tmp"}')"`；`wait_task "$D" completed`；`jq -er .result.final_answer "$T/$ID.body" > "$T/env.out"`；`grep -Fx 'SECRET=unset' "$T/env.out"`；`grep -Fx "HOME=$T/a/home" "$T/env.out"`；`grep -Fx "ID=$D" "$T/env.out"`；`grep -Fx "OUT=$T/a/w/outputs/$D" "$T/env.out"`；`grep -Eq '^TRACE=.+$' "$T/env.out"` | stdin 完整交付；不继承 daemon 测试秘密；HOME 与 workdir 分离；系统变量由 runner 注入 | P1 核心 |
| TC-16 | 超时及进程组终止 | qa-timeout 超时 1 秒 | `D="$(submit "$N1" qa-timeout 'mkdir -p "$SWARM_OUTPUTS_DIR"; sleep 60 & c=$!; printf "%s %s\n" "$$" "$c" > "$SWARM_OUTPUTS_DIR/pids"; wait')"`；`wait_task "$D" failed 15`；`check '.result.error\|contains("timeout")'`；`test -s "$T/a/w/outputs/$D/pids"`；`for p in $(cat "$T/a/w/outputs/$D/pids"); do ! kill -0 "$p" 2>/dev/null; done` | failed/timeout；父、子进程均消失，不能只看错误字符串 | P0 冒烟 |
| TC-17 | 文本/二进制产物、摘要与下载 | 两节点在线 | 执行下方“产物基线命令” | FinalAnswer、产物引用、原始字节、大小、SHA-256 和响应头一致 | P0 冒烟 |
| TC-18 | 产物反向与离线 | TC-17 | `api 400 GET "/api/v1/tasks/$ART/artifacts/-1"`；`check '.error=="invalid_request"'`；`api 404 GET "/api/v1/tasks/$ART/artifacts/99"`；`check '.error=="artifact_not_found"'`；`api 404 GET /api/v1/tasks/t-not-exist/artifacts/0`；`check '.error=="task_not_found"'`；`stop_a`；`wait_node "$N1" false`；`api 503 GET "/api/v1/tasks/$ART/artifacts/$TXT_INDEX"`；`check '.error=="node_offline"'`；`start_a`；`wait_node "$N1" true` | 非法下标、不存在任务/产物、执行节点离线有明确区分；离线不交付伪造内容 | P1 核心 |
| TC-19 | 产物篡改与冻结清单 | TC-17；node A 在线 | `TXT_PATH="$(jq -er --argjson i "$TXT_INDEX" '.result.artifacts[$i].path' "$T/task.$ART.json")"`；`cp "$T/a/w/$TXT_PATH" "$T/original-artifact"`；`printf 'TAMPERED\n' > "$T/a/w/$TXT_PATH"`；`api 409 GET "/api/v1/tasks/$ART/artifacts/$TXT_INDEX"`；`check '.error=="artifact_mismatch"'`；`cp "$T/original-artifact" "$T/a/w/$TXT_PATH"`；`printf late > "$T/a/w/outputs/$ART/late.txt"`；`api 200 GET "/api/v1/tasks/$ART"`；`check 'all(.result.artifacts[];.path\|endswith("late.txt")\|not)'` | SHA 不一致禁止交付；结束后新增文件不追认 | P1 核心 |
| TC-20 | 超预算提交 | qa-echo 可调用 | `request "$N1" qa-echo > "$T/oversize.json"`；`"$PYTHON" - "$T/oversize.json" <<<'import json,sys;p=sys.argv[1];d=json.load(open(p));d["input"]["stdin"]="x"*1048576;json.dump(d,open(p,"w"))'`；`api 400 POST /api/v1/tasks "@$T/oversize.json"`；`check '.error=="frame_too_large"'` | 按完整编码信封限制拒绝超 1MiB 的任务；不是只检查 stdin 长度 | P1 核心 |
| TC-21 | 输出预算、tail 与 UTF-8 | qa-sh 可用 | `D="$(submit "$N1" qa-sh "exec \"$PYTHON\" -c 'import sys;sys.stdout.buffer.write(b\"x\"*700000+b\"\\xff\");sys.stderr.write(\"e\"*100000)'")"`；`wait_task "$D" completed`；`"$PYTHON" - "$T/$ID.body" <<<'import json,sys;d=json.load(open(sys.argv[1]));r=d["result"];assert len(r["final_answer"].encode())<=512*1024;assert len(r["stdout_tail"].encode())<=65536;assert len(r["stderr_tail"].encode())<=65536;assert "truncated" in r.get("error","")'`；`D="$(submit "$N1" qa-sh "exec \"$PYTHON\" -c 'import sys;sys.stdout.buffer.write(b\"A\\xffB\")'")"`；`wait_task "$D" completed`；`check '.result.final_answer=="A�B"'` | 超量输出受限并标 truncated；非法 UTF-8 替换 U+FFFD；响应为合法 JSON | P2 边界 |
| TC-22 | 同 workdir 串行与不同目录并行 | qa-sh 与 qa-cat 同目录，qa-other 不同目录 | 执行下方“工作目录互斥命令” | 同目录任务执行区间不重叠；不同目录任务可以先于长任务完成 | P1 核心 |

产物基线命令：

```bash
export ART="$(submit "$N1" qa-sh \
  'mkdir -p "$SWARM_OUTPUTS_DIR"; printf "SWARM_ARTIFACT_OK\n" > "$SWARM_OUTPUTS_DIR/text.txt"; printf "\000\001\002" > "$SWARM_OUTPUTS_DIR/binary.bin"; printf "SWARM_FINAL_OK\n"')"
wait_task "$ART" completed
check '.result.final_answer=="SWARM_FINAL_OK\n" and (.result.artifacts|length)==2'

export TXT_INDEX="$(jq -er '.result.artifacts|to_entries[]|select(.value.path|endswith("/text.txt"))|.key' "$T/$ID.body")"
export BIN_INDEX="$(jq -er '.result.artifacts|to_entries[]|select(.value.path|endswith("/binary.bin"))|.key' "$T/$ID.body")"

api 200 GET "/api/v1/tasks/$ART/artifacts/$TXT_INDEX"
grep -Fx 'SWARM_ARTIFACT_OK' "$T/$ID.body"
grep -Ei '^Content-Type: application/octet-stream' "$T/$ID.headers"

EXPECTED_SHA="$(jq -er --argjson i "$TXT_INDEX" \
  '.result.artifacts[$i].sha256' "$T/task.$ART.json")"
ACTUAL_SHA="$("$PYTHON" - "$T/$ID.body" <<'PY'
import hashlib,sys
print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())
PY
)"
test "$ACTUAL_SHA" = "$EXPECTED_SHA"
grep -Fi "X-Artifact-Sha256: $EXPECTED_SHA" "$T/$ID.headers"

api 200 GET "/api/v1/tasks/$ART/artifacts/$BIN_INDEX"
"$PYTHON" - "$T/$ID.body" <<'PY'
import sys
assert open(sys.argv[1],"rb").read() == b"\x00\x01\x02"
PY
```

工作目录互斥命令：

```bash
W1="$(submit "$N1" qa-sh 'sleep 4; printf "LONG_DONE\n"')"
wait_task "$W1" running
W2="$(submit "$N1" qa-cat SECOND)"
W3="$(submit "$N1" qa-other 'printf "OTHER_DONE\n"')"

wait_task "$W3" completed 3
api 200 GET "/api/v1/tasks/$W1"
check '.status=="running"'
wait_task "$W1" completed
wait_task "$W2" completed

"$PYTHON" - "$T/task.$W1.json" "$T/task.$W2.json" <<'PY'
import json,sys,datetime
a,b=[json.load(open(p))["result"] for p in sys.argv[1:]]
parse=lambda s: datetime.datetime.fromisoformat(s.replace("Z","+00:00"))
assert parse(b["started_at"]) >= parse(a["finished_at"])
print("WORKDIR_SERIAL_OK")
PY
```

## 三、离线信箱、恢复、核对与控制批次

| 编号 | 模块 | 前置条件 | 步骤 | 期望结果 | 优先级 |
|---|---|---|---|---|---|
| TC-23 | 离线信箱与补推 | N2 已至少注册一次；无执行中任务 | `stop_b`；`wait_node "$N2" false`；`OFF="$(submit "$N2" qa-echo '' '["MAILBOX_OK"]')"`；`api 200 GET "/api/v1/tasks/$OFF"`；`check '.status=="submitted" and (has("result")\|not)'`；`start_b`；`wait_node "$N2" true`；`wait_task "$OFF" completed`；`check '.result.final_answer=="MAILBOX_OK\n"'` | 离线提交持久化并排队；重连自动补推完成，不靠任务拉取 HTTP 回退 | P0 冒烟 |
| TC-24 | 信箱条数配额与满箱幂等 | N2 信箱为空 | 执行下方“100 条信箱配额命令” | 100 条接受，第 101 条 503/mailbox_full；满箱同键重试仍返回原 ID；补推后容量释放 | P1 核心 |
| TC-25 | 信箱字节预算 | TC-24 已完全排空；N2 无执行中任务 | 执行下方“8MiB 信箱预算命令” | 在条数不足 100 时因字节预算返回 503/mailbox_full；已接受任务不丢 | P2 边界 |
| TC-26 | 重连与 epoch | 两节点在线 | `api 200 GET /api/v1/nodes`；`OLD_EPOCH="$(jq -er --arg n "$N1" '.nodes[]\|select(.node_id==$n)\|.epoch' "$T/$ID.body")"`；`stop_a`；`wait_node "$N1" false`；`start_a`；`wait_node "$N1" true`；`api 200 GET /api/v1/nodes`；`jq -e --arg n "$N1" --argjson e "$OLD_EPOCH" 'any(.nodes[];.node_id==$n and .epoch>$e)' "$T/$ID.body"`；`api 200 GET "/api/v1/tasks/$ECHO"`；`check '.status=="completed"'` | 新会话 epoch 递增；历史终态不回退 | P0 冒烟 |
| TC-27 | hub 重启恢复与会话失效 | PAT 有效；无执行中任务 | `cp "$COOKIE" "$T/pre-restart.cookies"`；`stop_b`；`wait_node "$N2" false`；`PENDING="$(submit "$N2" qa-echo '' '["HUB_RESTART_OK"]')"`；`stop_hub`；`start_hub`；`code="$(curl -sS -b "$T/pre-restart.cookies" -o "$T/stale-session.json" -w '%{http_code}' "$HUB/api/v1/nodes")"`；`test "$code" = 401`；`api 200 GET /api/v1/nodes '' pat`；`login`；`wait_node "$N1" true`；`api 200 GET "/api/v1/tasks/$ECHO"`；`check '.status=="completed"'`；`start_b`；`wait_node "$N2" true`；`wait_task "$PENDING" completed`；`api 200 GET /api/v1/auth/account`；`check '.username=="qa_admin"'` | 任务、能力、信箱、账户和 PAT 保留；内存 Cookie 失效；node 自动重连，待投任务完成 | P0 冒烟 |
| TC-28 | 会话断开不杀已执行任务 | N1 在线 | 执行下方“断线执行命令” | hub 断开期间 node 执行继续；本地提交明确不可达；恢复后补传完成结果，而非 needs_review | P1 核心 |
| TC-29 | 执行中崩溃与 needs_review | N1 在线 | 执行下方“执行中崩溃命令” | 独立终态 needs_review；error=node_restarted_midway；同任务副作用标记仅一次，不自动重跑 | P0 冒烟 |
| TC-30 | 核对记录与覆盖 | TC-29 | `api 200 POST "/api/v1/tasks/$REVIEW/review" '{"action":"ack_review","note":"checked-on-node","by":"forged"}'`；`check '.status=="needs_review" and .review.by=="admin" and .review.action=="ack_review" and .review.note=="checked-on-node" and (.review.at\|endswith("Z"))'`；`api 200 POST "/api/v1/tasks/$REVIEW/review" '{"action":"mark_failed","note":"manual-failure"}'`；`check '.status=="needs_review" and .result.status=="needs_review" and .review.action=="mark_failed" and .review.note=="manual-failure"'`；`stop_hub`；`start_hub`；`login`；`wait_node "$N1" true`；`wait_node "$N2" true`；`api 200 GET "/api/v1/tasks/$REVIEW"`；`check '.status=="needs_review" and .review.action=="mark_failed"'` | 核对身份不可伪造；重复覆盖旧记录；mark_failed 仅人工记录，不改协议终态；重启保留 | P1 核心 |
| TC-31 | 核对反向 | TC-30 | `api 400 POST "/api/v1/tasks/$REVIEW/review" '{"action":"rerun","note":"x"}'`；`check '.error=="invalid_request"'`；`api 409 POST "/api/v1/tasks/$ECHO/review" '{"action":"ack_review"}'`；`check '.error=="not_needs_review"'`；`api 404 POST /api/v1/tasks/t-not-exist/review '{"action":"ack_review"}'`；`check '.error=="task_not_found"'` | 非法动作、不适用状态、不存在任务分别拒绝；没有隐式重跑动作 | P1 核心 |
| TC-32 | 控制批次与部分准入 | 两节点在线 | `api 200 PATCH /api/v1/local/agents/qa-echo/exposed '{"exposed":false}' local-b`；`wait_directory "$N2" qa-echo false`；`B_BODY="$(jq -nc --arg a "$N1" --arg b "$N2" '{agent:"qa-echo",targets:[$a,$b,"not-registered"],input:{args:["BATCH_OK"]},idem_key:"batch-main"}')"`；`api 202 POST /api/v1/control-operations "$B_BODY"`；`export BATCH="$(jq -er .batch_id "$T/$ID.body")"`；`check '.status=="complete" and (.items\|length)==3 and (.items[0].task_id\|length)>0 and .items[1].skipped=="not_exposed" and .items[2].skipped=="offline"'`；`B_TASK="$(jq -er '.items[0].task_id' "$T/$ID.body")"`；`wait_task "$B_TASK" completed`；`api 200 GET "/api/v1/control-operations/$BATCH"`；`check '.items[0].status=="completed"'`；`api 200 PATCH /api/v1/local/agents/qa-echo/exposed '{"exposed":true}' local-b`；`wait_directory "$N2" qa-echo true` | 正常目标创建正式任务；未暴露/离线目标跳过且不阻塞其余；批次 complete 不等于任务已全部完成 | P1 核心 |
| TC-33 | 批次幂等、非法输入与恢复 | TC-32 | `api 200 POST /api/v1/control-operations "$B_BODY"`；`jq -e --arg id "$BATCH" '.batch_id==$id and .dup==true' "$T/$ID.body"`；`api 409 POST /api/v1/control-operations "$(jq '.input.args=["CHANGED"]' <<<"$B_BODY")"`；`check '.error=="conflict"'`；`api 400 POST /api/v1/control-operations '{"agent":"qa-echo","targets":[],"input":{}}'`；`api 404 GET /api/v1/control-operations/b-not-exist`；`check '.error=="batch_not_found"'`；`stop_hub`；`start_hub`；`login`；`wait_node "$N1" true`；`wait_node "$N2" true`；`api 200 GET "/api/v1/control-operations/$BATCH"`；`check '.status=="complete" and (.items\|length)==3'` | 同键不重新创建子任务；冲突 409；空目标/不存在批次拒绝；批次重启保留 | P1 核心 |

控制批次及开关测试使用目录收敛函数：

```bash
wait_directory() {
  local nid="$1" agent="$2" exposed="$3" stop=$((SECONDS+15))
  while (( SECONDS < stop )); do
    api 200 GET /api/v1/directory
    if jq -e --arg n "$nid" --arg a "$agent" --argjson e "$exposed" \
      '(any(.directory[]; .node_id==$n and .card.name==$a))==$e' \
      "$T/$ID.body" >/dev/null; then return 0; fi
    sleep 0.2
  done
  return 1
}
```

100 条信箱配额命令：

```bash
stop_b
wait_node "$N2" false
: > "$T/quota.ids"

for i in $(seq 1 100); do
  submit "$N2" qa-echo '' '["QUOTA_OK"]' '{}' "quota-$i" >> "$T/quota.ids"
done

api 503 POST /api/v1/tasks \
  "$(request "$N2" qa-echo '' '["OVER_QUOTA"]' '{}' quota-over)"
check '.error=="mailbox_full"'

api 200 POST /api/v1/tasks \
  "$(request "$N2" qa-echo '' '["QUOTA_OK"]' '{}' quota-1)"
jq -e --arg id "$(head -n 1 "$T/quota.ids")" \
  '.dup==true and .task_id==$id' "$T/$ID.body"

api 200 GET /api/v1/nodes
jq -e --arg n "$N2" 'any(.nodes[];.node_id==$n and .mailbox_used==100)' \
  "$T/$ID.body"

start_b
wait_node "$N2" true
while read -r task_id; do wait_task "$task_id" completed 120; done < "$T/quota.ids"

api 200 GET /api/v1/nodes
jq -e --arg n "$N2" 'any(.nodes[];.node_id==$n and .mailbox_used==0)' \
  "$T/$ID.body"
```

8MiB 信箱预算命令：

```bash
stop_b
wait_node "$N2" false
: > "$T/bytes.ids"
BYTE_LIMIT_HIT=0

for i in $(seq 1 20); do
  request "$N2" qa-cat '' '[]' '{}' "bytes-$i" > "$T/bytes.json"
  "$PYTHON" - "$T/bytes.json" <<'PY'
import json,sys
p=sys.argv[1]
d=json.load(open(p))
d["input"]["stdin"]="x"*800000
json.dump(d,open(p,"w"))
PY
  code="$(curl -sS --max-time 15 -b "$COOKIE" \
    -H 'Content-Type: application/json' --data-binary "@$T/bytes.json" \
    -o "$T/$ID.body" -w '%{http_code}' "$HUB/api/v1/tasks")"
  if test "$code" = 503; then
    check '.error=="mailbox_full"'
    BYTE_LIMIT_HIT=1
    break
  fi
  test "$code" = 202
  jq -er .task_id "$T/$ID.body" >> "$T/bytes.ids"
done
test "$BYTE_LIMIT_HIT" = 1
test "$(wc -l < "$T/bytes.ids" | tr -d ' ')" -lt 100

start_b
wait_node "$N2" true
while read -r task_id; do wait_task "$task_id" completed 120; done < "$T/bytes.ids"
```

断线执行命令：

```bash
NET_TASK="$(submit "$N1" qa-sh \
  'mkdir -p "$SWARM_OUTPUTS_DIR"; printf STARTED > "$SWARM_OUTPUTS_DIR/started"; sleep 5; printf "NETWORK_SURVIVED\n" > "$SWARM_OUTPUTS_DIR/done"; printf "NETWORK_SURVIVED\n"')"
wait_file "$T/a/w/outputs/$NET_TASK/started"
stop_hub

# 通过真实文件确认会话断开期间仍执行完成。
wait_file "$T/a/w/outputs/$NET_TASK/done"
grep -Fx NETWORK_SURVIVED "$T/a/w/outputs/$NET_TASK/done"

api 503 POST /api/v1/tasks \
  "$(request "$N2" qa-echo '' '["UNREACHABLE"]')" local-a
check '.error=="hub_unreachable"'

api 200 GET /api/v1/local/status '' local-a
check '.online==false'

start_hub
login
wait_node "$N1" true
wait_node "$N2" true
wait_task "$NET_TASK" completed
check '.result.final_answer=="NETWORK_SURVIVED\n"'
```

执行中崩溃命令：

```bash
export REVIEW="$(submit "$N1" qa-sh \
  'mkdir -p "$SWARM_OUTPUTS_DIR"; printf "%s\n" "$$" > "$SWARM_OUTPUTS_DIR/pgid"; printf "ONCE\n" >> "$SWARM_OUTPUTS_DIR/executions"; sleep 120; printf SHOULD_NOT_COMPLETE')"
wait_file "$T/a/w/outputs/$REVIEW/pgid"
wait_file "$T/a/w/outputs/$REVIEW/executions"
wait_task "$REVIEW" running

PGID="$(cat "$T/a/w/outputs/$REVIEW/pgid")"
kill -KILL "$A_PID"
# SIGKILL 的预期非零退出码单独消费。
if wait "$A_PID"; then
  printf 'node unexpectedly exited successfully\n' >&2
  exit 1
fi
# 清理测试故意制造的孤儿进程组；不修改任务账本。
if kill -0 "$PGID" 2>/dev/null; then kill -KILL -- "-$PGID"; fi

wait_node "$N1" false
start_a
wait_node "$N1" true
wait_task "$REVIEW" needs_review
check '.result.status=="needs_review" and .result.error=="node_restarted_midway"'
test "$(wc -l < "$T/a/w/outputs/$REVIEW/executions" | tr -d ' ')" = 1

# 第二次恢复仍不能重跑同一 task_id。
stop_a
start_a
wait_node "$N1" true
api 200 GET "/api/v1/tasks/$REVIEW"
check '.status=="needs_review"'
test "$(wc -l < "$T/a/w/outputs/$REVIEW/executions" | tr -d ' ')" = 1
```

## 四、node 本地 API

| 编号 | 模块 | 前置条件 | 步骤 | 期望结果 | 优先级 |
|---|---|---|---|---|---|
| TC-34 | 本地鉴权、状态与配置视图 | node A 在线 | `code="$(curl -sS -o "$T/local-denied.json" -w '%{http_code}' "$NODE_A/api/v1/local/status")"`；`test "$code" = 401`；`code="$(curl -sS -H 'Authorization: Bearer wrong' -o "$T/local-denied.json" -w '%{http_code}' "$NODE_A/api/v1/local/agents")"`；`test "$code" = 401`；`api 200 GET /api/v1/local/status '' local-a`；`check '.online==true and .epoch>=1 and .last_sync_at!=null and (.ledger\|has("accepted") and has("started") and has("finished"))'`；`check 'has("mailbox")==false and has("inflight")==false'`；`api 200 GET /api/v1/local/agents '' local-a`；`check 'any(.agents[];.name=="qa-sh" and .command=="/bin/sh" and .today_tasks>=0)'`；`api 200 GET /api/v1/agents '' local-a`；`check '(.local\|length)>0 and has("network")==false and ([.local[].node_id]|unique|length)==1 and .local[0].node_id=="'$N1'"'` | 本地 API 强制 local_token；状态只含本机事实（无 mailbox/inflight 键）；本机主人可看 ExecProfile；目录仅本机 agent、无 network 键（node 无全网视图） | P0 冒烟 |
| TC-35 | exposed 准入、发现与持久化 | node A 在线 | `api 200 PATCH /api/v1/local/agents/qa-echo/exposed '{"exposed":false}' local-a`；`check '.exposed==false and .hub_synced==true'`；`wait_directory "$N1" qa-echo false`；`api 404 POST /api/v1/tasks "$(request "$N1" qa-echo)"`；`check '.error=="agent_not_exposed"'`；`stop_a`；`start_a`；`wait_node "$N1" true`；`api 200 GET /api/v1/local/agents '' local-a`；`check 'any(.agents[];.name=="qa-echo" and .exposed==false)'`；`api 200 PATCH /api/v1/local/agents/qa-echo/exposed '{"exposed":true}' local-a`；`wait_directory "$N1" qa-echo true`；`api 404 PATCH /api/v1/local/agents/no-such-agent/exposed '{"exposed":true}' local-a`；`check '.error=="agent_not_found"'` | 停止暴露后新任务拒绝、目录移除、重启保持；重新暴露恢复；未知服务拒绝 | P1 核心 |
| TC-36 | 本机派发、查询、幂等与留痕 | 两节点在线 | `L_BODY="$(request "$N1" qa-echo '' '["LOCAL_ROUTE_OK"]' '{}' local-main)"`；`api 202 POST /api/v1/tasks "$L_BODY" local-a`；`export LOCAL_TASK="$(jq -er .task_id "$T/$ID.body")"`；`wait_task "$LOCAL_TASK" completed`；`jq -e --arg n "$N1" '.submitted_by==$n and .result.final_answer=="LOCAL_ROUTE_OK\n"' "$T/$ID.body"`；`api 200 GET "/api/v1/tasks/$LOCAL_TASK" '' local-a`；`check '.status=="completed"'`；`api 200 POST /api/v1/tasks "$L_BODY" local-a`；`check '.dup==true'`；`api 200 GET /api/v1/local/tasks '' local-a`；`jq -e --arg id "$LOCAL_TASK" '[.tasks[]\|select(.task_id==$id)]\|length==1' "$T/$ID.body"`；`api 409 POST /api/v1/tasks "$(jq '.input.args=["CHANGED"]' <<<"$L_BODY")" local-a`；`api 404 GET /api/v1/tasks/t-not-exist '' local-a`；`check '.error=="task_not_found"'` | 本地进程→本机 node→hub→本机执行完整闭环（v2 边界：本地 API 派单目标只能是本机 node，跨机派单经 hub 发起）；hub 按连接身份覆盖 submitted_by；幂等仅留一条日志 | P0 冒烟 |
| TC-56 | 边界锁：本地 API 跨机派单 400（D8） | 两节点在线 | `api 400 POST /api/v1/tasks "$(request "$N2" qa-echo 'SHOULD_NOT_RUN')" local-a`；`check '.error=="invalid_request" and (.message\|contains("跨机派单经 hub 发起"))'`；`api 200 GET /api/v1/local/tasks '' local-a`；`jq -e --arg n "$N2" '[.tasks[]\|select(.target_node==$n)]\|length==0' "$T/$ID.body"` | target_node 非本机 node_id 直接 400，不发帧、不留提交日志、不静默改写目标；跨机派单只能经 hub 发起（语义单测：TestSubmitCrossNodeRejected） | P1 核心 |
| TC-57 | 边界锁：node 会话 WSS 直发外机被 hub 拒（D9） | 仓库依赖缓存可用 | `go test -mod=readonly -count=1 ./internal/hub/core -run '^TestSubmitFrameCrossNodeRejected$' >"$T/d9.log" 2>&1`；`grep -Eq '^ok[[:space:]]' "$T/d9.log"`；`! grep -Eq '^FAIL' "$T/d9.log"` | node task_submit 帧 target≠提交方会话身份时 hub 回 err、不发 offer；HTTP 管理面不受此限；实时链路需 node 协议 WSS 客户端，机械执行以覆盖该语义的单测为载体（HTTP 入口的同语义锁见 TC-56） | P1 核心 |
| TC-58 | 边界锁：task_query 他节点任务 forbidden | 两节点在线 | `OTHER="$(submit "$N2" qa-echo 'printf QUERY_FORBIDDEN_MARK')"`；`wait_task "$OTHER" completed`；`api 502 GET "/api/v1/tasks/$OTHER" '' local-a`；`check '.error=="forbidden" and (.message\|contains("仅可查询本机任务"))'`；`api 200 GET "/api/v1/tasks/$OTHER"`；`check '.status=="completed"'` | node A 经本地 API 查 N2 任务：hub 回 err(forbidden)，本地 API 未映射该码按默认映射为 502 + error=forbidden；hub 管理面查同一任务不受限（语义单测：TestTaskQueryCrossNodeRejected） | P1 核心 |
| TC-59 | 旧配置兼容：废除字段忽略 | node A 在线 | `"$PYTHON" -c 'import pathlib,sys,yaml; p=pathlib.Path(sys.argv[1]); c=yaml.safe_load(open(p)); c["hub_api_url"]="http://127.0.0.1:9"; c["hub_api_token"]="deprecated-should-be-ignored"; p.write_text(yaml.safe_dump(c))' "$T/a/node.yaml"`；`stop_a`；`start_a`；`wait_node "$N1" true`；`api 200 GET /api/v1/local/status '' local-a`；`check '.online==true'`；`api 202 POST /api/v1/tasks "$(request "$N1" qa-echo 'DEPRECATED_FIELDS_OK')" local-a`；`export DEP_TASK="$(jq -er .task_id "$T/$ID.body")"`；`wait_task "$DEP_TASK" completed` | node.yaml 显式含已废除的 hub_api_url/hub_api_token 时仍正常启动、注册与派单（字段忽略，SPEC 第六节边界 v2） | P1 核心 |

## 五、Web 控制台九页交互

以下用例均使用浏览器执行器。每页同时检查主要行动、业务闭环或只读说明；不能用 `curl /` 返回 200 代替页面交互验证。

| 编号 | 模块 | 前置条件 | 步骤 | 期望结果 | 优先级 |
|---|---|---|---|---|---|
| TC-37 | hub 总览与登录入口 | 已有任务及 needs_review | 执行下方 TC-37 浏览器命令 | 总览展示真实节点、异常和任务；主行动可进入节点接入；过期会话回登录；密码登录可完成 | P0 冒烟 |
| TC-38 | hub 节点接入 | 两节点在线 | 执行下方 TC-38 浏览器命令 | 向导生成身份对应片段，令牌掩码；明确手工配置限制；“吊销”不冒充已生效能力 | P1 核心 |
| TC-39 | hub 任务 | TC-10、TC-17、TC-29 | 执行下方 TC-39 浏览器命令 | 筛选、空态、详情、真实产物下载、needs_review 警示和核对入口有效 | P0 冒烟 |
| TC-40 | hub 派发 | 两节点在线 | 执行下方 TC-40 浏览器命令 | 请求确已落库但响应丢失后，表单冻结；同键重试命中原批次，进入报告与下一步 | P1 核心 |
| TC-41 | hub 策略容量 | 已登录 | 执行下方 TC-41 浏览器命令 | 明确只读，显示容量/保留规则，无保存入口，不发送变更请求 | P1 核心 |
| TC-42 | hub 设置 | 账户已初始化 | 执行下方 TC-42 浏览器命令 | 账户设置存在；创建 PAT 明文一次展示；关闭后不再展示；UI 撤销后 Bearer 立即失效 | P1 核心 |
| TC-43 | node 总览与诊断 | node A 在线 | 执行下方 TC-43 浏览器命令 | 三卡（连接状态/执行账本/最近同步）可见且页面无「信箱余量」；剪贴板诊断为真实 JSON；派单导航有效 | P1 核心 |
| TC-44 | node 服务 | node A 在线 | 执行下方 TC-44 浏览器命令，并验证恢复暴露 | 配置只读；开关显示同步反馈，网络准入随之变化；操作不取消已有任务 | P1 核心 |
| TC-45 | node 任务 | 两节点在线 | 执行下方 TC-45 浏览器命令 | 目录选择、未知态冻结、同键重试、任务留痕、结果抽屉及下一步完整闭合 | P0 冒烟 |

TC-37：

```bash
ui hub overview '
await expect(page.locator("h1")).toHaveText("总览");
await expect(page.locator("body")).toContainText(E.N1);
await expect(page.locator("body")).toContainText("任务状态分布");
await page.locator(".page-head [data-act=go][data-page=nodes]").click();
await expect(page.locator("h1")).toHaveText("节点接入");
await context.clearCookies();
await page.reload();
await expect(page.locator("h1")).toHaveText("管理你的协作网");
await page.locator("[data-act=switch-login]").click();
await page.locator("#token").fill(E.ADMIN_TOKEN);
await page.locator("#login-form button[type=submit]").click();
await expect(page.locator("h1")).toHaveText("总览");
'
# 日常密码入口必须可用；账户已有初始化时不得误导再次创建账户。
ui guest login '
await expect(page.locator("#pw-login-form")).toBeVisible();
await page.locator("#login-user").fill("qa_admin");
await page.locator("#login-pass").fill("Qa_test_2026!");
await page.locator("#pw-login-form button[type=submit]").click();
await expect(page.locator("h1")).toHaveText("总览");
'
```

TC-38：

```bash
ui hub nodes '
await page.locator("[data-act=enroll]").first().click();
await page.locator("#enroll-id").fill("qa-new-node");
await expect(page.locator("#enroll-snippet")).toContainText("qa-new-node");
await expect(page.locator("#enroll-node-snippet")).toContainText("qa-new-node");
await expect(page.locator("[role=dialog]")).toContainText("V0 无动态签发");
assert(!(await page.locator("[role=dialog]").innerText()).includes(E.ADMIN_TOKEN));
await page.locator("[data-act=enroll-done]").click();
await page.locator("[data-act=revoke]").first().click();
await expect(page.locator("[role=dialog]")).toContainText("无撤销机制");
await expect(page.locator("[role=dialog]")).toContainText("不改变节点或会话状态");
'
api 200 GET /api/v1/nodes
jq -e --arg n "$N1" 'any(.nodes[];.node_id==$n and .online)' "$T/$ID.body"
```

TC-39：

```bash
ui hub tasks '
await page.locator("#f-trace").fill("no-task-should-match-this");
await expect(page.locator("tbody tr[data-task]")).toHaveCount(0);
await page.locator("[data-act=clear-filters]").click();
await page.locator("#f-trace").fill(E.ART);
await page.locator(`tr[data-task="${E.ART}"]`).click();
await expect(page.locator("[role=dialog]")).toContainText("SWARM_FINAL_OK");
const downloadPromise = page.waitForEvent("download");
await page.locator(`[data-act=artifact][data-index="${E.TXT_INDEX}"]`).click();
const download = await downloadPromise;
const path = E.T + "/web-artifact.txt";
await download.saveAs(path);
assert.equal(fs.readFileSync(path,"utf8"), "SWARM_ARTIFACT_OK\n");
await page.locator("[data-act=close]").click();
await page.locator("#f-trace").fill(E.REVIEW);
await page.locator(`tr[data-task="${E.REVIEW}"]`).click();
await expect(page.locator("[role=dialog]")).toContainText("结果不确定");
await expect(page.locator("[role=dialog]")).toContainText("不要重新提交");
await expect(page.locator("[data-act=review]").first()).toBeVisible();
'
```

TC-40：

```bash
ui hub control '
await page.locator("#ctrl-agent").selectOption(E.N1 + "/qa-cat");
await page.locator(`input[data-target="${E.N1}"]`).check();
await page.locator("#ctrl-stdin").fill("WEB_CONTROL_OK");
const keys=[];
let first=true;
await page.route("**/api/v1/control-operations", async route => {
  if(route.request().method()!=="POST") return route.continue();
  keys.push(route.request().postDataJSON().idem_key);
  if(first) {
    first=false;
    const response=await route.fetch();
    assert.equal(response.status(),202);
    await response.dispose();
    return route.abort("failed");
  }
  return route.continue();
});
await page.locator("#control-form button[type=submit]").click();
await expect(page.locator("#ctrl-feedback")).toContainText("结果未知");
await expect(page.locator("#ctrl-agent")).toBeDisabled();
await expect(page.locator("#ctrl-stdin")).toBeDisabled();
await page.locator("[data-act=ctrl-retry]").click();
await expect(page.locator("h1")).toContainText("批次报告");
assert.equal(keys.length,2);
assert.equal(keys[0],keys[1]);
await expect(page.locator("[data-act=go-tasks]")).toBeVisible();
'
```

TC-41：

```bash
ui hub policy '
const writes=[];
page.on("request",r=>{
  if(r.url().includes("/api/v1/") && !["GET","HEAD"].includes(r.method()))
    writes.push(r.url());
});
await expect(page.locator("h1")).toHaveText("策略容量");
await expect(page.locator("body")).toContainText("只读");
await expect(page.locator("body")).toContainText("256 MiB");
await expect(page.locator("body")).toContainText("640 KiB");
await expect(page.locator("body")).toContainText("7 天");
await expect(page.locator("body")).toContainText("24h");
await expect(page.getByRole("button",{name:"保存",exact:true})).toHaveCount(0);
assert.deepEqual(writes,[]);
'
```

TC-42：

```bash
ui hub tokens '
await expect(page.locator("h1")).toHaveText("设置");
await expect(page.locator("#account-form")).toBeVisible();
await page.locator("#token-name").fill("web-functional-pat");
await page.locator("#token-form button[type=submit]").click();
const plain=page.locator("#new-token-plain");
await expect(plain).toBeVisible();
const token=(await plain.innerText()).trim();
assert.match(token,/^smsw_/);
const list=await context.request.get(E.HUB+"/api/v1/auth/tokens");
const entry=(await list.json()).tokens.find(t=>t.name==="web-functional-pat");
assert(entry);
await page.locator("[data-act=token-done]").click();
await expect(page.locator("#new-token-plain")).toHaveCount(0);
await page.reload();
await expect(page.locator("#new-token-plain")).toHaveCount(0);
await page.locator(`[data-act=token-revoke][data-id="${entry.id}"]`).click();
await page.locator("[data-act=token-revoke-confirm]").click();
await expect(page.locator(`[data-act=token-revoke][data-id="${entry.id}"]`)).toHaveCount(0);
const denied=await context.request.get(E.HUB+"/api/v1/nodes",{
  headers:{Authorization:"Bearer "+token}
});
assert.equal(denied.status(),401);
'
```

TC-43：

```bash
ui node overview '
await expect(page.locator("h1")).toHaveText("总览");
for(const text of ["连接状态","执行账本","最近同步"])
  await expect(page.locator("body")).toContainText(text);
assert(!(await page.locator("body").innerText()).includes("信箱余量"));
await page.locator("[data-act=copy-diag]").click();
const diag=JSON.parse(await page.evaluate(()=>navigator.clipboard.readText()));
assert.equal(diag.node_id,E.N1);
assert.equal(diag.online,true);
assert(diag.epoch>=1);
await page.locator("[data-act=go-tasks]").first().click();
await expect(page.locator("h1")).toContainText("我的任务");
'
```

TC-44：

```bash
# 服务关闭期间，已开始的任务仍须完成。
SERVICE_TASK="$(submit "$N1" qa-sh 'sleep 4; printf "SERVICE_CONTINUES\n"')"
wait_task "$SERVICE_TASK" running

ui node services '
await expect(page.locator("h1")).toHaveText("服务");
await expect(page.locator("body")).toContainText("只读");
await page.locator("[data-act=\"expose-off:qa-sh\"]").click();
await expect(page.locator("#sync-qa-sh")).toContainText("网内生效");
await expect(page.locator("#svc-qa-sh")).toContainText("停止暴露");
'
wait_directory "$N1" qa-sh false
api 404 POST /api/v1/tasks "$(request "$N1" qa-sh 'echo denied')"
wait_task "$SERVICE_TASK" completed
check '.result.final_answer=="SERVICE_CONTINUES\n"'

ui node services '
await page.locator("[data-act=\"expose-on:qa-sh\"]").click();
await expect(page.locator("#sync-qa-sh")).toContainText("网内生效");
'
wait_directory "$N1" qa-sh true

# 离线更改验证“本机生效/待同步”，恢复后自动收敛。
stop_hub
ui node services '
await page.locator("[data-act=\"expose-off:qa-echo\"]").click();
await expect(page.locator("#sync-qa-echo")).toContainText("待同步");
'
start_hub
login
wait_node "$N1" true
wait_node "$N2" true
wait_directory "$N1" qa-echo false
api 200 PATCH /api/v1/local/agents/qa-echo/exposed '{"exposed":true}' local-a
wait_directory "$N1" qa-echo true
```

TC-45：

```bash
ui node tasks '
await page.locator("#f-target").selectOption(E.N2+"|qa-cat");
await page.locator("#f-stdin").fill("WEB_LOCAL_OK");
const keys=[];
let first=true;
await page.route("**/api/v1/tasks",async route=>{
  if(route.request().method()!=="POST") return route.continue();
  keys.push(route.request().postDataJSON().idem_key);
  if(first){
    first=false;
    const response=await route.fetch();
    assert.equal(response.status(),202);
    await response.dispose();
    return route.abort("failed");
  }
  return route.continue();
});
await page.locator("[data-act=form-submit]").click();
await expect(page.locator("#task-form")).toContainText("结果未知");
await expect(page.locator("#f-target")).toBeDisabled();
await expect(page.locator("#f-stdin")).toBeDisabled();
await page.locator("[data-act=form-resend]").click();
await expect(page.locator("#task-form")).toContainText("已确认");
assert.equal(keys.length,2);
assert.equal(keys[0],keys[1]);
await page.locator("[data-act=open-submitted]").click();
await expect(page.locator("#drawer")).toContainText("WEB_LOCAL_OK",{timeout:20000});
await expect(page.locator("#drawer")).toContainText("查看全部任务");
const rows=await page.locator("tr[data-task]").count();
assert(rows>=1);
'
```

## 六、MCP 工具发现与基础执行入口

`tool` 发送的实际 JSON-RPC 行结构为：

```json
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"swarmmind_nodes","arguments":{}}}
```

任务参数由 `jq` 展开，传输为逐行 JSON，不使用 HTTP MCP 或 `Content-Length` 帧。

| 编号 | 模块 | 前置条件 | 步骤 | 期望结果 | 优先级 |
|---|---|---|---|---|---|
| TC-46 | MCP 初始化及工具发现 | 主 PAT 有效 | `mcp tools/list '{}'`；`jq -e 'select(.id==1)\|.result.protocolVersion=="2024-11-05" and .result.serverInfo.name=="swarmmind"' "$T/$ID.rpc"`；`mcheck '([.result.tools[].name]\|sort)==(["swarmmind_nodes","swarmmind_agents","swarmmind_submit","swarmmind_status","swarmmind_wait","swarmmind_artifact","swarmmind_tasks","swarmmind_decide","swarmmind_dispatch_suggest","swarmmind_group_create","swarmmind_group_list","swarmmind_group_say","swarmmind_group_messages","swarmmind_group_close","swarmmind_work_create","swarmmind_work_get","swarmmind_work_list","swarmmind_work_takeover","swarmmind_work_collect"]\|sort) and all(.result.tools[];(.description\|length)>0 and .inputSchema.type=="object")'` | 初始化可解析；hub 当前 19 工具，固定前缀和非空能力描述；通知不产生多余应答 | P0 冒烟 |
| TC-47 | swarmmind_nodes / agents | 两节点在线 | `tool swarmmind_nodes '{}'`；`mcheck '.result.isError==false'`；`jq -er '.result.content[0].text' "$T/$ID.mcp" > "$T/mcp.nodes.txt"`；`grep -F "$N1：在线" "$T/mcp.nodes.txt"`；`tool swarmmind_agents '{}'`；`mcheck '.result.isError==false'`；`jq -er '.result.content[0].text' "$T/$ID.mcp" > "$T/mcp.agents.txt"`；`grep -F "qa-cat @ $N2" "$T/mcp.agents.txt"` | 节点在线/负载与 REST 一致；能力来自已暴露目录 | P0 冒烟 |
| TC-48 | swarmmind_submit / status / wait | qa-cat、qa-sh 可调用 | 执行下方“MCP 派单与等待命令” | MCP 可提交、查状态、等待 completed；失败也是终态；等待超时返回当前状态且不伪造终态 | P0 冒烟 |
| TC-49 | swarmmind_artifact | TC-17 | `tool swarmmind_artifact "$(jq -nc --arg id "$ART" --argjson i "$TXT_INDEX" '{task_id:$id,index:$i}')"`；`mcheck '.result.isError==false and (.result.content[0].text\|contains("SWARM_ARTIFACT_OK"))'`；`tool swarmmind_artifact "$(jq -nc --arg id "$ART" --argjson i "$BIN_INDEX" '{task_id:$id,index:$i}')"`；`mcheck '.result.isError==false and (.result.content[0].text\|contains("二进制")) and (.result.content[0].text\|contains("控制台"))'` | 文本回读；含 NUL 的二进制只提示经控制台取回；不把二进制当文本交付 | P1 核心 |
| TC-50 | MCP needs_review 语义 | TC-29；核对后状态仍 needs_review | `tool swarmmind_status "$(jq -nc --arg id "$REVIEW" '{task_id:$id}')"`；`mcheck '.result.isError==false and (.result.content[0].text\|contains("结果不确定")) and (.result.content[0].text\|contains("人工核对"))'`；`START=$SECONDS`；`tool swarmmind_wait "$(jq -nc --arg id "$REVIEW" '{task_id:$id,timeout_sec:30,interval_sec:1}')"`；`test "$((SECONDS-START))" -lt 10`；`mcheck '.result.isError==false and (.result.content[0].text\|contains("待人工核对")) and (.result.content[0].text\|contains("请勿"))'` | needs_review 是成功返回的合法终态，立即停止等待；明确结果不确定，不能折叠为 failed | P0 冒烟 |
| TC-51 | MCP 参数、认证和业务错误 | 主 PAT 有效 | 执行下方“MCP 反向命令” | 参数缺失、认证失败、业务不存在均可机器识别；工具业务错误 isError=true；协议错误用 JSON-RPC error | P1 核心 |
| TC-52 | admin_token Bearer 契约一致性 | SPEC 六.六 声明接受 PAT 或 admin_token | `api 200 GET /api/v1/nodes '' root`；`MCP_TOKEN="$ADMIN_TOKEN" tool swarmmind_nodes '{}'`；`mcheck '.result.isError==false'` | REST/MCP 接受文档声明的 admin_token Bearer；若只接受 PAT，必须记为契约失败，不能改测 PAT 冒充通过 | P1 核心 |

MCP 派单与等待命令：

```bash
tool swarmmind_submit "$(jq -nc --arg n "$N2" \
  '{node_id:$n,agent:"qa-cat",stdin:"MCP_TASK_OK"}')"
mcheck '.result.isError==false'
export MCP_TASK="$(jq -er '.result.content[0].text' "$T/$ID.mcp" \
  | grep -Eo 't-[0-9a-f]{32}' | head -n 1)"
test -n "$MCP_TASK"

tool swarmmind_wait "$(jq -nc --arg id "$MCP_TASK" \
  '{task_id:$id,timeout_sec:20,interval_sec:1}')"
mcheck '.result.isError==false and
  (.result.content[0].text|contains("已完成")) and
  (.result.content[0].text|contains("MCP_TASK_OK"))'

tool swarmmind_status "$(jq -nc --arg id "$MCP_TASK" '{task_id:$id}')"
mcheck '.result.isError==false and (.result.content[0].text|contains("MCP_TASK_OK"))'

# wait 等待上限不是任务超时，不能把仍运行的任务改成 failed。
SLOW="$(submit "$N2" qa-sh 'sleep 6; printf "MCP_LATE_OK\n"')"
wait_task "$SLOW" running
tool swarmmind_wait "$(jq -nc --arg id "$SLOW" \
  '{task_id:$id,timeout_sec:1,interval_sec:1}')"
mcheck '.result.isError==false and (.result.content[0].text|contains("等待超时"))'
wait_task "$SLOW" completed

FAILED="$(submit "$N2" qa-sh 'printf "EXPECTED_FAILURE\n" >&2; exit 7')"
tool swarmmind_wait "$(jq -nc --arg id "$FAILED" \
  '{task_id:$id,timeout_sec:20,interval_sec:1}')"
mcheck '.result.isError==false and
  (.result.content[0].text|contains("执行失败")) and
  (.result.content[0].text|contains("退出码：7"))'
```

MCP 反向命令：

```bash
tool swarmmind_submit '{"agent":"qa-cat"}'
mcheck '.result.isError==true and (.result.content[0].text|contains("必填"))'

tool swarmmind_wait '{}'
mcheck '.result.isError==true and (.result.content[0].text|contains("task_id"))'

MCP_TOKEN=wrong tool swarmmind_nodes '{}'
mcheck '.result.isError==true and (.result.content[0].text|contains("401"))'

tool swarmmind_artifact '{"task_id":"t-not-exist","index":0}'
mcheck '.result.isError==true and (.result.content[0].text|contains("404"))'

# SPEC 六.六：业务错误统一 isError=true。
tool swarmmind_status '{"task_id":"t-not-exist"}'
mcheck '.result.isError==true and (.result.content[0].text|contains("不存在"))'

tool swarmmind_unknown '{}'
mcheck '.error.code==-32602'

mcp no_such_method '{}'
mcheck '.error.code==-32601'

{
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"swarmmind_nodes","arguments":{}}}'
} | env -u SWARM_HUB_URL -u SWARM_HUB_TOKEN "$T/mcp" > "$T/unconfigured.rpc"
jq -e 'select(.id==2)|.result.isError==true and
  (.result.content[0].text|contains("未配置"))' "$T/unconfigured.rpc"
```

## 七、协议补充、真实跨云与 CLI

| 编号 | 模块 | 前置条件 | 步骤 | 期望结果 | 优先级 |
|---|---|---|---|---|---|
| TC-53 | 协议、持久化、保留期与故障注入补充 | TC-01；允许运行仓库测试 | 执行下方“协议及存储补充命令” | 精确复跑协议归属、epoch、确认重放、重复 offer、冲突结果、投递窗口、清理、写败不发布、批次创建中断恢复等测试；每组有实际测试记录 | P1 核心 |
| TC-54 | 真实跨云、异构 agent 与断线恢复 | 两台真实云机器、两种真实 agent 已接入本次 hub；完成下方变量准备 | 执行下方“真实跨云命令” | 双向发现→派单→真实 agent 执行→FinalAnswer→产物取回完成；跨云断线恢复及权限拒绝各一次；不得用本机双 node 替代 | P0 冒烟 |
| TC-55 | CLI 五命令兼容 | TC-10、TC-29；admin_token 有效 | `export SWARMMIND_HUB_TOKEN="$ADMIN_TOKEN"`；`"$T/ctl" --hub "$HUB" --json nodes > "$T/cli.nodes.json"`；`jq -e '.nodes\|length>0' "$T/cli.nodes.json"`；`"$T/ctl" --hub "$HUB" --json agents > "$T/cli.agents.json"`；`jq -e '.agents\|length>0' "$T/cli.agents.json"`；`"$T/ctl" --hub "$HUB" --json submit --node "$N1" --agent qa-echo --arg CLI_OK --idem cli-main > "$T/cli.submit.json"`；`C_ID="$(jq -er .task_id "$T/cli.submit.json")"`；`wait_task "$C_ID" completed`；`"$T/ctl" --hub "$HUB" --json query --id "$C_ID" > "$T/cli.query.json"`；`jq -e '.status=="completed" and .result.final_answer=="CLI_OK\n"' "$T/cli.query.json"`；`"$T/ctl" --hub "$HUB" review --id "$REVIEW" > "$T/cli.review.txt"`；`grep -F '请勿重新提交' "$T/cli.review.txt"` | submit/query/agents/nodes/review 均可用；CLI review 展示上下文和指引，不隐式修改终态或重跑 | P1 核心 |

### TC-53：协议及存储补充命令

这些测试用于覆盖普通 REST 请求无法精确制造的帧序、时钟与写盘故障。**它们不能替代前面的真实 hub 功能用例或真实跨云证据。**

```bash
go test -mod=readonly -count=1 -v -timeout=10m \
  ./internal/protocol \
  ./internal/hub/session \
  ./internal/hub/store \
  ./internal/hub/mailbox \
  ./internal/hub/core \
  ./internal/node/ledger \
  ./internal/node/service \
  ./internal/node/runner \
  ./internal/node/conn \
  > "$T/protocol-storage.log" 2>&1

! grep -Eq '^FAIL' "$T/protocol-storage.log"

for name in \
  TestStaleEpochIgnoredThenDisconnect \
  TestOwnershipCheck \
  TestLateAckConvergence \
  TestLateStartedIgnored \
  TestResultArbitration \
  TestDuplicateOffer \
  TestResultConflictStopsResend \
  TestResendOnReconnectRegisteredEvent \
  TestDeliveryWindow \
  TestOfferAckTimeoutFallback \
  TestSubmitIdemBeforeQuota \
  TestSubmitStorageFull \
  TestExpireSweep \
  TestTerminalCleanup \
  TestLedgerCleanupAcked \
  TestLedgerAckRetentionAcrossRestart \
  TestStoreWriteFailureNoPublishNoAck \
  TestLedgerWriteFailureNoPublishNoAck \
  TestControlBatchCrashRecovery \
  TestNodeWorkdirDifferentWorkdirParallel \
  TestHeadOfLineWorkdirSkip \
  TestWorkdirQueueBusyTimeout \
  TestWorkdirFallbackSelfLockReoffer \
  TestRunTimeoutKillsProcessGroup \
  TestTOFURejectsChangedCert \
  TestPinMode \
  TestRegisterAuthFailed \
  TestAuthFailedNoRetry \
  TestFetchArtifactTimeout
do
  grep -F -- "--- PASS: $name (" "$T/protocol-storage.log"
done
printf 'PROTOCOL_STORAGE_OK\n'
```

重点语义：

- 重复 offer 不重复执行，重复结果重放 Ack。
- 结果冲突保留先到终态，停止无意义重传。
- 旧 epoch 帧、非任务归属 node 的事件不能改变任务。
- 7 天过期、24 小时清理使用可控时钟测试，不通过修改真实 hub 快照或系统时间制造。
- 存储写失败不能先发布状态或先确认成功。
- 批次创建中断后只收尾已有子任务，不盲目重跑。
- TLS 指纹改变、身份令牌不匹配必须拒绝连接。

### TC-54：真实跨云命令

该用例的前置环境必须真实存在。环境缺失时记 `BLOCKED TC-54`，不能把回环测试结果填入。

两台云机须满足：

- SSH 别名分别通过 `CLOUD_SSH_A`、`CLOUD_SSH_B` 指定。
- node 已连接当前 hub，两个 node_id 和真实 agent 名称通过下方变量指定。
- 两个真实 agent 均允许 `prompt_text`，工作目录允许写入 runner 指定的产物目录。
- 云机本地 API 为 `127.0.0.1:8445`。
- 本地令牌自动生成于 `~/swarmmind/data/local_token`。
- node 使用 `systemd --user` 单元 `swarmmind-node`；暂停期间无其他业务。
- 远端真实 agent 的 `allow_env` 不包含 `PATH`。
- 云机上只有一个对应测试 node 实例。

准备变量并建立 SSH 转发：

```bash
: "${CLOUD_SSH_A:?设置云机 A 的 SSH 别名}"
: "${CLOUD_SSH_B:?设置云机 B 的 SSH 别名}"
: "${CLOUD_NODE_A:?设置云机 A node_id}"
: "${CLOUD_NODE_B:?设置云机 B node_id}"
: "${CLOUD_AGENT_A:?设置云机 A 真实 agent 名称}"
: "${CLOUD_AGENT_B:?设置云机 B 不同种类真实 agent 名称}"

export CLOUD_LOCAL_A="$(ssh "$CLOUD_SSH_A" 'cat ~/swarmmind/data/local_token')"
export CLOUD_LOCAL_B="$(ssh "$CLOUD_SSH_B" 'cat ~/swarmmind/data/local_token')"

ssh -N -o ExitOnForwardFailure=yes \
  -L 127.0.0.1:18445:127.0.0.1:8445 "$CLOUD_SSH_A" \
  >"$T/cloud-a.ssh.log" 2>&1 &
export CLOUD_TUNNEL_A=$!

ssh -N -o ExitOnForwardFailure=yes \
  -L 127.0.0.1:18446:127.0.0.1:8445 "$CLOUD_SSH_B" \
  >"$T/cloud-b.ssh.log" 2>&1 &
export CLOUD_TUNNEL_B=$!

# 留存两台实际机器身份；验收报告另附不同云厂商/实例证据。
ssh "$CLOUD_SSH_A" 'hostname; uname -a' > "$T/cloud-a.identity"
ssh "$CLOUD_SSH_B" 'hostname; uname -a' > "$T/cloud-b.identity"
wait_node "$CLOUD_NODE_A" true
wait_node "$CLOUD_NODE_B" true
```

定义真实节点本地目录读取与经 hub 管理面的双向派单函数（node 本地 API 只读本机目录；跨机派单与 MCP/A2A 同语义，一律经 hub 发起）：

```bash
cloud_api() {
  local port="$1" token="$2" method="$3" path="$4" body="${5:-}"
  local opts=()
  if test -n "$body"; then
    opts+=(-H 'Content-Type: application/json' --data-binary "$body")
  fi
  curl -fsS --max-time 40 -X "$method" \
    -H "Authorization: Bearer $token" "${opts[@]}" \
    "http://127.0.0.1:$port$path"
}
cloud_roundtrip() {
  local target="$1" agent="$2" marker="$3"
  local prompt tid idx

  prompt="这是 SwarmMind 真实跨云验收。请执行：
1. 在环境变量 SWARM_OUTPUTS_DIR 指向的目录创建 proof.txt，内容严格为 $marker 后接换行。
2. 最终 stdout 答复包含 $marker。
不得只描述计划；必须实际创建文件。"

  tid="$(submit "$target" "$agent" "$prompt" '[]' '{}' "$marker")"
  cp "$T/$ID.body" "$T/$marker.submit.json"
  wait_task "$tid" completed 600
  jq -e --arg m "$marker" '.result.final_answer|contains($m)' "$T/$ID.body"

  idx="$(jq -er '.result.artifacts|to_entries[]|
    select(.value.path|endswith("/proof.txt"))|.key' "$T/$ID.body")"
  api 200 GET "/api/v1/tasks/$tid/artifacts/$idx"
  grep -Fx "$marker" "$T/$ID.body"
  printf '%s\n' "$tid" > "$T/$marker.task-id"
}
```

执行发现、双向派单、权限拒绝和离线恢复：

```bash
cloud_api 18445 "$CLOUD_LOCAL_A" GET /api/v1/agents > "$T/cloud-a.directory.json"
jq -e --arg n "$CLOUD_NODE_A" --arg a "$CLOUD_AGENT_A" \
  'has("network")==false and ([.local[].node_id]|unique)==[$n] and any(.local[];.card.name==$a and .card.exposed)' \
  "$T/cloud-a.directory.json"

cloud_api 18446 "$CLOUD_LOCAL_B" GET /api/v1/agents > "$T/cloud-b.directory.json"
jq -e --arg n "$CLOUD_NODE_B" --arg a "$CLOUD_AGENT_B" \
  'has("network")==false and ([.local[].node_id]|unique)==[$n] and any(.local[];.card.name==$a and .card.exposed)' \
  "$T/cloud-b.directory.json"

cloud_roundtrip "$CLOUD_NODE_B" "$CLOUD_AGENT_B" SWARM_CLOUD_A_TO_B
cloud_roundtrip "$CLOUD_NODE_A" "$CLOUD_AGENT_A" SWARM_CLOUD_B_TO_A

# 权限拒绝必须发生在真实远端执行入口。
DENIED="$(submit "$CLOUD_NODE_B" "$CLOUD_AGENT_B" \
  '不要执行任何操作。' '[]' '{"PATH":"/forbidden"}' cloud-policy-denied)"
wait_task "$DENIED" failed 60
check '.result.error=="policy_denied"'

# 停止 B 的 node 后，经 hub 管理面派单（离线信箱受理）；恢复 B 后完成并取回产物。
ssh "$CLOUD_SSH_B" 'systemctl --user stop swarmmind-node'
wait_node "$CLOUD_NODE_B" false

PROMPT='实际创建 $SWARM_OUTPUTS_DIR/proof.txt，内容为 SWARM_CLOUD_OFFLINE_RECOVER 后接换行；最终 stdout 包含 SWARM_CLOUD_OFFLINE_RECOVER。'
CLOUD_OFFLINE="$(submit "$CLOUD_NODE_B" "$CLOUD_AGENT_B" "$PROMPT" '[]' '{}' cloud-offline-main)"
cp "$T/$ID.body" "$T/cloud-offline.submit.json"
api 200 GET "/api/v1/tasks/$CLOUD_OFFLINE"
check '.status=="submitted"'

ssh "$CLOUD_SSH_B" 'systemctl --user start swarmmind-node'
wait_node "$CLOUD_NODE_B" true
wait_task "$CLOUD_OFFLINE" completed 600
check '.result.final_answer|contains("SWARM_CLOUD_OFFLINE_RECOVER")'
CLOUD_INDEX="$(jq -er '.result.artifacts|to_entries[]|
  select(.value.path|endswith("/proof.txt"))|.key' "$T/$ID.body")"
api 200 GET "/api/v1/tasks/$CLOUD_OFFLINE/artifacts/$CLOUD_INDEX"
grep -Fx SWARM_CLOUD_OFFLINE_RECOVER "$T/$ID.body"

printf 'REAL_CROSS_CLOUD_OK\n'
```

## 八、DoD 覆盖对照

SPEC 当前 DoD 位于第七节；第六节及六.五、六.六为接口与交互验收依据。

| SPEC DoD | 核实内容 | 用例 |
|---|---|---|
| 1 | readonly 模式构建、go.sum | TC-01 |
| 2 | 协议、状态机、信箱、账本、runner、白名单、幂等测试复跑 | TC-01、TC-53 |
| 3 | 回环全链路、5 秒完成、FinalAnswer | TC-10、TC-17、TC-36 |
| 4 | 超时及进程组整组终止 | TC-16、TC-53 |
| 5 | 离线信箱与重连补推 | TC-23～TC-25 |
| 6 | 重连、epoch、旧帧隔离 | TC-26、TC-53 |
| 7 | hub 重启、状态与信箱恢复 | TC-27、TC-33 |
| 8 | 同键去重、409、重复 offer 不重复执行 | TC-11、TC-24、TC-33、TC-53 |
| 9 | 白名单及 PATH 拒绝 | TC-14、TC-15、TC-54 |
| 10 | started 崩溃恢复为 needs_review，禁止自动重跑 | TC-29～TC-31、TC-50 |
| 11 | 经 hub（MCP/A2A）发起的双机闭环：真实双机异构 agent、发现并派单、产物经 hub 取回、断线与权限拒绝 | TC-54 |

## 九、清理与证据保留

确认故障注入暂停的远端 node 已恢复，且本地 hub 正常运行。清理只停止本文启动的测试节点和 SSH 转发：

```bash
kill -TERM "$A_PID" "$B_PID"
wait "$A_PID"
wait "$B_PID"

if test -n "${CLOUD_TUNNEL_A:-}"; then kill -TERM "$CLOUD_TUNNEL_A"; fi
if test -n "${CLOUD_TUNNEL_B:-}"; then kill -TERM "$CLOUD_TUNNEL_B"; fi

# 撤销本轮主 PAT；测试证据保留在 T，不打印令牌。
begin CLEANUP
login
api 200 DELETE "/api/v1/auth/tokens/$PAT_ID"
printf 'EVIDENCE_DIR=%s\n' "$T"
```

保留脱敏后的结果清单、响应、任务 ID、构建/测试日志、浏览器 trace、真实云机身份和异构 agent 证据。验收报告必须分别记录功能用例与 DoD 结果；缺失真实跨云证据时不得宣布完整验收通过。

## 增补批次（2026-10-22 凌晨：账户/统筹/群聊/A2A/取消）

### 账户体系（E2E 已覆盖 E01-E02/E13/E17-E18）
- TC-A01 密码登录成功/失败（错误密码 401）
- TC-A02 改用户名/改密码（需当前密码验证，新密码可登录）
- TC-A03 admin_token 恢复通道重置
- TC-A04 PAT 创建（明文一次性）→ Bearer 鉴权 → 撤销 → 撤销后 401

### 统筹模式（E2E 已覆盖 E10/E10b + 实测）
- TC-P01 复合目标自动分解（双机磁盘对比）→ 并行派发 → 汇总报告
- TC-P02 报告含真实数据（非占位回显），needs_review 阻断语义
- TC-P03 Prime 幂等键规则（prime-<term>-<seq>）重派同键不重复执行
- TC-P04 预算护栏（工具 ≤60/墙钟 45min）触发时强制收尾在途任务

### 群聊 MVP（hub 骨架已测 11 项，以下为端到端补充）
- TC-G01 控制台建群（goal+成员）→ 群列表可见
- TC-G02 发言（user/agent/system）→ seq 单调 → 增量拉取正确
- TC-G03 agent 发言超 max_rounds → budget_exhausted 拒绝 + 群内黄条
- TC-G04 关闭群 → concluded 拒收消息（409）→ 重复关闭幂等
- TC-G05 群转任务：group_ref 提交 → system 消息注入 → 终态自动回流（幂等）
- TC-G06 消息体 >8KiB → 400（含中文多字节边界）

### A2A 兼容端点（E2E API 已验证三链）
- TC-A2A-01 GET /.well-known/agent.json 结构与 skills 聚合
- TC-A2A-02 message/send → 任务创建 → tasks/get 状态映射
- TC-A2A-03 未知 method -32601 / 鉴权失败 401
- TC-A2A-04 messageId 幂等（同键同内容不重复建任务，异内容 -32000）

### 任务取消（单测已覆盖，端到端补充）
- TC-C01 running 任务取消 → cancelled(user_cancelled) → 详情页可见
- TC-C02 已终态任务取消 → 409
- TC-C03 取消后同 idem 重派 → 新任务正常执行

### 执行说明补充
- 群/统筹/取消用例依赖 v0.2.x 部署版 hub；E2E 编号沿用 tests/e2e-console.mjs
- 降级演练见 scripts/drill-degrade.sh（P4 Gate 证据）

## 增补批次（2026-10-26：任务筛选 + 双端导航统一，终审必修4 补机械执行步骤）

### hub 导航与目标机器筛选（hub 模式：`node tests/e2e-console.mjs`，E2E_BASE 指向 hub）
- TC-NAV-01（E-NAV/E-NAV2）aside 导航词表 8 项（总览/机器/任务/派发/工作单/群聊/策略容量/设置）名称与顺序逐项断言；aria-current 指向当前页。定位一律用结构化选择器 `aside nav button[data-page=…]`，禁用 `:has-text("任务")` 宽泛匹配。
- TC-NAV-02（E20/E20x）「目标机器」select 筛选：行全部命中所选机器；E20x 真实触发导出按钮→捕获下载→解析导出 JSON，断言仅含所选机器且条数与列表一致（导出与列表同源 filteredTasks 实测，不挂空名）。
- TC-NAV-03（E20b）清除路径 A：select 改回「全部机器」→ 列表恢复、控件值复位。
- TC-NAV-04（E20c）清除路径 B：trace 无命中 + 叠加机器筛选 → 零命中空态点「清除筛选」→ trace 输入与机器 select 双复位、列表全恢复。
- TC-NAV-05（E-401）会话失效回归：登录→退出→登录表单出现→再登录（E16/E17）之外补 401 路径——`context.clearCookies()` 后等轮询 401，登录表单必须出现（登录页指纹缓存不残留业务 DOM）。
- TC-NAV-06（快照消失态）选中机器后该机器从快照消失：选项保留选中项、显示零结果（不静默回退「全部机器」）；`go test ./internal/node/localapi/ -run TestWebTaskFilterBehavior`（node-gone-kept 场景）为行为锁，浏览器级在验收实例构造。

### node 来源筛选与导航（node 模式：`E2E_BASE=<node local API> local_token=<本机管理令牌> node tests/e2e-console.mjs`）
- TC-NTF-01（N01）令牌门登录：local_token 经环境变量传入，`#gate-token` 填入后进入控制台。
- TC-NTF-02（E-NAV3）node 导航词表 4 项（总览/服务/任务/连接）名称与顺序逐项断言（`aside nav button[data-go=…]`）。
- TC-NTF-03（N02）来源筛选行恒在：3 chip（全部/本机派单/hub 下发）空快照也不消失（终审必修2）。
- TC-NTF-04（E-N1/E-N1b）轮询保持筛选：跨一个轮询周期（5s+余量）chip 选中态与行集不变；来源列全部命中所选来源。无已载任务记 SKIP。
- TC-NTF-05（E-N2）翻页叠加筛选：「加载更早」后 chip 仍选中、行集增长、新增行仍命中来源。无翻页入口记 SKIP。
- TC-NTF-06（E-N3）登出→令牌门→再登录后 S.tf 重置：「全部」chip 选中。
- 汇总口径：PASS/SKIP/FAIL 三态分开计，SKIP 注明原因不计通过数，仅 FAIL 判败退出（终审必修4）。

### 渲染行为锁（go test 常驻，不依赖部署实例）
- `go test ./internal/node/localapi/ -run 'TestWebNavFilterStructure|TestWebTaskFilterBehavior'`：双端导航词表/navnum/aria/workspace 结构锁、blankFilters 七字段与五处重建点锁、ORIGIN_WORDS 词典单源锁；myTasksCard 渲染过滤行为锁（chip 计数/留痕归 local/零命中翻页不阻断/空快照激活筛选全程可清除/与派单表单互不干扰）+ footer 口径三数分离（rows/all/total 互异分页场景）+ hub filteredTasks 交集与选项生命周期。

## 增补批次（2026-10-27：派发页参数输入，maturity-dispatch-args-ux 专项）

前置：TC-02/TC-09 已过（已登录、两节点在线、目录断言）；「浏览器执行器」依赖已安装；测试节点 fixture 含 qa-echo（allow_args=true）、qa-cat（allow_args=false）、qa-conv（allow_args=true+stdin_to_arg=true）。行为级单测锁：`go test ./internal/hub/web/ -run 'TestConsoleDispatch'`（args-zone-modes / args-submit-mapping 场景含首尾空白边界断言，本组用例为浏览器级端到端）。

### TC-DSP-01 参数输入区三态呈现、局部更新与字面量边界（hub 模式）

```bash
begin TC-DSP-01
ui hub control '
// 三态之一 allow_args=true（qa-echo）：输入区在场并声明字面量语义
// 两级下拉机械口径：先选机器（#ctrl-node option 值=node_id），agent 选项值=裸能力名（#ctrl-agent）
await page.locator("#ctrl-node").selectOption(E.N1);
await page.locator("#ctrl-agent").selectOption("qa-echo");
await expect(page.locator("#ctrl-args-zone textarea#ctrl-args")).toBeVisible();
await expect(page.locator("#ctrl-args-zone")).toContainText("一行一个参数");
await expect(page.locator("#ctrl-args-zone")).toContainText("字面量");
await page.locator("#ctrl-stdin").fill("DSP 前置描述");
// D22 局部更新：给任务描述 DOM 打标记，后续切换执行者后标记仍在=整页未重建
await page.evaluate(() => { document.querySelector("#ctrl-stdin").__dsp = 1; });
// 三态之二 allow_args=false（qa-cat）：无输入框+说明文案；任务描述 DOM 原样、草稿保留
await page.locator("#ctrl-agent").selectOption("qa-cat");
await expect(page.locator("#ctrl-args-zone textarea")).toHaveCount(0);
await expect(page.locator("#ctrl-args-zone")).toContainText("该能力未声明接受任务参数，内容将经 stdin 传递（如能力实际支持，请经 CLI/REST 以 args 传递或升级节点）");
assert.ok(await page.evaluate(() => document.querySelector("#ctrl-stdin").__dsp === 1), "切换执行者不得整页重建（#ctrl-stdin DOM 身份保持）");
assert.equal(await page.locator("#ctrl-stdin").inputValue(), "DSP 前置描述");
// 三态之三 双开（qa-conv）：参数输入区与 stdin_to_arg 转换提示并存
await page.locator("#ctrl-agent").selectOption("qa-conv");
await expect(page.locator("#ctrl-args-zone textarea#ctrl-args")).toBeVisible();
await expect(page.locator("#ctrl-args-zone")).toContainText("描述将作为命令行参数传递给该能力");
// 字面量边界提交：捕获批次应答拿任务号（首尾空白行原样、空行/纯空白行忽略）
let taskId = "";
await page.route("**/api/v1/control-operations", async route => {
  if (route.request().method() !== "POST") return route.continue();
  const response = await route.fetch();
  const j = await response.json();
  taskId = j.items[0].task_id;
  await route.fulfill({ response });   // 先 fulfill 后 dispose：dispose 先行会让 fulfill 拿到已释放的应答（disposed 错误）
  await response.dispose();
});
await page.locator("#ctrl-agent").selectOption("qa-echo");
await page.locator("#ctrl-stdin").fill("DSP 边界描述（走 stdin）");
await page.locator("#ctrl-args").fill("  hello world  \n\n\t\n--verbose");
await page.locator("#control-form button[type=submit]").click();
await expect(page.locator("h1")).toContainText("批次报告");
assert.ok(taskId.length > 0, "批次应答应含 task_id");
console.log("DSP_TASK=" + taskId);
'
DSP_TASK="$(sed -n 's/^DSP_TASK=//p' "$T/$ID.ui.log" | head -1)"
test -n "$DSP_TASK"
# 落库输入逐字核对：首尾空白保留（["  hello world  ","--verbose"]）、空行/纯空白行被忽略、描述走 stdin
api 200 GET "/api/v1/tasks/$DSP_TASK"
jq -e --arg t "DSP 边界描述（走 stdin）" '.input.args == ["  hello world  ", "--verbose"] and .input.stdin == $t' "$T/$ID.body"
# 执行端到端：echo 收到的参数与输入区所见逐字一致（字面量传递闭环）
wait_task "$DSP_TASK" completed 5
# 期望值按 /bin/echo 真实行为推：参数以单空格 join——"  hello world  "（首尾各两空格）
# + join 空格 + "--verbose" = world 与 --verbose 间恰三个空格（od -c 实测非笔误）
check '.result.final_answer == "  hello world   --verbose\n"'
pass
```

### TC-DSP-02 stdin_to_arg 转换端到端与目录声明一致（REST+目录）

```bash
begin TC-DSP-02
# qa-conv（/bin/echo，stdin_to_arg=true）经 stdin 派单：描述整体转单个尾参，echo 原样回显
D="$(submit "$N1" qa-conv 'DSP_CONV_OK' '[]' '{}')"
wait_task "$D" completed 5
check '.result.final_answer=="DSP_CONV_OK\n"'
# 目录声明与 fixture 一致：qa-conv 双开（allow_args=true + stdin_to_arg=true）
api 200 GET /api/v1/directory
jq -e --arg n "$N1" 'any(.directory[]; .node_id==$n and .card.name=="qa-conv" and .card.allow_args==true and .card.stdin_to_arg==true)' "$T/$ID.body"
pass
```

## 增补批次（2026-10-28：崩溃孤儿清扫，maturity-orphan-guard 专项）

前置：沿用「验收实例准备」的隔离环境与「公共执行函数」helper（begin/api/check/submit/wait_task/wait_node/start_a/stop_a 等）。本轮专项仅改一处 fixture：节点 A 的 `node.yaml` agents 追加直执 sleep 条目（comm=文件名 "sleep"，与配置基名同口径，身份校验可命中；sh 类解释器名实分离会走「放过」安全侧，不能作本用例 fixture）：

```bash
# fixture 准备（TC-02 前、节点未启动时）：a/node.yaml agents 段追加
#   - name: qa-sleeper
#     kind: cli
#     command: /bin/sleep
#     workdir: "<同 qa-sh 的 workdir>"
#     config_home: "<同 qa-sh 的 config_home>"
#     agent_args: []
#     timeout_sec: 90
#     allow_args: true
#     allow_env: []
#     description: 崩溃清扫专项 fixture
#     permission_mode: no-shell
```

语义说明：清扫身份证据 = 账本证据三元组（pgid + 组长启动指纹 + 启动周期 bootIdent，spawn 时同事务落账本），恢复清扫在服务就绪前一次性完成并记日志；证据不足/周期不符/指纹不符一律放过（宁可漏杀绝不误杀）。任务终态仍由既有 recover 降级 needs_review/node_restarted_midway 链路收敛，清扫不越权改态。

### TC-ORP-01 崩溃→重启→孤儿组被清扫 + 任务收敛 needs_review

```bash
begin TC-ORP-01
# 1. 派长任务并等 running（qa-sleeper 直执 sleep，组长即组内唯一成员）
ORP_TASK="$(submit "$N1" qa-sleeper '' '["60"]')"
wait_task "$ORP_TASK" running 10
# 2. 读账本已落盘证据（不猜进程树；pgid 由 spawn 时落盘，证据链数据源）
ORP_PGID="$(jq -er --arg t "$ORP_TASK" '.[$t].pgid' "$T/a/data/ledger.json")"
ORP_PS="$(jq -er --arg t "$ORP_TASK" '.[$t].proc_start' "$T/a/data/ledger.json")"
ORP_BI="$(jq -r --arg t "$ORP_TASK" '.[$t].boot_ident' "$T/a/data/ledger.json")"
test -n "$ORP_PGID" && test "$ORP_PGID" -gt 0
test -n "$ORP_PS" && test "$ORP_PS" -gt 0
test -n "$ORP_BI"
# 3. kill -9 node（模拟崩溃）：孤儿组必须仍存活（清扫前提）
kill -9 "$A_PID"
wait_node "$N1" false
sleep 1.2   # 跨秒裕量（微秒指纹下同秒也应命中；留裕量避免时钟粒度假阴性）
kill -0 "$ORP_PGID" 2>/dev/null || { echo "FAIL: node 崩溃后孤儿组应存活"; exit 1; }
# 4. 重启 node：New 内证据校验命中 → 整组清扫，日志记 killed
start_a
wait_node "$N1" true
grep "孤儿清扫 task=$ORP_TASK" "$T/a.log" | grep -q 'killed'
if kill -0 "$ORP_PGID" 2>/dev/null; then echo "FAIL: 清扫后孤儿组仍存活"; exit 1; fi
# 5. 任务收敛 needs_review/node_restarted_midway（既有 recover 链路，零改动）
wait_task "$ORP_TASK" needs_review 15
check '.result.error=="node_restarted_midway"'
# 6. 同 data_dir 第二实例被 flock 拒绝（单实例锁；断言锁冲突原因，配置等错误不得冒充锁证据）
ORP_LOG_BEFORE="$(wc -l < "$T/a.log")"
"$T/node" --config "$T/a/node.yaml" >>"$T/a.log" 2>&1 && { echo "FAIL: flock 应拒绝第二实例"; exit 1; }
tail -n +"$((ORP_LOG_BEFORE + 1))" "$T/a.log" | grep -qi '锁\|flock\|lock' || { echo "FAIL: 第二实例退出原因应含锁冲突"; exit 1; }
stop_a
pass
```

### TC-ORP-02 无孤儿时重启零误伤（账本无进行中条目的回归）

```bash
begin TC-ORP-02
# TC-ORP-01 结束后节点已停、无进行中任务：重启不应有任何清扫动作。
# 只检查本次启动追加的日志增量（a.log 为追加式，历史轮次的清扫记录不算数）
ORP_LOG_BEFORE="$(wc -l < "$T/a.log")"
start_a
wait_node "$N1" true
tail -n +"$((ORP_LOG_BEFORE + 1))" "$T/a.log" | grep -q '孤儿清扫' && { echo "FAIL: 无孤儿时不应出现清扫日志"; exit 1; }
stop_a
pass
```

边界声明：清扫是一次性尽力动作——降级持久化与清扫之间若再次崩溃，内存清单丢失且下次启动条目已终态不再收集（安全侧漏清扫，非必达保证）。pid 复用的确定性负例（同名异实例指纹不符）由单测 TestCleanupOrphanSkipsStartMismatch / TestReapStopsWhenGroupReowned 真进程与注入时序锁定，真机不重复造复用场景。
