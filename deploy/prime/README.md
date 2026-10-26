# Prime 配置与提示词

Prime 是执行任务拆解和结果汇总的 agent 角色。hub 的指定状态与 agent 实际使用的 MCP 配置需要分别设置。

## 全网编排

1. 在目标 node 登记可执行的 agent，确认其命令、工作目录、凭据与权限，并开放网络调用。
2. 在 hub 管理台指定该 agent 为 Prime。
3. 给该 agent 的实际运行环境配置 hub 模式 MCP：`SWARM_HUB_URL` 指向可达管理面，`SWARM_HUB_TOKEN` 使用 PAT。完整客户端示例见 [项目 README](../../README.md)。
4. 从该运行环境调用 `swarmmind_nodes` 与 `swarmmind_agents`，再向目标节点提交一个无副作用的测试任务并等待结果。仅看见 MCP 服务不表示执行链路已验证。

## 仅本机编排

node 主人指定本机 `prime_agent` 后，使用以下环境变量连接 MCP：

```json
{
  "SWARM_TARGET": "node",
  "SWARM_NODE_URL": "http://127.0.0.1:8445",
  "SWARM_NODE_PRIME_TOKEN": "<node data_dir/prime_token 内容>"
}
```

该模式仅调用本机能力。`SWARM_TARGET=node` 不可省略，否则 MCP 默认采用 hub 模式。本机指定或凭据轮换可能使旧凭据失效，应以实际响应和当前文件为准更新客户端。

对于受支持的 zcode 配置布局，本机管理面可自动合并 MCP 配置；检查响应 `wired` 与 `wired_target`。未自动写入时，手动合并返回的 `mcp_config` 并重连客户端，不要假定所有宿主都支持自动配置。

## 提示词维护状态

[PRIME.md](PRIME.md) 保存与运行模板锁定的行为节，其中已包含 Work 工作单流程；旧决策工具名、失败重试指导和跨机文件限制仍与当前能力不一致，不能据此推断运行问题已经解决。配置步骤以本文为准，正确接口语义见 [Prime 设计](../../docs/PRIME-DESIGN.md)。

hub 的运行模板是 `internal/hub/web/app.js` 中的 `PRIME_PROMPT`。修改模板行为时必须同步资产、设计说明与测试。W3 已加入 Work 创建、读取、接管和结果收集策略；这些增补不消除其他旧段落中的差异。
