# REVIEW：node standalone 方案三方审查记录

日期：2026-10-22

## 审查设置

三方并行：Hana（编排裁决）+ zcode（独立审查，GLM）+ codex（OpenAI）。
**codex 因执行环境审批通道未通未能参与本轮**（连续两次不可达），实际两方完成。按协作制度如实记录，codex 恢复后如需可补一轮复审。

## zcode 审查意见与裁决（全 12 条，11 采纳 1 部分采纳）

| # | 意见 | 裁决 |
|---|---|---|
| 1 | 状态机自相矛盾（配置存在但 hub_url 空既进 setup 又进 standalone） | 采纳：三态明确，yaml 解析失败报错退出不静默进 setup；补「断开组网」UI 入口 |
| 2 | 双路径漏恢复/重发链路 origin 过滤（切换组网后 standalone 期结果帧会成批推 hub） | 采纳：账本加 origin(local/hub)，Resend/Reschedule/回传按 origin 过滤，标阻塞项 |
| 3 | 唯一总理声明零执行力（prime_agent 纯装饰；「结构上无环」不成立——本机进程读 local_token 即可派单；不可测） | **采纳并做实**：新增 prime_token（编排专属凭证），local_token 调编排 → 403 可测；安全边界诚实声明为本机用户边界（防不经意环，非恶意模型） |
| 4 | setup 写盘路径歧义（-config 默认 example 文件，写回即事故；data_dir 相对路径 cwd 漂移） | 采纳：写回原 cfgPath；data_dir 绝对路径化 |
| 5 | 接入串校验规则两端漂移 | 采纳：三正则下沉 protocol 包同源 |
| 6 | node MCP 用 if 散落 12 工具会烂 | 采纳：backend interface，hub/node 两实现 |
| 7 | agents 编辑三缺口（yaml 丢注释/漏安全关键字段/重启杀在途任务） | 采纳：yaml.Node round-trip；补 allow_args/allow_env/config_home 编辑+强校验；热重载优先，重启仅留结构性变更 |
| 8 | 验证矩阵编号重复 | 采纳：正向 1-5 / 切换 6-10 / 边界 11-21 |
| 9 | setup 鉴权鸡蛋（免鉴权写盘 = agents.command RCE 面） | 采纳：setup 端点同样验 local_token，页面明示 token 位置 |
| 10 | 重启时序（应答先返回再退出） | 采纳 |
| 11 | origin 是 3.2 阻塞子项；3.4 阻塞于总理语义先定稿 | 采纳，写入开发顺序 |
| 12 | 工作量估算（合计 7-10.5 天） | 采纳，写入开发顺序 |

## Hana（第三方）补充审查

1. **prime_token 做实的论证**：伦哥防环目标是「不经意环」（多 agent 都配 MCP 意外互派），非恶意模型（恶意进程直接跑命令不经 MCP）。凭证分发把不经意环结构性排除；本地_token 调编排 403 让防护可测（矩阵 #14）。
2. **矩阵 #6 语义修正**：原写「组网后新任务走 hub 账本」与双路径模型矛盾，改为「hub 下发任务正常投递」（本机派单永远本地账本）。
3. **local_token 与 prime_token 双真相源收敛**：prime_token 存 data_dir/prime_token（同 local_token 惯例），prime_agent 声明仅展示；轮换一键即时重载+原子写盘。
4. **换总理的运行中影响**：旧总理 MCP 下一请求 401，文档明示换总理三步。

## 最终版

docs/PLAN-node-standalone.md v2（验证矩阵 21 条：正向 5 + 切换 5 + 边界 11）。
