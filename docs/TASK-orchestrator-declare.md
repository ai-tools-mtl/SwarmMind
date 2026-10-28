# TASK-orchestrator-declare

> 智行管家指定权变更：客户端（node）声明，中心端（hub）只接收汇聚，不再提供中心端配置入口。
> 伦哥 2026-10-28 下令：「智行管家是客户端设置，中心端接收。不要让中心端来配置这个了。」

## 1. 背景与语义变更

现状（2026-10-23 定案的网页指定）：hub 网页「指定智行管家」按钮 → PUT /api/v1/prime →
hub store PrimeDesignation 持久化。指定权在中心端。

变更为：**声明权在客户端（node），hub 只做接收与汇聚展示**。语义对齐「能力声明」模型：
智行管家是一个 agent 声明的角色（与 exposed/allow_args 同层的自我声明），hub 全网汇聚，
单一声明即生效；多声明 = 配置漂移冲突（无生效管家，回退名字约定 + 控制台警告）；
零声明 = 回退名字约定（能力名含 zcode-dev，向后兼容保留）。

## 2. 方案

### 2.1 协议（internal/protocol）

- AgentCard 增 `Orchestrator bool json:"orchestrator,omitempty"`（node 声明本能力承担统筹）。
- ExecProfile 增 `Orchestrator bool yaml:"orchestrator,omitempty"`（YAML 基线）。

### 2.2 node（客户端）

- agents-state.json 增 Orchestrator 开关覆盖（完全复用 exposed 模式：网页开关叠加 YAML
  基线、先写后见、回归基线时删除覆盖项、重启保持）。
- 新端点 `PATCH /api/v1/local/agents/{name}/orchestrator`（authRead，对齐 exposed）。
- CardsFromProfiles 映射 Orchestrator → AgentCard。
- node 控制台能力行加「智行管家」开关（与 exposed 同款交互），变更即 Sync(SendCardUpdate)。

### 2.3 hub（中心端，只接收）

- Core.PrimeFromDirectory()：扫全网能力卡聚合——0 声明 → nil；恰 1 → 该条目；≥2 →
  冲突态（nil + 冲突清单）。hub 不再做任何写入口。
- GET /api/v1/prime 改聚合输出：`{designation, conflicts}`（designation 兼容旧结构
  {node_id, agent_name}；conflicts 仅冲突态非空）。
- 删 PUT /api/v1/prime（handlePrimeSet）。store.Prime 退役：读取丢弃 + diff 自动清表
  （旧数据无害消亡，无 schema 迁移）。
- hub 网页：移除「指定智行管家」按钮 / prime-manage 弹窗 / prime-save / prime-clear；
  orch-line 改只读展示（生效声明来源 / 冲突警告 / 未指定回退提示）。isPrimeDir 逻辑不变
  （S.prime=null 时回退名字约定）。

### 2.4 文档与测试

- SPEC 六.五 prime 节重写；INTERACTION 同步；TESTPLAN 涉及用例核对。
- 单测：node 开关（agentsstate）、映射断言；hub 聚合三态；PUT 删除后 404/405。
- 回归：build/vet/22 包全绿 + 前端判定测试。

## 3. DoD（验收清单）

1. node 声明开关：网页设某能力为智行管家 → agents-state.json 落盘 → 卡片带
   orchestrator=true → hub 目录可见（声明即时上报）。
2. hub GET /api/v1/prime 三态正确（0/1/≥2 声明）；PUT /api/v1/prime 已删除。
3. hub 网页：无配置入口；展示生效声明来源；冲突态有警告且回退名字约定。
4. 未声明时回退 zcode-dev 名字约定（向后兼容锁）。
5. build/vet/全量测试绿；前端 E2E 不回归。
6. 双端部署（hub 云服务器 + mac-mini node）+ 冒烟。

## 4. 已知陷阱

- AgentCard 协议字段新增必须 omitempty（旧 hub/旧 node 混跑兼容：缺字段=false 不声明）。
- store.Prime 退役不能动 SQLite schema（读丢弃 + diff 清表即可，避免迁移成本）。
- 前端 S.prime 消费点多（isPrimeDir/orch-line/派发页），GET 响应结构保持 designation
  字段兼容，前端最小改动。
- node 控制台开关变更要触发 SendCardUpdate（exposed 同款），否则 hub 目录滞后。
