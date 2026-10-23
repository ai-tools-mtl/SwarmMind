# 任务书：能力配置编辑抽屉化（行内展开 → 弹窗）

任务 ID：t20261023-a4b1
日期：2026-10-23
提出人：伦哥（「这个编辑不能弹窗出现吗？有些输入的你可以使用弹窗啊」）
编排：Hana

## 背景与目标

能力配置的「编辑」目前是表格行内 details 展开（上一轮 t20261023-9546 已统一按钮风格），但 10+ 字段塞在最后一列里，左列大片空白、表单被列宽压住。伦哥钦点改为弹窗。

复用现有抽屉基建：`.overlay`/`.drawer-overlay`/`.drawer`（style.css:139-141，700px 白面板）+ 任务详情抽屉的会话守卫模式（drawerSeq，app.js:849-862）。**视觉与任务详情抽屉同一家族。**

## 交互设计

1. 表格行内「编辑」pill：details 全拆，改 `button('编辑', 'cfg-open:'+i)`（保留上一轮小 pill 档位——`.cfg-edit>summary` 样式迁移为该按钮复用或直接落 `.table-action button` 档，二选一以实现最简为准）。
2. 点击开右侧抽屉：标题「编辑 agent · <name>」，表单沿用 cfgField 字段集（name/command/workdir/timeout_sec/description/config_home/agent_args/allow_env + 三个 cfg-check + 删除），抽屉 700px 内可两列排布（cfg-field 栅格化，窄抽屉回退单列），以截图效果为准。
3. 底部操作条：`取消`（ghost）+ `删除此 agent`（off-btn 危险操作，与取消之间留白）+ `保存`（primary，写「保存（校验+写盘）」口径与现「保存全部」区分：保存只落盘本行，见数据流）。保存成功关抽屉。
4. **取消还原单行**：打开抽屉时对该 agent 做深拷贝快照，取消/Esc/遮罩关闭时还原 `S.cfg.agents[idx]` 为快照并关抽屉（现有行内模式没有单行取消；弹窗交互必须有明确取消语义）。
5. 关闭交互与任务详情抽屉一致：右上关闭按钮必有；遮罩点击/Esc 若现有 drawer 基建已支持则带上，不支持就只做关闭按钮（不为此扩基建）。

## 数据流（不动，只换壳）

- input 同步机制保持：字段改动仍直写 `S.cfg.agents[idx]`（编辑副本即真相），保存仍是 `cfgSave()` 的 PUT 整表协议（/api/v1/local/config），服务端校验/写盘/热重载/pending 分类全部不变。
- **轮询挂起语义保持**：抽屉打开期间（S.cfg.editing 或等价状态为真）全局刷新照常拉数据，但表格 DOM 不重绘（app.js:137 现有守卫迁移到新状态位），抽屉内输入不丢。
- 保存拒绝：alert 服务端错误 + 抽屉保持打开 + 副本保留（现口径）。
- 删除此 agent：沿用现有 cfg-del 协议，成功后关抽屉。
- pending/structural 提示（表格上方 notice）逻辑不动。

## 验证（三层走查）

1. **功能**：资产断言更新（details/summary 旧标记移除锁 + 新 data-act 锁）；cfg 相关 e2e 若有则同步。
2. **页面视觉**：「我的服务」整页截图（1400 + 1276）——表格行内只剩「编辑」pill，无展开痕迹。
3. **深层态走查（本轮重点，逐帧截图存 docs/）**：抽屉打开空态（1400+1276）→ 填入修改 → 点遮罩/取消还原（确认表格值回滚）→ 再开修改 → 保存成功反馈 → 保存被拒（模拟 400，副本保留抽屉不关）→ 编辑中轮询保护（编辑抽屉开着模拟刷新，输入仍在）。每帧人眼核查。
4. `go build ./...` 与 `go test ./... -count=1` 全绿。

## 完成记录

docs/TASK-cfg-drawer.md 末尾追加完成记录（改动清单 + 每帧截图文件名 + 测试结果 + 数据流不变声明）。不要 git commit。

---

## 完成记录（2026-10-23，zcode 实现 + Hana 收尾核验）

> 说明：zcode 会话在深层态走查完成后、写记录前进程中断（transcript 20:12 停更），以下由 Hana 依据 git diff、测试复跑与截图逐一核验后补记。

### 改动清单

| 文件 | 改动 |
|---|---|
| internal/node/web/app.js（±134 行） | ① cfgRow 行内 details 全拆，「编辑」改 `button('编辑','cfg-open:'+i)` 小 pill；② 新增抽屉渲染/开关（复用任务详情 drawer 基建与会话守卫模式），表单两列栅格（窄幅回退单列）；③ 打开时快照单行，取消/Esc/遮罩关闭还原快照；④ 字段同步仍 data-cfg 直写 `S.cfg.agents[idx]`（副本即真相，数据流不变）；⑤ 轮询挂起守卫迁移至抽屉态（编辑中全局刷新照常拉数据、表格 DOM 不动、抽屉输入不丢）；⑥ cfgSave 协议不变（PUT 整表），保存成功关抽屉，被拒抽屉保留副本；⑦ cfg-del 成功后关抽屉 |
| internal/node/web/style.css（+27/-1） | 抽屉内 cfg 表单两列栅格、复选框行间距、底部操作条布局；旧 .cfg-edit 行内展开样式清理 |
| internal/node/localapi/web_cfg_edit_drawer_test.go（新增） | 资产断言：旧 details/summary 编辑标记移除锁 + cfg-open 抽屉标记锁 + 轮询守卫状态位锁（反向禁回归） |

### 深层态走查截图（docs/，Hana 逐帧人眼核查）

- TASK-cfgdrawer-open-1400.png / -open-1276.png：抽屉打开空态，两列表单、取消/删除/保存分区清晰
- TASK-cfgdrawer-editing-1400.png：填改中（描述/timeout 45/allow_env TMPDIR）
- TASK-cfgdrawer-cancel-1400.png：取消还原后 command 回滚 /bin/echo，表格无展开痕迹
- TASK-cfgdrawer-save-ok-1400.png：保存成功反馈
- TASK-cfgdrawer-save-rejected-1400.png：保存被拒（模拟 400）——抽屉保持、副本保留、按钮恢复常态
- TASK-cfgdrawer-poll-guard-1400.png：编辑中模拟全局刷新——输入全部保留
- TASK-cfgdrawer-services-1400.png / -1276.png：我的服务整页（行内只剩编辑 pill）

### 数据流不变声明

字段名、PUT /api/v1/local/config 协议、服务端校验/写盘/热重载/pending 分类、cfg-del 协议全部未动；仅 UI 层从行内 details 迁移为抽屉，新增单行取消还原快照语义。

### 测试结果（Hana 复跑）

`go build ./...` ✅；`go test ./... -count=1` ✅ 15 包全绿 0 FAIL。

### 审查收尾批（2026-10-23，独立 zcode 审查 + Hana 修复）

- 独立审查结论：无必须修；1 条「建议修·优先」+ 3 建议 + 2 可不修（已留档于本节末）。
- 已修：① cfg-del 分支入口补 `if (cfgSaving) return;`（保存 PUT 在途禁删，堵 idx 错位写串行窄窗口）；② cfgMaskClose 补 `if (!cfgEdit) return;` 空守卫（消与任务抽屉遮罩的隐式耦合）；③ 资产断言锁串去行尾中文注释（防文档性改动误报）+ 新增两锁。
- 未修（留档）：抽屉互斥单向（实际不可达且可自愈）；完成记录「样式清理」措辞失准（HEAD 中本无 cfg 专属规则）。
- Hana 自踩自录：Go 反引号原始串 `\n` 不解释换行，跨行锁串改单行锁（第二次踩同款坑，见 web_spacing 轮）。
- 最终：`go build ./...` ✅；`go test ./... -count=1` ✅ 15 包全绿；真机部署 epoch=87。
