'use strict';
/* SwarmMind Node 本地控制台（Loop 7，INTERACTION 客户端三旅程）。
 * 数据面：本机回环 /api/v1/*（Bearer local_token，令牌持于 localStorage）。
 * 页面：总览（旅程③运维）· 我的服务（旅程①服务提供者：开放开关三态）·
 * 派任务·我的任务（旅程②消费者：本机服务派单 + 留痕跟踪；跨机协作经 hub，
 * node 控制台不含全网目录——方案 v2 边界）。与 hub 控制台同交互闭环
 * （INTERACTION 四：300ms 反馈、终态带下一步、结果未知锁表单；文案口径见
 * PRODUCT-DIRECTION v2 术语翻译表：人话在前，技术态进详情/悬停）。
 * 无框架原生 JS，hash 路由 SPA。 */

const $ = s => document.querySelector(s);
const esc = v => String(v ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

/* ===== md 渲染器 shared block begin（双端同步维护：本块在 node/hub 两端 app.js
 * 逐字节一致，internal/node/localapi/web_md_render_test.go 抽块比对锁一致性——
 * 改本块必须两端同改同验。规格见 docs/TASK-md-render.md。）
 * 架构铁律：先全文 HTML escape，再在已转义文本上解析结构/行内标记——不存在任何
 * HTML 直通路径，agent 输出 <script>/<img onerror> 必然显示为字面文本。 ===== */
function mdToHtml(text) {
  try {
    const src = String(text ?? '');
    if (!src.trim()) return '';
    const escaped = src.replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
    return mdBlocks(escaped.split('\n'), 0);
  } catch (e) { return `<pre>${esc(text)}</pre>`; } // 审查 R1 纵深防御：任何渲染缺陷降级为原文展示，抽屉永不崩
}
function mdBlocks(lines, depth) {
  const out = [];
  let i = 0;
  // 块级起始判定（段落收集据此断行）：围栏/标题/引用/分隔线/列表/表格候选行
  const blockStart = s =>
    /^```/.test(s) || /^#{1,6}\s/.test(s) || /^\s*&gt;/.test(s) ||
    /^\s*(-{3,}|\*{3,})\s*$/.test(s) || /^\s*([-*]|\d{1,9}[.)])\s+/.test(s) || mdIsRow(s);
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) { i++; continue; }
    // 围栏代码块 ```（可带语言标注 → 徽标；内部文本已转义，只直出不解析行内）
    const fence = line.match(/^```([\w#+.-]*)\s*$/);
    if (fence) {
      const buf = [];
      i++;
      while (i < lines.length && !/^```\s*$/.test(lines[i])) buf.push(lines[i++]);
      i++;   // 跳过闭合 ```；未闭合到文末按已闭合处理
      out.push(`<div class="md-code">${fence[1] ? `<span class="md-lang">${fence[1]}</span>` : ''}<pre><code>${buf.join('\n')}</code></pre></div>`);
      continue;
    }
    // 标题 # ~ ######（h1-h6，抽屉内分级字号见 style.css .md-body h1-h6）
    const h = line.match(/^(#{1,6})\s+(.+?)\s*#*\s*$/);
    if (h) { const n = h[1].length; out.push(`<h${n} class="md-h md-h${n}">${mdInline(h[2])}</h${n}>`); i++; continue; }
    // 引用 >（escape-first 后行首 > 为 &gt;）：连续行收齐后递归按块解析；
    // 深度封顶 32（审查 R1：单行数千个 > 的递归爆栈 DoS——超限直接平铺已转义文本）
    if (/^\s*&gt;/.test(line)) {
      const buf = [];
      while (i < lines.length && /^\s*&gt;/.test(lines[i])) buf.push(lines[i++].replace(/^\s*&gt; ?/, ''));
      out.push(`<blockquote class="md-quote">${depth >= 32 ? esc(buf.join('\n')) : mdBlocks(buf, depth + 1)}</blockquote>`);
      continue;
    }
    // 分隔线 --- / ***（单独成行）
    if (/^\s*(-{3,}|\*{3,})\s*$/.test(line)) { out.push('<hr>'); i++; continue; }
    // 列表（无序 -/*、有序 1./1)；两层缩进嵌套（行首 ≥2 空格为二级，更深并入二级）
    const li = line.match(/^( *)([-*]|\d{1,9}[.)])\s+(.*)$/);
    if (li) {
      const items = [];
      while (i < lines.length) {
        const m = lines[i].match(/^( *)([-*]|\d{1,9}[.)])\s+(.*)$/);
        if (!m) break;
        items.push({ d: m[1].length >= 2 ? 1 : 0, mk: m[2], tx: m[3].trim() });
        i++;
      }
      let p = 0;
      // 同层标记类型（有序/无序）一致为一段列表；类型变化即另起列表（- a 后紧跟
      // 1. b 不并入同一 ul，序号不丢）。嵌套层递归消费至遇到更浅缩进。
      const build = depth => {
        let html = '';
        while (p < items.length && items[p].d === depth) {
          const ordered = /^\d/.test(items[p].mk), tag = ordered ? 'ol' : 'ul';
          let body = '';
          while (p < items.length && items[p].d === depth && /^\d/.test(items[p].mk) === ordered) {
            const it = items[p++];
            let inner = mdInline(it.tx);
            if (p < items.length && items[p].d > depth) inner += build(depth + 1);
            body += `<li>${inner}</li>`;
          }
          html += `<${tag}>${body}</${tag}>`;
        }
        return html;
      };
      out.push(build(items[0].d));
      continue;
    }
    // 表格：头行 + |---|---| 分隔行 + 数据行；头/分隔列数不匹配则整块按段落降级
    if (mdIsRow(line) && i + 1 < lines.length && mdIsSep(lines[i + 1])) {
      const head = mdCells(line), sep = mdCells(lines[i + 1]);
      if (head.length >= 2 && head.length === sep.length) {
        const rows = [];
        i += 2;
        while (i < lines.length && mdIsRow(lines[i])) rows.push(mdCells(lines[i++]));
        out.push(`<div class="md-table"><table><thead><tr>${head.map(c => `<th>${mdInline(c)}</th>`).join('')}</tr></thead><tbody>${
          rows.map(r => `<tr>${head.map((_, k) => `<td>${mdInline(r[k] || '')}</td>`).join('')}</tr>`).join('')}</tbody></table></div>`);
        continue;
      }
    }
    // 普通段落：连续非空行合并，段内换行 <br>；块级起始行不并入
    const buf = [];
    while (i < lines.length && lines[i].trim() && !blockStart(lines[i])) buf.push(lines[i++].trim());
    if (!buf.length) { out.push(`<p>${mdInline(lines[i++].trim())}</p>`); continue; }   // 表格降级等孤立行单独成段
    out.push(`<p>${buf.map(mdInline).join('<br>')}</p>`);
  }
  return out.join('\n');
}
// mdInline 行内标记（输入为已转义文本）：`code` 占位保护 → **粗体** → *斜体*
//（两端须紧贴非空白才配对，不配对字面保留）→ [文本](url)（url 白名单 http(s)://
// 与 / 相对路径，其余如 javascript: 降级纯文本原样显示；外链 target=_blank +
// noopener noreferrer）。不支持：图片、脚注、HTML 直通、删除线、强调嵌套。
function mdInline(s) {
  const codes = [];
  s = s.replace(/`([^`\n]+)`/g, (_, c) => `\x00${codes.push(c) - 1}\x00`);
  s = s.replace(/\*\*([^*\s](?:[^*]*[^*\s])?)\*\*/g, '<strong>$1</strong>');
  s = s.replace(/\*([^*\s](?:[^*]*[^*\s])?)\*/g, '<em>$1</em>');
  s = s.replace(/\[([^\]]+)\]\(([^()\s]+)\)/g, (m, label, url) =>
    /^(https?:\/\/|\/(?!\/|\\))/i.test(url)
      ? `<a href="${url}" target="_blank" rel="noopener noreferrer">${label}</a>`
      : m);
  return s.replace(/\x00(\d+)\x00/g, (m, k) => { const c = codes[+k]; return c === undefined ? m : `<code>${c}</code>`; }); // 审查 R3：越界占位原样保留（NUL 伪造防复制错位）
}
// 表格行/分隔行/拆格：mdIsRow 要求首尾竖线全形式；mdIsSep 每格须为 :?-+:?（对齐
// 冒号可带）；mdCells 去首尾竖线后按 | 拆格。
function mdIsRow(s) { return /^\s*\|.*\|\s*$/.test(s); }
function mdIsSep(s) { const c = mdCells(s); return c.length >= 1 && c.every(x => /^:?-+:?$/.test(x)); }
function mdCells(s) { return s.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map(c => c.trim()); }
/* ===== md 渲染器 shared block end ===== */

/* ===== 抽屉展示词典 shared block begin ===== */
/* 双端任务抽屉共用词典（SPEC 六.五「双端 Web 展示一致性契约」第一条，2026-10-25
 * 抽屉统一立）：状态/错误/终态/核对动作四件套两端逐字节同文；node 页面其他区块
 * （列表徽标/总览卡）亦从此取词；hub 群聊/批次等非任务词典（GROUP_STATES 等）留
 * 块外。cancelled 系双端必备：node 本地列表虽只有本机派单留痕，路由一旦打通
 * hub 取消形态立即可达（TASK-drawer-sync 潜伏漂移项）。 */
const STATES = {
  submitted: '已提交', offered: '已投递', acknowledged: '已接收',
  running: '执行中', completed: '已完成', failed: '执行失败', needs_review: '等你确认',
  cancelled: '已取消',
};
const ERRORS = {
  timeout: '执行超过本机配置的超时上限，进程组已整组终止',
  policy_denied: '参数或环境变量超出本机白名单，未执行',
  expired: '离线任务超过 7 天保留期，已判失败',
  agent_not_found: '执行机器的配置已变化，找不到该 agent',
  agent_not_exposed: '目标机器没有开放这个能力，任务没有接收',
  node_restarted_midway: '执行到一半机器重启，结果不确定',
  user_cancelled: '管理员手动取消了此任务，进程已整组终止',
  truncated: '结果超过编码预算，答复或诊断日志已截断',
  mailbox_full: '目标机器的离线暂存已满（条数或字节预算）',
  storage_full: 'hub 存储全局预算已满',
  frame_too_large: '提交体超过单消息 1MiB 上限',
};
const TERMINAL = new Set(['completed', 'failed', 'needs_review', 'cancelled']);
const REVIEW_ACTIONS = { ack_review: '确认结果有效', mark_failed: '标记失败' };
/* ORIGIN_WORDS 任务来源词典（TASK-nav-filter A1 词典同源，SPEC 六.五 文案层单一起源）：
 * 「hub 下发 | 本机派单」双端逐字节同文——来源徽标、筛选 chip、抽屉来源行唯一出处，
 * 不得再复制字面量。 */
const ORIGIN_WORDS = { hub: 'hub 下发', local: '本机派单' };
/* PERM_MODE_NOTES 权限模式人话词典（FE-POLISH P1-03 声明性降格，2026-10-29）：
 * permission_mode 是能力自报的权限机制标签；执行器白名单只约束命令入口（args/
 * env），不参与运行期约束，SPEC「权限模型（诚实声明）」明确白名单不构成 agent
 * 行为隔离——此前「不能改动本机文件」「仅工作目录内可写」是未经执行器验证的
 * 绝对保证，禁再现。词典值只写该声明的意图（不带「不能/仅」绝对断言），
 * permissionNote 统一挂声明性尾注。键=常见映射值（小写命中）；未知值/未声明
 * 同口径兜底，不虚构隔离。双端逐字节同文，UI 词典同源。 */
const PERM_MODE_NOTES = {
  'read-only': '只读使用',
  'workspace-write': '工作目录内写入',
  'danger-full-access': '无附加限制（等同直接运行）',
  'no-shell': '固定命令入口，不开放任意命令',
};
/* permissionNote 声明性一句人话：统一句式「该能力声明为 <mode>（意图：…）；
 * 实际边界取决于执行器权限配置，SwarmMind 白名单不构成 agent 行为隔离」——
 * 只转述声明，不替 agent 承诺边界。 */
function permissionNote(mode) {
  const m = String(mode || '').trim();
  if (!m) return '该能力未声明权限模式；实际边界取决于执行器权限配置，SwarmMind 白名单不构成 agent 行为隔离';
  const note = PERM_MODE_NOTES[m.toLowerCase()];
  return `该能力声明为 ${m}（意图：${note || '本系统未收录该值的解释'}）；实际边界取决于执行器权限配置，SwarmMind 白名单不构成 agent 行为隔离`;
}
/* permLabel 展示行形态：原始值已内嵌于 permissionNote 句中（技术态可核对）。 */
function permLabel(mode) {
  return `权限：${permissionNote(mode)}`;
}
/* summarizeTask 任务列表人话摘要（FE-POLISH 轮2 H11，双端同文）：列表主列给
 * 「能力名+一句摘要」，摘要=任务描述首行截断（stdin 优先，args 首参兜底——
 * Prime 提示词形态 args[0] 首行即标题）；两者皆空如实留空（不由 ID 猜）。
 * 完整 ID/trace 收详情。数据面事实：hub 任务列表响应含 input 字段可算摘要；
 * node 列表响应（taskListItem）不含输入字段——node 端如实不渲染摘要行。 */
function summarizeTask(t) {
  const src = String(t?.input?.stdin || (t?.input?.args || [])[0] || '').split('\n').map(s => s.trim()).find(Boolean) || '';
  return src.slice(0, 48) + (src.length > 48 ? '…' : '');
}
/* 会话词典（T3 双端会话界面，SPEC 六.五「会话与轮次」；W01-05 加法：会话登记态
 * pending + 轮次投影加法值 cancelled——旧六值不改名不删，W01-01 §3）：会话状态
 *（hub active / node open 两键同词）+ 轮次投影态与本机 ok/failed 键（node
 * 内核账本）统一取词。unverified 徽标文案即警示词「工作结果未证实」——绝不
 * 渲染为成功（VISION 诚实呈现契约）；sessBadge 色阶走 sst-* 类（语义色四色预算
 * 内：unverified=heat、failed=danger、running/进行中=live、终态与排队退灰），
 * 样式规则双端同文（web_drawer_sync 锁）。SESS_TURN_ZONES 会话抽屉分区表
 *（头部+绑定/轮次/详情/动作四分区，双端同构），复用「抽屉分区更新 shared
 * block」泛化 names 参数。 */
const SESS_STATES = { active: '进行中', open: '进行中', closed: '已关闭', pending: '登记中（首轮未发布）' };
const SESS_TURN_STATES = {
  pending: '排队中', awaiting_node: '已投递', running: '执行中',
  done: '已完成', ok: '已完成', unverified: '工作结果未证实', failed: '执行失败',
  cancelled: '已取消',
};
const SESS_TURN_SIX = ['pending', 'awaiting_node', 'running', 'done', 'unverified', 'failed'];
const SESS_TURN_SEVEN = SESS_TURN_SIX.concat(['cancelled']);   // W01-03 投影加法值（筛选行全集）
const SESS_TURN_ZONES = ['head', 'binding', 'turns', 'detail', 'actions'];
/* SESS_TURN_INFLIGHT 在途轮投影态集（可取消判定，双端同文）：W01-03 cancel 语义
 * 只对非终态轮成立——终态行（done/unverified/failed/cancelled）不再出取消入口。 */
const SESS_TURN_INFLIGHT = new Set(['pending', 'awaiting_node', 'running']);
/* SESS_CANCEL_WORDS 取消记账码人话（W01-03 §6.3 三态展示，双端同文）：cancelled
 * 投影轮按行 Error 记账码细分 queued/executed；turn_cancel_outcome_unknown 投影
 * failed=待核对（绝不渲染为已停止/成功）。 */
const SESS_CANCEL_WORDS = {
  turn_cancelled_queued: '本轮在进入执行前被撤销（未运行，确定性终止）',
  turn_cancelled_executed: '本轮执行已被终止（进程组已整组回收）',
  turn_cancel_outcome_unknown: '取消已触发，但停止事实未证实——待核对',
};
/* SESS_ERRORS 会话操作错误码人话（W01-03 §7 映射表的数据源，双端同文）：页面错误
 * 提示先查本表；未收录码回落 errLine 原文（技术态不吞）。 */
const SESS_ERRORS = {
  turn_queue_full: '该会话进行中的轮次已达上限（16）：等待在途轮次完成后再投',
  close_in_flight: '会话仍有在途轮次，已拒绝关闭——请先取消或等待在途轮次完成',
  request_conflict: '请求 ID 已被不同内容占用：刷新后重试，勿换内容复用同一 ID',
  turn_seq_conflict: '轮次序号与已有记录冲突：请刷新详情核对后再试',
  turn_in_flight: '已有同号轮次在途：等待其完成',
  session_closed: '会话已关闭：不再接受新轮次',
  turn_already_finished: '轮次已是终态：取消未生效（先到保留）',
  turn_not_found: '没有该轮次的受理记录',
  cancel_unconfirmed: '取消已触发但结果未确认：请稍后查询核对，勿盲目重试',
  workspace_denied: '工作目录越出本机允许的工作区根（node.yaml session_workspace_roots）',
  session_unsupported: '目标未声明会话能力（session_adapter）',
  agent_not_found: '执行机配置已变化，找不到该 agent',
};
/* sessCancelKind 轮行取消态判定（三态展示判定源，双端同文）：返回
 * 'queued'|'executed'|'unknown'|''——cancelled 投影行按 Error 记账码细分；
 * failed 行 Error=turn_cancel_outcome_unknown 为待核对（绝不记成功）。 */
function sessCancelKind(t) {
  const st = String(t?.status || '');
  const err = String(t?.error || '');
  if (st === 'cancelled') return err.startsWith('turn_cancelled_queued') ? 'queued' : err.startsWith('turn_cancelled_executed') ? 'executed' : '';
  if (st === 'failed' && err.startsWith('turn_cancel_outcome_unknown')) return 'unknown';
  return '';
}
/* sessErrHuman 会话错误人话（SESS_ERRORS 查表；组合码取首段，未收录回落原文）。 */
function sessErrHuman(data) {
  const code = String(data?.error || '').split(/[;:]/)[0];
  return SESS_ERRORS[code] || errLine(data);
}
/* sessWsSummary 工作区摘要（列表工作区列，双端同文）：长路径保尾段（执行侧语义
 * 段），完整路径进 title 悬停。 */
function sessWsSummary(cwd) {
  const p = String(cwd || '');
  return p.length > 32 ? '…' + p.slice(-31) : p;
}
function sessBadge(s) {
  return `<span class="badge sst-${esc(s)}" title="${esc(s)}">${esc(SESS_TURN_STATES[s] || SESS_STATES[s] || s)}</span>`;
}
/* sessShortId 技术标识短形式（幂等指纹/追踪号/原生轮次 ID）：12 字符截断，完整值
 * 进 title 悬停；空值兜底「—」（D16 无证据口径）。 */
function sessShortId(s) {
  const t = String(s || '');
  return t ? `<code title="${esc(t)}">${esc(t.slice(0, 12))}…</code>` : '<span class="muted">—</span>';
}
/* SESS_UNVERIFIED_NOTE unverified 警示条正文（双端同文，T1 四路分离口径）：
 * 执行成立 ≠ 工作成功，答复仅供参考。 */
const SESS_UNVERIFIED_NOTE = '本轮执行成立，但 CLI 输出无法证明工作成功——答复仅供参考，重要结论须自行核对后再采纳。';
const SESS_EMPTY_TURNS = '还没有轮次记录。';
/* sessPermOptionsHtml 权限模式断言下拉（双端同文）：缺省「跟随节点配置」；可选项
 * 取自 PERM_MODE_NOTES 并剔除全权档位（danger-full-access——SPEC 六.五 红线：UI
 * 绝不提供全权选项，REST 同值 400 双防线）。显式断言与能力卡声明不符由后端 409
 * 人话拒绝（断言仅供核对，非换绑通道）。 */
function sessPermOptionsHtml(selected) {
  const opts = [['', '跟随节点配置（推荐 · 能力卡声明为准）']];
  for (const [k, note] of Object.entries(PERM_MODE_NOTES)) {
    if (k === 'danger-full-access') continue;
    opts.push([k, `${note}（${k}）`]);
  }
  return opts.map(([v, label]) => `<option value="${v}"${v === (selected || '') ? ' selected' : ''}>${esc(label)}</option>`).join('');
}
/* ===== 抽屉展示词典 shared block end ===== */

/* ===== 抽屉时间工具 shared block begin ===== */
/* 双端抽屉时间口径（D17 裁决：UTC + 显式标注；SPEC 六.五 第四条）：完整时间
 * YYYY-MM-DD HH:mm:ss UTC，经 Date 解析转 UTC 格式化（禁 ISO 串切片后硬贴 UTC
 * 后缀）；realTime 有效时刻判定（D16）：空/Go 零值 0001-01-01/非法串一律「无
 * 证据」。表格/总览等非抽屉面的全站迁移另批（C4），页面旧函数暂留本块外。 */
function realTime(s) {
  if (!s) return false;
  return !String(s).startsWith('0001-01-01') && !Number.isNaN(new Date(s).getTime());
}
function fmtUTC(s) {
  if (!realTime(s)) return '';
  const d = new Date(s), p = n => String(n).padStart(2, '0');
  return `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())} ${p(d.getUTCHours())}:${p(d.getUTCMinutes())}:${p(d.getUTCSeconds())} UTC`;
}
/* ===== 抽屉时间工具 shared block end ===== */

/* ===== 抽屉事件与错误 shared block begin ===== */
/* 双端任务时间线（D15/D16/D20 统一，node 对齐 hub ol.timeline 事件流）：受理/
 * 开始执行/终态/人工核对完成；零值与非法时刻无证据、不生成事件（D16 乱码根因：
 * Go time.Time 零值序列化 0001-01-01T00:00:00Z 穿透，omitempty 对结构体不生效；
 * 阶段 D 终审补全：受理/终态/核对三处一律先过 realTime 完整有效判定，无证据
 * 时刻连事件骨架一起不生成，不留空 <small> 行）；needs_review 终态点为灰点流中
 * 唯一 heat 热点。node 账本无 review 字段，末条自然不出现，两端同源。
 * taskErrorNotice 错误通知条（D5 口径统一）：组合错误码按首段查词典——hub 旧全码
 * 直查遇 `timeout;artifacts_skipped=1` 类组合码落空即误报「执行中断」；人话在前，
 * 原码与退出码（存在时）同行技术态，needs_review 追加一行语境说明。 */
function drawerTimelineHtml(t) {
  const ev = [];
  if (realTime(t.created_at)) ev.push(['任务已受理', fmtUTC(t.created_at), false]);
  if (realTime(t.result?.started_at)) ev.push(['开始执行', fmtUTC(t.result.started_at), false]);
  if (TERMINAL.has(t.status)) {
    const fin = [t.result?.finished_at, t.updated_at].find(realTime);
    if (fin) ev.push([`${STATES[t.status]}`, fmtUTC(fin), t.status === 'needs_review']);
  }
  if (t.review && realTime(t.review.at)) ev.push([`人工核对完成（${REVIEW_ACTIONS[t.review.action] || t.review.action}，${t.review.by}）`, fmtUTC(t.review.at), false]);
  return `<ol class="timeline">${ev.map(([e, time, hl]) => `<li${hl ? ' class="hl"' : ''}>${esc(e)}<br><small>${esc(time)}</small></li>`).join('')}</ol>`;
}
function taskErrorNotice(r, needs) {
  if (!r?.error) return '';
  const human = ERRORS[String(r.error).split(';')[0]] || '执行中断';
  const exit = typeof r.exit_code === 'number' ? ` · 退出码 ${r.exit_code}` : '';
  return `<div class="notice warn"><b>${esc(human)}</b> · <code>${esc(r.error)}</code>${exit}${needs ? '<br>任务执行到一半机器重启，无法确认实际进度。' : ''}</div>`;
}
/* ===== 抽屉事件与错误 shared block end ===== */

/* ===== 抽屉答复与日志 shared block begin ===== */
/* 双端抽屉最终答复与诊断日志渲染（D8/D10/D11/D12 统一，材质基准 e821bf8）：
 * mdAnswer 白底浮起主角，空白输入兜底「（空）」——「无内容」不得做成全抽屉视觉
 * 最重的主体，空答复兜底由分区模板按状态给人话（D8/D9）；drawerLogInner 诊断日志
 * 区（标题「诊断日志（stdout / stderr 尾部）」由分区模板携带）：默认 md 渲染、
 * 恒显 stdout/stderr 两段、空段兜底「（空）」（hub 旧跳过空段形态废除，D11）；
 * 原文视图统一单 pre 双段（stdout:/stderr: 行前缀）。切换态由调用方经参数传入
 * （存各自 S.drawer.logRaw）。诊断日志渲染条件（stdout_tail||stderr_tail）与
 * 「最多保留各 64KiB。」保留期说明由分区模板携带，双端同文。
 * drawerAnswerHtml 最终答复区（阶段 D 必须修 4，D9 状态优先）：终态判定先行——
 * cancelled + result=null 是后端合法态（cancel.go Result 留空），不得落「尚未到
 * 终态」误导兜底，按无答复人话兜底；仅非终态且无结果才显示「尚未到终态」。 */
function mdAnswer(text) {
  return `<div class="md-body md-answer">${mdToHtml(text) || '<p class="muted">（空）</p>'}</div>`;
}
function drawerAnswerHtml(t, res, needs) {
  return `<h3>最终答复</h3>${!res
    ? TERMINAL.has(t.status)
      ? '<p class="muted">该任务没有文本答复（例如等待类任务）；如有文件输出，见下方产物列表。</p>'
      : `<p class="muted">尚未到终态（${esc(STATES[t.status] || t.status)}）。抽屉每 5s 自动刷新，可先去忙别的。</p>`
    : res.final_answer ? mdAnswer(res.final_answer)
    : needs ? '<p class="muted">结果还没确认——这个任务结果拿不准，等你核对。</p>'
    : res.stdout_tail ? '<p class="muted">该任务没有单独的文本答复，输出内容见下方诊断日志与产物。</p>'
    : '<p class="muted">该任务没有文本答复（例如等待类任务）；如有文件输出，见下方产物列表。</p>'}`;
}
function drawerLogInner(res, raw) {
  const head = `<div class="md-log-head"><span class="muted small">${raw ? '当前为原始输出。' : '默认按 Markdown 渲染；空格对齐的进程输出请切原文。'}</span>${button(raw ? '查看渲染' : '查看原文', 'log-raw', 'link')}</div>`;
  if (raw) return `${head}<pre>stdout:\n${esc(res.stdout_tail || '（空）')}\n\nstderr:\n${esc(res.stderr_tail || '（空）')}</pre>`;
  const sec = (label, tail) => `<b class="md-log-label">${label}</b><div class="md-body md-log">${mdToHtml(tail || '') || '<p class="muted">（空）</p>'}</div>`;
  return head + sec('stdout', res.stdout_tail) + sec('stderr', res.stderr_tail);
}
/* promptZoneHtml 任务指令分区（t20261026-webfix 新增；t20261026-notify-unify 改动四
 * 提升进本块双端同文——node Ledger Entry.Task 本就持久化完整任务输入，TaskView 投影
 * 后 node 单查端点含 input，双端同构点亮；t20261026-prompt-args-md 重构内容源判定，
 * 伦哥反馈「任务指令希望支持渲染 md」——Prime 派发的整篇提示词走 input.args，旧
 * 「stdin 空⇒短命令行参数」假设与真实派发形态不符）：args 形态判据=任一参数自身
 * 含换行或 args 总长 >300 字符⇒「提示词文本形态」（md 渲染+查看原文切换）；否则
 * 「短参数形态」（逐参数 code 胶囊保留参数边界）。判据取「参数自身含换行」而非
 * join 含换行：多参数 join('\n') 恒含换行，会把多参数短命令误判成文本形态、丢逐参
 * 胶囊。stdin（控制台/表单派单走 stdin 语义，即原始提示词）默认 Markdown 渲染；
 * stdin+args 并存（旧版 args 被完全忽略的盲区）双小节标题「任务说明（stdin）」
 * 「命令行参数（args · N 段）」共享一个查看原文切换（raw 态单 pre 全部原文，
 * stdin:/args: 行前缀同诊断日志先例；切换态存各自 S.drawer.promptRaw，切换经分区
 * diff 只重写 prompt 分区）；单源任务不显示小节标题（短参数沿用 muted 引言行，
 * 视觉与现状一致）；两者皆空分区省略。底部「任务输入（原样 JSON）」details（hub
 * 独有白名单）保留完整技术细节，两不误。
 * 预览折叠（t20261026-prompt-preview，伦哥反馈「任务指令显示预览，需要时点击全部
 * 显示」）：任务指令总内容（stdin + args join，现有拼接口径）超过 PROMPT_PREVIEW_MAX
 * 字符 ⇒ 内容容器折叠预览（限高+底部渐隐遮罩+「展开全部」按钮），≤阈值全显（现状
 * 形态，无展开按钮）；短参数胶囊形态本就短，走阈值判定前的早退分支不参与折叠。
 * 展开态存 S.drawer.promptExpanded（与 promptRaw 同模式，S.drawer 随抽屉生命周期
 * 重置），渲染函数从状态算 HTML——轮询/切换经分区 diff 天然保持（D22 声明式，无
 * 「重建后 JS 恢复」反模式）；原文态同样折叠（raw 数百行同样长），展开态跨 raw/
 * 渲染两态共享同一个 promptExpanded；双源折叠作用于整个内容容器（一个按钮控制
 * 全部小节）。 */
const PROMPT_PREVIEW_MAX = 1200;   // 折叠判据阈值：约一屏 md 渲染量——超过即预览折叠，未超保持全显（字符数模板可算，无 JS 测量）
function promptZoneHtml(t) {
  const stdin = t.input?.stdin || '';
  const args = t.input?.args || [];
  if (!stdin && !args.length) return '';
  const text = args.join('\n');
  const argsMd = args.some(a => a.includes('\n')) || text.length > 300;
  const dual = !!stdin && args.length > 0;
  const pills = `<p class="cmd-args">${args.map(a => `<code>${esc(a)}</code>`).join(' ')}</p>`;
  if (!stdin && !argsMd) return `<h3>任务指令</h3><p class="muted small">本任务以命令行参数下达（逐参数展示，保留边界）：</p>${pills}`;
  const raw = !!S.drawer?.promptRaw;
  const expanded = !!S.drawer?.promptExpanded;
  const head = `<div class="md-log-head"><span class="muted small">${raw ? '当前为原始输入。' : '默认按 Markdown 渲染；需要看原文请切原文。'}</span>${button(raw ? '查看渲染' : '查看原文', 'prompt-raw', 'link')}</div>`;
  const mdSec = (label, src) => (label ? `<b class="md-log-label">${label}</b>` : '') + `<div class="md-body">${mdToHtml(src) || '<p class="muted">（空）</p>'}</div>`;
  const argsTitle = dual ? `命令行参数（args · ${args.length} 段）` : '';
  const argsSec = !args.length ? '' : argsMd ? mdSec(argsTitle, text) : (dual ? `<b class="md-log-label">${argsTitle}</b>` : '') + pills;
  const body = raw
    ? `<pre>${esc(dual ? `stdin:\n${stdin}\n\nargs:\n${text}` : stdin || text)}</pre>`
    : `${stdin ? mdSec(dual ? '任务说明（stdin）' : '', stdin) : ''}${argsSec}`;
  if (stdin.length + text.length <= PROMPT_PREVIEW_MAX) return `<h3>任务指令</h3>${head}${body}`;
  return `<h3>任务指令</h3>${head}<div class="prompt-preview${expanded ? ' prompt-open' : ''}">${body}${expanded ? '' : '<div class="prompt-fade"></div>'}</div><div class="prompt-foot">${button(expanded ? '收起' : '展开全部', 'prompt-expand', 'link')}</div>`;
}
/* fmtSize 字节数 → 易读大小（FE-POLISH 轮2 H15 产物行，双端同文）：产物行前置
 * 文件名+易读大小（如 1.2 MB）；完整路径/原始字节数/SHA 交由各端产物行详情折叠
 * 承载（hub 行内取回按钮 / node 本机路径指引为 SPEC 六.五白名单差异）。 */
function fmtSize(n) {
  const b = Number(n) || 0;
  if (b < 1024) return b + ' B';
  const units = ['KB', 'MB', 'GB'];
  let v = b, u = -1;
  do { v /= 1024; u++; } while (v >= 1024 && u < units.length - 1);
  return (v >= 100 ? Math.round(v) : v.toFixed(1)) + ' ' + units[u];
}
/* ===== 抽屉答复与日志 shared block end ===== */

/* ===== 抽屉分区更新 shared block begin ===== */
/* 双端抽屉稳定 DOM 机制（D22 硬验收，SPEC 六.五 第三条局部更新铁律）：首次打开
 * 建壳（drawerShellHtml 稳定分区容器），刷新一律 drawerZonesApply 分区 diff——
 * 分区 HTML 与缓存一致即跳过（数据无变化不碰 DOM：details 开合、原文模式、输入
 * 焦点、滚动位置随未动分区自然保持）；drawerZoneSwap 替换分区时按序回放 details
 * 开合态（内容变化不收回用户展开）。迟到防护在各自数据通路（hub 层世代/抽屉生命
 * 周期，node drawerSeq/drawerReqSeq），本块只做「缓存对不上才写」最后一道；入场
 * 动画只随壳播放一次，分区更新不重放。names 缺省 DRAWER_ZONES（任务抽屉，FE-POLISH
 * 轮2 H13 五段化定序：指令→事实与下一步→结果与可取文件→异常核查→折叠证据；
 * evidence 段承载身份 trace/提交人/恢复密钥与证据段头）；同机制其他长驻视图
 *（P5 W3 hub 工作单抽屉）传自有分区表复用本机制（2026-10-25 双端同改同验，
 * SPEC 六.五 分区更新机制单一来源）。 */
const DRAWER_ZONES = ['head', 'prompt', 'intro', 'related', 'answer', 'artifacts', 'fail', 'review', 'evidence', 'input', 'logs', 'timeline', 'next'];
function drawerShellHtml() { return DRAWER_ZONES.map(z => `<div data-zone="${z}"></div>`).join(''); }
function drawerZoneSwap(el, html) {
  const open = [...el.querySelectorAll('details')].map(d => d.open);
  el.innerHTML = html;
  [...el.querySelectorAll('details')].forEach((d, i) => { if (open[i]) d.open = true; });
  /* 分区 one-shot 淡入（FE-POLISH B4，2026-10-29）：触发被缓存 diff 天然门控——
   * 数据没变不进本函数，轮询不重放；打开抽屉首轮建壳填充即全分区一次性淡入。
   * 动画在容器不在子节点，details 开合回放不受影响；reduced-motion 由 CSS 降级。 */
  el.classList.remove('zone-in'); void el.offsetWidth; el.classList.add('zone-in');
}
function drawerZonesApply(root, zones, cache, names = DRAWER_ZONES) {
  let touched = false;
  for (const name of names) {
    const html = zones[name] || '';
    if (cache[name] === html) continue;
    const el = root.querySelector(`[data-zone="${name}"]`);
    if (!el) continue;
    cache[name] = html;
    drawerZoneSwap(el, html);
    touched = true;
  }
  return touched;
}
/* ===== 抽屉分区更新 shared block end ===== */

/* ===== 通知表单与词典 shared block begin（双端同步维护：通知渠道编辑表单骨架、
 * 类型/消息格式词典与弹层头部 modalHead 双端逐字节同文，
 * internal/node/localapi/web_drawer_sync_test.go 抽块比对锁一致性——改本块必须
 * 两端同改同验。t20261026-notify-polish。）
 * · 双端一致三层（2026-10-26 伦哥裁决）：本块承载渲染层与文案层；操作逻辑层
 *   （弹层形态=居中 modal、表单结构、卡片位置）由代码结构承载，不进块内。
 * · 字段区恒定骨架（webhook 类型）：消息格式 / 推送地址 / 自定义请求头三件恒显
 *   恒位，切格式只变 label 文本与 disabled，#nf-type-fields 高度不变（零跳动）；
 *   格式相关说明独立 #nf-hint 槽位（位置恒定，内容随格式切换，不占字段区高度）。
 * · 类型枚举表参数化：NOTIFY_TYPES 为全集词典；端上可见类型键经
 *   notifyFormHtml(ch, typeKeys) 传入（hub 缺省=全集；node 传 NOTIFY_TYPE_KEYS
 *   不含 inbox——站内通知为 hub 铃铛专属，如实口径不在词典上分叉）。
 * · 飞书格式：headers textarea 恒显但 disabled（协议不支持自定义头），行内说明进
 *   label；切走再切回经 nfSecFromDom 保留已输入地址与请求头。 ===== */
const NOTIFY_TYPES = {
  email: { label: '邮件', form: '需要 SMTP 服务器、端口、账号、密码。端口 465=TLS 直连，587=STARTTLS；明文端口（25）不发送认证。' },
  webhook: { label: 'Webhook', form: '任意能收 JSON POST 的 HTTP 地址；可选自定义请求头（每行一个「名称: 值」）。' },
  inbox: { label: '站内通知', form: '无需配置。终态事件进入顶栏铃铛（只存在内存里，重启后清空）。' },
  sms: { label: '短信', form: '' },
};
/* 消息格式词典（webhook 类型专用维度）：generic=通用 JSON POST（结构化载荷+可带
 * 自定义头）；feishu=飞书自定义机器人文本消息（不带自定义头——飞书机器人不读）。 */
const NOTIFY_FORMATS = { generic: '通用 JSON', feishu: '飞书机器人' };
/* 飞书格式 hint（t20261026-webfix 修正口径沿用：桌面端精确路径 + 堵「去开发者后台
 * 建应用」岔路）。 */
const FEISHU_HINT = '路径：电脑端进群 → 右上角「···」→ 设置 → 群机器人 → 添加机器人 → 自定义机器人（手机端添加不了）。创建后把 Webhook 地址贴进来——这是群内创建的机器人，不需要去开放平台开发者后台建应用。';
const NOTIFY_EVENT_OPTS = [
  ['task_final', '全部终态（完成 / 失败 / 等你确认 / 取消）'],
  ['task_failed', '仅失败'],
  ['task_needs_review', '仅等你确认（结果拿不准）'],
];
/* modalHead 弹层头部（id="layer-title" 供对话框 aria-labelledby 指向；× 关闭动作
 * 双端同词 act=close。× 按钮为内联字面量：双端 button() 助手签名不同（hub 4 参
 * extra / node 5 参 inner+attr），带 aria-label 的调用无法同文，故不经助手）。 */
function modalHead(title) {
  return `<div class="row"><h2 id="layer-title">${title}</h2><button type="button" data-act="close" class="close" aria-label="关闭弹层">×</button></div>`;
}
/* NOTIFY_DELIV 渠道投递状态文案词典（FE-POLISH P0-3「告警通道失效不告警」双端
 * 同源）：渠道卡「最近成功/最近失败+原因」事实行与修复入口按钮词的唯一出处；
 * 时间格式化与 DOM 组装留各端（button() 签名双端不同，见 modalHead 注）。
 * 状态数据（last_ok_at/last_fail_at/last_fail_reason）随渠道列表响应下发。 */
const NOTIFY_DELIV = { ok: '最近成功投递', fail: '最近投递失败', fix: '修复（检查配置）' };
/* notifyTypeOptionsHtml 类型下拉选项（typeKeys=端上可见类型键序，缺省=全集；
 * sms 在枚举中占位，option disabled 灰显「需接入服务商，暂未开放」）。 */
function notifyTypeOptionsHtml(type, typeKeys) {
  return (typeKeys || Object.keys(NOTIFY_TYPES)).map(k => `<option value="${k}"${k === type ? ' selected' : ''}${k === 'sms' ? ' disabled title="需接入服务商，暂未开放"' : ''}>${esc(NOTIFY_TYPES[k].label)}${k === 'sms' ? '（暂未开放）' : ''}</option>`).join('');
}
/* notifyHintHtml 格式相关说明槽内容（独立于字段区：切换不改字段区高度）。 */
function notifyHintHtml(type, format) {
  if (type === 'webhook') {
    const fmt = NOTIFY_FORMATS[format] ? format : 'generic';
    return fmt === 'feishu' ? FEISHU_HINT : NOTIFY_TYPES.webhook.form;
  }
  return NOTIFY_TYPES[type]?.form || '';
}
/* notifyFormHtml 渠道编辑弹层内容（新建 ch=null；typeKeys=端上可见类型键序）。
 * 类型/格式切换只局部重绘 #nf-hint 与 #nf-type-fields（nfRepaint），名称与事件
 * 勾选不被冲掉（局部刷新纪律）。 */
function notifyFormHtml(ch, typeKeys) {
  const type = ch?.type || 'webhook';
  const format = ch?.format || 'generic';
  const hint = notifyHintHtml(type, format);
  return `${modalHead(ch ? '编辑通知渠道 · ' + esc(ch.name) : '添加通知渠道')}
  <form id="notify-form" class="fields" onsubmit="return false">
    <input type="hidden" id="nf-id" value="${esc(ch?.id || '')}">
    <label>渠道类型
      <select id="nf-type">
        ${notifyTypeOptionsHtml(type, typeKeys)}
      </select>
    </label>
    <label>渠道名称（必填）<input id="nf-name" value="${esc(ch?.name || '')}" placeholder="例如：伦哥的飞书群" maxlength="64" required></label>
    <p class="muted small" id="nf-hint"${hint ? '' : ' hidden'}>${esc(hint)}</p>
    <div id="nf-type-fields">${notifyTypeFieldsHtml(type, format, ch?.secrets || {})}</div>
    <fieldset class="nf-events"><legend>什么时候通知（全不勾 = 全部终态都通知）</legend>
      ${NOTIFY_EVENT_OPTS.map(([k, label]) => {
        const on = !ch || (ch.events || []).includes(k);
        return `<label class="checkline"><input type="checkbox" class="nf-event" value="${k}"${on ? ' checked' : ''}> ${esc(label)}</label>`;
      }).join('')}
    </fieldset>
    <div class="actions">${button(ch ? '保存修改' : '创建渠道', 'notify-save', 'primary')}${button('取消', 'close', 'link')}</div>
  </form>
  <p class="footer-note">凭证只保存在本机（data_dir/notify_channels.json，0600）；密码与自定义头的值以 ****** 显示，保存原样传回即不修改；自定义头以保存时的提交内容为准——界面上删掉的行就是删掉了。</p>`;
}
/* notifyTypeFieldsHtml 恒定骨架字段区（#nf-type-fields，容器高度切换不变）：
 * webhook=消息格式/推送地址/自定义请求头三件恒显恒位，切格式仅 label 文本与
 * headers 的 disabled 变化；飞书格式 headers disabled（协议不支持）。编辑回填：
 * password 与自定义头的值显示 ****** 占位（保存原样传回 = 不修改，GitHub 式
 * 语义）；内嵌登录凭证（user:pass@host）的地址整串显示 ******；其余地址类字段
 * 明文回填可核对。 */
function notifyTypeFieldsHtml(type, format, sec) {
  const v = k => esc(String(sec[k] ?? ''));
  if (type === 'webhook') {
    const fmt = NOTIFY_FORMATS[format] ? format : 'generic';
    const feishu = fmt === 'feishu';
    const headers = Object.keys(sec).filter(k => k.startsWith('header.')).map(k => esc(String(k.slice(7)) + ': ' + sec[k])).join('\n');
    return `<label>消息格式<select id="nf-format">
        ${Object.keys(NOTIFY_FORMATS).map(k => `<option value="${k}"${k === fmt ? ' selected' : ''}>${esc(NOTIFY_FORMATS[k])}</option>`).join('')}
      </select></label>
    <label>${feishu ? '飞书机器人 Webhook 地址' : '推送地址'}<input id="nf-url" value="${v('url')}" placeholder="${feishu ? 'https://open.feishu.cn/open-apis/bot/v2/hook/xxx' : 'https://example.com/hook'}" autocomplete="off" spellcheck="false"></label>
    <label>${feishu ? '自定义请求头（飞书机器人协议不支持自定义请求头）' : '自定义请求头（可选，每行一个「名称: 值」）'}<textarea id="nf-headers" rows="2" placeholder="Authorization: Bearer xxx" spellcheck="false"${feishu ? ' disabled' : ''}>${headers}</textarea></label>`;
  }
  if (type === 'email') {
    return `<label>SMTP 服务器<input id="nf-host" value="${v('host')}" placeholder="smtp.example.com" autocomplete="off" spellcheck="false"></label>
      <div class="form-grid">
        <label>端口<input id="nf-port" value="${v('port')}" placeholder="465" inputmode="numeric" autocomplete="off"></label>
        <label>账号<input id="nf-username" value="${v('username')}" autocomplete="off" spellcheck="false"></label>
      </div>
      <label>密码 / 授权码<input id="nf-password" type="password" value="${v('password')}" placeholder="${sec.password ? '保持 ****** 即不修改' : '邮箱授权码'}" autocomplete="new-password"></label>
      <div class="form-grid">
        <label>发件人<input id="nf-from" value="${v('from')}" placeholder="alert@example.com" autocomplete="off" spellcheck="false"></label>
        <label>收件人<input id="nf-to" value="${v('to')}" placeholder="you@example.com" autocomplete="off" spellcheck="false"></label>
      </div>`;
  }
  if (type === 'inbox') {
    return `<div class="notice">站内通知无需配置：任务终态会出现在中心端控制台顶栏的铃铛里。提醒：只存在内存中，hub 重启后清空。</div>`;
  }
  return `<div class="notice warn">短信渠道需接入服务商，暂未开放。</div>`;
}
/* nfRepaint 通知表单局部重绘（D2）：类型/格式切换只写 #nf-hint 与
 * #nf-type-fields 两个槽位，名称/事件勾选/按钮区不碰；空说明整槽隐藏（不占位）。 */
function nfRepaint(type, format, sec) {
  const hint = notifyHintHtml(type, format);
  const hp = $('#nf-hint');
  if (hp) { hp.textContent = hint; hp.hidden = !hint; }
  const box = $('#nf-type-fields');
  if (box) box.innerHTML = notifyTypeFieldsHtml(type, format, sec);
}
/* nfSecFromDom 读当前地址与请求头输入原值（格式切换回填用：切走再切回不丢已
 * 输入；原样不 trim——回填保真，保存时才收口）。 */
function nfSecFromDom() {
  const sec = { url: $('#nf-url')?.value || '' };
  for (const line of ($('#nf-headers')?.value || '').split('\n')) {
    const idx = line.indexOf(':');
    if (idx <= 0) continue;
    const hname = line.slice(0, idx).trim(), hval = line.slice(idx + 1).trim();
    if (hname) sec['header.' + hname] = hval;
  }
  return sec;
}
/* collectNotifyForm 从弹层 DOM 收集请求体；headers 文本经 nfSecFromDom 按行解析
 * 为 header.<名> 键（地址保存时才 trim 收口）。 */
function collectNotifyForm() {
  const id = $('#nf-id')?.value || '';
  const type = $('#nf-type')?.value || '';
  const name = $('#nf-name')?.value.trim() || '';
  const events = [...document.querySelectorAll('.nf-event:checked')].map(c => c.value);
  let format = '', secrets = {};
  if (type === 'webhook') {
    format = $('#nf-format')?.value || 'generic';
    secrets = nfSecFromDom();
    secrets.url = secrets.url.trim();
  } else if (type === 'email') secrets = {
    host: $('#nf-host')?.value.trim() || '', port: $('#nf-port')?.value.trim() || '',
    username: $('#nf-username')?.value.trim() || '', password: $('#nf-password')?.value || '',
    from: $('#nf-from')?.value.trim() || '', to: $('#nf-to')?.value.trim() || '',
  };
  return { id, type, format, name, events, secrets };
}
/* ===== 通知表单与词典 shared block end ===== */

/* formError 错误就地留痕（FE-POLISH 轮2 A3，与 hub 同机制同文案口径）：失败条留
 * 在通知渠道表单顶部（notice.danger：人话原因+重试指引），toast 可同时短报；
 * 同表单重复失败先摘旧条再插；草稿一律不清。 */
function formError(title, bodyHtml) {
  const f = $('#notify-form');
  if (!f) return;
  $('#notify-form-error')?.remove();
  f.insertAdjacentHTML('afterbegin', `<div class="notice danger" id="notify-form-error" role="alert"><b>${esc(title)}</b> · ${bodyHtml}</div>`);
}
/* 通知渠道词典（类型全集/消息格式/事件订阅）与表单骨架已收进「通知表单与词典
 * shared block」（上方），双端同文。node 端差异只有可见类型键序（类型枚举表
 * 参数化，2026-10-26 notify-polish 改动二）：无 inbox——站内通知为 hub 控制台
 * 铃铛专属，服务端准入同样拒绝（如实口径不破坏）；sms 占位灰显未开放。 */
const NOTIFY_TYPE_KEYS = ['email', 'webhook', 'sms'];

/* ---------- 全局状态 ---------- */
const TOKEN_KEY = 'swarmmind_node_token';
// 列表分页（codex 审查④容量边界）：每页条数与后端 tasksPageDefault 同值，显式
// 传递——后端默认变更不影响前端契约；更早历史经 offset 翻页（加载更早）。
const TASKS_PAGE = 200;
const S = {
  token: localStorage.getItem(TOKEN_KEY) || '',
  authed: false,
  page: 'overview',
  nodeID: '',
  status: null,            // GET /api/v1/local/status
  agents: [],              // GET /api/v1/local/agents（本机 ExecProfile 视图）
  tasks: [],               // GET /api/v1/local/tasks 当前已载入的列表（首页+「加载更早」历史页；账本行自带 status+origin，留痕补尾行无 status）。轮询只刷新首页、历史保序并入，不整列表覆盖（复审②-1）
  tasksTotal: 0,           // 列表合并总行数（后端 total）：「已载 X / 共 Y 条」与翻页依据
  tasksMoreBusy: false,    // 「加载更早」在途标志（防双击重复翻页；只由翻页流程自身解除，轮询不得提前复位——复审②-2）
  tasksGen: 0,             // 列表代次：loadAll 每次写入列表态 +1；翻页应答按发起时代次校验，轮询刷新后旧应答作废（复审②-2）
  catalog: [],             // 派单目录（本机 exposed 能力；node 无全网视图）
  snap: '',                // 轮询数据指纹：未变化不重绘
  drawer: null,            // 打开的任务详情 {id, timer}
  f: { stage: 'idle', target: '', stdin: '', idem: newIdem(), message: '' },   // 派单表单状态机
  tf: { origin: '' },      // 「我的任务」来源筛选（TASK-nav-filter A1；''=全部/'local'/'hub'）。渲染层过滤已载入条目；生命周期同会话边界复位（resetPrimeState：登出/401/换令牌重置；轮询/翻页/页面往返保留）
  focusForm: false,
  wiz: { join: '', preview: null, stage: 'idle', msg: '', written: '' },       // 接入向导状态机（方案 3.3）
  primeNew: '',            // 统筹凭证轮换后的新凭证（仅此一次展示），
  primeNewWire: null,      // 轮换自动接线结果（t20261023-62a2）：{target} 命中 zcode 布局；null=降级手动配给
  primeDecl: { picking: false, busy: false, done: null },  // 网页钦定流程（方案 3.2.1）：picking=选择视图展开；done={agent,token,mcp,wired,target,orch,orchErr,synced} 钦定完成一次性展示（orch/orchErr/synced=声明联动三态原始值，P1-02；orch/synced 缺省=应答未带该位，完成页不作声明承诺）
  mcpAccess: { loaded: false, unavailable: false, enabled: null, credential: '', scope: '', primeAgent: '', updatedAt: '' },  // MCP 编排授权（W06-03 P08：GET /api/v1/local/mcp/access 视图；enabled null=未加载；credential 恒为服务端脱敏引用，本端不再脱敏）
  mcp: { form: null, view: null, apply: null, board: null, boardErr: '', boardLoading: false, lastV0: null, lastV2: null },   // MCP 接入子页（W03-04 P07）：form=最近一次预览的表单快照；view=最近预览应答；apply=最近应用结果（含 revert_id，仅本页会话）；board=验证板应答；lastV0/lastV2=最近检查结果
  cfg: { loaded: false, unavailable: false, agents: [], pending: false, supervised: true,
         restart_cmd: '', config_path: '', structural: [], msg: '' },  // 能力配置编辑（方案 3.5）；编辑会话在模块级 cfgEdit（t20261023-a4b1 抽屉化）
  sessionEpoch: 0,         // 会话代次（第五轮收敛 3）：login/logout/401/换令牌时 +1（resetPrimeState 统一挂点）；
                           // 在途请求发起时捕获，应答返回时代次不一致 = 迟到应答，丢弃不写状态
  notifyChannels: [],      // 通知渠道列表（已脱敏形态；三模式装配后恒可用，status.notify_available 门控）
  notifyLoading: false,    // 通知区块加载态（D2 模式：局部更新）
  notifyError: '',         // 通知渠道加载失败信息（空=正常）
  notifyLoaded: false,     // 本会话已拉取过渠道列表（进 services 页拉一次，变更后重拉）
  notifyAlso: null,        // 「同时通知」开关（notify-unify 改动三；null=未加载，networked toggle 用）
  notifyAlsoSaving: false, // 开关保存中（codex 审查必修 2：保存态进渲染，toggle 置灰显示目标值）
  notifyAlsoPending: null, // 保存中的目标值（保存成功/失败落定即清空；null=无在途保存）
  sessions: [],            // 本机会话列表（agent 会话面：内核快照 ∪ pending 投影；W01-05）
  sessLoaded: false,       // 会话列表首载落定标记（T3 R1 建议3）：false=本轮会话尚无任何应答（加载中），不冒充「还没有会话」
  sessUnsupported: false,  // 本机未装配会话内核（503 session_unsupported 诚实降级专态）
  sessError: '',           // 会话列表最近一次加载失败信息（空=正常）
  sessFilter: '',          // 会话列表筛选（''=全部 / 登记 + 七值轮投影态；W01-05 P01）
  sessQuery: '',           // 会话列表搜索词（W01-05 P01：标题/agent/工作区/ID 客户端过滤）
  sessId: '',              // hash 路由 #/sessions/{id} 的会话 ID（W01-05 P02 独立详情页；空=列表主入口）
  sessPage: null,          // 详情页视图状态 {id, session, zones, sel}（W01-05 P02；fetchSessionPage 驱动）
  sessModal: false,        // 新建/继续会话居中 modal 在开（W01-05：表单迁入 modal，草稿跨开关保留）
  sess: freshSessForm(),   // 新建/继续表单状态机（C 形；与派单表单 f 同域）
  sessDrawer: null,        // 打开的轮次执行明细抽屉（W01-05：旧会话抽屉退役）{id, turn, zones, seq, returnFocus}
  loadError: '',           // 最近一轮 loadAll 失败信息（A4 五态：失败不冒充空列表；成功即清）
};
/* 列表五态占位（FE-POLISH 轮2 A4，与 hub 同文案口径）：「加载中」「加载失败可重试」
 * 不再让失败冒充「还没有任务」。 */
function listLoadingBox(label) {
  return `<div class="empty"><h2>正在加载${esc(label)}…</h2><p class="muted">正在从本机 API 读取；这一步只读、不产生任何变更，几秒内自动就绪。</p></div>`;
}
function listErrorBox(act, label) {
  return `<div class="empty"><h2>${esc(label)}加载失败</h2><p class="muted">最近一次读取没有成功（本机 API 不可达或应答异常），不是没有数据。可以点重试，也可以等轮询自动恢复。</p>${button('重试', act)}</div>`;
}
function listStaleBar(act, label) {
  return `<div class="notice warn" style="margin-bottom:14px"><b>${esc(label)}暂时读不到最新数据</b>：下方是最后一次成功加载的快照（可能已过期）；网络恢复后轮询自动刷新，也可点${button('重试', act)}。</div>`;
}
function newIdem() {
  return 'web-' + (globalThis.crypto?.randomUUID?.() || Date.now() + '-' + Math.random().toString(36).slice(2));
}
/* 会话边界统一清理（安全审查修复 5）：登出/401/换令牌/再登录时清空一次性凭证
   与上个会话的页面状态。同时递增会话代次（第五轮收敛 3）：清理前已发出的在途
   请求，其迟到应答凭代次不一致被丢弃，不得把已清理的一次性凭证写回 S.primeNew /
   S.primeDecl.done。通知渠道状态同属会话（返工建议修 3）：notifyLoaded 不随会话
   重置的话，重新登录后进 services 页不再重拉，展示上个会话的残留列表（或空列
   表）；loading 一并复位，边界前在途的渠道请求迟到应答直接丢弃、不遗留加载态。 */
function resetPrimeState() {
  S.sessionEpoch++;
  if (S.sessModal) closeSessModal();   // 会话表单 modal 同属会话边界（W01-05）：草稿留 S.sess，跨登录不恢复
  if (S.sessDrawer) closeSessionDrawer();   // 执行明细抽屉同属会话边界：closeSessionDrawer 递增 sessDrawerSeq 使在途应答全部作废
  S.sessId = ''; S.sessPage = null;   // 详情页路由态随会话边界复位（hash 仍指详情时由 route 重挂）
  S.sessQuery = ''; S.sessFilter = '';
  S.primeDecl = { picking: false, busy: false, done: null };
  S.primeNew = '';
  S.primeNewWire = null;
  S.mcpAccess = { loaded: false, unavailable: false, enabled: null, credential: '', scope: '', primeAgent: '', updatedAt: '' };   // 授权状态同属会话边界（W06-03）：不跨登录残留上个会话的授权视图
  S.mcp = { form: null, view: null, apply: null, board: null, boardErr: '', boardLoading: false, lastV0: null, lastV2: null };   // MCP 接入子页同属会话边界（W03-04）：应用结果与验证板不跨登录残留
  if (mcpModal) closeMcpModal(false);   // 配置表单弹层同属会话边界（不返焦——焦点链随会话重建）
  if (mcpDrawer) closeMcpDrawer();      // 诊断抽屉同属会话边界
  S.tf = { origin: '' };   // 来源筛选随会话边界重置（TASK-nav-filter A1 codex 必须修项）
  S.sess = freshSessForm();   // 会话表单同属会话边界（T3）
  S.sessions = [];
  S.sessLoaded = false;   // 首载标记随会话边界复位（回到加载中，不冒充空列表）
  S.sessUnsupported = false;
  S.sessError = '';
  S.notifyChannels = [];
  S.notifyLoaded = false;
  S.notifyLoading = false;
  S.notifyError = '';
  S.notifyAlso = null;
  S.notifyAlsoSaving = false;
  S.notifyAlsoPending = null;
}

/* ---------- API 访问（Bearer 本机管理令牌） ---------- */
/* 返回 {code, data}；网络层失败抛 {network:true}（结果未知），401 抛前先回令牌页。 */
async function api(method, path, body, opt = {}) {
  let resp;
  const tokenAtReq = S.token, epochAtReq = S.sessionEpoch; // 会话边界竞态防护：捕获发起时令牌与代次
  try {
    resp = await fetch(path, {
      method,
      headers: {
        ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
        ...(S.token ? { 'Authorization': 'Bearer ' + S.token } : {}),
      },
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
  } catch (e) {
    throw { network: true, cause: e };
  }
  const data = await resp.json().catch(() => null);
  if (resp.status === 401 && !opt.noGate) {
    // 迟到 401 防护（codex 终验两轮收敛）：请求发起后发生过会话边界
    //（登出/换令牌/前次 401 清理，epoch 已递增）——不得清空当前（可能是新）会话。
    if (S.sessionEpoch === epochAtReq) {
      S.authed = false;
      resetPrimeState(); // 同代次内真实失效：清凭证回门（reset 再递增 epoch 表示新边界）
      render();
    }
    throw { unauthorized: true, stale: S.sessionEpoch !== epochAtReq, data };
  }
  return { code: resp.status, data };
}
function errLine(data) {
  if (!data) return '无应答体';
  return esc(data.error || 'error') + ' · ' + esc(data.message || '');
}

/* ---------- 数据加载与轮询（页面可见时 5s） ---------- */
async function loadAll() {
  const [st, ag, tk, dir] = await Promise.all([
    api('GET', '/api/v1/local/status'),
    api('GET', '/api/v1/local/agents'),
    api('GET', '/api/v1/local/tasks?limit=' + TASKS_PAGE),
    api('GET', '/api/v1/agents'),
  ]);
  /* P1-02（五态加载链收口）：非 200 或结构不合格不得提交快照。api() 对非 401 HTTP
   * 错误原样返回 {code,data}——这是历史形状且不能一刀切改抛：409（幂等冲突）、
   * 503（配置未接线）、502（hub 不可达）在本机协议里是业务码，写路径按 code 分诊。
   * 加载链在此逐资源校验状态码与必需字段（与 hub loadResource 的「结构校验：非 200
   * 或关键字段缺失不提交」同纪律），任何一项不合格整轮抛错给调用方落 loadError
   * 失败/陈旧态——四资源一个都不提交，保留最近成功数据，不再把 500 的 {error} 体
   * 当成功加载、清空列表冒充「没有任务」。 */
  if (st.code !== 200 || !st.data || typeof st.data.node_id !== 'string') throw { load: 'status' };
  if (ag.code !== 200 || !Array.isArray(ag.data?.agents)) throw { load: 'agents' };
  if (tk.code !== 200 || !Array.isArray(tk.data?.tasks) || typeof tk.data?.total !== 'number') throw { load: 'tasks' };
  if (dir.code !== 200 || !Array.isArray(dir.data?.local)) throw { load: 'agents' };
  S.nodeID = st.data?.node_id || '';
  S.status = st.data || null;
  S.agents = ag.data?.agents || [];
  // 列表刷新（复审②-1）：取新首页（TASKS_PAGE 条），已载历史保序并入而非整列表
  // 覆盖——轮询清历史页会让「加载更早」翻到的内容 5s 内消失（207/207→200/207）。
  // 合并正确性：提交路径新任务插队首部、旧行相对序不变，新首页含旧载行前段（有
  // 重叠），其后旧载行按原相对序接尾，覆盖无缺口（间隔新任务 < 一页时恒成立）；
  // total 以后端为准。
  S.tasks = mergeTaskPages(tk.data?.tasks || [], S.tasks);
  S.tasksTotal = tk.data?.total || 0;
  S.tasksGen++; // 作废在途翻页应答（复审②-2）：其 offset 基线随本次刷新失效
  // 派单目录只含本机 agent（/api/v1/agents 无 network 键——node 无全网视图）。
  // 建议修 10（2026-10-22）：standalone 本机主权——private（未暴露）能力也可本机
  // 派单（exposed 只约束 hub 准入），目录不过滤；组网模式维持只列 exposed。
  S.catalog = (dir.data?.local || []).filter(e => e.card?.exposed || S.status?.mode === 'standalone');
  // 列表行自带 status（账本全量驱动，2026-10-26 全量审计裁决）——无需逐条补查，
  // 状态真相即本机账本，列表快照内联。
  const snap = JSON.stringify([S.status, S.agents, S.tasks, S.tasksTotal, S.catalog]);
  /* P1-03：新鲜度转移进渲染判据——失败态在身时本轮加载成功，即使数据指纹与失败前
   * 完全相同也必须返回 changed=true，让恢复落 DOM（撤失败态/陈旧条）；否则指纹门控
   * 把恢复吞掉，失败态永远挂在页面上。 */
  const changed = snap !== S.snap || !!S.loadError;
  S.snap = snap;
  S.loadError = '';   // 本轮全量加载成功：清除失败态（A4）
  return changed;
}
/* 轮询刷新合并（复审②-1）：新首页在前、旧载历史保序并入，按 task_id 去重。
   零重叠 = 两次刷新间隔新任务超过一整页，新首页与旧历史之间必有缺口，保序拼接
   不可得——整段弃用退回纯首页（按钮仍在，用户可重新展开）。 */
function mergeTaskPages(fresh, old) {
  if (!fresh.length || !old.length) return fresh;
  const ids = new Set(fresh.map(t => t.task_id));
  if (!old.some(t => ids.has(t.task_id))) return fresh;
  return fresh.concat(old.filter(t => !ids.has(t.task_id)));
}
// loadMoreTasks 追加下一页（offset=已载行数；与后端「排序在切片前」契约配合，
// 追加序即全序）。失败提示后可重试；在途防重入（双击），busy 只由本函数解除——
// 轮询刷新不得提前复位（复审②-2）；应答按发起时代次校验，期间发生过轮询刷新
// （offset 基线已失效）则整体丢弃，防旧尾页拼上新列表出漏行/重复。
async function loadMoreTasks() {
  if (S.tasksMoreBusy || S.tasks.length >= S.tasksTotal) return;
  const loaded = S.tasks.length, genAtReq = S.tasksGen;
  S.tasksMoreBusy = true; render();
  try {
    const r = await api('GET', '/api/v1/local/tasks?limit=' + TASKS_PAGE + '&offset=' + loaded);
    if (S.tasksGen === genAtReq) {
      /* P1-02 同口径：翻页应答非 200 或结构不合格不并入——HTTP 错误不得当作
       * 「没有更早任务」静默吞掉，也不得让 {error} 体污染已载列表。 */
      if (r.code === 200 && Array.isArray(r.data?.tasks)) {
        S.tasks = S.tasks.concat(r.data.tasks);
        S.tasksTotal = r.data?.total ?? S.tasksTotal;
      } else toast('加载更早任务失败');
    }
  } catch (e) {
    if (!e.unauthorized) toast('加载更早任务失败');
  }
  S.tasksMoreBusy = false;
  S.snap = ''; render();
}
/* ---------- 会话（W01-05 P01/P02 迁移：独立会话主入口 + 独立详情页） ----------
 * P01「会话」独立导航页（由旧任务页内区块迁出，任务页留迁移说明卡）：搜索 + 状态
 * chips（含登记中/cancelled）+ 列表（标题/agent/机器/工作区/状态/最近活动/操作，
 * 列序与 hub 同构）+ 页脚。新建/继续表单迁入居中 modal（同 hub 形态；node 固定本
 * 机——单级目标下拉，字段序 标题→agent→绑定→工作区→输入；草稿跨开关保留）。
 * 数据面升级为 agent 会话面 /api/v1/sessions（主人 local_token 恒放行）：列表=
 * 内核快照 ∪ 受理中会话 pending 投影——新建登记后首轮发布前即可见（登记中（首轮
 * 未发布）如实标注，W01-03 §2）；创建/投轮携前端生成 request_id，响应丢失后同参
 * 重试由服务端幂等兜底（同键同文 200 dup）。关闭仍走主人旧面 /api/v1/local/
 * sessions/{id}/close（本机唯一关闭入口；在途 409 close_in_flight 人话指向取消/
 * 等待）。取消走 agent 面人工专属端点（W01-03 §6.1）：回执 queued/executed/
 * unknown 如实三态，unknown 待核对绝不渲染为已停止。
 * P02 #/sessions/{id} 独立详情页：绑定/轮次时间线/答复/动作四分区（fetchSession-
 * Page 每 5s 同步）；轮次「执行明细」右抽屉保留（旧 700px 会话抽屉退役——全部旧
 * 入口 goSessionDetail 兼容跳转详情页）。轮次时间线三路合并：内核轮行（published）
 * ∪ pending 受理投影（登记中）∪ 本机账本在途/取消事实（S.tasks 幂等键 lsturn:<sid>:
 * <seq> 扫描，补内核同步阻塞式留痕的在途盲区）。 */
function freshSessForm() {
  /* snap：提交参数快照（R1 必修4）——unknown 重试从快照取参重发。reqId/turnReqId：
   * 稳定请求 ID（W01-03 §1/§3）——提交时生成、重试复用；fresh 换新。 */
  return { mode: 'new', contId: '', contSnap: null, agent: '', title: '', cwd: '', prompt: '', stage: 'idle', message: '', ackId: '', nextSeq: 0, snap: null, reqId: '', turnReqId: '' };
}
/* newSessRequestId 前端请求 ID（W01-05 交付三：UUID 形态；crypto 不可得回落时间+
 * 随机——唯一性由调用方「提交时生成、重试复用」纪律保证）。 */
const newSessRequestId = () => 'web-' + (globalThis.crypto?.randomUUID?.() || Date.now() + '-' + Math.random().toString(36).slice(2));
/* loadSessions 会话列表加载（agent 会话面，内核快照 ∪ pending 投影）：登录代次+
 * 请求序号双守卫先行（R1 必修5 口径保持）；503 session_unsupported 诚实降级专态。 */
let sessListSeq = 0;   // 会话列表请求序号：新请求发出即作废更早在途应答
async function loadSessions() {
  const epochAtReq = S.sessionEpoch, seqAtReq = ++sessListSeq;
  const r = await api('GET', '/api/v1/sessions').catch(() => null);
  if (S.sessionEpoch !== epochAtReq || seqAtReq !== sessListSeq) return;   // 双守卫：换界或在途已被新请求顶替，本应答整体作废
  S.sessLoaded = true;   // 首载已落定（含失败态）
  if (!r) { S.sessError = '会话列表暂时读不到（网络）'; return; }
  if (r.code === 503) { S.sessUnsupported = true; S.sessions = []; S.sessError = ''; return; }
  if (r.code !== 200 || !Array.isArray(r.data?.sessions)) { S.sessError = '会话列表加载失败'; return; }
  S.sessUnsupported = false; S.sessError = '';
  S.sessions = r.data.sessions;
}
/* ---------- 轮次归一与三路合并（node 数据形态 → 双端同构展示模型） ---------- */
/* nodeProjTurn pending 受理投影轮（agentPendingTurnView {seq,state,status?,error?}）：
 * queued→排队中、running→执行中（在途可取消）；finished 按账本结果状态映射——
 * completed→unverified（node 完成轮恒 unverified，内核第四路口径）、cancelled→
 * 已取消（无内核轮行=进入执行前撤销）、failed/needs_review→failed（重启收敛等
 * 平台终态，错误码随行）。 */
function nodeProjTurn(t) {
  if (t.state === 'queued') return { seq: t.seq, status: 'pending', error: t.error || '', inflight: true };
  if (t.state === 'running') return { seq: t.seq, status: 'running', error: t.error || '', inflight: true };
  const st = String(t.status || '');
  if (st === 'cancelled') return { seq: t.seq, status: 'cancelled', error: t.error || 'turn_cancelled_queued' };
  if (st === 'failed' || st === 'needs_review') return { seq: t.seq, status: 'failed', error: t.error || '' };
  return { seq: t.seq, status: 'unverified', error: '' };
}
/* nodeKernelTurn 内核轮行（published 会话；同步阻塞式——轮行存在即已终态）。 */
function nodeKernelTurn(t) {
  return { seq: t.seq, status: t.status === 'failed' ? 'failed' : 'unverified', error: t.error || '', kernel: true, raw: t };
}
/* nodeSessionTurns 会话轮次合并视图（按 seq 升序去重，内核行优先）：
 * 1) published：内核行全集 ∪ S.tasks 幂等键 lsturn:<sid>:<seq> 扫描补盲——在途
 *    （acknowledged/running）与取消事实（cancelled 记账）。内核行 + 账本 cancelled
 *    = 执行取消已证停止（cancelBooked）；无内核行 + cancelled = 排队取消（未 spawn）。
 *    账本分页（首页 200）外的更早已受理轮不在此列——已知限制，详情以内核行为准。
 * 2) pending：受理投影行（nodeProjTurn）。 */
function nodeSessionTurns(v) {
  if (v.pending) return (v.turns || []).map(nodeProjTurn).sort((a, b) => a.seq - b.seq);
  const rows = (v.turns || []).map(nodeKernelTurn);
  const bySeq = new Map(rows.map(t => [t.seq, t]));
  for (const e of S.tasks || []) {
    const m = /^lsturn:(.+):(\d+)$/.exec(String(e.idem_key || ''));
    if (!m || m[1] !== v.id) continue;
    const seq = parseInt(m[2], 10), st = String(e.status || '');
    if (bySeq.has(seq)) {
      const tv = bySeq.get(seq);
      if (st === 'cancelled' && tv.status === 'failed') {
        tv.cancelBooked = true;   // 本机账本取消记账（终审必修1：内核行取消分类判据；三态由 nodeTurnKind 依治理证据细分——账本 cancelled 蕴含 ClassifyCancelTurn 已证停止，timeout:+exit-1 恒 Executed，此为快路径免探针）
      } else if (st === 'failed' && tv.status === 'failed' && !tv.cancelBooked) {
        // 终审必修A：探针总体=全部无 cancelBooked 的 failed 内核行（unknown 真实总体是
        // 非治理形态：ClassifyCancelTurn 的 unknown=exit-1 且错误非 timeout 前缀——
        // process_governance_failed:/exit_code=N: 等落账 failed+turn_cancel_outcome_unknown，
        // sessionturn.go 落账口径）。内核留痕不可区分自然失败/unknown 取消——探针本身即
        // 区分器（任务详情记账码 turn_cancel_outcome_unknown → unknown，否则 natural）。
        tv.probeTask = e.task_id || '';
      }
      continue;
    }
    if (st === 'acknowledged') rows.push({ seq, status: 'pending', error: '', inflight: true });
    else if (st === 'running') rows.push({ seq, status: 'running', error: '', inflight: true });
    else if (st === 'cancelled') rows.push({ seq, status: 'cancelled', error: 'turn_cancelled_queued' });
    else if (st === 'failed') rows.push({ seq, status: 'failed', error: '' });
  }
  return rows.sort((a, b) => a.seq - b.seq);
}
/* nodeTurnKind 轮行取消三态判定（node 形态；W01-05 终审必修1/2 修正）：
 * 1) cancelled 投影行（pending 受理投影 / 账本扫描无内核行）：Error 首段记账码为
 *    准——turn_cancelled_executed→executed（执行取消：进程组已整组终止并回收，
 *    绝无「未运行」字样）；turn_cancelled_queued 或裸 cancelled→queued（进入执行
 *    前撤销，「未运行」文案仅此分支）。
 * 2) 内核行 + cancelBooked（本机账本取消记账）：分类与 sessionop.ClassifyCancelTurn
 *    同源（证据口径同式：error 前缀 timeout: 且无真实退出码=runner 治理已整组终止
 *    并回收 → executed；其余错误形态=停止事实未证实 → unknown 待核对——徽标+答复区
 *    警示条，绝不渲染「执行失败」冒认进程错误语义）。
 * 3) 内核行无 cancelBooked 且探针判为 unknown（任务详情账本记账码
 *    turn_cancel_outcome_unknown，sessCancelProbe 缓存）→ unknown；探针 natural →
 *    维持「执行失败」如实。其余沿 shared 判定（failed + turn_cancel_outcome_unknown
 *    → unknown）。 */
function nodeTurnKind(tv) {
  const err = String(tv.error || '');
  if (tv.status === 'cancelled') {
    if (err.startsWith('turn_cancel_outcome_unknown')) return 'unknown';   // 防御位：unknown 记账恒投影 failed，若记账码在案绝不按 queued 出「未运行」文案
    return err.startsWith('turn_cancelled_executed') ? 'executed' : 'queued';
  }
  if (tv.status === 'failed') {
    const ec = tv.raw?.exit_code;
    const governed = err.startsWith('timeout:') && !(typeof ec === 'number' && ec >= 0);
    if (tv.cancelBooked) return governed ? 'executed' : 'unknown';
    if (tv.probeTask && sessCancelProbe.get(tv.probeTask) === 'unknown') return 'unknown';   // 终审必修A：探针本身即 unknown/natural 区分器——无 governed 门（治理形态走 cancelBooked 快路径，见 nodeSessionTurns 附加条件）
  }
  return sessCancelKind(tv);
}
/* nodeTurnBadge 轮行状态徽标（取消三态感知，终审必修1）：executed/queued → 已取消
 * 徽标；unknown → 待核对徽标（绝不渲染「执行失败」冒认进程错误语义）；其余按投影
 * 态直出。 */
function nodeTurnBadge(tv) {
  const kind = nodeTurnKind(tv);
  if (kind === 'unknown') return `<span class="badge sst-unverified" title="turn_cancel_outcome_unknown">待核对</span>`;
  if (kind === 'executed' || kind === 'queued') return sessBadge('cancelled');
  return sessBadge(tv.status);
}
/* sessCancelProbe 无 cancelBooked 失败内核行的取消探针缓存（task_id →
 * 'unknown'|'natural'）：自然失败与 unknown 取消在内核留痕上不可区分，取消是否在案
 * 以任务详情账本记账码（turn_cancel_outcome_unknown）为唯一判据——每 task 至多探
 * 一次成功；网络失败不落缓存（下轮轮询重探，避免瞬时失败把任务永久锁出待核对通
 * 道）；404=任务已过保留期记账码不可达，按自然失败保守呈现并落缓存（终态判定）。 */
const sessCancelProbe = new Map();
let sessCancelProbeBusy = false;
async function probeSessCancelUnknowns() {
  if (sessCancelProbeBusy || !S.sessId || S.page !== 'sessions' || !S.sessPage?.session) return;
  const targets = nodeSessionTurns(S.sessPage.session)
    .filter(t => t.kernel && !t.cancelBooked && t.probeTask && !sessCancelProbe.has(t.probeTask))
    .map(t => t.probeTask);
  if (!targets.length) return;
  sessCancelProbeBusy = true;
  let changed = false;
  try {
    for (const tid of targets) {
      const r = await api('GET', '/api/v1/tasks/' + encodeURIComponent(tid)).catch(() => null);
      if (!r) continue;   // 建议2：网络失败不落缓存——下轮轮询重探
      if (r.code === 404) { sessCancelProbe.set(tid, 'natural'); continue; }   // 任务已过保留期：记账码不可达，保守终态
      if (r.code !== 200 || !r.data) continue;
      const err = String(r.data.result?.error || '');
      sessCancelProbe.set(tid, err.startsWith('turn_cancel_outcome_unknown') ? 'unknown' : 'natural');
      if (err.startsWith('turn_cancel_outcome_unknown')) changed = true;
    }
  } finally { sessCancelProbeBusy = false; }
  if (changed) { syncSessPageZones(); syncSessTurnDrawerFromPage(); }   // 待核对判定落定：详情/抽屉即绘
}
/* sessStateBadge 会话注册表状态徽标（详情页头；pending 键双义分流：会话级登记态
 * 取 SESS_STATES「登记中（首轮未发布）」，轮级 pending=排队中走 sessBadge——徽标词
 * 不得把登记中会话误标排队中）。 */
function sessStateBadge(v) {
  if (v.pending || v.status === 'pending') return `<span class="badge sst-pending" title="pending">${esc(SESS_STATES.pending)}</span>`;
  return sessBadge(v.status);
}
function sessRowState(v) {
  if (v.pending) return 'pending';   // 登记中（会话级；chip 词取 SESS_STATES）
  const t = nodeSessionTurns(v).slice(-1)[0];
  if (!t) return '';   // 关闭且无轮次：无筛选态（徽标「待首轮」如实）
  if (t.status === 'pending') return 'running';   // 在途排队并入「执行中」口径（时间线行仍精确显示排队中）
  if (t.status === 'failed' || t.status === 'cancelled') {
    const k = nodeTurnKind(t);   // 取消三态感知（终审必修1）：执行/排队取消入「已取消」，待核对入「执行失败」chip 计数（时间线/详情展示待核对徽标）
    if (k === 'executed' || k === 'queued') return 'cancelled';
    if (k === 'unknown') return 'failed';
  }
  return t.status;
}
/* ---------- P01 会话列表主入口 ---------- */
const sessViewPaint = new WeakMap();   // 列表页三槽缓存（WeakMap 绑元素实例；切页重建后旧缓存天然失效）
function paintSessZoneById(id, html) {
  const el = document.getElementById(id);
  if (!el || sessViewPaint.get(el) === html) return;
  sessViewPaint.set(el, html);
  el.innerHTML = html;
}
/* paintSessionsView 会话页统一刷新入口（受理/关闭/筛选/轮询共用）：chips/列表/页脚
 * 三槽指纹门控局部写入，搜索输入 DOM 不受触碰。 */
function paintSessionsView() {
  if (S.page !== 'sessions' || S.sessId) return;   // 详情页无列表槽位（详情分区走 fetchSessionPage）
  paintSessZoneById('sess-chips-zone', sessChipsZone());
  paintSessZoneById('sess-list-zone', sessListZone());
  paintSessZoneById('sess-foot-zone', sessFootZone());
}
function sessStateCounts() {
  const c = {};
  for (const v of S.sessions) { const st = sessRowState(v); if (st) c[st] = (c[st] || 0) + 1; }
  return c;
}
function filteredSessions() {
  let list = S.sessFilter ? S.sessions.filter(v => sessRowState(v) === S.sessFilter) : S.sessions;
  const q = S.sessQuery.trim().toLowerCase();
  if (q) list = list.filter(v => [v.agent_name, v.title || '', v.cwd, v.id, S.nodeID || '本机'].join('\n').toLowerCase().includes(q));
  return list;
}
/* sessChipsNode 状态筛选 chips（node 全集：登记中 + 七值轮投影态；hub 无登记中
 * ——endpoint 能力差异，白名单登记）。 */
function sessChipsNode() {
  const counts = sessStateCounts();
  const all = `<button data-act="sess-chip" data-chip="" class="${!S.sessFilter ? 'on' : ''}">全部 (${S.sessions.length})</button>`;   // 终审必修3：全部 chip 恒在（与 hub 同构；任一筛选态的主返回路径）
  return [all].concat(['pending'].concat(SESS_TURN_SEVEN.filter(x => x !== 'pending')).map(s => {   // 登记中 chip 独立（会话级）；轮投影 pending（排队中）并入登记口径计数
    const n = counts[s] || 0;
    const word = s === 'pending' ? SESS_STATES.pending : (SESS_TURN_STATES[s] || s);
    const bd = n ? (s === 'pending' ? `<span class="badge sst-pending" title="pending">${esc(SESS_STATES.pending)}</span>` : sessBadge(s)) : `<span class="badge">${esc(word)}</span>`;
    return `<button data-act="sess-chip" data-chip="${s}" class="${S.sessFilter === s ? 'on' : ''}${n ? '' : ' zero'}" title="${esc(s)}">${bd} ${n}</button>`;
  }));
}
/* sessListZone 列表行状态列（终审必修B）：关闭态优先「已关闭」徽标（与 hub sessTable
 * 同构——7bd09a3 起 hub 即 closed 优先，node 旧渲染按最近轮次出徽标属双端漂移，
 * 一处两修：消 SN10「已关闭」断言永 FAIL 矛盾）；登记中其次；其余按取消三态感知
 * 轮徽标（nodeTurnBadge）；无轮次「待首轮」。chips 计数（sessRowState）不受本渲染
 * 改动影响——closed 会话按最近轮次投影计数，与 hub sessStateCounts 同口径。 */
function sessListZone() {
  if (S.sessUnsupported) return `<div class="empty"><h2>本机未装配会话内核</h2><p class="muted">没有能力声明 session_adapter（node.yaml 能力级配置，变更须「应用并重启」）。本机原生线程管理不可用；跨机会话请经 hub 控制台。</p></div>`;
  if (!S.sessLoaded) return listLoadingBox('会话列表');   // 首载未落定：不冒充「还没有会话」（R1 建议3）
  if (S.sessError && !S.sessions.length) return `<div class="empty"><h2>会话列表加载失败</h2><p class="muted">最近一次读取没有成功（本机 API 不可达或应答异常），不是没有数据。可点重试，轮询也会自动恢复。</p>${button('重试', 'sess-reload')}</div>`;
  const list = filteredSessions();
  if (!list.length) return (S.sessError ? listStaleBar('sess-reload', '会话列表') : '')
    + `<div class="empty"><h2>${S.sessions.length ? '没有符合筛选的会话' : '还没有会话'}</h2>${S.sessions.length ? button('清除筛选', 'sess-clear-filter') : `<p class="muted">与声明会话能力的智能体开启多轮协作；轮次账本留痕在本机，未证实的工作结果如实标注。</p>${button('新建会话', 'sess-new', 'primary')}`}</div>`;
  return (S.sessError ? listStaleBar('sess-reload', '会话列表') : '')
    + `<div class="table-wrap"><table><thead><tr><th>标题</th><th>agent</th><th>机器</th><th>工作区</th><th>状态</th><th>最近活动（UTC）</th><th>操作</th></tr></thead><tbody>
  ${list.map(v => { const turns = nodeSessionTurns(v); const lastT = turns[turns.length - 1]; const st = sessRowState(v); const closed = v.status === 'closed' && !v.pending; return `<tr data-sess="${esc(v.id)}" tabindex="0" aria-label="查看会话 ${esc(v.id)}">
    <td class="task-title">${v.title ? `<strong>${esc(v.title)}</strong>` : '<span class="muted">（无标题）</span>'}<small class="task-sum">${sessShortId(v.id)}</small></td>
    <td><strong>${esc(v.agent_name)}</strong></td>
    <td><small>${esc(S.nodeID || '本机')}</small></td>
    <td><small title="${esc(v.cwd || '')}">${esc(sessWsSummary(v.cwd))}</small></td>
    <td>${v.pending ? sessStateBadge(v) : closed ? sessBadge('closed') : lastT ? nodeTurnBadge(lastT) : '<span class="muted small">待首轮</span>'}</td>
    <td><small>${esc(fmtUTC(v.updated_at) || '—')}</small></td>
    <td class="table-action">${!closed ? button('继续', 'sess-continue:' + esc(v.id), 'link') : ''}${button('查看 →', 'sess-open:' + esc(v.id), 'link')}</td>
  </tr>`; }).join('')}</tbody></table></div>`;
}
function sessFootZone() {
  return `${S.sessions.length} 个会话 · 时间为 UTC · 会话实体暂不自动清理（保留策略随产品化批次另行设计）；关闭后的会话仅可查看。`;
}
function sessionListPageView() {
  return `<div class="page-head"><div><h1>会话</h1><p class="muted">本机持久会话线程：与声明会话能力的智能体多轮协作；轮次账本留痕在本机，未证实的工作结果如实标注。</p></div><div class="actions">${S.sessUnsupported ? '' : button('新建会话', 'sess-new', 'primary')}</div></div>
  <section class="card sess-scope">
  <label class="spaced">搜索（标题 / agent / 工作区 / ID）<input id="sess-query" type="search" placeholder="例如：zcode 或 s-9f2c…" value="${esc(S.sessQuery)}" spellcheck="false" autocomplete="off"></label>
  <div class="filters spaced" id="sess-chips-zone">${S.sessUnsupported ? '' : sessChipsNode().join('')}</div>
  <div id="sess-list-zone">${sessListZone()}</div>
  <p class="footer-note" id="sess-foot-zone">${sessFootZone()}</p></section>`;
}
/* ---------- P02 独立详情页 ---------- */
function sessionDetailPageView() {
  return `<div class="page-head"><div><button type="button" class="link" data-act="sess-back">← 返回会话列表</button><h1>会话详情</h1><p class="muted">绑定与轮次账本留痕；在途轮次可逐轮取消（回执如实三态），答复与用量按轮留查。</p></div></div>
  <section class="card sess-scope" id="sess-detail"><p class="muted" id="sess-detail-loading">正在加载会话详情…</p>${SESS_TURN_ZONES.map(z => `<div data-zone="${z}"></div>`).join('')}</section>`;
}
function sessionsPageView() {
  return S.sessId ? sessionDetailPageView() : sessionListPageView();
}
async function fetchSessionPage() {
  const id = S.sessId;
  if (!id || S.page !== 'sessions' || !S.authed) return;
  const r = await api('GET', '/api/v1/sessions/' + encodeURIComponent(id)).catch(() => null);
  if (!S.sessId || S.sessId !== id || S.page !== 'sessions') return;   // 换会话/关页：旧应答作废
  if (r && r.code === 404) { toast('会话不存在（本机内核无映射且无受理登记）'); location.hash = '#/sessions'; return; }
  if (!r || (r.code !== 200 && r.code !== 503) || !r.data?.session) return;
  if (!S.sessPage || S.sessPage.id !== id) S.sessPage = { id, session: null, zones: {}, sel: null };
  S.sessPage.session = r.data.session;
  const seqs = nodeSessionTurns(S.sessPage.session).map(t => t.seq);
  if (S.sessPage.sel != null && !seqs.includes(S.sessPage.sel)) S.sessPage.sel = null;
  $('#sess-detail-loading')?.remove();
  syncSessPageZones();
  probeSessCancelUnknowns();   // 治理形态失败轮的取消探针（unknown 待核对判定；有判定变更时内部即绘）
}
function syncSessPageZones() {
  const root = document.getElementById('sess-detail');
  if (root && S.sessPage?.id === S.sessId && S.sessPage.session) {
    /* DOM 身份对账（P1-03 残余 A 同款）：整页 render 重建 #app 后 zones 缓存仍命中
     * 会全部分区跳写→白页；元素实例换了即清缓存全量重写。 */
    if (S.sessPage.el !== root) { S.sessPage.el = root; S.sessPage.zones = {}; }
    drawerZonesApply(root, sessPageZones(S.sessPage.session), S.sessPage.zones, SESS_TURN_ZONES);
  }
}
/* sessPageZones 详情页四分区（与 hub 同构：①绑定与状态 ②轮次时间线 ③选中轮答复
 * ④动作区）。时间线行 data-turn 可点；在途轮出「取消」；每轮「明细」开执行明细
 * 右抽屉。 */
function sessPageZones(v) {
  const sp = S.sessPage || {};
  const closed = v.status === 'closed' && !v.pending;
  const turns = nodeSessionTurns(v);
  const sel = sp.sel != null ? turns.find(t => t.seq === sp.sel) : null;
  const inflight = turns.filter(t => t.inflight);
  return {
    head: `<div class="row"><h2>${esc(v.agent_name)} @ ${esc(S.nodeID || '本机')} ${sessStateBadge(v)}</h2><small class="muted">${esc(v.id)}</small></div>`,
    binding: `<h3>绑定与状态</h3>
    <div class="table-wrap"><table><tbody>
    <tr><td>适配器</td><td><code>${esc(v.adapter || '—')}</code></td></tr>
    <tr><td>权限模式</td><td><small>${esc(v.permission_mode || '（未声明）')}</small></td></tr>
    <tr><td>工作区</td><td><code>${esc(v.cwd || '—')}</code></td></tr>
    <tr><td>原生会话</td><td>${sessShortId(v.native_id)}</td></tr>
    <tr><td>轮次</td><td><small>${turns.length} 轮</small></td></tr>
    <tr><td>创建</td><td><small>${esc(fmtUTC(v.created_at) || '—')}</small></td></tr>
    ${v.request_id ? `<tr><td>创建请求 ID</td><td>${sessShortId(v.request_id)}</td></tr>` : ''}
    </tbody></table></div>`,
    turns: `<h3 class="spaced">轮次（${turns.length}）</h3>${turns.length
    ? `<div class="table-wrap"><table><thead><tr><th>#</th><th>状态</th><th>时间（UTC）</th><th>标识</th><th>操作</th></tr></thead><tbody>
      ${turns.map(t => { const time = t.kernel && t.raw ? (realTime(t.raw.finished_at) ? t.raw.finished_at : t.raw.started_at) : ''; const k = nodeTurnKind(t); return `<tr data-turn="${t.seq}" tabindex="0" aria-label="查看第 ${t.seq} 轮" class="${sp.sel === t.seq ? 'on' : ''}">
      <td>#${t.seq}</td>
      <td>${nodeTurnBadge(t)}${t.status === 'failed' && t.error && !k ? ` <small class="help" title="${esc(t.error)}">ⓘ</small>` : ''}</td>
      <td><small>${esc(fmtUTC(time) || '—')}</small></td>
      <td>${sessShortId(t.kernel ? t.raw.native_turn_id : '')}</td>
      <td class="table-action">${t.inflight ? button('取消', 'sess-turn-cancel', 'link', '', `data-id="${esc(v.id)}" data-seq="${t.seq}"`) : ''}${button('明细', 'sess-turn-detail', 'link', '', `data-seq="${t.seq}"`)}</td>
    </tr>`; }).join('')}</tbody></table></div>
    <p class="footer-note">标识为原生轮次 ID 短形式（完整值进明细抽屉）；时间线含平台受理事实（排队/取消账本行），序号不连续=缺口轮次未进入原生执行（排队撤销等平台侧终态，序号已消耗）。在途轮可取消。</p>`
    : `<p class="muted">${SESS_EMPTY_TURNS}</p>`}`,
    detail: sessTurnAnswerHtml(sel, v),
    actions: `<h3 class="spaced">动作</h3>
    <div class="actions spaced">${closed ? '<span class="muted">会话已关闭：记录仅可查看，续轮须新建会话。</span>'
    : `${button('继续此会话', 'sess-continue:' + esc(v.id), 'primary')}${button('关闭会话', 'sess-close:' + esc(v.id), 'danger')}`}</div>
    ${inflight.length ? `<div class="notice warn spaced"><b>有 ${inflight.length} 轮在途</b>：可在时间线逐轮取消（回执 queued/executed/unknown 如实三态），或等待完成；此期间关闭会被拒绝。</div>` : ''}
    <p class="footer-note">轮次异步执行：详情每 5 秒自动刷新；「${SESS_TURN_STATES.unverified}」表示执行成立但工作结果未经证实，重要结论须自行核对。取消结果按证据记账——停止事实未证实的轮标记「待核对」，绝不渲染为已停止。</p>`,
  };
}
/* sessTurnAnswerHtml 选中轮答复/受理事实（unverified 置顶 heat 警示条 SESS_UNVERIFIED_NOTE
 * shared 同文；取消三态横幅；内核行展示答复与用量，受理投影/在途行如实呈现受理
 * 事实——原生轮行待内核落定后可见）。 */
function sessTurnAnswerHtml(tv, v) {
  if (!tv) return `<h3 class="spaced">轮次答复</h3><p class="muted">在时间线选择一轮，查看输入、答复与用量。</p>`;
  const kind = nodeTurnKind(tv);
  const banner = kind === 'unknown'
    ? `<div class="notice warn"><b>取消结果待核对</b> · ${esc(SESS_CANCEL_WORDS.turn_cancel_outcome_unknown)}；本记录绝不记为成功或已停止，请核对本机实际状态。</div>`
    : kind === 'queued' ? `<div class="notice"><b>已取消（进入执行前撤销）</b> · 本轮未运行，确定性终止；平台序号已受理消耗。</div>`
    : kind === 'executed' ? `<div class="notice"><b>已取消（执行已终止）</b> · 进程组已整组回收。</div>`
    : tv.status === 'unverified' && (tv.kernel || tv.raw) ? `<div class="notice warn"><b>工作结果未证实</b> · ${esc(SESS_UNVERIFIED_NOTE)}</div>`
    : tv.status === 'failed' ? `<div class="notice danger"><b>执行失败</b> · <code>${esc(tv.error || '原因未留痕')}</code>${tv.kernel && typeof tv.raw?.exit_code === 'number' && tv.raw.exit_code >= 0 ? ` · 退出码 ${tv.raw.exit_code}` : ''}</div>` : '';
  if (!tv.kernel) {
    return `<h3 class="spaced">第 ${tv.seq} 轮 ${nodeTurnBadge(tv)}</h3>${banner}
    <p class="muted small">平台受理事实：原生轮行待内核落定后可见${tv.error ? ' · 记账码 <code>' + esc(tv.error) + '</code>' : ''}。</p>`;
  }
  const t = tv.raw || {};
  const usage = t.usage && (t.usage.total_tokens || t.usage.input_tokens || t.usage.output_tokens)
    ? `<p class="muted small">用量：输入 ${t.usage.input_tokens || 0} · 输出 ${t.usage.output_tokens || 0} · 共 ${t.usage.total_tokens || 0} tokens</p>` : '';
  return `<h3 class="spaced">第 ${tv.seq} 轮答复 ${nodeTurnBadge(tv)}</h3>${banner}
  <p class="muted small">开始 ${esc(fmtUTC(t.started_at) || '—')}${realTime(t.finished_at) ? ' · 完成 ' + esc(fmtUTC(t.finished_at)) : ''} · 原生轮次 ${sessShortId(t.native_turn_id)}</p>
  <h3 class="spaced">答复</h3>${mdAnswer(t.response)}${usage}`;
}
/* ---------- 执行明细右抽屉（旧 700px 会话抽屉退役为逐轮执行明细） ---------- */
const SESS_TURN_DRAWER_ZONES = ['head', 'facts', 'answer'];
let sessDrawerSeq = 0;    // 抽屉会话序号：防被关/被换（openDrawer 同款）
function goSessionDetail(id) {
  location.hash = '#/sessions/' + encodeURIComponent(id);   // 旧入口兼容跳转（旧锚点/动作全走详情页）
}
function openSessTurnDrawer(seq) {
  const sp = S.sessPage;
  const tv = sp?.session ? nodeSessionTurns(sp.session).find(x => x.seq === seq) : null;
  if (!tv) { toast('没有该轮次的留痕'); return; }
  sp.sel = seq;
  const back = document.activeElement && document.activeElement !== document.body ? document.activeElement : null;
  S.sessDrawer = { id: sp.id, turn: tv, zones: {}, seq: ++sessDrawerSeq, returnFocus: back };
  $('#layer').innerHTML = `<div class="overlay drawer-overlay"><section class="drawer sess-scope" id="sess-drawer" role="dialog" aria-modal="true" aria-labelledby="drawer-title" tabindex="-1">${SESS_TURN_DRAWER_ZONES.map(z => `<div data-zone="${z}"></div>`).join('')}</section></div>`;
  layerInert(true);
  $('#sess-drawer').focus();
  playDrawerOpen();
  $('#layer').addEventListener('click', sessDrawerMaskClose);
  syncSessTurnDrawerZones();
  syncSessPageZones();   // 选中态回写页面时间线（行高亮）
}
function sessDrawerMaskClose(e) {
  if (!S.sessDrawer) return;
  if (e.target.classList.contains('overlay')) closeSessionDrawer();
}
function closeSessionDrawer() {
  const back = S.sessDrawer?.returnFocus;
  S.sessDrawer = null;
  sessDrawerSeq++;   // 在途应答全部作废
  layerInert(false);
  $('#layer').innerHTML = '';
  if (back && back.isConnected) back.focus();
  else {
    const tab = document.querySelector('aside nav button.active') || document.querySelector('aside nav button');
    if (tab) tab.focus();
  }
}
function syncSessTurnDrawerZones() {
  const root = document.getElementById('sess-drawer');
  if (root && S.sessDrawer?.turn) drawerZonesApply(root, sessTurnDrawerZones(S.sessDrawer.turn, S.sessPage?.session), S.sessDrawer.zones, SESS_TURN_DRAWER_ZONES);
}
/* syncSessTurnDrawerFromPage 执行明细抽屉随页数据同步（fetchSessionPage 同一次取数
 * 零重复请求；抽屉已关/换会话即作废）。 */
function syncSessTurnDrawerFromPage() {
  const sd = S.sessDrawer;
  if (!sd || !S.sessPage?.session) return;
  const tv = nodeSessionTurns(S.sessPage.session).find(x => x.seq === sd.turn.seq);
  if (!tv) return;
  sd.turn = tv;
  syncSessTurnDrawerZones();
}
/* sessTurnDrawerZones 执行明细抽屉分区（①头 ②受理与执行事实 ③输入与答复——内核
 * 行出原生轮次/起止/退出码，受理投影/在途行出受理态与记账码；技术态集中在此）。 */
function sessTurnDrawerZones(tv, v) {
  const t = tv.raw || {};
  return {
    head: `<div class="row"><h2 id="drawer-title">第 ${tv.seq} 轮执行明细</h2>${button('×', 'close-sess-drawer', 'close', '', 'aria-label="关闭抽屉"')}</div>
    <div class="row"><h2>${esc(v?.agent_name || '')} @ ${esc(S.nodeID || '本机')} ${nodeTurnBadge(tv)}</h2></div>`,
    facts: `<h3>受理与执行事实</h3>
    <div class="table-wrap"><table><tbody>
    ${tv.kernel ? `<tr><td>原生轮次</td><td>${sessShortId(t.native_turn_id)}</td></tr>
    <tr><td>开始</td><td><small>${esc(fmtUTC(t.started_at) || '—')}</small></td></tr>
    <tr><td>完成</td><td><small>${esc(fmtUTC(t.finished_at) || '—')}</small></td></tr>
    ${typeof t.exit_code === 'number' ? `<tr><td>退出码</td><td><small>${t.exit_code}</small></td></tr>` : ''}
    ${t.error ? `<tr><td>错误码</td><td><code>${esc(t.error)}</code></td></tr>` : ''}`
    : `<tr><td>受理态</td><td><small>${esc(SESS_TURN_STATES[tv.status] || tv.status)}</small></td></tr>
    ${tv.error ? `<tr><td>记账码</td><td><code>${esc(tv.error)}</code></td></tr>` : ''}
    <tr><td>说明</td><td><small>平台受理事实；原生轮行待内核落定后可见。</small></td></tr>`}
    </tbody></table></div>`,
    answer: sessTurnAnswerHtml(tv, v),
  };
}
/* refreshSessionsAfterWrite 写动作后的统一刷新（受理/取消/关闭共用）：列表槽位
 * 即绘 + 详情页（若在）即取 + 执行明细抽屉随页同步。 */
function refreshSessionsAfterWrite() {
  loadSessions().then(() => { paintSessionsView(); if (S.sessId && S.page === 'sessions') fetchSessionPage().then(syncSessTurnDrawerFromPage).catch(() => { }); }).catch(() => { });
}
/* ---------- 新建/继续会话居中 modal（与 hub 同形态；node 固定本机） ---------- */
let sessModalReturnFocus = null;
const sessModalPaint = new WeakMap();   // modal 体槽位缓存（WeakMap 绑元素实例；关层重建后天然失效）
function sessModalHtml() {
  const F = S.sess;
  return `<div class="row"><h2 id="layer-title">${F.mode === 'continue' ? '继续会话' : '新建会话'}</h2>${button('×', 'close-sess-modal', 'close', '', 'aria-label="关闭弹层"')}</div><div id="sess-modal-body" class="sess-scope">${sessFormZoneHtml(F)}</div>`;
}
function sessFormFullHtml() { return sessFormZoneHtml(S.sess); }
function openSessModal() {
  if (S.sessUnsupported) { toast('本机未装配会话内核'); return; }
  if (S.drawer) closeDrawer();   // 任务抽屉同族互斥
  if (S.sessDrawer) closeSessionDrawer();
  const a = document.activeElement;
  sessModalReturnFocus = a && a !== document.body ? a : null;
  S.sessModal = true;
  $('#layer').innerHTML = `<div class="overlay"><section class="modal" id="sess-modal" role="dialog" aria-modal="true" aria-labelledby="layer-title" tabindex="-1">${sessModalHtml()}</section></div>`;
  layerInert(true);
  $('#sess-modal').focus();
  playDrawerOpen();
  $('#sess-modal').addEventListener('click', sessModalMaskClose);
  sessModalPaint.set($('#sess-modal-body'), sessFormFullHtml());
  requestAnimationFrame(() => $('#sess-title')?.focus());
}
function sessModalMaskClose(e) {
  if (!S.sessModal) return;
  if (e.target.classList.contains('overlay')) closeSessModal();
}
function closeSessModal() {
  S.sessModal = false;
  if (!$('#sess-modal')) return;
  layerInert(false);
  $('#layer').innerHTML = '';
  const back = sessModalReturnFocus;
  sessModalReturnFocus = null;
  if (back && back.isConnected) back.focus();
  else {
    const tab = document.querySelector('aside nav button.active') || document.querySelector('aside nav button');
    if (tab) tab.focus();
  }
}
/* syncSessModal modal 体统一刷新（C 形状态机各阶段迁移唯一入口；先比后写——输入
 * 焦点/光标随未动分区保持）。 */
function syncSessModal() {
  if (!S.sessModal) return;
  syncSessModalHead();   // 头题随模式（新建/继续）同步（create 受理即转 continue 形态）
  const body = document.getElementById('sess-modal-body');
  if (!body) return;
  const html = sessFormFullHtml();
  if (sessModalPaint.get(body) === html) return;
  sessModalPaint.set(body, html);
  body.innerHTML = html;
}
function syncSessModalHead() {
  const h = document.getElementById('layer-title');
  if (h && S.sessModal) h.textContent = S.sess.mode === 'continue' ? '继续会话' : '新建会话';
}
/* paintSessBindZoneNode 目标切换只重写绑定摘要区（适配器/权限只读声明随能力卡），
 * 标题/工作区/消息输入 DOM 原样保持（D22 表单纪律）。 */
function paintSessBindZoneNode() {
  if (!S.sessModal || S.sess.mode !== 'new') return;
  const zone = document.getElementById('sess-bind-zone');
  if (!zone) return;
  const html = sessBindZoneHtml();
  if (zone.innerHTML === html) return;
  zone.innerHTML = html;
}
/* sessAgentOptions 会话表单目标选项（单级下拉，固定本机；声明 session_adapter 的
 * 能力可选，空值禁选并注明——对应后端 404/422 诚实降级，UI 禁选是第一道）。 */
function sessAgentOptionsNode() {
  const capable = S.catalog.filter(e => e.card.session_adapter);
  const opt = e => {
    const v = e.node_id + '|' + e.card.name;
    const tip = `${e.card.description || '（无描述）'} · 适配器 ${e.card.session_adapter}`;
    return `<option value="${esc(v)}" ${S.sess.agent === v ? 'selected' : ''} title="${esc(tip)}">${esc(e.card.name)}</option>`;
  };
  return `<option value="" ${!S.sess.agent ? 'selected' : ''} disabled>选择智能体…</option>` + capable.map(opt).join('');
}
function sessBindZoneHtml() {
  const sel = S.catalog.find(e => e.node_id + '|' + e.card.name === S.sess.agent);
  return `<div class="form-grid">
  <label>适配器（能力卡声明 · 只读）<input readonly value="${esc(sel ? (sel.card.session_adapter || '未声明会话适配器') : '先选择目标')}"></label>
  <label>权限模式（能力卡声明 · 只读）<input readonly value="${esc(sel ? (sel.card.permission_mode || '（未声明）') : '—')}"></label>
  </div>
  ${sel ? `<small class="muted">${esc(permLabel(sel.card.permission_mode))}</small>` : ''}`;
}
/* sessFormZoneHtml modal 表单体（字段序与 hub 同构：标题→目标→绑定→工作区→输入；
 * node 固定本机——无机器选择；hub 创建端点权限断言为端点能力，本机创建经 agent
 * 面不带断言、绑定以能力卡声明只读展示——白名单差异）。 */
function sessFormZoneHtml(F) {
  const locked = F.stage === 'submitting' || F.stage === 'unknown' || F.stage === 'acked';
  if (S.sessUnsupported) return `<div class="notice warn"><b>本机未装配会话内核</b>：没有能力声明 session_adapter（node.yaml 能力级配置，变更须「应用并重启」）。本机原生线程管理不可用；跨机会话请经 hub 控制台。</div>`;
  if (F.mode === 'new' && !S.catalog.some(e => e.card.session_adapter)) return `<div class="empty"><h2>本机还没有会话智能体</h2><p class="muted">在 node.yaml 的能力上配置 session_adapter: codex|zcode 并「应用并重启」后，这里即可开本机持久会话。</p></div>`;
  const continueTarget = F.mode === 'continue'
    ? `<div class="form-grid">
  <label>目标智能体（会话绑定 · 只读）<input readonly value="${esc(F.contSnap?.agent || F.agent.split('|')[1] || '—')}"></label>
  <label>适配器（绑定快照 · 只读）<input readonly value="${esc(F.contSnap?.adapter || '—')}"></label>
  <label>权限模式（绑定快照 · 只读）<input readonly value="${esc(F.contSnap?.permission_mode || '（未声明）')}"></label>
  </div>`
    : `<label class="spaced">目标智能体（须声明会话能力）<select id="sess-agent" ${locked ? 'disabled' : ''}>${sessAgentOptionsNode()}</select></label>
  <div id="sess-bind-zone" class="spaced">${sessBindZoneHtml()}</div>`;
  return `<fieldset ${locked ? 'disabled' : ''}>
  <div class="row"><h2>${F.mode === 'continue' ? '继续会话' : '新建会话'}</h2><span class="badge offered">轮次异步执行 · 账本留痕</span></div>
  <label class="spaced">会话标题（可选 · 首轮发布前随「登记中」可见）<input id="sess-title" value="${esc(F.title)}" placeholder="例如：官网重构讨论" spellcheck="false" autocomplete="off" ${F.mode === 'continue' ? 'readonly' : ''}></label>
  ${continueTarget}
  <label class="spaced">会话工作目录（本机绝对路径）<input id="sess-cwd" value="${esc(F.cwd)}" placeholder="例如：/Users/you/demo" spellcheck="false" autocomplete="off" ${F.mode === 'continue' ? 'readonly' : ''}></label>
  <label class="spaced">${F.mode === 'continue' ? `续轮消息（第 ${F.nextSeq} 轮）` : '首轮消息'}<textarea id="sess-prompt" rows="5" placeholder="这一轮要做什么；上下文由本机原生线程保持。">${esc(F.prompt)}</textarea></label>
  </fieldset>
  <div id="sess-feedback" class="statusline" role="status">${sessStageView(F)}</div>
  <div class="actions">
    ${F.stage === 'acked' ? button('再建一会话', 'sess-again', 'primary') : button(F.stage === 'submitting' ? '提交中… 请勿重复操作' : F.mode === 'continue' ? '发送这一轮' : '创建会话并发送首轮', 'sess-submit', 'primary', F.stage === 'submitting' ? 'disabled' : '')}
    ${F.stage === 'unknown' ? button('同参重试', 'sess-retry', 'link') + button('放弃等待', 'sess-abandon', 'link') : ''}
    ${F.mode === 'continue' && F.stage !== 'acked' ? button('改为新建会话', 'sess-switch-new', 'link') : ''}
  </div>
  <p class="footer-note">创建/投轮携前端生成的请求 ID：响应丢失后「同参重试」由服务端幂等兜底，不会重复建会话或重复投递。登记中（首轮未发布）会话在「会话」页立即可见。</p>`;
}
function sessStageView(F) {
  if (F.stage === 'unknown') return `<div class="notice warn"><b>结果未知 · 表单已锁定</b>：${esc(F.message)}<br>${F.mode === 'continue' ? '「同参重试」携同一请求 ID 与同文重发——服务端幂等兜底命中原轮回执，不会重复投递。' : '「同参重试」携同一请求 ID 重发——服务端同键同文返回原会话（幂等兜底），不会重复建会话。'}</div>`;
  if (F.stage === 'submitting') return '<span class="muted">正在提交…</span>';
  if (F.stage === 'failed') return `<div class="notice warn"><b>提交被拒绝</b>：${esc(F.message || '请修正后重试。')}</div>`;
  if (F.stage === 'acked') return `<div class="notice ok"><b>已受理</b>：会话 <code>${esc(sessIdShort(F.ackId || F.contId))}</code> 轮次异步执行。${button('查看会话 →', 'sess-open:' + esc(F.ackId || F.contId), 'link')}</div>`;
  return '<span class="muted">就绪 · 受理后异步执行，轮次状态进列表详情查看</span>';
}
/* submitSess 表单提交（C 形状态机；agent 会话面两段：创建登记 201 → 首轮受理 202；
 * 均携前端生成 request_id——unknown 同参重试幂等兜底）。提交参数快照（R1 必修4）：
 * fresh 提交时对 DOM 直读结果建快照，重试从快照取参不重读 DOM。 */
async function submitSess(snap) {
  const F = S.sess;
  if (F.stage === 'submitting' || F.stage === 'unknown' || F.stage === 'acked') return;
  if (!snap) {
    F.title = $('#sess-title')?.value ?? F.title;
    F.cwd = $('#sess-cwd')?.value ?? F.cwd;         // DOM 直读收口（input 镜像兜底）
    F.prompt = $('#sess-prompt')?.value ?? F.prompt;
    if (F.mode === 'new') {
      if (!F.agent) { toast('请先选择目标智能体'); return; }
      if (!F.cwd.trim()) { toast('请填写会话工作目录（本机绝对路径）'); return; }
      if (!F.prompt.trim()) { toast('请填写首轮消息'); return; }
    } else if (!F.prompt.trim()) { toast('请填写续轮消息'); return; }
    snap = { mode: F.mode, agent: F.agent, title: F.title, cwd: F.cwd.trim(), prompt: F.prompt, contId: F.contId, reqId: F.reqId, turnReqId: F.turnReqId };
    F.snap = snap;
  }
  if (!snap.reqId) snap.reqId = newSessRequestId();
  if (!snap.turnReqId) snap.turnReqId = newSessRequestId();
  F.stage = 'submitting'; F.message = '';
  syncSessModal(); toast('已锁定表单：正在提交…');
  try {
    let r;
    if (snap.mode === 'new') {
      const body = { agent: (snap.agent.split('|')[1] || snap.agent), cwd: snap.cwd, request_id: snap.reqId };
      if (snap.title && snap.title.trim()) body.title = snap.title.trim();
      const r1 = await api('POST', '/api/v1/sessions', body);
      if (r1.code !== 201 && r1.code !== 200) { F.stage = 'failed'; F.message = sessErrHuman(r1.data); syncSessModal(); return; }
      /* 创建登记受理即绑定：转 continue 形态（绑定快照+nextSeq=1）——首轮任何失败
       * 都留在同一会话重试；dup（同键同文重放）返原会话同样适用。 */
      F.mode = 'continue'; F.contId = r1.data.session.id; F.ackId = F.contId;
      F.contSnap = { agent: r1.data.session.agent_name, adapter: r1.data.session.adapter, permission_mode: r1.data.session.permission_mode };
      F.nextSeq = 1;
      syncSessModal();
      r = await api('POST', `/api/v1/sessions/${encodeURIComponent(F.contId)}/turns`, { prompt: snap.prompt, request_id: snap.turnReqId });
      if (r.code !== 202 && r.code !== 200) {
        F.stage = 'failed';
        F.message = `${sessErrHuman(r.data)} —— 会话已登记但首轮未受理：修正后「发送这一轮」将投递到同一会话。`;
        syncSessModal(); return;
      }
    } else {
      r = await api('POST', `/api/v1/sessions/${encodeURIComponent(snap.contId)}/turns`, { prompt: snap.prompt, request_id: snap.turnReqId });
      if (r.code === 202 || r.code === 200) F.ackId = snap.contId;
    }
    if (r.code === 202 || r.code === 200) {
      F.stage = 'acked';
      toast(r.code === 200 ? '受理回执（同请求 ID 幂等命中，未重复投递）' : '已受理：轮次异步执行');
      refreshSessionsAfterWrite();
    } else { F.stage = 'failed'; F.message = sessErrHuman(r.data); }
  } catch (e) {
    if (e.unauthorized) return; // 401 已回令牌门
    F.stage = 'unknown';
    F.message = '网络层失败，提交是否生效不确定。';
  }
  syncSessModal();
}
/* sessContinue 继续此会话（详情页动作区/列表行 → continue 形态 modal；登记中会话
 * 同样可续——预分配平台 ID 受理 create 轮）。 */
function sessContinue(id) {
  const src = (S.sessPage && S.sessPage.id === id ? S.sessPage.session : null) || S.sessions.find(x => x.id === id);
  if (!src) { toast('会话信息不可得'); return; }
  if (src.status === 'closed' && !src.pending) { toast('会话已关闭：续轮请新建会话'); return; }
  S.sess = { ...freshSessForm(), mode: 'continue', contId: src.id, contSnap: { agent: src.agent_name, adapter: src.adapter, permission_mode: src.permission_mode }, cwd: src.cwd || '', nextSeq: (src.turn_count || 0) + 1, agent: S.nodeID + '|' + src.agent_name };
  openSessModal();
  requestAnimationFrame(() => $('#sess-prompt')?.focus());
}
/* ---------- 任务页迁移说明卡（W01-05：会话区块迁出，旧位置留链接） ---------- */
function sessMigrateCard() {
  return `<section class="card spaced" id="sess-card"><div class="row"><h2>本机会话</h2></div>
  <div class="empty"><h2>本机会话已迁至独立「会话」页</h2><p class="muted">会话列表、新建/继续（居中表单）与详情查看现集中在侧栏「会话」导航页；任务页不再承载会话表单。</p>${button('前往会话页 →', '', 'primary', '', 'data-go="sessions"')}</div></section>`;
}
async function pollTick() {
  if (!S.authed || document.visibilityState !== 'visible') return;
  const wasStale = !!S.loadError;   // P1-03：新鲜度基线（失败→恢复转移判据）
  try {
    if (await loadAll()) {
      if (cfgEdit) return; // 能力配置编辑抽屉开着：数据照常刷，表格 DOM 不动（编辑会话不受重绘干扰）
      /* 会话交互豁免（T3 R1 必修10；R2 必修10 收敛）：会话抽屉开放或焦点在会话表单
       * 内（或刚从 stale 恢复）时，数据变化走双槽位局部更新（任务列表+会话列表）+
       * 连接徽标独立直更——整页 render 会重建 #app，任务页上的会话表单焦点/光标与
       * 抽屉下方页面 DOM 被替换；镜像草稿不满足交互 DOM 保持纪律（D22 局部刷新铁
       * 律），表单与背景壳必须稳定。豁免只决定绘制方式、不截断资源加载：此处不
       * return，落到函数尾公共路径照常 loadSessions（R2 N1：此前提前 return 使豁
       * 免轮跳过会话 GET，持续变化期间列表新增/关闭/轮次推进与 503 恢复全部滞后；
       * wasStale 恢复同样经槽位呈现不经整页 render——P1-03 原分支语义，其条件已被
       * 本分支 wasStale 涵盖）。epoch/seq 守卫在 loadSessions 内维持（R1 必修5）。 */
      if (S.page === 'sessions') {
        /* 会话页（W01-05 P01/P02）：槽位局部更新（chips/列表/页脚 + 详情分区），
         * 整页 render 会冲掉搜索输入焦点与 #app 下页面 DOM（D22 局部刷新铁律）。
         * 会话表单在 #layer modal（render 不触及），轮询零干扰。 */
        paintConnChip(); paintSessionsView();
        if (S.sessId) { try { await fetchSessionPage(); } catch (e2) { if (e2?.unauthorized) return; } syncSessTurnDrawerFromPage(); }
      } else if (S.page === 'tasks' && (wasStale || S.sessModal)) {
        paintConnChip(); paintTasksCard();
      } else {
        render();
      }
    } else if (wasStale) {
      if (S.page === 'sessions') paintSessionsView();   // 同数据恢复：指纹门控下 changed 恒 false，靠槽位更新撤陈旧条
      else paintTasksCard();
    }
    if (S.page === 'sessions') {
      /* 会话列表随轮询（独立于 loadAll：503 专态不拖垮会话页数据链）；paintSessionsView
       * 只重写槽位，搜索输入与 #layer modal 不受触碰。 */
      try { await loadSessions(); } catch (e2) { if (e2?.unauthorized) return; }
      paintSessionsView();
    }
  } catch (e) {
    /* 401 已由 api() 处理；其余落失败态（A4 五态：列表区分「失败可重试」与
     * 「真的没有」，不再静默冒充空列表），下轮轮询自动恢复。失败首次转移时就地
     * 更新让陈旧/失败态及时可见——tasks 页只刷任务列表槽位（派单表单在页，5s
     * 轮询整页重绘会打断输入，2026-10-21 验收口径；P1-03 前该页整页豁免使失败态
     * 在派单页永不出现），其余页整页 render。 */
    if (!e.unauthorized) {
      const was = S.loadError;
      S.loadError = '本机 API 暂不可达';
      if (!was) {
        if (S.page === 'tasks') paintTasksCard();
        else if (!cfgEdit) render();
      }
    }
  }
}

/* ---------- 任务列表槽位局部更新（P1-03，FE-POLISH 轮2 修复） ----------
 * paintTasksCard 只重写 #my-tasks-card 内部：新鲜度转移（失败态出现/同数据恢复
 * 撤陈旧条）不经整页 render——派单表单在页（2026-10-21 验收口径），表单焦点与
 * 草稿不受触碰。内容与上次写入一致不碰 DOM（稳定轮询零重绘，动画/焦点/滚动不受
 * 影响）；焦点在卡内（列表行/重试钮/加载更早）时按 data-task/data-act 回位，
 * 失存回落侧栏当前页签（与 closeDrawer 兜底同构）。
 * P1-03 残余 A（r2 复审）：缓存以 WeakMap 绑卡元素实例而非跨页面字符串——切页
 * 整页 render 重建 DOM 后新卡无记录，旧缓存天然失效；否则「失败→切走→恢复→切回
 * →再失败」的第二次失败提示会被旧缓存命中吞掉。
 * R2F-P2-01（r2 复审）：只写卡内内容 myTasksZone()——壳 section 与唯一 id 由
 * myTasksCard() 在整页 render 时创建一次，不把完整壳写回自身（重复 ID/双层卡片）。 */
const tasksCardPaint = new WeakMap();   // 卡元素实例 → 上次写入的 HTML（变了才写）
function paintTasksCard() {
  const card = document.getElementById('my-tasks-card');
  if (!card) return;
  const html = myTasksZone();
  if (tasksCardPaint.get(card) === html) return;
  const active = document.activeElement;
  const mark = active && card.contains(active)
    ? { task: active.closest('[data-task]')?.getAttribute('data-task') || '', act: active.getAttribute('data-act') || '' }
    : null;
  tasksCardPaint.set(card, html);
  card.innerHTML = html;
  if (mark) {
    const el = mark.task
      ? card.querySelector(`[data-task="${CSS.escape(mark.task)}"]`)
      : mark.act ? card.querySelector(`[data-act="${CSS.escape(mark.act)}"]`) : null;
    if (el) el.focus();
    else {
      const tab = document.querySelector('aside nav button.active') || document.querySelector('aside nav button');
      if (tab) tab.focus();
    }
  }
}

/* ---------- 令牌门（本机管理令牌 = 本机控制台的登录） ---------- */
async function tryToken(token) {
  const epochAtReq = S.sessionEpoch; // 捕获登录发起时代次
  $('#gate-error').innerHTML = '';   // A3：新一轮验证先清上一轮错误条
  S.token = token;
  resetPrimeState(); // 新会话边界：清上个会话凭证，epoch +1
  const myEpoch = S.sessionEpoch; // 本登录会话的代次基准（自身 reset 之后取，免自误判）
  try {
    const r = await api('GET', '/api/v1/local/status', undefined, { noGate: true });
    if (S.sessionEpoch !== myEpoch) return; // 登录期间发生别的会话边界（logout/再登录/401），本次应答丢弃
    if (r.code === 200) {
      localStorage.setItem(TOKEN_KEY, token);
      S.authed = true;
      S.snap = '';
      render();
      loadAll().then(c => { if (c) render(); }).catch(() => {});
      loadMcpAccess().then(() => { if (S.page === 'overview') render(); applyPendingAnchor(); }).catch(() => {}); // 终审建议①：令牌门首登落在总览页时授权卡自举（否则滞留 loading 且 loading 态无重试入口，须手动切页才自愈）
      return;
    }
    /* A3 错误就地留痕：令牌验证失败留在令牌门页（人话原因+核对指引），toast 只短报。 */
    gateError('令牌未被接受', errLine(r.data) + '。请核对 node.yaml 的 <code>local_token</code> 或 <code>data_dir/local_token</code> 文件内容后重试。');
    toast('令牌未被接受 · ' + errLine(r.data));
  } catch (e) {
    if (S.sessionEpoch !== myEpoch) return; // 迟到失败同理不碰新会话
    gateError('连接失败', '本机 API 不可达：确认 node 已启动后重试；输入内容保留在输入框里。');
    toast('本机 API 不可达，确认 node 已启动');
  }
  if (S.sessionEpoch === myEpoch) {
    S.token = localStorage.getItem(TOKEN_KEY) || '';
  }
}
/* gateError 令牌门错误条（A3）：与表单错误条同 danger 语义；开始新一次验证时清除。 */
function gateError(title, bodyHtml) {
  const el = $('#gate-error');
  if (el) el.innerHTML = `<div class="notice danger"><b>${esc(title)}</b> · ${bodyHtml}</div>`;
}

/* ---------- 路由（hash SPA） ---------- */
const PAGES = [['overview', '总览'], ['services', '服务'], ['tasks', '任务'], ['sessions', '会话'], ['wizard', '连接']];   // W01-05 P01：会话独立主入口（由任务页区块迁出）
// setup 模式只开向导 + 有限本机浏览（方案 3.3：可看本机环境，不能派单）。
const SETUP_PAGES = [['wizard', '连接向导'], ['overview', '本机环境']];
function route() {
  const h = (location.hash || '#/overview').replace(/^#\//, '');
  const [ph, arg] = h.split('/');
  const allowed = S.authed && S.status?.mode === 'setup' ? SETUP_PAGES : PAGES;
  /* W03-04 P07：「#/mcp」为 MCP 接入子页（资源域；不占主导航——PAGES 词表为权威
   * 锁定面，子页经授权卡/管家卡指路与深链进入；setup 形态不可达——授权与配置面
   * 在完成接入后开放，路由按未知 hash 回落向导页）。 */
  if (ph === 'mcp' && S.authed && S.status?.mode !== 'setup') S.page = 'mcp';
  else S.page = allowed.some(p => p[0] === ph) ? ph : allowed[0][0];
  /* W01-05 P02：#/sessions/{id} 独立详情页路由。换会话/退列表即换视图状态；
   * 表单 modal 随任意路由变更关闭（草稿留 S.sess）；执行明细抽屉仅随所属详情页
   * 存续（换会话/退列表即关——离开会话页后无轮询驱动，滞留即冻结假象）。 */
  const prevSessId = S.sessId;
  S.sessId = S.page === 'sessions' && arg ? arg : '';
  if (S.sessId && S.sessId !== prevSessId) S.sessPage = { id: S.sessId, session: null, zones: {}, sel: null };
  if (!S.sessId) S.sessPage = null;
  if ($('#sess-modal')) closeSessModal();
  /* W03-04 P07：换路由即关 MCP 配置弹层与诊断抽屉（草稿/预览属页面会话，重开即
   * 从最近表单快照出发；在途应答凭 mcpBoardSeq/会话代次丢弃）。 */
  if (mcpModal && ph !== 'mcp') closeMcpModal(false);
  if (mcpDrawer && ph !== 'mcp') closeMcpDrawer();
  if (S.sessDrawer && (!S.sessId || S.sessDrawer.id !== S.sessId)) closeSessionDrawer();
  if (S.page === 'services' && S.authed && S.status?.mode !== 'setup' && !S.cfg.loaded && !S.cfg._loading) {
    S.cfg._loading = true;
    loadCfg().finally(() => { S.cfg._loading = false; });
  }
  // 通知区块（notify-unify 改动三起三模式渲染）：status.notify_available 由装配
  // 决定（全装配恒 true；setup 页面裁剪不可达）；进页拉一次，变更动作各自重拉。
  // 「同时通知」开关同点拉取。
  if (S.page === 'services' && S.authed && S.status?.notify_available && !S.notifyLoaded && !S.notifyLoading) {
    loadNotifyChannels();
    loadNotifySettings();
  }
  // P08 决策设置（W06-03）：MCP 编排授权卡随总览页拉取（不进轮询——开关变更仅经
  // 本页 PUT 与盘上显式修改发生，进页即取最新；状态行不参与 loadAll 指纹）。
  // setup 形态不拉（卡片收缩为指引态，无操作面）。
  if (S.page === 'overview' && S.authed && S.status?.mode !== 'setup' && !mcpAccessLoading) loadMcpAccess();
  // W01-05 P01/P02：会话独立页（列表主入口/详情页）；进页拉一次（轮询随 pollTick
  // 保持新鲜；503 专态如实降级）。详情页即取（fetchSessionPage 守卫换会话/关页）。
  render();
  applyPendingAnchor();   // P08 深链（W06-03）：跨页锚点在路由与重绘后落点
  if (S.page === 'mcp') mcpEnter();   // W03-04 P07：六分区子页进页（分区绘制+验证板拉取）
  if (S.page === 'sessions' && S.authed) {
    loadSessions().then(paintSessionsView).catch(() => { });
    if (S.sessId) {
      syncSessPageZones();   // 已有快照即绘（分区缓存随 DOM 身份对账失效重写）
      fetchSessionPage();
    }
  }
}
window.addEventListener('hashchange', route);

/* ---------- 基础组件 ---------- */
function button(label, act, cls = '', inner = '', attr = '') {
  return `<button data-act="${act}" class="${cls}" ${attr} ${inner}>${label}</button>`;
}
function badge(status) {
  const s = String(status || 'submitted');
  return `<span class="badge ${esc(s)}" title="${esc(s)}">${esc(STATES[s] || s)}</span>`;
}
function timeShort(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  return isNaN(d) ? esc(iso) : d.toLocaleTimeString('zh-CN', { hour12: false });
}
function timeFull(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  return isNaN(d) ? esc(iso) : d.toLocaleString('zh-CN', { hour12: false });
}

/* ---------- 布局（shell + 侧栏导航；setup 模式导航裁剪为向导+本机环境） ---------- */
function layout(content, pages = (S.status?.mode === 'setup' ? SETUP_PAGES : PAGES)) {
  const online = S.status?.online;
  /* 导航壳与 hub 同构（TASK-nav-filter B）：navnum 01-04 同格式、aria-label="旅程导航"
   * + aria-current="page"、workspace 行序 small 在上 strong 在下（与 hub 同构）。 */
  return `<div class="shell"><aside><div class="workspace"><small class="muted">本机控制台</small><strong class="workspace-brand" title="${esc(S.nodeID || 'node')}"><img class="brand-logo" src="/logo.svg" alt="" width="24" height="24"><span class="ws-name">${esc(S.nodeID || 'node')}</span></strong></div>
  <nav aria-label="旅程导航">${pages.map(([id, label], i) =>
    `<button data-go="${id}" class="${S.page === id ? 'active' : ''}" ${S.page === id ? 'aria-current="page"' : ''}><span class="navnum">0${i + 1}</span>${label}</button>`).join('')}</nav>
  <div class="aside-bottom">连接 hub：${online ? '<span class="badge online">在线</span>' : '<span class="badge offline">离线</span>'}<br>
  <span style="opacity:.75">本机控制台<br>仅本机可访问</span></div></aside>
  <main>${content}${versionFoot()}</main></div>`;
}
/* versionFoot 页脚版本（TASK-maturity-version-var，与 hub 管理台同构：main 末尾
 * 纯文本 v<版本号>，复用 footer-note 样式；值来自本机 API status 端点
 * server_version 字段——禁止硬编码进本资产。未取到前不渲染）。 */
function versionFoot() {
  const v = S.status?.server_version;
  return v ? `<p class="footer-note" id="server-version">v${esc(v)}</p>` : '';
}

/* ---------- 渲染 ---------- */
/* paintConnChip 顶栏连接徽标唯一写入点（T3 R1 必修10 自 render 提取）：会话豁免
 * 路径（槽位局部更新）与整页 render 共用，豁免期间连接状态不滞留旧值。 */
function paintConnChip() {
  if (S.authed && S.status?.online != null) {
    const chip = $('#conn-chip');
    chip.className = 'badge ' + (S.status.online ? 'online' : 'offline');
    chip.textContent = S.status.online ? '中心端在线 · 重连 ' + S.status.epoch + ' 次' : 'hub 离线';
  }
}
function render() {
  $('#conn-chip').hidden = !S.authed;
  $('#logout').hidden = !S.authed;
  $('#who').textContent = S.authed ? 'Bearer 令牌已就绪' : '';
  paintConnChip();
  $('#app').innerHTML = !S.authed ? gateView() : layout(pageView());
  if (S.drawer) refreshDrawer(S.drawer.seq); // 第六轮修复 3：抽屉打开时随全局数据刷新重取内容（传 seq：非当前抽屉会话的迟到应答由守卫丢弃）
  /* W03-04 P07：整页重绘（轮询数据变化/动作落定）会重建 #mcp-page 分区容器——
   * 缓存清零后全量重绘一次；页内局部更新走 paintMcpZones（缓存 diff，不整页）。 */
  if (S.authed && S.page === 'mcp') { mcpZoneCache = {}; paintMcpZones(); }
}
// pageView 按路由出页面（setup 模式下 route() 已把页面收敛到 SETUP_PAGES）。
function pageView() {
  if (S.page === 'wizard') return wizardPage();
  if (S.page === 'services') return servicesPage();
  if (S.page === 'tasks') return tasksPage();
  if (S.page === 'sessions') return sessionsPageView();
  if (S.page === 'mcp') return mcpPage();   // W03-04 P07：MCP 接入子页（六分区）
  return overviewPage();
}
function gateView() {
  return `<main><div class="card login"><div class="mark"><img class="brand-logo" src="/logo.svg" alt="SwarmMind" width="64" height="64"></div><h1>SwarmMind Node 本地控制台</h1>
  <p class="muted">本机管理令牌鉴权（Bearer）。令牌来自 node.yaml 的 <code>local_token</code>；未配置时 node 首次启动已生成，见启动日志或 <code>data_dir/local_token</code> 文件。</p>
  <label>本机管理令牌<input id="gate-token" type="password" placeholder="local_token" autocomplete="off"></label>
  <div id="gate-error" class="statusline" role="status"></div>   <!-- A3 错误就地留痕：令牌验证失败不再只 toast -->
  ${button('进入控制台', 'gate-enter', 'primary')}
  <p class="footer-note">仅本机回环可达；令牌只存本浏览器（localStorage），不出本机。</p></div></main>`;
}

/* ---------- 连接中心端（方案 3.3：粘贴连接串 → 预览 node.yaml → 保存落盘重启；
 * setup 模式的首启页，standalone/组网经「连接」页可重新连接/切换 hub） ---------- */
function wizardPage() {
  const w = S.wiz;
  const locked = w.stage === 'previewing' || w.stage === 'saving' || w.stage === 'done';
  const stageView = {
    idle: '',
    previewing: `<div class="statusline"><span class="badge offered">解析中…</span></div>`,
    saving: `<div class="statusline"><span class="badge offered">保存中…</span></div>`,
    done: `<div class="notice ok"><b>已写入 <code>${esc(w.written)}</code></b>：node 将在数秒后自动重启进入组网模式（保活拉起）。若为裸前台运行，请手动重启进程后刷新本页。</div>`,
    failed: `<div class="notice warn"><b>保存失败</b>：${esc(w.msg)}</div>`,
  }[w.stage] || '';
  const preview = w.preview ? `<label>预览：将写入的 node.yaml（agents 段留空，连接后在网页/手编添加）<textarea readonly rows="12" class="mono">${esc(w.preview.yaml_preview)}</textarea></label>` : '';
  return `<div class="page-head"><div><h1>连接中心端</h1>
    <p class="muted">从 hub 控制台「添加机器向导 → 复制连接串」拿到 <code>swarmmind://join?…</code>，粘贴到下面完成连接（写入前可反复预览）。</p></div></div>
  <section class="card">
    <label>连接串<textarea id="wiz-join" rows="3" placeholder="swarmmind://join?hub=wss%3A%2F%2F…&node=…&token=…" ${locked ? 'disabled' : ''}>${esc(w.join)}</textarea></label>
    <div class="row">
      ${button(w.preview ? '重新预览' : '预览', 'wiz-preview', w.preview ? '' : 'primary', locked ? 'disabled' : '')}
      ${w.preview && w.stage !== 'done' ? button('保存并重启', 'wiz-apply', 'primary', locked ? 'disabled' : '') : ''}
    </div>
    ${w.preview ? `<div class="notice ok"><b>识别结果</b>：hub <code>${esc(w.preview.hub_url)}</code> · 本机节点 <code>${esc(w.preview.node_id)}</code></div>` : ''}
    ${preview}${stageView}
    <p class="footer-note">本页与保存接口同样受本机管理令牌保护（data_dir/local_token）——免鉴权写配置等于把 agent 执行权交给任何本机进程。保存即写回启动时的配置路径，node 随后自动重启；本机历史任务不受影响。</p>
  </section>`;
}

/* ---------- 页面①：总览（旅程③运维 + 三步引导空态） ---------- */
function overviewPage() {
  const st = S.status;
  const led = st?.ledger || {};
  const head = `<div class="page-head"><div><h1>总览</h1>
    <p class="muted">本机 node 状态与最近任务；下一步：<b>＋派单</b> 把工作交给本机服务。</p></div>
    <div class="actions">${button('复制诊断信息', 'copy-diag')}${button('＋ 派单', 'go-tasks', 'primary')}</div></div>`;
  if (!S.agents.length) {
    return head + `<section class="card"><h2>三步让本机加入协作网</h2>
      <p class="muted">本机还没有任何 agent。推荐网页登记（也可手编 node.yaml）：</p>
      <div class="step"><span>1</span><div><b>登记能力</b><div class="muted">去「服务 → 能力配置」点「＋ 新增 agent」（command / workdir 绝对路径）；习惯手编的编辑 <code>node.yaml</code> 的 <code>agents:</code> 段，完整字段见 <code>configs/node.example.yaml</code>。</div></div></div>
      <div class="step"><span>2</span><div><b>应用并重启</b><div class="muted">网页保存结构性变更（新增 agent）后点「应用并重启」；手编的重启 node。重启不丢已确认的任务结果。</div></div></div>
      <div class="step"><span>3</span><div><b>钦定管家并派单</b><div class="muted">在「本机管家」区钦定一个 agent 担任智行管家，随后即可在本机派单；需要开放给其他机器调用时再打开开关。</div></div></div></section>`
      + (st?.mode === 'setup' ? primeCard() : '') + mcpAccessCard();
  }
  return head + `
  <div class="grid cols3 spaced">
    <section class="card"><h3>连接状态</h3><div class="metric"><span class="badge ${st?.online ? 'online' : 'offline'}">${st?.online ? '在线' : '离线'}</span></div>
      <small class="muted">掉线自动重连，任务不丢</small></section>
    <section class="card"><h3>重启恢复</h3><small class="muted">执行到一半的任务重启后标成「等你确认」，绝不自动重跑；已完成的结果原样保留。</small>
      <div class="ledger-line">账本 · 受理 <b>${esc(led.accepted ?? 0)}</b> · 进行中 <b>${esc(led.started ?? 0)}</b> · 已结束 <b>${esc(led.finished ?? 0)}</b></div></section>
    <section class="card"><h3>最近同步</h3><div class="metric">${st?.last_sync_at ? timeShort(st.last_sync_at) : '从未'}</div>
      <small class="muted">${st?.last_sync_at ? '最近一次注册成功 · ' + esc(timeFull(st.last_sync_at)) : '注册成功后这里显示时间'}</small></section>
  </div>
  ${primeCard()}${mcpAccessCard()}${recentTasksCard()}`;
}
/* ---------- P08 决策设置深链（W06-03）：三区互相锚点，不建新页 ----------
 * 「默认管家设置」（总览·管家卡）↔「MCP 授权」（总览·编排授权卡）↔「能力发现」
 * （服务·能力配置区，发现入口在该区内）。跨页锚点先路由到宿主页再滚动：
 * pendingAnchor 在 route/render 链与目标区异步加载（loadMcpAccess/loadCfg）完成后
 * 由 applyPendingAnchor 消费——深链落点不因加载重绘丢失。锚点 <span id> 一律放
 * 在卡片字节锁前缀之后（TestWebOverviewSpacing 逐字锁定各区 <section> 开头串，
 * 锚点内嵌不得破坏既有断言）。setup 形态下 services 页不可达，setup 分支不渲染
 * 互链行（锚点 span 无害保留）。 */
const P08_ANCHOR_PAGE = { 'prime-setup': 'overview', 'mcp-access': 'overview', 'capability-config': 'services' };
let pendingAnchor = '';
function gotoAnchor(id) {
  if (!P08_ANCHOR_PAGE[id]) return;
  if (S.page !== P08_ANCHOR_PAGE[id]) { pendingAnchor = id; location.hash = '#/' + P08_ANCHOR_PAGE[id]; return; }
  const el = document.getElementById(id);
  if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' });
  else toast('目标区域当前不可见（如尚未登记能力或未完成接入）——完成对应设置后此链接即可跳转'); // 终审建议③：同页死按钮给指引不静默
}
let pendingAnchorTries = 0;
function applyPendingAnchor() {
  if (!pendingAnchor || S.page !== P08_ANCHOR_PAGE[pendingAnchor]) return;
  const el = document.getElementById(pendingAnchor);
  if (!el) {
    pendingAnchorTries++;
    if (pendingAnchorTries >= 3) { toast('跳转目标「' + (P08_LINKS.find(l => l[0] === pendingAnchor) || ['', pendingAnchor])[1] + '」尚未出现——完成对应设置后可再次点击链接'); pendingAnchor = ''; pendingAnchorTries = 0; }
    return; // 未达上限：待目标区渲染后的下一轮消费（终审建议③：跨页缺失不再无限挂起）
  }
  pendingAnchor = '';
  pendingAnchorTries = 0;
  el.scrollIntoView({ behavior: 'smooth', block: 'start' });
}
/* deepLinksRow/linkTo 决策设置互链（文案层单一起源：三区词典同一数组，互链行与
 * 行内指路链接共用；skip=当前区自身不出现在自己的互链行里）。 */
const P08_LINKS = [['prime-setup', '默认管家设置'], ['mcp-access', 'MCP 编排授权'], ['capability-config', '能力发现 · 能力配置']];
function deepLinksRow(skip) {
  return P08_LINKS.filter(([id]) => id !== skip)
    .map(([id, label]) => button(label, 'goto:' + id, 'link'))
    .join('<span class="muted"> · </span>');
}
function linkTo(id, label) {
  return button(label, 'goto:' + id, 'link');
}

/* 本机管家 · 形态（方案 3.1/3.2.1）：网页钦定闭环——未钦定/换管家走选择视图
 * → declare → 钦定完成视图（凭据事实块 + MCP 配置片段；W02-01 起钦定不换发/不返回凭据）→ 已钦定常态（轮换 +
 * 换管家 + 取消管家入口）。2026-10-29 部署后走查反馈一：能力卡管家按钮全退役
 * （能力卡只留「管家·本机」徽标做身份展示），钦定/换/取消/轮换唯一入口收敛到
 * 本卡；取消管家沿用 2026-10-28 声明制口径（弱化文字链，PATCH orchestrator=false）。
 * setup 模式只提示不开放（矩阵 #9/#18）。全部动态值过 esc()。
 * W06-03 P08：head 增「决策设置」互链行（→MCP 编排授权/能力发现；setup 分支除外
 * ——services 页在 setup 不可达）；行内「MCP 授权」区指路文案升格为深链。 */
function primeCard() {
  const st = S.status;
  const pa = st?.prime_agent || '';
  const modeTxt = st?.mode === 'standalone' ? '本机自治，不连中心端'
    : st?.mode === 'setup' ? '未连接中心端' : '组网在线';
  // 技术态代号只进悬停（PRODUCT-DIRECTION-v2：人话在前，技术态进详情/悬停）
  const modeTech = st?.mode === 'standalone' ? 'standalone'
    : st?.mode === 'setup' ? 'setup' : st?.mode === 'networked' ? 'networked' : '';
  const modeStat = `<span><b${modeTech ? ` title="形态代号：${modeTech}"` : ''}>${esc(modeTxt)}</b><small>当前形态</small></span>`;
  const foot = `<p class="footer-note">断开组网后：还在 hub 离线暂存里、未送达的任务将留到过期；本机历史任务不受影响，本机派单照常可用。</p>`;
  if (st?.mode === 'setup') {
    return `<section class="card spaced"><h2>本机管家 · 形态</h2><span id="prime-setup"></span>
    <div class="mini-stat spaced"><span><b>未钦定</b><small>本机管家</small></span>${modeStat}</div>
    <small class="muted">先完成连接并配置能力后再钦定管家。</small></section>`;
  }
  const head = `<section class="card spaced"><div class="row"><h2>本机管家 · 形态</h2>
    <div class="actions">${st?.mode === 'networked' ? button('断开组网', 'net-disconnect') : ''}</div></div>
  <span id="prime-setup"></span>
  <div class="mini-stat spaced"><span><b>${pa ? esc(pa) : '未钦定'}</b><small>本机管家</small></span>${modeStat}</div>
  <div class="spaced"><small class="muted">决策设置：</small>${deepLinksRow('prime-setup')}</div>`;
  const newTok = S.primeNew ? `<div class="notice warn spaced"><b>新管家凭证（仅此一次展示，立即复制）</b>：<code>${esc(S.primeNew)}</code> ${button('复制', 'copy-prime-token', 'link')}<br><small>旧凭证已失效；${S.primeNewWire ? `新凭证已自动写入管家的 MCP 配置（<code>${esc(S.primeNewWire.target)}</code>，重连后生效），或「换一个管家」重新走钦定。` : `请立即配给管家的 MCP 连接（SWARM_NODE_PRIME_TOKEN），或「换一个管家」重新走钦定；其他宿主条目的在线生成/预览见${mcpPageLink('「MCP 接入」页')}。`}</small></div>` : '';
  /* 声明状态行（P2-01 双源如实，2026-10-29 走查反馈批）：本机凭证持有人=
   * prime_agent（node.yaml），本机声明=能力卡 Orchestrator 位（agents-state
   * 覆盖层）。两源分列实值，不一致时点名「取消管家将撤声明 B」；取消入口按
   * 实际声明存在决定——prime_agent 为空但声明仍在（自旧声明制升级/手编 yaml
   * 的合法态）入口保持可达。不凭本地卡位宣称「全网已生效」：本地声明随能力表
   * 上报 hub 汇聚，全网恰一与否以 hub 目录为准（跨机冲突 hub 告警），本行不作
   * 全网确认承诺。 */
  const declaredList = S.agents.filter(a => a.orchestrator).map(a => a.name);
  const orchRow = () => {
    const names = declaredList.map(n => `<code>${esc(n)}</code>`).join('、');
    let txt;
    if (declaredList.length === 1 && declaredList[0] === pa) {
      txt = `本机凭证持有人 <code>${esc(pa)}</code> · 本机声明 <code>${esc(declaredList[0])}</code>（两源一致）。声明随能力表上报 hub 汇聚，全网是否恰一以 hub 控制台目录为准（跨机冲突由 hub 告警）；取消管家只撤本机声明，凭证不动（换管家同样不换发凭据；作废凭据走「轮换管家凭证」）。`;
    } else if (declaredList.length === 1) {
      txt = `本机凭证持有人 <code>${esc(pa || '（未设）')}</code> / 本机声明 <code>${esc(declaredList[0])}</code>——两源不一致：「取消管家」将撤声明 <code>${esc(declaredList[0])}</code>，凭证持有人不动。`;
    } else if (declaredList.length > 1) {
      txt = `本机声明异常（多声明：${names}）——正常至多一个；下方按钮逐一点击撤除，或「换一个管家」重新钦定收敛为恰一。`;
    } else {
      txt = `本机声明未生效（hub 回退按能力名含 <code>zcode-dev</code> 识别）；「换一个管家」重新钦定即随钦定一并上报声明。`;
    }
    const cancels = declaredList.map(n => button(declaredList.length > 1 ? '撤除声明·' + esc(n) : '取消管家', 'orch-off:' + esc(n), 'link', '', `data-agent="${esc(n)}"`)).join('');
    return `<div class="orch-state spaced"><small class="muted">${txt}</small>${cancels ? `<div class="row spaced">${cancels}</div>` : ''}</div>`;
  };
  if (S.primeDecl.done) return head + primeDoneView() + foot + '</section>';
  if (!pa || S.primeDecl.picking) {
    // 选择视图（未钦定引导/换管家）：存在声明位时状态行+取消入口仍可达（P2-01）
    return head + (declaredList.length ? orchRow() : '') + primePickView(pa) + foot + '</section>';
  }
  return head + `<small class="muted">智行管家经 MCP（prime_token）获得的是<b>本机管家权限</b>：只管本机派单；本机主人（local_token）在网页派单同样放行。钦定/轮换/配置管理等管理操作仅主人可用。</small>
  ${orchRow()}
  <div class="row spaced">${button('轮换管家凭证', 'prime-rotate', 'link')}${button('换一个管家', 'prime-pick', 'link')}</div>${newTok}${foot}</section>`;
}
/* 钦定选择视图（未钦定引导态与「换一个管家」共用；数据源本机 agents 列表）。 */
function primePickView(pa) {
  const opts = S.agents.map(a => `<option value="${esc(a.name)}">${esc(a.name)}${a.description ? ' — ' + esc(a.description) : ''}</option>`).join('');
  return `<small class="muted">${pa ? '当前管家将被替换：钦定/换管家不再换发共享编排凭据（prime_token），现有 MCP 连接与授权状态持续有效；如需作废旧凭据请用「轮换管家凭证」' : `钦定一个本机 agent 担任智行管家：它经 MCP（prime_token）获得本机编排权（本机管家权限）——且须主人在${linkTo('mcp-access', '「MCP 授权」区')}显式启用编排授权（新实例默认禁用，禁用期间该派单被 403 mcp_disabled 拒绝）；本机主人网页派单不依赖管家凭证。`}</small>
  ${S.agents.length ? `<label>本机 agent<select id="prime-select">${opts}</select></label>
  <div class="row spaced">${button(S.primeDecl.busy ? '钦定中…' : '钦定为智行管家', 'prime-declare', 'primary', S.primeDecl.busy ? 'disabled' : '')}${pa ? button('取消', 'prime-pick-cancel', 'link') : ''}</div>`
    : `<div class="empty"><p class="muted">本机还没有可钦定的 agent：先在「服务 → 能力配置」登记并重启 node。</p>${button('去看服务', 'go-services', '')}</div>`}`;
}
/* 钦定完成视图：成功提示 + 凭据事实块 + MCP 配置片段。wired=true（t20261023-62a2
   自动接线）：明示已写入的影子 home 配置路径，零手贴；降级：保留手动粘贴指引。 */
function primeDoneView() {
  const d = S.primeDecl.done;
  if (!d) return '';
  const mcpJSON = d.mcp ? JSON.stringify(d.mcp, null, 2) : '（应答未含 mcp_config，请点「换一个管家」重新钦定）';
  const wiredNote = d.wired
    ? `<div class="notice ok spaced"><b>已自动接线</b>：管家 MCP 连接已写入 <code>${esc(d.target)}</code>（mcpServers.swarmmind）——零手贴，agent 重连后即可本机派单；原配置备份在同目录 <code>setting.json.bak-prime</code>。</div>`
    : '';
  const tokTail = d.wired ? '凭证已随接线写入宿主配置，此处展示仅供留档备份。' : '请立即配给管家的 MCP 连接（SWARM_NODE_PRIME_TOKEN）。';
  /* 声明三态（P1-02，2026-10-29 走查反馈批）：消费 declare 应答的
   * orchestrator/orchestrator_error/hub_synced——orch=false 本机声明失败（原因+
   * 重试入口，失败态随 done 持久展示）；orch=true & hub_synced=true
   * 已提交上报（措辞不越界：提交成功≠hub 全网恰一确认，恰一以 hub 目录为准）；
   * hub_synced=false 本机成功、hub 待同步（重连随注册收敛）；hub_synced 缺省=
   * standalone 未组网（无上报环节）。orch 缺省=应答未带声明位（测试构造），
   * 不作任何声明承诺。W02-01 起钦定不换发/不返回共享凭据（调用权与管家角色
   * 解耦）：应答 prime_token 恒空、mcp_config 凭据位脱敏——凭证展示块仅在应答
   * 真带明文时出现（兼容构造），缺省呈现「凭据不变」事实与轮换指引。 */
  /* 失败说明（fix2 去臆测，2026-10-29 复审 P1-02 残余）：只陈述本机事实——
   * 本次目标声明未完成 + 既有有效声明保持原样（先写后见：失败整集不发布）+
   * 本机当前声明位实值（S.agents 同源）。不推断 hub 目录内容：落盘失败时 hub
   * 可仍持旧有效声明，「hub 目录暂无本机声明位」属无依据断言，删除。 */
  const declaredNow = (S.agents || []).filter(a => a.orchestrator).map(a => a.name);
  const declList = declaredNow.length ? declaredNow.map(n => '<code>' + esc(n) + '</code>').join('、') : '无';
  const declNote = d.orch === false
    ? `<div class="notice warn spaced"><b>钦定已生效，本机声明失败</b>（${esc(d.orchErr || '原因未返回')}）：管家声明（node.yaml prime_agent）已写入并即时生效，本次能力目录 Orchestrator 位声明未完成；既有有效声明保持原样——本机当前声明位：${declList}。点「重试声明」补写（声明补写不涉及凭据），或「换一个管家」重新走钦定。</div>`
    : d.orch === true
      ? (d.synced === true
        ? `<small class="muted decl-note">本机声明已随钦定一并提交上报 hub（能力目录 Orchestrator 位）；hub 全网是否恰一以 hub 控制台目录为准（跨机冲突由 hub 告警）。</small>`
        : d.synced === false
          ? `<div class="notice warn spaced"><b>本机声明成功，hub 待同步</b>：能力表上报未送达（hub 离线或链路故障）；node 重连注册后会随能力表自动收敛，无需重做钦定。</div>`
          : `<small class="muted decl-note">本机声明已生效（standalone 未组网，无 hub 上报环节）。</small>`)
      : '';
  const retryBtn = d.orch === false ? button('重试声明', 'prime-retry-orch', '', '', `data-agent="${esc(d.agent)}"`) : '';
  /* 凭据事实块（W02-01 解耦）：declare 不换发不返回明文——仅在应答真带明文时
   * 展示（兼容构造）；缺省按 wired 两态如实呈现当前凭据状态与作废指引。 */
  const credBlock = d.token
    ? `<div class="notice warn spaced"><b>管家凭证（仅此一次展示，立即复制）</b>：<code>${esc(d.token)}</code> ${button('复制凭证', 'copy-prime-declare', 'link')}<br><small>${tokTail}</small></div>`
    : (d.wired
      ? `<div class="notice warn spaced"><b>共享编排凭据不变</b>：钦定不换发凭据；当前凭据已随自动接线写入管家宿主配置，现有 MCP 连接持续有效。如需作废旧凭据请用「轮换管家凭证」（新明文仅轮换时一次展示）。</div>`
      : `<div class="notice warn spaced"><b>共享编排凭据不变</b>：钦定不换发凭据。管家 MCP 连接未自动写入——下方片段中凭据位为脱敏占位、不可直接贴用；请先「轮换管家凭证」获取一次性明文（SWARM_NODE_PRIME_TOKEN）再配给管家，或重新钦定命中可接线的宿主布局。</div>`);
  return `<div class="notice ok spaced"><b>钦定完成</b>：「${esc(d.agent)}」已是本机管家——已写入 node.yaml 的 prime_agent 并即时生效（无需重启）；共享编排凭据与授权状态不变，现有 MCP 连接持续有效。</div>${declNote}${wiredNote}
  ${credBlock}
  <label>MCP 配置片段（${d.wired ? '已自动写入——如需手工迁移其他宿主可复制（复制片段的凭据位为脱敏占位，迁移时须替换为有效明文）' : '凭据位为脱敏占位——结构可复用，凭据需轮换获取明文后替换'}）<pre><code>${esc(mcpJSON)}</code></pre></label>
  <div class="row spaced">${retryBtn}${button('复制 MCP 配置', 'copy-prime-mcp', '')}${button('换一个管家', 'prime-pick', 'link')}</div>
  <p class="footer-note">${d.wired ? `接线发生在钦定事务内（走本机主人授权），写入的是当前共享凭据；轮换管家凭证时会自动刷新同一配置，无需重复操作。管家实际派单还须主人在${linkTo('mcp-access', '「MCP 授权」区')}显式启用编排授权（新实例默认禁用，未启用时管家派单被 403 mcp_disabled 拒绝）。手动配置其他宿主：可复制上方片段结构、凭据经「轮换管家凭证」获取一次性明文后替换；其他宿主的在线生成/预览见${mcpPageLink('「MCP 接入」页')}。` : `把上方 JSON 片段的结构粘进管家 agent 的 MCP 服务器配置；凭据位（SWARM_NODE_PRIME_TOKEN）为脱敏占位，须经「轮换管家凭证」获取一次性明文后替换，且主人在${linkTo('mcp-access', '「MCP 授权」区')}显式启用编排授权（新实例默认禁用）后它才可本机派单。其他宿主的在线生成/预览见${mcpPageLink('「MCP 接入」页')}。`}</p>`;
}

/* ---------- MCP 编排授权卡（W06-03 P08 决策设置） ----------
 * 与「本机管家 · 形态」卡并列但视觉分流——独立卡片、独立开关，授权与角色解耦
 * 的页面级表达。数据源 GET /api/v1/local/mcp/access（视图凭据位恒为服务端脱敏
 * 引用，本端只渲染不加工）；启用/禁用调既有 PUT（主人显式启停），操作影响 scope
 * 人话由服务端返回、原样渲染不二次创作。声明≠调用权：启用无需钦定管家；钦定/
 * 取消管家不改本开关；禁用=门控拒绝（403 mcp_disabled）不撤销凭据，作废走管家
 * 卡「轮换管家凭证」。不进轮询（开关变更仅经本页 PUT 与盘上显式修改发生，进页
 * 即取最新）。setup 形态不渲染操作面（编排门 setup 分支恒 403 prime_required，
 * 开关无生效对象——如实收缩为指引）。 */
function mcpAccessCard() {
  const a = S.mcpAccess;
  if (S.status?.mode === 'setup') {
    return `<section class="card spaced" id="mcp-access"><h2>MCP 编排授权</h2>
    <small class="muted">编排授权开关在接入中心端后开放（当前形态下外部编排调用本就被拒绝）；先完成连接。</small></section>`;
  }
  if (!a.loaded) {
    return `<section class="card spaced" id="mcp-access"><h2>MCP 编排授权</h2>
    <small class="muted">正在读取授权状态…</small></section>`;
  }
  if (a.unavailable) {
    return `<section class="card spaced" id="mcp-access"><div class="row"><h2>MCP 编排授权</h2></div>
    <small class="muted">授权状态读取失败（本机 API 不可达或本机版本未装配授权端点）。</small>
    <div class="row spaced">${button('重试读取', 'mcp-reload', 'link')}</div>
    <p class="footer-note">决策设置：${deepLinksRow('mcp-access')}</p></section>`;
  }
  const stat = `<div class="mini-stat spaced"><span><b>${a.enabled ? '已启用' : '未启用'}</b><small>编排授权</small></span><span><b>${a.primeAgent ? esc(a.primeAgent) : '未钦定'}</b><small>当前管家（仅展示，声明入口在「默认管家设置」卡）</small></span></div>`;
  const credLine = `<small class="muted">共享编排凭据（prime_token）：${esc(a.credential || '未生成')}${a.updatedAt ? ' · 开关最近变更 ' + esc(timeFull(a.updatedAt)) : ''}</small>`;
  const scopeLine = a.scope ? `<div class="notice spaced"><small class="muted">${esc(a.scope)}</small></div>` : '';
  const toggle = a.enabled ? button('禁用编排授权', 'mcp-toggle', 'link') : button('启用编排授权', 'mcp-toggle', 'primary');
  return `<section class="card spaced" id="mcp-access"><div class="row"><h2>MCP 编排授权</h2>
  <div class="actions">${mcpPageLink('MCP 接入引导 →')}</div></div>
  ${stat}${credLine}${scopeLine}
  <div class="row spaced">${toggle}</div>
  <p class="footer-note">授权与管家角色相互独立：启用编排授权无需钦定管家；钦定/取消管家不改变本开关。禁用只是门控拒绝外部派单（403 mcp_disabled），不撤销凭据——作废旧凭据请用「默认管家设置」卡的「轮换管家凭证」。宿主配置的生成/应用与六层接入验证在${mcpPageLink('「MCP 接入」页')}。决策设置：${deepLinksRow('mcp-access')}</p></section>`;
}

/* ---------- MCP 编排授权数据链（W06-03） ---------- */
let mcpAccessLoading = false;   // GET 在途防重入（同 notifyLoading 家族；模块级不随 reset 翻转，迟到应答凭会话代次丢弃）
function applyMcpAccessView(v) {
  S.mcpAccess = { loaded: true, unavailable: false, enabled: !!v.enabled, credential: v.credential_masked || '', scope: v.scope || '', primeAgent: v.prime_agent || '', updatedAt: v.updated_at || '' };
}
async function loadMcpAccess() {
  if (mcpAccessLoading) return;
  mcpAccessLoading = true;
  const epoch = S.sessionEpoch;   // 会话代次：迟到应答不写状态（同 primeNew 一次性凭证纪律）
  try {
    const r = await api('GET', '/api/v1/local/mcp/access');
    if (S.sessionEpoch !== epoch) { mcpAccessLoading = false; return; }
    if (r.code === 200 && r.data && typeof r.data.enabled === 'boolean') applyMcpAccessView(r.data);
    else S.mcpAccess = { ...S.mcpAccess, loaded: true, unavailable: true };
  } catch (e) {
    if (e?.unauthorized) { mcpAccessLoading = false; return; }   // 401 已回令牌门
    S.mcpAccess = { ...S.mcpAccess, loaded: true, unavailable: true };
  }
  mcpAccessLoading = false;
  render();
  applyPendingAnchor();
}
/* mcpAccessToggle 主人显式启停（PUT /api/v1/local/mcp/access）：确认弹层预告词用
 * 服务端 scope 人话原话（mcpaccess.go 返回，如实转述不二次创作）；保存失败如实
 * 报错（服务端保留旧态，不虚报成功），成功以 PUT 应答视图落状态（同 GET 形态，
 * 不含任何密钥）。 */
function mcpAccessToggle(btn) {
  const cur = S.mcpAccess.enabled;
  if (typeof cur !== 'boolean') { toast('授权状态未就绪，请先等状态行读取完成'); return; }
  const target = !cur;
  const word = target ? '启用' : '禁用';
  confirmModal(word + ' MCP 编排授权', esc(S.mcpAccess.scope || (target
    ? '启用后，持共享编排凭据（prime_token）的外部 MCP 调用方可向本机派发任务。'
    : '禁用后，持共享编排凭据的外部调用被拒绝（403 mcp_disabled）；禁用不撤销凭据——作废旧凭据请用「轮换管家凭证」。')), '确认' + word, async () => {
    if (btn) btn.disabled = true;
    const epoch = S.sessionEpoch;   // 会话代次：迟到应答不写状态
    try {
      const r = await api('PUT', '/api/v1/local/mcp/access', { enabled: target });
      if (S.sessionEpoch !== epoch) return;
      if (r.code === 200 && r.data && typeof r.data.enabled === 'boolean') {
        applyMcpAccessView(r.data);
        toast('已' + word + ' MCP 编排授权');
      } else {
        toast(word + '失败 · ' + errLine(r.data));
      }
    } catch (e) {
      if (e.unauthorized) return;
      // api() 仅抛 {network:true}/{unauthorized:true} 两类（终审建议②：原「旧状态保持，
      // 可重试」分支不可达死码删除；500 服务端文案已含「授权状态保留为 X」语义）
      toast(e.network ? '本机 API 不可达' : word + '失败 · ' + (e.message || '请重试'));
    }
    render();
    applyPendingAnchor();
    if (btn && document.contains(btn)) btn.disabled = false;
  });
}
/* ---------- MCP 接入（P07 六分区子页，W03-04；docs/tasks/v3/W03-04-rfc.md） ----------
 * 资源域子页 #/mcp（不占主导航——PAGES 词表为权威锁定面；入口=授权卡/管家卡指路 +
 * 深链；setup 形态不可达）。六分区定序：①范围拓扑 ②调用环境 ③人工授权 ④预览/
 * 应用 ⑤分层验证 ⑥维护故障；页内更新走 drawerZonesApply 分区 diff（shared block
 * 只消费不改）——动作落定只重写受影响分区，整页 render 后缓存清零全量重绘一次。
 * 点亮纪律（硬规）：V0 自动（服务端检查）；V2 服务端视角探测恒标「服务端视角」；
 * V1/V3/V4/V5 仅用户确认登记（user_confirmed+时间+配置版本）；apply/revert/换范围
 * 后相关层由服务端版本锚如实回落「未验证」；自动路径绝不冒认外部验证（禁词清单
 * 由 web 结构测试与模型金丝同源锁定，本注释亦不引用禁词原句）。
 * 换范围先失效旧响应：全部验证板写路径发送前自增 mcpBoardSeq，应答序号不符即丢弃；
 * 弹层预览应答携请求时表单指纹，不符即丢弃并提示重新预览。
 * 复制提示词零副作用：仅写剪贴板，不发起任何验证或任务请求。 */
const MCP_ZONES = ['scope', 'env', 'auth', 'gen', 'verify', 'maint'];
let mcpZoneCache = {};   // 六区缓存（drawerZonesApply diff 基准）
let mcpBoardSeq = 0;     // 验证板请求代次（发送前自增，迟到应答丢弃）
const MCP_LAYER_ORDER = ['v0', 'v1', 'v2', 'v3', 'v4', 'v5'];
const MCP_PROMPT_V4 = '请通过我配置的 SwarmMind MCP 列出当前目标的机器与 agent，明确它是本机还是中心范围；只查询，不创建任务、不启动模型工作。若工具不可用，报告缺少的工具或错误。';
const MCP_PROMPT_V5 = '请通过我配置的 SwarmMind MCP 向测试 agent「在此填写能力名」提交一条低影响验证任务：使用独立工作区，只做只读观察并报告结果，不修改项目、不执行发布。注意：执行验证会消耗模型额度——请先向我复述你将提交的内容，等我确认后再提交；完成后按四类结局如实报告：任务被接受 / 执行失败 / 工具权限拒绝 / 工作失败。';
const MCP_VERIFY_WORDS = { not_checked: '未验证', pass: '通过', fail: '未通过', warn: '警示', user_confirmed: '用户确认' };
const MCP_SOURCE_WORDS = { server_side_view: '服务端视角', user_confirmed: '用户确认' };
const MCP_PATH_WORDS = { local_probed: '本机探测存在', user_provided: '已填（本端未探测）', external_unverified: '待外部验证', placeholder: '待填写' };
const MCP_HOST_WORDS = { verified_zcode: '已实测适配', manual_generic_codex: '通用手动（未实测适配）', manual_generic: '通用手动（未验证）' };
const MCP_LAYER_FALLBACK = { v0: 'V0 配置就绪', v1: 'V1 调用机连接', v2: 'V2 授权有效', v3: 'V3 宿主工具发现', v4: 'V4 无副作用查询', v5: 'V5 可选执行验证' };
function mcpPageLink(label) { return button(label, 'go-mcp', 'link'); }
function mcpShortHash(v) { return v ? String(v).slice(0, 12) + '…' : '—'; }
function mcpIsCustomHost(h) { return h !== 'zcode' && h !== 'codex'; }

function mcpPage() {
  return `<div class="page-head"><div><h1>MCP 接入</h1>
    <p class="muted">让选定的外部 agent 可靠调用本机或全网能力——六个稳定分区同序走完：范围拓扑 → 调用环境 → 人工授权 → 预览应用 → 分层验证 → 维护故障。</p>
    <p class="muted small">操作表单为居中弹层，逐层诊断明细在右侧抽屉；每层验证独立呈现，不用「已连接」掩盖缺工具或无执行者。</p></div>
    <div class="actions">${button('← 返回总览', 'go-overview', 'link')}</div></div>
  <div id="mcp-page">${MCP_ZONES.map(z => `<section class="card spaced" data-zone="${z}"></section>`).join('')}</div>`;
}
function mcpEnter() {
  mcpZoneCache = {};
  paintMcpZones();
  loadMcpAccess();   // ③ 人工授权数据源（与总览授权卡同链；内部自带防重入与代次守卫）
  mcpBoardLoad();    // ⑤ 验证板快照
}
function paintMcpZones() {
  const root = $('#mcp-page');
  if (!root || S.page !== 'mcp' || !S.authed) return;
  drawerZonesApply(root, {
    scope: mcpScopeZone(), env: mcpEnvZone(), auth: mcpAuthZone(),
    gen: mcpGenZone(), verify: mcpVerifyZone(), maint: mcpMaintZone(),
  }, mcpZoneCache, MCP_ZONES);
}
/* mcpFormReq 由最近预览的表单快照组 V0/v2 请求体（服务端校验与凭据自取；请求面
 * 绝无凭据字段）。 */
function mcpFormReq(f) {
  return {
    scope: f.scope, caller_env: f.caller_env,
    host: mcpIsCustomHost(f.host) ? (f.custom_host || 'unknown') : f.host,
    server_url: f.server_url, mcp_command_path: f.mcp_command_path,
    config_name: f.config_name, config_home: f.config_home,
  };
}

/* ① 范围与拓扑（只读事实 + 表单入口；拓扑提示为形态表统一要求口径）。 */
function mcpScopeZone() {
  const f = S.mcp.form;
  return `<div class="row"><h2>① 范围与拓扑</h2><div class="actions">${button('打开配置表单', 'mcp-cfg-open', 'primary')}</div></div>
  <div class="mini-stat spaced"><span><b>${f ? (f.scope === 'hub' ? '调用中心（hub）' : '调用本机（node）') : '未选择'}</b><small>目标范围（在配置表单中选择）</small></span>
  <span><b>${f ? (f.caller_env === 'remote' ? '外部调用机' : '与目标同机') : '—'}</b><small>MCP 适配进程位置</small></span></div>
  <p class="muted small">拓扑事实：MCP 适配进程（swarmmind-mcp）运行在<b>调用者机器</b>上，它经 API 访问本机 node 或中心 hub；<b>127.0.0.1 属 MCP 进程所在的那台机器</b>。跨机调用须经已配置的安全转发访问，默认不开放管理面公网入口；错误回环地址不会点亮任何外部验证层（V0 即失败）。</p>`;
}
/* ② 调用环境（最近预览的视图事实；远端路径恒标「待外部验证」）。 */
function mcpEnvZone() {
  const v = S.mcp.view;
  if (!v) return `<div class="row"><h2>② 调用环境</h2></div><p class="muted">尚未生成配置视图：点「打开配置表单」填写宿主、配置名、服务地址与调用机路径后预览（零写盘，可反复改参重出）。</p>`;
  const rows = [
    ['宿主', `${v.host_kind}（${MCP_HOST_WORDS[v.host_support_level] || v.host_support_level}）`],
    ['配置名', v.config_name + (v.config_name_is_default ? '（默认命名）' : '')],
    ['服务地址', v.service_url],
    ['适配进程路径', `${v.mcp_command_path}（${MCP_PATH_WORDS[v.mcp_path_status] || v.mcp_path_status}）`],
  ];
  return `<div class="row"><h2>② 调用环境</h2><div class="actions">${button('重新预览…', 'mcp-cfg-open', 'link')}</div></div>
  <div class="table-wrap"><table><tbody>${rows.map(([k, val]) => `<tr><th>${k}</th><td>${esc(val)}</td></tr>`).join('')}</tbody></table></div>`;
}
/* ③ 人工授权（本机=编排授权卡同源；hub=PAT 人工入口指路中心控制台——本端无此
 * 能力，如实边界不承诺）。 */
function mcpAuthZone() {
  const a = S.mcpAccess, b = S.mcp.board, f = S.mcp.form;
  if (f && f.scope === 'hub') {
    return `<div class="row"><h2>③ 人工授权</h2></div>
    <p class="muted">目标范围为中心（hub）：凭据为管理员签发的访问令牌（PAT），在<b>中心控制台「设置 → 访问令牌」</b>人工创建/撤销——本机控制台不持有中心凭据，也无管理入口（如实边界）。</p>
    <p class="footer-note">模板中凭据位为占位：粘贴前请替换为你在中心控制台签发的 PAT 明文。PAT 撤销/换发即旧配置失效；管理密钥与模型密钥不进入 MCP 配置。</p>`;
  }
  const cred = b ? (b.credential_masked || '未生成') : '读取中…';
  return `<div class="row"><h2>③ 人工授权</h2><div class="actions">${linkTo('mcp-access', '授权开关与轮换 →')}</div></div>
  <div class="mini-stat spaced"><span><b>${a.loaded && !a.unavailable ? (a.enabled ? '已启用' : '未启用') : '—'}</b><small>MCP 编排授权</small></span>
  <span><b>${esc(cred)}</b><small>${esc(b?.credential_env_var || 'SWARM_NODE_PRIME_TOKEN')}（脱敏引用）</small></span></div>
  <p class="muted small">本机编排调用凭据由主人授予/轮换（绝不使用 local_token，也不进模型配置）；明文只在轮换操作结果中一次展示，本页只显示脱敏引用。启用/禁用与轮换入口在总览「MCP 编排授权」卡。</p>`;
}
/* ④ 配置预览 / 应用（最近应用结果 + 模板复制/下载；预览/应用本体在居中弹层）。 */
function mcpGenZone() {
  const ap = S.mcp.apply, v = S.mcp.view;
  const applyBlock = ap ? `<div class="notice ok spaced"><b>已写入宿主配置</b>：<code>${esc(ap.file)}</code> · 配置名 ${esc(ap.config_name)}${ap.backup ? ` · 备份 <code>${esc(ap.backup)}</code>` : ''}<br><small class="muted">${esc(ap.note || '')}（写入于 ${esc(timeFull(ap.at))}；撤回凭据仅保留在本页会话，刷新后须重新预览/应用或手动处理）</small></div>
  <div class="row spaced">${button('撤回本次写入', 'mcp-revert', 'link')}</div>` : '';
  const snippetBtns = v ? `<div class="row spaced">${button('复制模板（凭据位为占位）', 'mcp-copy-snippet', 'link')}${button('下载模板', 'mcp-download-snippet', 'link')}</div>` : '';
  return `<div class="row"><h2>④ 配置预览 / 应用</h2><div class="actions">${button('打开配置表单', 'mcp-cfg-open', 'primary')}</div></div>
  ${applyBlock}
  <p class="muted small">预览=脱敏差异（零写盘）；应用=备份后原子合并（只动目标条目，失败原文件保留）；撤回=只撤本次条目（宿主配置其后被改动则 409 如实拒绝，绝不回滚整文件）。自动写入仅本机 zcode（已实测适配宿主）可用；外部调用机与其他宿主走模板手动粘贴。</p>
  ${snippetBtns}
  <p class="footer-note">生成配置不安装二进制、不自动开放网络、不替外部机器注入配置；调用机缺 swarmmind-mcp 时先完成安装再填路径。</p>`;
}
/* ⑤ 分层验证（服务端板面；每行=状态+视角+时间+版本+诊断入口）。 */
function mcpVerifyZone() {
  const b = S.mcp.board;
  let body;
  if (!b && S.mcp.boardLoading) body = '<p class="muted">验证板加载中…</p>';
  else if (S.mcp.boardErr) body = `<div class="notice warn"><b>验证板读取失败</b>：${esc(S.mcp.boardErr)}</div><div class="row spaced">${button('重试', 'mcp-board-reload', 'link')}</div>`;
  else if (!b) body = '<p class="muted">尚未读取验证板。</p>';
  else {
    const anchor = b.board.config_version ? `<code>${esc(mcpShortHash(b.board.config_version))}</code>` : '未锚定（先运行 V0 检查或完成一次应用写入）';
    body = `<p class="muted small">当前配置版本锚：${anchor}——每层结果绑定锚；配置应用/撤回/换范围/凭据轮换后相关层如实回落「未验证」（历史在诊断抽屉可查）。</p>
    <div class="row spaced">${button('运行 V0 配置检查', 'mcp-v0', '')}${S.mcp.form && S.mcp.form.scope === 'node' ? button('V2 服务端视角探测', 'mcp-v2', 'link') : ''}</div>
    ${MCP_LAYER_ORDER.map(l => mcpLayerRow(l, b)).join('')}`;
  }
  return `<div class="row"><h2>⑤ 分层验证（V0–V5）</h2></div>
  <p class="muted small">状态不是单一绿灯：任一层结果不推断下一层；「服务端视角」不是外部验证；「用户确认」不是服务端验证。V5 默认不运行（消耗模型额度）。</p>
  ${body}`;
}
function mcpLayerRow(layer, b) {
  const st = (b.board.layers || []).find(x => x.layer === layer) || { status: 'not_checked' };
  const g = (b.guidance || {})[layer] || { label: MCP_LAYER_FALLBACK[layer] };
  const word = MCP_VERIFY_WORDS[st.status] || st.status;
  const cls = st.status === 'pass' || st.status === 'user_confirmed' ? 'online' : st.status === 'fail' ? 'offline' : '';
  const src = st.checked_by ? (MCP_SOURCE_WORDS[st.checked_by] || st.checked_by) : '';
  const external = layer === 'v1' || layer === 'v3' || layer === 'v4' || layer === 'v5';
  const meta = [src, st.checked_at ? '检查于 ' + esc(timeFull(st.checked_at)) : '', st.config_version ? '版本 ' + esc(mcpShortHash(st.config_version)) : ''].filter(Boolean).join(' · ');
  return `<div class="chan-card spaced"><div class="row"><div><b>${esc(g.label || MCP_LAYER_FALLBACK[layer])}</b> <span class="badge ${cls}">${esc(word)}</span></div>
  <div class="actions">${button('诊断', 'mcp-drawer:' + layer, 'link')}${external ? button('登记我的确认', 'mcp-confirm:' + layer, 'link') : ''}</div></div>
  ${meta ? `<small class="muted">${meta}</small>` : ''}
  ${st.detail ? `<div class="spaced"><small class="muted">${esc(st.detail)}</small></div>` : ''}</div>`;
}
/* ⑥ 维护与故障（轮换/禁用→重新配置；提示词复制=零副作用）。 */
function mcpMaintZone() {
  return `<div class="row"><h2>⑥ 维护与故障</h2></div>
  <div class="spaced">
  <p class="muted small"><b>轮换 / 禁用 → 重新配置</b>：作废共享凭据用总览${linkTo('mcp-access', '「MCP 编排授权」卡')}轮换（禁用只门控不撤销凭据）。轮换后旧配置内的凭据即失效，相关验证自动回落「未验证」——重新应用新配置后重跑检查/确认，不用旧结果点亮新配置。</p>
  <p class="muted small"><b>中心范围（PAT）</b>：在中心控制台「设置 → 访问令牌」撤销/换发；PAT 变更后旧配置即失效，须重新生成并更新调用机配置。</p>
  </div>
  <div class="row spaced">${button('复制 V4 无副作用查询提示词', 'mcp-copy-prompt-v4', 'link')}${button('复制 V5 执行验证提示词（默认不运行）', 'mcp-copy-prompt-v5', 'link')}</div>
  <p class="muted small">复制动作零副作用：仅写入剪贴板，不发起任何验证或任务。V4 只查询；V5 会消耗模型额度，默认不运行，须你明确发起并指定测试 agent 与独立工作区。</p>
  <p class="footer-note">常见故障先看各层「诊断」：回环地址×外部调用机=危险拓扑（V0 即失败）；连接类失败归 V1（DNS/拒绝/证书/超时/转发缺失）；401/403 归 V2（转人工授权）；宿主未重载/启动失败归 V3（apply 成功不等于宿主已重载）。</p>`;
}

/* ---------- MCP 接入数据链（验证板；全部写路径 seq 守卫——换范围先失效旧响应） ---------- */
async function mcpBoardLoad() {
  const seq = ++mcpBoardSeq, epoch = S.sessionEpoch;
  S.mcp.boardLoading = true;
  paintMcpZones();
  try {
    const r = await api('GET', '/api/v1/local/mcp/verify');
    if (seq !== mcpBoardSeq || S.sessionEpoch !== epoch) return;   // 迟到应答：已被更新请求/新会话作废
    if (r.code === 200 && r.data && r.data.board) { S.mcp.board = r.data; S.mcp.boardErr = ''; }
    else S.mcp.boardErr = errLine(r.data);
  } catch (e) {
    if (e?.unauthorized || seq !== mcpBoardSeq || S.sessionEpoch !== epoch) return;
    S.mcp.boardErr = '本机 API 不可达';
  }
  S.mcp.boardLoading = false;
  paintMcpZones();
  mcpSyncDrawer();
}
/* mcpBoardAction 验证板变更动作公共骨架（v0/v2/confirm/config-version）：应答统一
 * 回落板面并分区重绘；check 应答（v0/v2）另落 lastV0/lastV2 供诊断抽屉逐项事实；
 * 失败如实 toast 不清板。 */
async function mcpBoardAction(path, body, btn, okMsg) {
  if (btn) busyBtn(btn, '执行中…');
  const seq = ++mcpBoardSeq, epoch = S.sessionEpoch;
  try {
    const r = await api('POST', path, body);
    if (seq !== mcpBoardSeq || S.sessionEpoch !== epoch) return;
    if (r.code === 200 && r.data && r.data.board) {
      S.mcp.board = { ...(S.mcp.board || {}), board: r.data.board };
      if (r.data.check) {
        if (path.endsWith('/v0')) S.mcp.lastV0 = r.data.check;
        if (path.endsWith('/v2')) S.mcp.lastV2 = r.data.check;
      }
      if (okMsg) toast(okMsg);
    } else {
      toast('操作未成功 · ' + errLine(r.data));
    }
  } catch (e) {
    if (!e?.unauthorized) toast(e.network ? '本机 API 不可达' : '操作未成功 · ' + (e.message || '请重试'));
  }
  paintMcpZones();
  mcpSyncDrawer();
  if (btn && document.contains(btn)) idleBtn(btn);
}
function mcpRequireForm() {
  if (!S.mcp.form) { toast('先在「配置表单」完成一次预览，再运行检查'); return null; }
  return S.mcp.form;
}
async function mcpRunV0(btn) {
  const f = mcpRequireForm();
  if (!f) return;
  S.mcp.lastV0 = null;
  await mcpBoardAction('/api/v1/local/mcp/verify/v0', mcpFormReq(f), btn, '');
  const st = (S.mcp.board?.board?.layers || []).find(x => x.layer === 'v0');
  if (st) toast(st.status === 'pass' ? 'V0 通过：配置侧就绪（不推断 V1–V5）' : 'V0 未通过：见⑤区检查明细与「诊断」指引');
}
function mcpRunV2(btn) {
  const f = mcpRequireForm();
  if (!f) return;
  if (f.scope !== 'node') { toast('V2 服务端视角探测仅本机凭据可执行（中心凭据为 PAT，本端不持有）'); return; }
  S.mcp.lastV2 = null;
  mcpBoardAction('/api/v1/local/mcp/verify/v2', { scope: 'node', service_url: f.server_url }, btn, '');
}
function mcpConfirmLayer(layer) {
  const ver = S.mcp.board?.board?.config_version;
  if (!ver) { toast('尚无配置版本锚：先运行 V0 检查或完成一次应用写入'); return; }
  const desc = {
    v1: '你已在外部调用机上确认服务地址可达（连接/TLS 通过）。',
    v3: '你已在外部宿主中看到 MCP 初始化成功与工具列表（tools/list）。',
    v4: '你已在外部宿主中调用目录/状态工具，并核对范围与来源。',
    v5: '你已明确发起一项低影响执行验证，并核对了它的结局。',
  }[layer];
  if (!desc) return;
  confirmModal('登记用户确认 · ' + (MCP_LAYER_FALLBACK[layer] || layer),
    esc(desc) + `<br><small class="muted">本登记标注为「用户确认」（非服务端验证），绑定当前配置版本 <code>${esc(mcpShortHash(ver))}</code>；配置变更后本确认如实回落「未验证」（历史可查）。</small>`,
    '我确认，登记', () => mcpBoardAction('/api/v1/local/mcp/verify/confirm', { layer, config_version: ver }, null, '已登记用户确认（' + (MCP_LAYER_FALLBACK[layer] || layer) + '）'));
}

/* ---------- MCP 接入配置表单（居中 modal；预览/应用/冲突确认在此层完成） ---------- */
let mcpModal = null;
let mcpModalReturnFocus = null;
function mcpFingerprint(f) { return JSON.stringify(f); }
function openMcpCfgModal() {
  if (S.drawer) closeDrawer();
  if (S.sessDrawer) closeSessionDrawer();
  if (mcpDrawer) closeMcpDrawer();
  if (cfgEdit) closeCfgDrawer(true);
  const f = S.mcp.form;
  mcpModal = {
    draft: f ? { ...f } : { scope: 'node', caller_env: 'same_machine', host: 'zcode', custom_host: '', config_name: '', config_home: '', server_url: 'http://127.0.0.1:8445', mcp_command_path: '' },
    preview: null, previewErr: '', busy: false, applying: false, applied: null,
  };
  const fa = document.activeElement;
  mcpModalReturnFocus = fa && fa !== document.body ? fa : null;
  paintMcpModal();
}
function paintMcpModal() {
  const m = mcpModal;
  if (!m) return;
  const d = m.draft;
  const fld = (k, label, ph = '') => `<label>${esc(label)}<input data-mcp="${k}" value="${esc(d[k] ?? '')}" placeholder="${esc(ph)}"></label>`;
  const custom = mcpIsCustomHost(d.host);
  const hostSel = `<label>宿主类型<select data-mcp="host">
    <option value="zcode"${d.host === 'zcode' ? ' selected' : ''}>zcode（已实测适配，可自动写入）</option>
    <option value="codex"${d.host === 'codex' ? ' selected' : ''}>codex（通用手动，未实测适配）</option>
    <option value="__custom"${custom ? ' selected' : ''}>其他宿主（通用手动模板，未验证）</option>
  </select></label>`;
  $('#layer').innerHTML = `<div class="overlay"><section class="modal" id="mcp-modal" role="dialog" aria-modal="true" aria-labelledby="layer-title" tabindex="-1">
    ${modalHead('MCP 接入 · 配置表单')}
    <p class="muted small">预览零写盘；自动写入仅本机 zcode（备份+原子合并，只动目标条目）。凭据不入表单：node 范围预览取当前共享编排凭据（脱敏引用进预览），hub 范围用占位（PAT 由中心控制台人工管理）。</p>
    <div class="cfg-form-cols">
      <label>目标范围<select data-mcp="scope">
        <option value="node"${d.scope === 'node' ? ' selected' : ''}>调用本机（node；默认）</option>
        <option value="hub"${d.scope === 'hub' ? ' selected' : ''}>调用中心（hub）</option>
      </select></label>
      <label>外部调用者在哪里<select data-mcp="caller_env">
        <option value="same_machine"${d.caller_env === 'same_machine' ? ' selected' : ''}>与目标同机（默认；可探测本机路径）</option>
        <option value="remote"${d.caller_env === 'remote' ? ' selected' : ''}>外部调用机（路径与可达性待外部验证）</option>
      </select></label>
      ${hostSel}
      ${custom ? fld('custom_host', '其他宿主标识', '如 warp / cursor 等（按宿主文档核对合并点）') : ''}
      ${fld('config_name', '配置名（空=默认命名）', d.scope === 'hub' ? 'swarmmind-hub' : 'swarmmind-node')}
      ${custom ? '' : fld('config_home', '宿主配置根（绝对路径）', '/Users/you')}
      ${fld('server_url', '服务地址（调用机可达口径；127.0.0.1 属 MCP 进程所在机器）', 'http://127.0.0.1:8445')}
      ${fld('mcp_command_path', 'swarmmind-mcp 绝对路径（调用机上的；本端不代填服务器路径）', d.caller_env === 'remote' ? '/home/you/bin/swarmmind-mcp' : '/usr/local/bin/swarmmind-mcp')}
    </div>
    <div class="row spaced">${button(m.busy ? '预览生成中…' : '生成预览', 'mcp-preview', 'primary', m.busy || m.applying ? 'disabled' : '')}${button('关闭', 'mcp-modal-close', 'link')}</div>
    <div id="mcp-preview-zone" class="spaced">${mcpPreviewZoneHtml()}</div>
  </section></div>`;
  layerInert(true);
  $('#layer [role=dialog]')?.focus();
  playDrawerOpen();
  $('#layer').addEventListener('click', mcpModalMaskClose);   // 具名引用重复注册幂等去重
}
function mcpModalMaskClose(e) {
  if (!mcpModal) return;
  if (e.target.classList.contains('overlay')) closeMcpModal(true);
}
function closeMcpModal(back) {
  mcpModal = null;
  layerInert(false);
  $('#layer').innerHTML = '';
  const b = mcpModalReturnFocus;
  mcpModalReturnFocus = null;
  if (back && b?.isConnected) b.focus();
  else {
    const tab = document.querySelector('aside nav button.active') || document.querySelector('aside nav button');
    if (tab) tab.focus();
  }
}
/* mcpPreviewZoneHtml 预览/应用结果区（居中 modal 内；随动作整区重绘）。 */
function mcpPreviewZoneHtml() {
  const m = mcpModal;
  if (!m) return '';
  if (m.applied) {
    const a = m.applied;
    return `<div class="notice ok"><b>已应用写入</b>：<code>${esc(a.file)}</code> · 配置名 ${esc(a.config_name)} · 备份 <code>${esc(a.backup)}</code><br>
    <small class="muted">${esc(a.note || '')}</small></div>
    <div class="row spaced">${button('完成，返回 →', 'mcp-modal-close', 'link')}</div>`;
  }
  if (m.previewErr) return `<div class="notice warn"><b>预览被拒</b>：${esc(m.previewErr)}<br><small class="muted">未写盘；修正表单后重新预览。</small></div>`;
  const p = m.preview;
  if (!p) return '<p class="muted small">尚无预览：填好后点「生成预览」（零写盘，可反复改参重出）。</p>';
  const v = p.view || {};
  const wr = p.writability || {};
  const file = p.file;
  const conflict = p.conflict;
  const parts = [];
  // 视图事实（宿主/范围/路径状态/凭据引用——全部脱敏口径直出）。
  parts.push(`<div class="table-wrap"><table><tbody>
    <tr><th>范围 / 调用环境</th><td>${esc(v.target_scope_label || '')} · ${esc(v.caller_env_label || '')}</td></tr>
    <tr><th>宿主</th><td>${esc(v.host_kind || '')}（${esc(MCP_HOST_WORDS[v.host_support_level] || v.host_support_level || '')}）</td></tr>
    <tr><th>适配进程路径</th><td>${esc(v.mcp_command_path || '')}（${esc(MCP_PATH_WORDS[v.mcp_path_status] || v.mcp_path_status || '')}）</td></tr>
    <tr><th>凭据位</th><td>${esc(v.credential_ref || '')} <small class="muted">（${esc(v.credential_env_var || '')}；脱敏引用/占位，绝不含明文）</small></td></tr>
    ${file ? `<tr><th>宿主配置文件</th><td><code>${esc(file.path || '')}</code>${file.exists ? '' : '（不存在——本端点绝不创建宿主目录）'}${file.symlink ? ' <span class="readonly">符号链接：不读写</span>' : ''} · ${file.writable ? '可写' : '不可写'} · 版本 <code>${esc(mcpShortHash(file.version))}</code></td></tr>` : ''}
  </tbody></table></div>`);
  if ((v.notes || []).length) parts.push(`<ul class="muted small spaced">${v.notes.map(n => `<li>${esc(n)}</li>`).join('')}</ul>`);
  // 差异（create/replace；既有条目只报结构，env 值不回显）。
  if (p.diff) {
    const ex = p.diff.existing_entry;
    parts.push(`<p class="muted small">差异：<b>${p.diff.kind === 'replace' ? '替换既有条目' : '新建条目'}</b>${ex ? `（既有 command：<code>${esc(ex.command || '')}</code>；既有 env 键：${Object.keys(ex.env || []).map(esc).join(', ') || '—'}——值不回显）` : ''}</p>`);
  }
  // 冲突（不静默覆盖：建议名或显式确认替换）。
  if (conflict && conflict.conflict) {
    parts.push(`<div class="notice warn"><b>配置名冲突</b>：${esc(conflict.resolution || '')}</div>
    <div class="row spaced">${conflict.suggested_name ? button('改用建议名 ' + conflict.suggested_name, 'mcp-suggest-name', 'link') : ''}</div>`);
  }
  // 可写性/手动指引。
  parts.push(`<p class="muted small">${wr.can_auto_write ? '本机已实测适配宿主：可自动写入（备份+原子合并，只动目标条目）。' : esc((wr.reasons || []).join('；') || '该组合不提供自动写入：请复制/下载模板手动粘贴（凭据位为占位）。')}</p>`);
  if ((wr.apply_requirements || []).length) parts.push(`<ul class="muted small">${wr.apply_requirements.map(n => `<li>${esc(n)}</li>`).join('')}</ul>`);
  // 文件态问题指路 V0（preview 应答不含逐条 issue 文案——精确事实由 V0 host_file 项给出）。
  if (file && (!file.exists || file.symlink)) parts.push(`<p class="muted small">文件态异常的逐项事实可运行⑤区「V0 配置检查」查看。</p>`);
  // 动作行：应用（仅 can_auto_write）+ 模板复制/下载。
  const actions = [];
  if (wr.can_auto_write) actions.push(button(m.applying ? '应用写入中…' : '应用写入宿主配置', 'mcp-apply', 'primary', m.applying || m.busy ? 'disabled' : ''));
  actions.push(button('复制模板（凭据位为占位）', 'mcp-copy-snippet', 'link'));
  actions.push(button('下载模板', 'mcp-download-snippet', 'link'));
  parts.push(`<div class="row spaced">${actions.join('')}</div>`);
  if (wr.can_auto_write) parts.push('<p class="footer-note">应用要求 command 路径真实存在、携带本次预览的文件版本（不符 409 file_changed）；配置名冲突须显式确认替换。</p>');
  return parts.join('');
}
async function mcpPreview(btn) {
  const m = mcpModal;
  if (!m || m.busy) return;
  const fp = mcpFingerprint(m.draft);
  m.busy = true; m.previewErr = '';
  if (btn) busyBtn(btn, '预览生成中…');
  try {
    const r = await api('POST', '/api/v1/local/mcp/config/preview', mcpFormReq(m.draft));
    if (!mcpModal || mcpModal !== m) return;                    // 弹层已关：应答丢弃
    if (mcpFingerprint(m.draft) !== fp) { m.busy = false; toast('表单已变化，旧预览已丢弃——请重新生成预览'); paintMcpModal(); return; }   // 换范围先失效旧响应
    if (r.code === 200) { m.preview = r.data; m.previewErr = ''; }
    else { m.preview = null; m.previewErr = errLine(r.data); }
  } catch (e) {
    if (!mcpModal || mcpModal !== m) return;
    if (!e?.unauthorized) m.previewErr = e.network ? '本机 API 不可达' : '预览失败：' + (e.message || '请重试');
  }
  m.busy = false;
  paintMcpModal();
  if (btn && document.contains(btn)) idleBtn(btn);
}
function mcpApply(btn) {
  const m = mcpModal;
  if (!m || m.applying) return;
  const p = m.preview;
  if (!p) { toast('先「生成预览」再应用'); return; }
  const conflict = p.conflict && p.conflict.conflict;
  const doApply = () => mcpApplyDo(btn, !!(conflict));
  if (conflict) {
    // 显式确认替换（绝不静默覆盖）：确认词=冲突处置说明原话。
    confirmModal('显式确认替换既有条目', esc(p.conflict.resolution || ''), '确认替换并应用', doApply);
    return;
  }
  doApply();
}
async function mcpApplyDo(btn, replaceConfirmed) {
  const m = mcpModal;
  if (!m || m.applying) return;
  const p = m.preview;
  if (!p) return;
  const fp = mcpFingerprint(m.draft);
  const body = { ...mcpFormReq(m.draft), credential_ref: m.draft.scope === 'hub' ? 'placeholder' : 'current_prime_token', expected_version: p.file ? p.file.version : '', replace_confirmed: replaceConfirmed };
  m.applying = true;
  if (btn) busyBtn(btn, '应用写入中…');
  try {
    const r = await api('POST', '/api/v1/local/mcp/config/apply', body);
    if (!mcpModal || mcpModal !== m) return;
    if (mcpFingerprint(m.draft) !== fp) { m.applying = false; toast('表单已变化，旧应用请求已丢弃——请重新预览'); paintMcpModal(); return; }
    if (r.code === 200 && r.data && r.data.applied) {
      const d = r.data;
      m.applied = { file: d.file, config_name: d.config_name, backup: d.backup, note: d.note };
      S.mcp.form = { ...m.draft };       // 表单快照落页（⑤区检查的数据源）
      S.mcp.view = p.view || null;
      S.mcp.apply = { revert_id: d.revert ? d.revert.revert_id : '', file: d.file, config_name: d.config_name, backup: d.backup, note: d.note, at: new Date().toISOString() };
      toast('已应用写入宿主配置');
      mcpAnchorVersion(d.version_after);   // apply 后版本锚推进（相关验证如实回落）
    } else {
      m.previewErr = errLine(r.data);
      m.preview = null;
      toast('应用未成功：' + m.previewErr);
    }
  } catch (e) {
    if (!mcpModal || mcpModal !== m) return;
    if (!e?.unauthorized) m.previewErr = e.network ? '本机 API 不可达' : '应用失败：' + (e.message || '请重试');
  }
  m.applying = false;
  paintMcpModal();
  paintMcpZones();
  if (btn && document.contains(btn)) idleBtn(btn);
}
/* mcpAnchorVersion apply/revert 后推进服务端配置版本锚（W03-03 交接面：旧绑定结果
 * 如实回落 not_checked）；失败不阻塞——下一次 V0 运行会以文件事实重锚。 */
function mcpAnchorVersion(version) {
  if (!version) { mcpBoardLoad(); return; }
  const seq = ++mcpBoardSeq, epoch = S.sessionEpoch;
  api('POST', '/api/v1/local/mcp/verify/config-version', { config_version: version }).then(r => {
    if (seq !== mcpBoardSeq || S.sessionEpoch !== epoch) return;
    if (r.code === 200 && r.data && r.data.board) S.mcp.board = { ...(S.mcp.board || {}), board: r.data.board };
    paintMcpZones();
    mcpSyncDrawer();
  }).catch(() => { });
}
function mcpRevert(btn) {
  const ap = S.mcp.apply;
  if (!ap || !ap.revert_id) { toast('没有可撤回的本页应用记录'); return; }
  confirmModal('撤回本次写入', `只撤回本页最近一次应用写入的条目（<code>${esc(ap.config_name)}</code> @ <code>${esc(ap.file)}</code>）；宿主配置其后若被改动将如实拒绝（409），绝不回滚整文件。`, '确认撤回', async () => {
    if (btn) btn.disabled = true;
    const epoch = S.sessionEpoch;
    try {
      const r = await api('POST', '/api/v1/local/mcp/config/revert', { revert_id: ap.revert_id });
      if (S.sessionEpoch !== epoch) return;
      if (r.code === 200 && r.data && r.data.reverted) {
        S.mcp.apply = null;
        toast('已撤回本次写入的条目');
        mcpAnchorVersion(r.data.version_after);
      } else {
        toast('撤回未成功 · ' + errLine(r.data));
      }
    } catch (e) {
      if (!e?.unauthorized) toast(e.network ? '本机 API 不可达' : '撤回失败 · ' + (e.message || '请重试'));
    }
    paintMcpZones();
    if (btn && document.contains(btn)) btn.disabled = false;
  });
}
function mcpSuggestName() {
  const m = mcpModal;
  if (!m) return;
  const name = m.preview?.conflict?.suggested_name;
  if (!name) return;
  m.draft.config_name = name;
  m.preview = null; m.previewErr = '';
  paintMcpModal();
  toast('已改用建议名 ' + name + '：请重新生成预览');
}
function mcpCopySnippet() {
  const snip = S.mcp.view?.template?.snippet || mcpModal?.preview?.view?.template?.snippet;
  if (!snip) { toast('先完成一次预览，再复制模板'); return; }
  copyText(snip, 'MCP 模板', '模板已复制（凭据位为占位——粘贴前须替换为一次性明文）');
}
function mcpDownloadSnippet() {
  const v = S.mcp.view || mcpModal?.preview?.view;
  if (!v || !v.template || !v.template.snippet) { toast('先完成一次预览，再下载模板'); return; }
  const ext = v.template.format === 'toml' ? 'toml' : 'json';
  const blob = new Blob([v.template.snippet], { type: 'text/plain' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = (v.entry_name || 'swarmmind-mcp') + '.' + ext;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 2000);
  toast('模板已下载（凭据位为占位——使用前须替换为一次性明文）');
}

/* ---------- MCP 接入诊断明细（右抽屉；复用分区更新机制，零 shared block 改动） ---------- */
const MCP_DRAWER_ZONES = ['head', 'detail', 'history'];
let mcpDrawer = null;
function openMcpDrawer(layer) {
  closeMcpDrawer();
  if (mcpModal) closeMcpModal(false);   // 同一 #layer 互斥：配置弹层让位
  const back = document.activeElement && document.activeElement !== document.body ? document.activeElement : null;
  $('#layer').innerHTML = `<div class="overlay drawer-overlay"><section class="drawer" id="mcp-drawer" role="dialog" aria-modal="true" aria-labelledby="drawer-title" tabindex="-1">${MCP_DRAWER_ZONES.map(z => `<div data-zone="${z}"></div>`).join('')}</section></div>`;
  layerInert(true);
  $('#mcp-drawer').focus();
  playDrawerOpen();
  $('#layer').addEventListener('click', mcpDrawerMaskClose);   // 具名引用重复注册幂等去重
  mcpDrawer = { layer, zones: {}, returnFocus: back };
  mcpPaintDrawer();
}
function mcpDrawerMaskClose(e) {
  if (!mcpDrawer) return;
  if (e.target.classList.contains('overlay')) closeMcpDrawer();
}
function closeMcpDrawer() {
  if (!mcpDrawer) return;
  const back = mcpDrawer.returnFocus;
  mcpDrawer = null;
  layerInert(false);
  $('#layer').innerHTML = '';
  if (back && back.isConnected) back.focus();
  else {
    const tab = document.querySelector('aside nav button.active') || document.querySelector('aside nav button');
    if (tab) tab.focus();
  }
}
function mcpSyncDrawer() { if (mcpDrawer) mcpPaintDrawer(); }   // 板面更新随开着的抽屉分区刷新
function mcpPaintDrawer() {
  const root = $('#mcp-drawer');
  if (!root || !mcpDrawer) return;
  drawerZonesApply(root, mcpDrawerZones(mcpDrawer.layer), mcpDrawer.zones, MCP_DRAWER_ZONES);
}
function mcpDrawerZones(layer) {
  const b = S.mcp.board;
  const g = (b?.guidance || {})[layer] || { label: MCP_LAYER_FALLBACK[layer], title: '', steps: [] };
  const st = b ? (b.board.layers || []).find(x => x.layer === layer) : null;
  const word = st ? (MCP_VERIFY_WORDS[st.status] || st.status) : '未验证';
  const cls = st && (st.status === 'pass' || st.status === 'user_confirmed') ? 'online' : st && st.status === 'fail' ? 'offline' : '';
  const external = layer === 'v1' || layer === 'v3' || layer === 'v4' || layer === 'v5';
  // V0 最近检查逐项事实（lastV0 已并入板面 detail；逐项明细直接取板面外的缓存）。
  const v0items = layer === 'v0' && S.mcp.lastV0 ? S.mcp.lastV0 : null;
  const detail = [];
  if (st && st.detail) detail.push(`<p class="muted small">${esc(st.detail)}</p>`);
  if (g.steps && g.steps.length) {
    detail.push(`<h3 class="spaced">检查指引</h3><ol class="muted small">${g.steps.map(s => `<li>${esc(s)}</li>`).join('')}</ol>`);
  }
  if (external && b?.board?.config_version) {
    detail.push(`<div class="row spaced">${button('登记我的确认', 'mcp-confirm:' + layer, 'link')}</div>`);
  }
  if (layer === 'v0') {
    detail.push(`<div class="row spaced">${button('运行 V0 配置检查', 'mcp-v0', '')}</div>`);
    if (v0items && v0items.checks) {
      detail.push(`<h3 class="spaced">逐项事实</h3><div class="table-wrap"><table><tbody>${v0items.checks.map(it =>
        `<tr><th>${esc(it.key)}</th><td><span class="badge ${it.status === 'pass' ? 'online' : it.status === 'fail' ? 'offline' : ''}">${esc(MCP_VERIFY_WORDS[it.status] || it.status)}</span> <small class="muted">${esc(it.detail)}</small></td></tr>`).join('')}</tbody></table></div>`);
    }
  }
  if (layer === 'v2') detail.push(`<div class="row spaced">${button('V2 服务端视角探测', 'mcp-v2', 'link')}</div>`);
  const hist = b ? (b.board.history || []).filter(h => h.layer === layer).slice(-8).reverse() : [];
  const history = hist.length
    ? `<h3 class="spaced">历史（时间与来源可查）</h3><div class="table-wrap"><table><tbody>${hist.map(h =>
      `<tr><th>${esc(timeFull(h.at))}</th><td>${esc(MCP_VERIFY_WORDS[h.result] || h.result)} · ${esc(MCP_SOURCE_WORDS[h.source] || h.source)} · 版本 <code>${esc(mcpShortHash(h.config_version))}</code>${h.note ? `<br><small class="muted">${esc(h.note)}</small>` : ''}</td></tr>`).join('')}</tbody></table></div>`
    : '<p class="muted small spaced">本层暂无历史记录。</p>';
  return {
    head: `<div class="row"><h2 id="drawer-title">诊断明细 · ${esc(g.label || MCP_LAYER_FALLBACK[layer])}</h2>${button('×', 'mcp-drawer-close', 'close', '', 'aria-label="关闭抽屉"')}</div>
    <div class="row"><span class="badge ${cls}">${esc(word)}</span><small class="muted">状态不是单一绿灯：本层结果不推断其他层。</small></div>`,
    detail: detail.join(''),
    history,
  };
}

function recentTasksCard() {
  const rows = S.tasks.slice(0, 8);
  return `<section class="card spaced"><div class="row"><h2>最近任务（本机执行过的）</h2><small>${S.tasksTotal} 条 · 含 hub 下发</small></div>
  ${rows.length ? `<div class="table-wrap"><table><thead><tr><th>时刻</th><th class="task-title">目标</th><th>状态</th></tr></thead><tbody>
    ${rows.map(e => `<tr data-task="${esc(e.task_id)}" tabindex="0" aria-label="查看任务 ${esc(e.task_id)}"><td>${timeShort(e.created_at)}</td><td><strong>${esc(e.target_agent)}</strong> @ ${esc(e.target_node)}</td><td>${listStatusBadge(e)}</td></tr>`).join('')}
  </tbody></table></div><p class="footer-note">点击行查看详情；此处为最近 8 条（含本机派单与 hub 下发），完整清单见「任务」页。最近任务不展示任务描述摘要：本机列表响应不含任务输入字段（如实留空，不由 ID 猜——FE-POLISH 轮2 H11），完整描述与答复在详情抽屉。</p>`
    : `<div class="empty"><h2>还没有执行过任务</h2><p class="muted">去「任务」选一个本机服务，描述要做的事即可；hub 下发任务入账后同样出现在这里。</p>${button('＋ 派单', 'go-tasks', 'primary')}</div>`}</section>`;
}

/* ---------- 能力配置加载（方案 3.5；不进轮询——编辑中状态不被覆盖） ---------- */
async function loadCfg() {
  try {
    const r = await api('GET', '/api/v1/local/config');
    if (r.code === 503) { S.cfg.unavailable = true; S.cfg.loaded = true; render(); return; }
    if (r.code !== 200) { S.cfg.unavailable = true; S.cfg.loaded = true; render(); return; }
    const d = r.data || {};
    S.cfg = {
      loaded: true, unavailable: false,
      agents: (d.agents || []).map(normalizeProfile),
      pending: !!d.pending_restart, supervised: d.supervised !== false,
      restart_cmd: d.restart_cmd || '', config_path: d.config_path || '', structural: [],
      // pending=true（第六轮修复 1）：本表读自盘上 node.yaml = 用户最新提交；
      // 待应用窗口期刷新页面拿到的不再是被覆盖前的旧运行时表。
      msg: d.pending ? '下方表格为盘上最新提交（结构性变更待重启生效），可直接继续编辑或「应用并重启」' : '',
    };
    render();
  } catch (e) { if (!e?.unauthorized) { S.cfg.unavailable = true; S.cfg.loaded = true; render(); } }
}

// cfgSave 保存全部：PUT 整表（服务端校验+round-trip 写盘+热重载/待应用分类）。
// 功能性修复 4（2026-10-22）：应答 pending_restart=true 时**不重新 GET**——
// 编辑副本才是用户最新意图，重新 GET 会用它覆盖编辑中的表单（丢未保存输入）。
// 第六轮修复 1 后 pending 窗口期 GET 已回盘上表（=用户已保存的提交），刷新页面
// 不再丢提交；本处保持不 GET 只是保护编辑中副本，与后端口径互补。
// t20261023-a4b1：label 参数区分入口按钮文案（表格「保存全部」/抽屉「保存」），
// 协议不变；返回是否成功——抽屉入口凭它决定关抽屉（被拒保持打开，副本保留）。
async function cfgSave(btn) {
  busyBtn(btn, '保存中…');
  try {
    const r = await api('PUT', '/api/v1/local/config', { agents: S.cfg.agents });
    if (r.code === 200) {
      const d = r.data || {};
      S.cfg.pending = !!d.pending_restart;
      S.cfg.structural = d.structural || [];
      S.cfg.msg = d.note || (d.applied ? '已保存并热重载生效' : '已写入 node.yaml（待应用）');
      if (d.pending_restart) {
        S.cfg.msg = (d.note || '已写入 node.yaml（待应用）') + ' · 下方表格为你的最新编辑';
        await loadAll().catch(() => {});
        render();
      } else {
        await loadCfg();
        await loadAll().catch(() => {}); render();
      }
      return true;
    } else {
      // 拒绝（校验/写盘失败）：编辑副本与编辑抽屉原样保留，仅弹服务端错误
      alert('保存被拒：' + errLine(r.data));
      return false;
    }
  } catch (e) {
    if (!e?.unauthorized) alert('网络层失败：结果未知，请刷新后从文件重载核对');
    return false;
  } finally { idleBtn(btn); }
}

// cfgReload 从文件重载（丢弃未保存编辑，服务端重读走同一校验/分类链）。
async function cfgReload(btn) {
  if (btn) btn.disabled = true;
  try {
    const r = await api('POST', '/api/v1/local/agents/reload');
    if (r.code === 200) {
      const d = r.data || {};
      S.cfg.pending = !!d.pending_restart;
      S.cfg.msg = d.note || '已从文件重载';
      await loadCfg(); await loadAll().catch(() => {}); render();
    } else { alert('重载失败：' + errLine(r.data)); }
  } catch (e) { if (!e?.unauthorized) alert('网络层失败：结果未知'); }
  finally { if (btn) btn.disabled = false; }
}

// cfgRestart 应用并重启（结构性变更收口）：应答先回，进程延时退出靠保活拉起。
async function cfgRestart(btn) {
  busyBtn(btn, '重启中…');
  try {
    const r = await api('POST', '/api/v1/local/restart');
    if (r.code === 200) {
      const d = r.data || {};
      S.cfg.msg = d.note || 'node 重启中，稍后自动恢复';
      render();
      // 保活拉起后自动恢复：6s/12s 两次补拉 + 5s 轮询兑底
      setTimeout(() => { S.cfg.loaded = false; loadCfg(); }, 6000);
      setTimeout(() => { S.cfg.loaded = false; loadCfg(); }, 12000);
    } else { alert('重启失败：' + errLine(r.data)); idleBtn(btn); }
  } catch (e) {
    if (!e?.unauthorized) alert('网络层失败：进程可能已退出，请稍后刷新');
  }
}

// normalizeProfile 编辑副本归一（服务端字段全集；exposed 三态归一布尔）。
// session_adapter 入副本（W06-02 登记面「适配器」）：随整表提交 round-trip，
// 网页编辑不再把已声明的适配器静默丢成缺省空值。
function normalizeProfile(p) {
  return {
    name: p.name || '', kind: p.kind || 'cli', command: p.command || '', workdir: p.workdir || '',
    agent_args: p.agent_args || [], timeout_sec: p.timeout_sec || 600,
    allow_args: !!p.allow_args, allow_env: p.allow_env || [], config_home: p.config_home || '',
    description: p.description || '', permission_mode: p.permission_mode || '', exposed: p.exposed !== false, orchestrator: !!p.orchestrator,
    stdin_to_arg: !!p.stdin_to_arg,
    session_adapter: p.session_adapter || '',
  };
}

/* ---------- 页面②：我的服务（旅程①服务提供者：开关三态 + 只读配置） ---------- */
function servicesPage() {
  /* P06/P00 指引（W06-02）：「本机独立使用为正常态」为锚点文案——不连中心一切
   * 功能照常；连接走既有「连接」页接入向导（不建新页）。 */
  const head = `<div class="page-head"><div><h1>服务</h1>
    <p class="muted">本机 agent 即服务：是否开放给其他机器调用，由本机决定；关闭后其他机器的<b>新任务</b>不再接收，本机派单不受影响，已在执行/排队的任务照常跑到终态。</p>
    <p class="muted small">本机独立使用为完全正常的形态（发现 / 登记 / 派单 / 通知离线照常）；需要跨机协作时，${button('走「连接」页的接入向导', 'go:wizard', 'link')}，凭据人工核对。</p></div></div>`;
  // 功能性修复 2（2026-10-22）：空态不再提前 return——能力配置区（新增按钮）
  // 恒渲染，网页即可登记第一个 agent；去掉「只引导手编 YAML」的死路文案。
  const empty = S.agents.length ? '' : `<section class="card"><div class="empty"><h2>还没有 agent</h2>
    <p class="muted">在下方「能力配置」点「＋ 新增 agent」或「发现本机 agent」登记本机服务（名称 / command / workdir 均填绝对路径），保存后按提示应用并重启生效；习惯手编的也可编辑 node.yaml 后点「从文件重载」。</p>
    <p class="muted small">本机独立使用为正常态；连接中心走「连接」页接入向导。</p></div></section>`;
  /* 通知卡上移至页头后第一位（2026-10-26 notify-polish 改动四：渠道配置是服务页
   * 高频入口，不再沉底；空态/agent 服务卡居中，能力配置编辑区沉底）。 */
  return head + notifyCard() + empty + S.agents.map(a => svcCard(a)).join('') +
    (S.agents.length ? `<p class="footer-note">开关变更即时持久化到 <code>data/agents-state.json</code>，重启保持。完整能力编辑（含新增/删除/参数）见下方「能力配置」。</p>` : '') +
    cfgSection();
}

/* ---------- 能力配置编辑区（方案 3.5：yaml.Node round-trip + 热重载分类） ---------- */
function cfgSection() {
  const c = S.cfg;
  if (!c.loaded) {
    return `<section class="card spaced"><div class="row"><div><h2>能力配置</h2></div>
      ${button('加载配置', 'cfg-load', '')}</div><span id="capability-config"></span><p class="muted">读取 node.yaml 的 agents 表进行网页编辑。</p></section>`;
  }
  if (c.unavailable) {
    return `<section class="card spaced"><h2>能力配置</h2><span id="capability-config"></span><p class="muted">配置编辑不可用（节点版本未装配配置端点，或接口不可达）。</p></section>`;
  }
  // 功能性修复 4：待应用横幅注明「下方表格为最新编辑」——pending 窗口期编辑
  // 副本是用户意图的权威（盘上已是新表，运行时基线待重启收敛）。
  // 第六轮修复 1 后刷新页面 GET 的也是盘上表（=同一份最新提交），旧文案
  // 「刷新会回到运行时基线、勿重复保存」已随该修复作废。
  const pending = c.pending ? `<div class="notice warn spaced"><b>已写入盘，重启后生效</b>：${esc((c.structural || []).join('；') || '详见保存应答')}。
    下方表格为盘上最新提交（此刻刷新页面得到的也是它）；在此基础上继续编辑保存即可，或点「应用并重启」使新表生效。
    ${button('应用并重启', 'cfg-restart', 'primary', c.supervised ? '' : '')}
    ${c.supervised ? '' : `<small class="muted">裸前台运行：请手动重启——<code>${esc(c.restart_cmd)}</code></small>`}</div>` : '';
  const msg = c.msg ? `<div class="notice ok spaced">${esc(c.msg)}</div>` : '';
  const rows = c.agents.map((a, i) => cfgRow(a, i)).join('') ||
    '<p class="muted">（空表：新增至少一个 agent 才能对外提供服务）</p>';
  return `<section class="card spaced"><div class="row"><div><h2>能力配置 <span class="badge">node.yaml</span></h2>
    <small class="muted">配置文件：<code>${esc(c.config_path)}</code> · 保存时服务端强校验；yaml 注释与未知字段保留（round-trip）。普通字段保存即热重载；增删 agent / 改 command 需重启。</small></div></div>
    <span id="capability-config"></span>
    <div class="spaced"><small class="muted">决策设置（总览页）：</small>${deepLinksRow('capability-config')}</div>
    ${pending}${msg}
    <div class="table-wrap"><table class="cfg-table"><thead><tr><th>名称</th><th>命令</th><th>超时</th><th>开放</th><th></th></tr></thead>
    <tbody>${rows}</tbody></table></div>
    <div class="row spaced">
      ${button('＋ 新增 agent', 'cfg-add', '')}
      ${button('发现本机 agent', 'discover-open', '')}
      ${button('保存全部（校验+写盘）', 'cfg-save', 'primary')}
      ${button('从文件重载', 'cfg-reload', '')}
    </div>
    <details ${c.agents.length ? '' : 'open'}><summary>编辑字段说明</summary><p class="muted small">
    command / workdir / config_home 必须绝对路径；timeout_sec 正数（秒）；allow_env 仅环境变量键名或 <code>*</code> 全开；
    关闭开放（exposed=false）只影响其他机器调用，本机派单不受限。结构性变更（增删 / command / agent_args / kind / session_adapter）保存后需「应用并重启」。</p>
    <p class="muted small">「发现本机 agent」只探测你逐个显式给出的候选命令（零磁盘扫描、零写盘）；「登记预览」算差异与检测结果，确认后回到本表，<b>保存仍走「保存全部」</b>（整表 PUT，无第二写路径）。全权档位 permission_mode（yolo / danger-full-access）保存链拒绝。</p></details></section>`;
}

function cfgRow(a, i) {
  return `<tr data-cfg-idx="${i}">
    <td><code>${esc(a.name)}</code>${a.description ? `<br><small class="muted">${esc(a.description)}</small>` : ''}</td>
    <td><code>${esc(a.command)}</code></td>
    <td>${a.timeout_sec}s</td>
    <td>${a.exposed ? '网内' : '仅本机'}</td>
    <td class="table-action">${button('编辑', 'cfg-open:' + i)}</td>
  </tr>`;
}

function cfgField(idx, field, label, value, type = 'text') {
  return `<label class="cfg-field">${esc(label)}<input type="${type}" data-cfg="${idx}.${field}" value="${esc(value)}"></label>`;
}

/* ---------- 能力配置编辑抽屉（t20261023-a4b1：行内 details 展开改右侧弹窗） ----------
 * 基建与任务详情抽屉同家族（#layer + .overlay/.drawer-overlay/.drawer）；数据流
 * 不动：data-cfg 输入仍直写 S.cfg.agents[idx]（编辑副本即真相），保存仍走
 * cfgSave 的 PUT 整表协议。新增单行取消语义：打开时对 agent 深拷贝快照，
 * 取消/遮罩/×关闭还原该行。cfgEdit 非空 = 编辑会话进行中（pollTick 守卫位：
 * 数据照常拉，表格 DOM 不重绘，抽屉内输入不丢）。 */
let cfgEdit = null;    // {idx, snap} 打开中的编辑抽屉；null=无编辑会话
let cfgSaving = false; // 抽屉保存在途：收抽屉一律暂拒（防「取消还原」与在途 PUT 竞态分叉）
let cfgReturnFocus = null;   // 抽屉打开前的触发元素，关闭后还给它（A2 键盘基线）
// playDrawerOpen 抽屉入场动画（D1 对齐 hub 层生命周期，2026-10-25 抽屉统一）：内容
// 已渲染 → 强制一次布局提交初始态 → 下一帧加 .open 触发过渡；层已关/被换后迟到的
// 帧不得点亮旧层（overlay 已随 innerHTML 清理消失，?. 自然短路）。任务/能力配置
// 抽屉与通知编辑 modal 同壳同动效家族（.open 同机制点亮，notify-polish 改动三）。
function playDrawerOpen() {
  const ov = $('#layer .overlay');
  if (!ov) return;
  void ov.offsetWidth;
  requestAnimationFrame(() => ov.classList.add('open'));
}
function openCfgDrawer(idx) {
  const a = S.cfg.agents[idx];
  if (!a) return;
  if (S.drawer) closeDrawer(); // 同一 #layer 互斥：任务详情抽屉让位
  if (S.sessDrawer) closeSessionDrawer();   // 会话抽屉让位（同上）
  if (cfgEdit && S.cfg.agents[cfgEdit.idx]) S.cfg.agents[cfgEdit.idx] = cfgEdit.snap; // 换行编辑（UI 不可达，防御）：旧行先还原
  cfgEdit = { idx, snap: JSON.parse(JSON.stringify(a)) };
  const fa = document.activeElement;
  cfgReturnFocus = fa && fa !== document.body ? fa : null;   // A2：关闭返焦
  $('#layer').innerHTML = `<div class="overlay drawer-overlay"><section class="drawer" id="cfg-drawer" role="dialog" aria-modal="true" aria-labelledby="layer-title" tabindex="-1">${cfgDrawerView(a, idx)}</section></div>`;
  layerInert(true);   // A2 键盘基线：背景隔离
  playDrawerOpen();
  $('#cfg-drawer')?.focus();
  $('#layer').addEventListener('click', cfgMaskClose); // 具名引用重复注册幂等去重（与任务抽屉遮罩关闭同机制）
}
function cfgMaskClose(e) {
  if (!cfgEdit) return; // 抽屉已关后残留监听不动作（免与任务抽屉遮罩隐式耦合）
  if (e.target.classList.contains('overlay')) closeCfgDrawer(true); // 遮罩关闭=取消语义（还原单行）
}
// closeCfgDrawer 关编辑抽屉并重绘表格；restore=true 还原快照（取消/遮罩/×/Esc），
// false 保留编辑副本（保存成功/删除——内容已是意图终态）。A2：释放背景 inert 并
// 返焦触发元素（失存不强求，对齐任务抽屉口径）。
function closeCfgDrawer(restore) {
  if (cfgSaving) return;
  if (cfgEdit && restore && S.cfg.agents[cfgEdit.idx]) S.cfg.agents[cfgEdit.idx] = cfgEdit.snap;
  cfgEdit = null;
  layerInert(false);
  $('#layer').innerHTML = '';
  const back = cfgReturnFocus;
  cfgReturnFocus = null;
  render();
  /* A2 返焦兜底同 closeDrawer：失存回落侧栏当前页签（与 hub 同构）。 */
  if (back?.isConnected) back.focus();
  else {
    const tab = document.querySelector('aside nav button.active') || document.querySelector('aside nav button');
    if (tab) tab.focus();
  }
}
// cfgDrawerSave 抽屉保存：同一 PUT 整表协议，口径「保存（校验+写盘）」只落盘本行
// 的编辑意图；成功关抽屉，被拒/网络未知保持打开（副本保留，现口径）。
async function cfgDrawerSave(btn) {
  cfgSaving = true;
  let ok = false;
  try { ok = await cfgSave(btn); } finally { cfgSaving = false; }
  if (ok || !S.authed) closeCfgDrawer(false); // 401 已回令牌门：抽屉随会话收起
}
function cfgDrawerView(a, i) {
  return `<div class="row"><h2 id="layer-title">编辑 agent · ${esc(a.name) || '（未命名）'}</h2>${button('×', 'cfg-close', 'close')}</div>
    <div class="cfg-form-cols">
      ${cfgField(i, 'name', '名称', a.name)}
      ${cfgField(i, 'command', 'command（绝对路径）', a.command)}
      ${cfgField(i, 'workdir', 'workdir（绝对路径）', a.workdir)}
      ${cfgField(i, 'timeout_sec', 'timeout_sec（秒）', String(a.timeout_sec), 'number')}
      ${cfgField(i, 'description', '描述', a.description)}
      ${cfgField(i, 'config_home', 'config_home（可空）', a.config_home)}
      ${cfgField(i, 'agent_args', 'agent_args（逗号分隔）', (a.agent_args || []).join(','))}
      ${cfgField(i, 'allow_env', 'allow_env（逗号分隔或 *）', (a.allow_env || []).join(','))}
      <label class="cfg-check"><input type="checkbox" data-cfg="${i}.allow_args" ${a.allow_args ? 'checked' : ''}> allow_args（允许任务传参）</label>
      <label class="cfg-check"><input type="checkbox" data-cfg="${i}.stdin_to_arg" ${a.stdin_to_arg ? 'checked' : ''}> stdin_to_arg（args 型 CLI 兼底：任务描述转命令行尾参，如 zcode -p）</label>
      <label class="cfg-check"><input type="checkbox" data-cfg="${i}.exposed" ${a.exposed ? 'checked' : ''}> 开放给其他机器调用（exposed；本机派单不受限）</label>
    </div>
    <div class="cfg-drawer-foot">
      ${button('取消', 'cfg-cancel', '')}
      <span class="actions">${button('删除此 agent', `cfg-del:${i}`, 'danger')}${button('保存（校验+写盘）', 'cfg-save-row', 'primary')}</span>
    </div>`;
}
/* orchNote 智行管家声明提示：生效/待同步同 exposed 三态；冲突（hub 汇聚发现
 * 多声明）由目录状态展示（orchestrator_conflicts），本机侧只提示声明已生效。 */
function orchNote(a) {
  if (!a.orchestrator) return '';
  const name = 'orch-' + CSS.escape(a.name);
  if (a._pending) return `<div class="notice warn spaced" id="${name}">同步中…</div>`;
  if (a._synced === false) return `<div class="notice warn spaced" id="${name}"><b>本机已生效 · 待同步</b>：hub 暂不可达，重连注册后自动生效。</div>`;
  return `<div class="notice ok spaced" id="${name}"><b>智行管家已声明</b>：复合目标将交由本能力拆解派发汇总（hub 汇聚全网声明，冲突时告警并回退名字识别）。</div>`;
}

/* ---------- 能力卡检测徽标（W06-02 四值投影，加法） ----------
 * detectGlyph 单值 → 徽标：pass 绿 ✓ / fail 红 ✗ / not_checked 灰 –（未检测如实
 * 灰置，绝不冒认已检测）；detectBadgeHtml 能力卡头部四值行；旧后端无 detect 字段
 * 不渲染（不冒认）。execution_ready pass 必带「模型未验证」口径行——绝不合并绿灯。 */
function detectGlyphHtml(label, v, reason) {
  const mark = v === 'pass' ? '✓' : v === 'fail' ? '✗' : '–';
  const cls = v === 'pass' ? 'online' : v === 'fail' ? 'offline' : '';
  const tip = (reason ? reason + ' · ' : '') + (v === 'pass' ? '通过' : v === 'fail' ? '未通过' : '未检测（如实呈现，不推断）');
  return `<span class="badge ${cls}" title="${esc(tip)}">${esc(label)} ${mark}</span>`;
}
function detectBadgeHtml(a) {
  const d = a?.detect;
  if (!d) return '';
  // 就绪徽标 tooltip 强制携带「模型未验证」（终审建议修：卡头一眼层面不得让
  // 「就绪=模型可用」误读成立；W06-resources.md 检测模型节该层 pass 必带口径）。
  const readyTip = d.execution_ready === 'pass' ? '执行前置检查通过（模型未验证）' : '';
  return ` ${detectGlyphHtml('安装', d.installed, '')}${detectGlyphHtml('配置', d.config_recognized, '')}` +
    detectGlyphHtml('就绪', d.execution_ready, readyTip) + detectGlyphHtml('调用', d.call_allowed, '');
}
/* detectSummaryRow 配置详情表「检测摘要」行：四值 + 检查时间 + 未验证原因；
 * execution_ready pass 时强制带原注记（执行前置检查通过（模型未验证））。 */
function detectSummaryRow(a) {
  const d = a?.detect;
  if (!d) return '';
  const fmt = v => v === 'pass' ? '通过' : v === 'fail' ? '未通过' : '未检测';
  const parts = [`安装 ${fmt(d.installed)}`, `配置识别 ${fmt(d.config_recognized)}`, `执行就绪 ${fmt(d.execution_ready)}`, `本机调用 ${fmt(d.call_allowed)}`];
  if (d.execution_ready === 'pass') parts.push('执行前置检查通过（模型未验证）：未运行真实模型探针，不声称账号/额度可用');
  if (d.checked_at) parts.push('检查于 ' + timeFull(d.checked_at));
  if (d.not_checked_reason) parts.push(d.not_checked_reason);
  return `<tr><th>检测摘要</th><td><small class="muted">${parts.map(esc).join(' · ')}</small><small>数据源=本机「发现/登记预览」的最近一次检测（进程内存，重启后如实回到未检测）；全网投影不在本端</small></td></tr>`;
}

function svcCard(a) {
  const on = !!a.exposed;
  const syncNote = a._pending
    ? `<div class="notice warn spaced" id="sync-${esc(a.name)}">同步中…</div>`
    : a._synced === false
      ? `<div class="notice warn spaced" id="sync-${esc(a.name)}"><b>本机已生效 · 待同步</b>：hub 暂不可达，重连注册后自动生效（已排队任务不受影响）。</div>`
      : a._synced === true
        ? `<div class="notice ok spaced" id="sync-${esc(a.name)}"><b>已同步 hub · 网内生效</b>：${on ? '能力已进入全网目录（经 hub）。' : '新任务将不再准入；注册表保留一条下线登记。'}</div>`
        : '';
  return `<section class="card svc-card spaced ${on ? '' : 'off'}" id="svc-${esc(a.name)}">
    <div class="row"><div><h2 style="margin-bottom:4px">${esc(a.name)} <span class="badge">${esc(a.kind)}</span> <span class="badge">${on ? '网内可调用' : '仅本机'}</span>${a.orchestrator ? ' <span class="badge online">管家 · 本机</span>' : ''}${detectBadgeHtml(a)}</h2>
      <small class="muted">${esc(a.description || '（无描述）')} · ${esc(permLabel(a.permission_mode))} · 今日已执行 <b>${esc(a.today_tasks ?? 0)}</b> 次</small></div>
    <div class="actions"><div class="expose-ctl">
    ${/* 开放控件单开关化（2026-10-29 部署后走查反馈二）：开放/停止双按钮视觉权重
        * 不对称退役——单拨动开关+标签（复用通知渠道 .switch 同家族组件，on=开放中）；
        * off 态开关旁一行 muted 小字保留「已停止接受新任务」语义要素。setExposed
        * 行为语义原样（暴露/收起只换皮）；role=switch + aria-checked 可达语义。 */
    `<label class="checkline" title="开放给其他机器调用：关闭后只停止接收外部新任务——不停已在执行/排队的任务（本控制台暂无停止入口，hub 来源任务可在 hub 管理台取消）">开放给其他机器调用<button type="button" role="switch" aria-checked="${on}" class="switch${on ? ' on' : ''}" data-act="expose-toggle:${esc(a.name)}" data-agent="${esc(a.name)}"></button></label>`}
      ${on ? '' : '<small class="muted">已停止接受新任务；在途任务不受影响</small>'}
    </div></div></div>${syncNote}${orchNote(a)}
    <details><summary>配置详情（只读 · 来自本机 ExecProfile）</summary>
      <div class="table-wrap"><table><tbody>
        <tr><th>command</th><td><code>${esc(a.command)}</code> ${a.allow_args ? '' : '<span class="readonly">allow_args=false：任务参数将被拒绝</span>'}</td></tr>
        <tr><th>workdir</th><td><code>${esc(a.workdir)}</code></td></tr>
        <tr><th>timeout_sec</th><td>${esc(a.timeout_sec)}s<small>唯一超时源；超时整组终止进程组</small></td></tr>
        <tr><th>allow_args</th><td>${a.allow_args ? 'true · 接受任务参数（解释器类=全参数授权，选用即知情）' : 'false · 非空参数 → policy_denied'}</td></tr>
        <tr><th>allow_env</th><td>${(a.allow_env || []).length ? '<code>' + a.allow_env.map(esc).join('</code> <code>') + '</code>' : '（空 · 任务不可覆盖环境变量）'}</td></tr>
        <tr><th>config_home</th><td><code>${esc(a.config_home || '（空 = data_dir/home）')}</code><small>进程 HOME 指此，与 workdir 分离；凭证不出本机</small></td></tr>
        <tr><th>agent_args</th><td>${(a.agent_args || []).length ? '<code>' + a.agent_args.map(esc).join(' ') + '</code>' : '（无固定附加参数）'}<small>固定拼在任务参数前，任务不可改</small></td></tr>
        <tr><th>permission_mode</th><td>${esc(a.permission_mode || '未声明')}<small>${esc(permissionNote(a.permission_mode))}</small></td></tr>
        ${detectSummaryRow(a)}
      </tbody></table></div>
      <p class="footer-note">这些字段永不离开本机；对外只可见能力卡（名称/描述/权限模式/开关）。</p></details></section>`;
}
async function setExposed(name, exposed, btn) {
  const card = $('#svc-' + CSS.escape(name));
  const note = card?.querySelector(`#sync-${CSS.escape(name)}`);
  if (btn) btn.disabled = true;                     // 300ms 反馈：立即锁按钮
  if (note) note.outerHTML = `<div class="notice spaced" id="sync-${esc(name)}">同步中…</div>`;
  try {
    const r = await api('PATCH', `/api/v1/local/agents/${encodeURIComponent(name)}/exposed`, { exposed });
    if (r.code === 200) {
      const a = S.agents.find(x => x.name === name);
      if (a) { a.exposed = r.data.exposed; a._synced = !!r.data.hub_synced; }
      toast(exposed ? '已开放 · 即时生效' : '已停止接受新任务 · 其他机器的新任务不再接收（已在执行/排队的任务不受影响）');
    } else {
      toast('开关失败 · ' + errLine(r.data));
    }
  } catch (e) {
    toast(e.network ? '本机 API 不可达' : '开关失败');
  }
  render();
  pollTick();
}

async function setOrchestrator(name, orchestrator, btn) {
  const card = $('#svc-' + CSS.escape(name));
  const note = card?.querySelector('#orch-' + CSS.escape(name));
  if (btn) btn.disabled = true;
  if (note) note.outerHTML = `<div class="notice spaced" id="orch-${esc(name)}">同步中…</div>`;
  try {
    const r = await api('PATCH', `/api/v1/local/agents/${encodeURIComponent(name)}/orchestrator`, { orchestrator });
    if (r.code === 200) {
      // hub_synced 消费兼容（P1-02 残余修正，与 declare 消费同口径）：组网=boolean
      // 实况（true 已提交上报 / false 待同步）；standalone 应答缺省该字段（无上报
      // 环节，后端已改缺省）——存 undefined 回落初始渲染基线，不得把缺省压成
      // false 误显「待同步」（standalone 无 hub 可待）。
      const a = S.agents.find(x => x.name === name);
      if (a) { a.orchestrator = r.data.orchestrator; a._synced = typeof r.data?.hub_synced === 'boolean' ? r.data.hub_synced : undefined; }
      toast(orchestrator ? '已声明智行管家 · 即时生效' : '已取消智行管家声明');
    } else if (r.code === 409) {
      toast('声明被拒 · ' + errLine(r.data));
    } else {
      toast('开关失败 · ' + errLine(r.data));
    }
  } catch (e) {
    toast(e.network ? '本机 API 不可达' : '开关失败');
  }
  render();
  pollTick();
}

/* ---------- 通知渠道管理（2026-10-24 standalone 补盲区；2026-10-26 notify-unify
 * 改动三起 networked 同样渲染——「同时通知」裁决）：D2 局部刷新纪律：进页渲染容器
 *（notifyCard），loadNotifyChannels 只 innerHTML 替换 #notify-section，能力配置区
 * 与 agent 卡不被触碰；编辑走右侧抽屉家族（#layer，与能力配置/任务详情同基建），
 * 抽屉期间 pollTick 重绘不碰 #layer。「同时通知」toggle 声明式渲染（codex 审查
 * 必修 2）：唯一真相是 S.notifyAlso/S.notifyAlsoSaving/S.notifyAlsoPending，
 * 轮询 render 重建节点与保存回调重绘都从状态出发——不再持 DOM 节点跨 await
 * 操作（轮询重建后旧节点脱挂，写它不落屏，显示与实际值脱节）。 */
function notifyAlsoHtml() {
  const saving = S.notifyAlsoSaving;
  const val = saving && S.notifyAlsoPending != null ? S.notifyAlsoPending : S.notifyAlso;
  return `<label class="checkline" title="开启后，本机执行的任务终态除中心端渠道外，本机渠道也同步通知"><input type="checkbox" id="notify-also"${val === false ? '' : ' checked'}${S.notifyAlso == null || saving ? ' disabled' : ''}> 同时通知${saving ? '（保存中…）' : ''}</label>`;
}
function paintNotifyAlso() {
  const el = $('#notify-also-zone');
  if (el) el.innerHTML = notifyAlsoHtml();
}
function notifyCard() {
  if (!S.status?.notify_available) return '';
  /* 「同时通知」toggle 仅 networked 出现（standalone 本机通知即唯一通知，语义
   * 不存在；setup 页面裁剪不可达）。加载未完成（S.notifyAlso==null）先禁用，
   * 应答回填。 */
  const networked = S.status?.mode === 'networked';
  const alsoToggle = networked ? `<span id="notify-also-zone">${notifyAlsoHtml()}</span>` : '';
  const footer = networked
    ? '组网模式：任务终态通知由中心端渠道发送；开启「同时通知」后，本机执行任务的终态本机渠道也同步发一份（本机未配渠道则自然静默）。开关即时生效。'
    : 'standalone 模式由本机直接发送（本机通知即唯一通知）。多渠道并存，同一条终态会广播给所有启用且订阅了该事件的渠道；投递失败自动重试 3 次，绝不影响任务本身。';
  return `<section class="card spaced"><div class="row"><div><h2>通知</h2>
    <small class="muted">任务到达终态（完成 / 失败 / 拿不准 / 取消）时主动给你发消息：飞书、邮件、自建 webhook。</small></div>
    <div class="actions">${alsoToggle}${button('＋ 添加通知渠道', 'notify-add', 'primary')}</div></div>
    <div id="notify-section" class="spaced">${notifySectionHtml()}</div>
    <p class="footer-note">${footer}</p></section>`;
}
/* loadNotifySettings 拉「同时通知」开关（GET）：应答落状态后局部重绘 toggle
 *（声明式，D2）。会话代次防护与 loadNotifyChannels 同款：登出/再登录后迟到
 * 应答不写状态、不碰新会话 DOM。 */
async function loadNotifySettings() {
  const epochAtReq = S.sessionEpoch;
  const r = await api('GET', '/api/v1/local/notify/settings').catch(() => null);
  if (S.sessionEpoch !== epochAtReq) return;
  if (r && r.code === 200 && typeof r.data?.also_notify === 'boolean') {
    S.notifyAlso = r.data.also_notify;
    paintNotifyAlso();
  }
}
/* saveAlsoNotify 开关写入（声明式，codex 审查必修 2）：进入保存态即重绘（toggle
 * 置灰、显示目标值+保存中），PUT 应答落状态后再重绘——保存期间轮询 render 重建
 * 节点同样从状态出发，显示与实际值不再脱节；失败回滚到已保存值。会话代次防护：
 * 登出/再登录后迟到应答不写状态不重绘（新会话会重新拉取）。 */
async function saveAlsoNotify(cb) {
  if (S.notifyAlsoSaving) return;
  const want = cb.checked;
  const epochAtReq = S.sessionEpoch;
  S.notifyAlsoSaving = true;
  S.notifyAlsoPending = want;
  paintNotifyAlso();
  const r = await api('PUT', '/api/v1/local/notify/settings', { also_notify: want }).catch(() => null);
  if (S.sessionEpoch !== epochAtReq) return; // 迟到应答：会话已换代，状态归新会话重拉
  S.notifyAlsoSaving = false;
  S.notifyAlsoPending = null;
  if (r && r.code === 200) {
    S.notifyAlso = want;
    toast(want ? '已开启同时通知：本机渠道与中心端渠道都会收到终态通知' : '已关闭同时通知：终态通知只走中心端渠道');
  } else {
    toast('开关保存失败 · ' + (r ? errLine(r.data) : '网络异常'));
  }
  paintNotifyAlso();
}
/* loadNotifyChannels 拉渠道列表（已脱敏形态），完成后只重绘通知区块。
   会话代次防护（返工建议修 4）：登出/再登录后迟到的应答不写回状态——加载态已随
   会话边界复位，新会话进 services 页由 route() 重拉。 */
async function loadNotifyChannels() {
  const epochAtReq = S.sessionEpoch;
  S.notifyLoading = true; S.notifyError = '';
  paintNotifySection();
  const r = await api('GET', '/api/v1/local/notify/channels').catch(() => null);
  if (S.sessionEpoch !== epochAtReq) return; // 迟到应答：会话已换代，状态归新会话重拉
  S.notifyLoading = false;
  if (r && r.code === 200 && Array.isArray(r.data?.channels)) {
    S.notifyChannels = r.data.channels; S.notifyError = '';
    S.notifyLoaded = true;
  } else {
    S.notifyError = r ? errLine(r.data) : '网络异常 · 未收到服务器应答';
  }
  paintNotifySection();
}
function paintNotifySection() {
  if (S.page !== 'services') return;
  const el = $('#notify-section');
  if (el) el.innerHTML = notifySectionHtml();
}
function notifySectionHtml() {
  if (S.notifyLoading) return '<p class="muted">通知渠道加载中…</p>';
  if (S.notifyError) return `<div class="notice warn"><b>通知渠道加载失败</b><br>${esc(S.notifyError)}</div><div class="actions spaced">${button('重试', 'notify-retry', '')}</div>`;
  if (!S.notifyChannels.length) return `<div class="empty"><h2>还没有通知渠道</h2><p class="muted">添加一个渠道，本机任务完成、失败或等你确认时主动给你发消息——飞书群、邮件都行；不配置也完全不影响派活。</p>${button('＋ 添加第一个渠道', 'notify-add', 'primary')}</div>`;
  return S.notifyChannels.map(notifyChannelCardHtml).join('');
}
/* deliveryLineHtml 渠道最近投递事实行（FE-POLISH P0-3「告警通道失效不告警」，
 * 2026-10-29）：与 hub 同词典（NOTIFY_DELIV shared block）同判据——数据=渠道列表
 * 响应的 last_ok_at/last_fail_at/last_fail_reason（Dispatcher 内存态，自进程启动
 * 起记，重启清零——「尚未投递」不冒充成功，无记录不渲染）；「当前疑似失败」判据
 * =服务端 failing 字段（P2-02：失败晚于成功或从无成功由服务端按原始时刻含亚毫秒
 * 判定，前端 Date 只保留毫秒不自比）：事实行着 danger + 前置「修复」入口（打开
 * 渠道编辑）。DOM 组装留本端（button() 签名差异，见 NOTIFY_DELIV 块注）。 */
function deliveryLineHtml(ch) {
  const ok = realTime(ch.last_ok_at), fail = realTime(ch.last_fail_at);
  if (!ok && !fail) return '';
  const failing = !!ch.failing;
  const parts = [];
  if (ok) parts.push(`${NOTIFY_DELIV.ok} ${esc(timeFull(ch.last_ok_at))}`);
  if (fail) parts.push(`${NOTIFY_DELIV.fail} ${esc(timeFull(ch.last_fail_at))}${ch.last_fail_reason ? '（' + esc(ch.last_fail_reason) + '）' : ''}`);
  const fix = failing ? ' · ' + button(NOTIFY_DELIV.fix, 'notify-edit:' + esc(ch.id), 'link') : '';
  return `<div class="chan-deli${failing ? ' deli-fail' : ''}">${parts.join(' · ')}${fix}</div>`;
}
function notifyChannelCardHtml(ch) {
  /* 徽标：webhook+飞书格式沿用「飞书机器人」心智词，与 hub 同规则（notify-unify
   * 改动二）。 */
  const label = ch.type === 'webhook' && ch.format === 'feishu' ? NOTIFY_FORMATS.feishu : (NOTIFY_TYPES[ch.type] || { label: ch.type }).label;
  const ev = (ch.events || []).map(e => (NOTIFY_EVENT_OPTS.find(([k]) => k === e) || ['', e])[1]).join('、') || '全部终态';
  const secretKeys = Object.keys(ch.secrets || {}).filter(k => k !== 'password' && !k.startsWith('header.'));
  const meta = secretKeys.length ? secretKeys.map(k => ch.secrets[k]).filter(Boolean).join(' · ') : '';
  return `<div class="chan-card${ch.enabled ? '' : ' off'}">
    <div class="chan-main">
      <button type="button" class="switch${ch.enabled ? ' on' : ''}" data-act="notify-toggle:${esc(ch.id)}" aria-label="${ch.enabled ? '停用' : '启用'} ${esc(ch.name)}" title="${ch.enabled ? '点击停用' : '点击启用'}"></button>
      <span class="badge">${esc(label)}</span>
      <span class="chan-name">${esc(ch.name)}</span>
    </div>
    <div class="chan-actions">
      ${button('发测试', 'notify-test:' + esc(ch.id), '', '', `data-name="${esc(ch.name)}"`)}
      ${button('编辑', 'notify-edit:' + esc(ch.id), '')}
      ${button('删除', 'notify-del:' + esc(ch.id), 'danger', '', `data-name="${esc(ch.name)}"`)}
    </div>
    <div class="chan-meta">订阅：${esc(ev)}${meta ? ' · ' + esc(meta) : ''} · 创建于 ${esc(timeFull(ch.created_at))}${ch.enabled ? '' : ' · 已停用'}</div>
    ${deliveryLineHtml(ch)}
  </div>`;
}

/* notifyFormHtml/notifyTypeFieldsHtml/collectNotifyForm/notifyHintHtml/nfRepaint/
 * nfSecFromDom 已收进「通知表单与词典 shared block」（双端同文，见上方块内定义）；
 * node 调用 notifyFormHtml(ch, NOTIFY_TYPE_KEYS) 传入可见类型键序（无 inbox）。 */
async function saveNotifyChannel() {
  const f = collectNotifyForm();
  if (!f.name) { formError('请先填写渠道名称', '渠道名称必填；其余已填内容全部保留。'); toast('请先填写渠道名称'); $('#nf-name')?.focus(); return; }
  const editing = !!f.id;
  const btn = document.querySelector('[data-act="notify-save"]');
  busyBtn(btn, editing ? '保存中…' : '创建中…');
  try {
    const r = editing
      ? await api('PUT', '/api/v1/local/notify/channels/' + encodeURIComponent(f.id), f).catch(() => null)
      : await api('POST', '/api/v1/local/notify/channels', f).catch(() => null);
    if (r && (r.code === 201 || r.code === 200)) {
      closeNotifyForm();
      await loadNotifyChannels();
      paintNotifySection();
      toast(editing ? '渠道已更新' : '渠道已创建 · 点「发测试」验证一下');
    } else {
      /* A3 错误就地留痕：失败条留在表单顶部（草稿不清、可改后原钮重试），toast 只短报。 */
      formError(editing ? '保存失败' : '创建失败',
        (r ? errLine(r.data) : '网络异常 · 未收到服务器应答，结果未知') + '。已填内容全部保留，修正后可原按钮重试。');
      toast((editing ? '保存失败' : '创建失败') + '：' + (r ? errLine(r.data) : '网络异常'));
    }
  } finally { if ($('#notify-form')) idleBtn(btn); }
}
/* 通知渠道编辑弹层基建（居中 modal，与 hub openLayerPlain 同形态——双端一致三层
 * 操作逻辑层，2026-10-26 notify-polish 改动三：右侧抽屉形态退役；遮罩关闭=取消
 * 语义。无障碍对齐 hub：role=dialog/aria-modal/aria-labelledby(layer-title)/焦点
 * 移入。仍属 #layer 直清家族（白名单：无 hub 280ms 出场动画与世代机制）。 */
let nfReturnFocus = null;   // 通知编辑弹层打开前的触发元素（A2 关闭返焦）
function openNotifyForm(ch) {
  if (S.drawer) closeDrawer();       // 同一 #layer 互斥：任务详情抽屉让位
  if (S.sessDrawer) closeSessionDrawer();   // 会话抽屉让位（同上）
  if (cfgEdit) closeCfgDrawer(true); // 能力配置抽屉让位（还原快照）
  const fa = document.activeElement;
  nfReturnFocus = fa && fa !== document.body ? fa : null;   // A2：关闭返焦
  $('#layer').innerHTML = `<div class="overlay"><section class="modal" id="notify-modal" role="dialog" aria-modal="true" aria-labelledby="layer-title" tabindex="-1">${notifyFormHtml(ch, NOTIFY_TYPE_KEYS)}</section></div>`;
  layerInert(true);   // A2 键盘基线：背景隔离
  $('#layer [role=dialog]')?.focus();
  playDrawerOpen();
  $('#layer').addEventListener('click', nfMaskClose); // 具名引用重复注册幂等去重
}
function nfMaskClose(e) {
  if (!$('#notify-modal')) return; // 弹层已关后残留监听不动作（与 cfgMaskClose 同机制）
  if (e.target.classList.contains('overlay')) closeNotifyForm();
}
function closeNotifyForm() {
  layerInert(false);
  $('#layer').innerHTML = '';
  const back = nfReturnFocus;
  nfReturnFocus = null;
  /* A2 返焦兜底同 closeDrawer：失存回落侧栏当前页签（与 hub 同构）。 */
  if (back?.isConnected) back.focus();
  else {
    const tab = document.querySelector('aside nav button.active') || document.querySelector('aside nav button');
    if (tab) tab.focus();
  }
}
async function toggleNotifyChannel(id, btn) {
  const ch = S.notifyChannels.find(x => x.id === id);
  if (!ch) return;
  btn.disabled = true;
  const r = await api('PUT', '/api/v1/local/notify/channels/' + encodeURIComponent(id), { enabled: !ch.enabled }).catch(() => null);
  if (r && r.code === 200) {
    ch.enabled = r.data.enabled;
    toast(ch.enabled ? '渠道已启用' : '渠道已停用');
  } else {
    toast('开关失败 · ' + (r ? errLine(r.data) : '网络异常'));
  }
  paintNotifySection();
}
async function testNotifyChannel(id, btn) {
  /* A1-7（FE-POLISH 轮2）：发测试进行时与 hub 同词同状态（busyBtn「发送中…」）——
   * 仅 disabled 像「点了没反应」（codex I03 漂移项）；idleBtn 恢复原文案。 */
  busyBtn(btn, '发送中…');
  try {
    const r = await api('POST', '/api/v1/local/notify/channels/' + encodeURIComponent(id) + '/test').catch(() => null);
    if (r && r.code === 200) {
      toast(r.data?.ok ? '测试消息已发出 · 请查收' : '发送失败：' + (r.data?.error || '未知原因'));
    } else {
      toast('测试请求失败 · ' + (r ? errLine(r.data) : '网络异常'));
    }
    await loadNotifyChannels();   // P0-3：测试结果同落投递状态，卡片事实行随刷新（与 hub 同口径）
  } finally { idleBtn(btn); }
}
function deleteNotifyChannel(id, name) {
  confirmModal('删除通知渠道 · ' + esc(name), `删除后不再收到「${esc(name)}」的任何通知（不可恢复）；后续可重新添加。`, '确认删除', async () => {
    const r = await api('DELETE', '/api/v1/local/notify/channels/' + encodeURIComponent(id)).catch(() => null);
    if (r && r.code === 200) {
      await loadNotifyChannels();
      paintNotifySection();
      toast('渠道已删除');
    } else {
      toast('删除失败 · ' + (r ? errLine(r.data) : '网络异常'));
    }
  });
}

/* ---------- 本机 agent 发现与登记预览（W06-02） ----------
 * 双端形态约定：node 单端能力（hub 无发现/登记按钮，任务书双端形态表明列差异）；
 * 弹层沿用居中 modal 家族（openNotifyForm 同壳：#layer + .overlay > .modal，
 * modalHead 复用 shared block 同词 ×，不触碰块内）。发现=只探测显式候选（零磁盘
 * 扫描、零写盘）；登记预览=差异三态+检测四层（同零写盘）；确认只把登记面加进
 * 能力配置编辑表，保存仍走 cfgSave 的既有 PUT 整表链——前端不发明第二保存路径。
 * 层状态词与后端口径逐字对齐：pass=通过 / fail=未通过 / not_checked=未检测；
 * execution_ready pass 必带「模型未验证」注记，绝不合并绿灯。 */
let disc = null;          // 发现会话：{form:{name,host,command,config_home}, results:[], note:'', err:'', busy}
let discReturnFocus = null;
let prevReg = null;       // 登记预览会话：{idx, draft:{...登记面}, preview, err, busy}
let prevReturnFocus = null;

function openDiscoverModal() {
  if (S.drawer) closeDrawer();       // 同一 #layer 互斥：任务详情抽屉让位
  if (S.sessDrawer) closeSessionDrawer();
  if (cfgEdit) closeCfgDrawer(true); // 能力配置抽屉让位（还原快照）
  if (!disc) disc = { form: { name: '', host: '', command: '', config_home: '' }, results: [], note: '', err: '', busy: false };
  const fa = document.activeElement;
  discReturnFocus = fa && fa !== document.body ? fa : null;
  paintDiscoverModal();
}
function paintDiscoverModal() {
  $('#layer').innerHTML = `<div class="overlay"><section class="modal" id="disc-modal" role="dialog" aria-modal="true" aria-labelledby="layer-title" tabindex="-1">
    ${modalHead('发现本机 agent')}
    <p class="muted small">只探测你在下方逐个显式给出的候选（command 填绝对路径）；不做磁盘 / PATH 扫描。检测纯元数据：宿主配置只回「是否可用 + 引用路径」，内容与凭证永不读取返回。</p>
    <div class="cfg-form-cols">
      <label>名称<input data-disc="name" value="${esc(disc.form.name)}" placeholder="预备登记名（如 my-codex）"></label>
      <label>宿主<select data-disc="host">
        <option value=""${disc.form.host === '' ? ' selected' : ''}>未知宿主（按通用手动检测）</option>
        <option value="codex"${disc.form.host === 'codex' ? ' selected' : ''}>codex</option>
        <option value="zcode"${disc.form.host === 'zcode' ? ' selected' : ''}>zcode</option>
      </select></label>
      <label>command（绝对路径，只探测它）<input data-disc="command" value="${esc(disc.form.command)}" placeholder="/usr/local/bin/codex"></label>
      <label>config_home（可空；空=配置层不检测）<input data-disc="config_home" value="${esc(disc.form.config_home)}" placeholder="/Users/me"></label>
    </div>
    <div class="row spaced">${button(disc.busy ? '探测中…' : '探测此候选', 'disc-run', 'primary', disc.busy ? 'disabled' : '')}${disc.results.length ? button('清空结果', 'disc-clear', 'link') : ''}</div>
    <div id="disc-zone" class="spaced">${discResultsHtml()}</div>
  </section></div>`;
  layerInert(true);
  $('#layer [role=dialog]')?.focus();
  playDrawerOpen();
  $('#layer').addEventListener('click', discMaskClose); // 具名引用重复注册幂等去重
}
function discMaskClose(e) {
  if (!$('#disc-modal')) return; // 弹层已关后残留监听不动作（同 nfMaskClose 机制）
  if (e.target.classList.contains('overlay')) closeDiscoverModal();
}
function closeDiscoverModal() {
  disc = null;
  layerInert(false);
  $('#layer').innerHTML = '';
  const back = discReturnFocus;
  discReturnFocus = null;
  if (back?.isConnected) back.focus();
  else {
    const tab = document.querySelector('aside nav button.active') || document.querySelector('aside nav button');
    if (tab) tab.focus();
  }
}
/* discLayerChip 检测层徽标：状态词与四层口径同源（未检测不冒认、失败带原因）。 */
function discLayerChip(label, lc) {
  const st = lc?.status || 'not_checked';
  const word = st === 'pass' ? '通过' : st === 'fail' ? '未通过' : '未检测';
  const cls = st === 'pass' ? 'online' : st === 'fail' ? 'offline' : '';
  return `<span class="badge ${cls}" title="${esc(lc?.reason || '')}">${esc(label)} ${word}</span>`;
}
function discResultsHtml() {
  if (disc.err) return `<div class="notice warn"><b>探测失败</b>：${esc(disc.err)}</div>`;
  if (!disc.results.length) return `<p class="muted small">尚无结果：填好候选后点「探测此候选」。发现≠登记——结果零写盘、不自动开放能力。</p>`;
  return disc.results.map((r, i) => {
    const rep = r.report || {};
    return `<div class="chan-card${r.registered ? '' : ' off'}" id="disc-res-${i}">
      <div class="row"><div><b>${esc(r.name)}</b> <span class="badge">${esc(r.host || '未知宿主')}</span>${r.registered ? ` <span class="badge online">已登记（${r.registered_by === 'command' ? '按命令命中' : '按名命中'}）</span>` : ' <span class="badge">未登记</span>'}</div>
      <div class="actions">${button('登记预览…', 'disc-preview:' + i, '')}</div></div>
      <div class="spaced">${discLayerChip('安装', rep.installed)}${discLayerChip('配置识别', rep.config_recognized)}${discLayerChip('执行就绪', rep.execution_ready)}${discLayerChip('本机调用', rep.call_allowed)}</div>
      ${rep.execution_ready?.status === 'pass' ? `<p class="muted small">${esc(rep.execution_ready.reason || '')}</p>` : (rep.execution_ready?.reason ? `<p class="muted small">执行就绪：${esc(rep.execution_ready.reason)}</p>` : '')}
      <p class="muted small">允许本机调用：${esc(r.local_call?.note || '')}<br>exposed 对中心开放：${esc(r.exposed?.note || '')}</p>
    </div>`;
  }).join('') + (disc.note ? `<p class="footer-note">${esc(disc.note)}</p>` : '');
}
async function discRun(btn) {
  if (disc.busy) return;
  disc.busy = true;
  disc.err = '';
  busyBtn(btn, '探测中…');
  try {
    const r = await api('POST', '/api/v1/local/agents/discover', { candidates: [disc.form] });
    if (r.code === 200) {
      const d = r.data || {};
      disc.results = (d.results || []).concat(disc.results); // 新结果在前（同候选重跑=最新在前）
      disc.note = d.note || '';
    } else {
      disc.err = errLine(r.data);
    }
  } catch (e) {
    if (!e?.unauthorized) disc.err = '网络层失败：结果未知';
  }
  disc.busy = false;
  paintDiscoverModal();
}
/* draftFor 登记面预填：已登记名优先取现有编辑副本（改名式修正从现状出发），
 * 未登记名取候选回显 + 缺省（workdir 必填留空待填）。 */
function draftFor(r) {
  const existing = (S.cfg.agents || []).find(x => x.name === r.name);
  if (existing) return JSON.parse(JSON.stringify(existing));
  return {
    name: r.name || '', kind: 'cli', command: r.command || '', workdir: '',
    agent_args: [], timeout_sec: 600, allow_args: false, allow_env: [],
    config_home: r.config_home || '', description: '', permission_mode: '',
    exposed: true, stdin_to_arg: false,
    session_adapter: (r.host === 'codex' || r.host === 'zcode') ? r.host : '',
  };
}
function openPreviewModal(idx) {
  const r = disc?.results[idx];
  if (!r) return;
  const fa = document.activeElement;
  prevReturnFocus = fa && fa !== document.body ? fa : null;
  prevReg = { idx, draft: draftFor(r), preview: null, err: '', busy: false };
  paintPreviewModal();
  prevRefresh(); // 打开即出预览（零写盘，可反复改参重出）
}
function paintPreviewModal() {
  const d = prevReg.draft;
  const fld = (field, label, value, type = 'text') => `<label>${esc(label)}<input data-prev="${field}" type="${type}" value="${esc(value)}"></label>`;
  $('#layer').innerHTML = `<div class="overlay"><section class="modal" id="prev-modal" role="dialog" aria-modal="true" aria-labelledby="layer-title" tabindex="-1">
    ${modalHead('登记预览 · ' + (esc(d.name) || '（未命名）'))}
    <p class="muted small">登记面与现有配置的差异 + 检测四层即时结果。<b>预览零写盘</b>；确认后加入下方能力配置表，保存仍走「保存全部」（整表 PUT，无第二写路径）。</p>
    <div class="cfg-form-cols">
      ${fld('name', '名称', d.name)}
      ${fld('command', 'command（绝对路径）', d.command)}
      ${fld('workdir', 'workdir（绝对路径，必填）', d.workdir)}
      ${fld('agent_args', 'agent_args（逗号分隔）', (d.agent_args || []).join(','))}
      ${fld('allow_env', 'allow_env（逗号分隔或 *）', (d.allow_env || []).join(','))}
      ${fld('timeout_sec', 'timeout_sec（秒）', String(d.timeout_sec ?? 600), 'number')}
      ${fld('config_home', 'config_home（可空）', d.config_home)}
      ${fld('description', '描述', d.description)}
      ${fld('permission_mode', 'permission_mode（全权档位 yolo / danger-full-access 预览即拒）', d.permission_mode)}
      <label>适配器 session_adapter<select data-prev="session_adapter">
        <option value=""${!d.session_adapter ? ' selected' : ''}>无（不支持持久会话）</option>
        <option value="codex"${d.session_adapter === 'codex' ? ' selected' : ''}>codex</option>
        <option value="zcode"${d.session_adapter === 'zcode' ? ' selected' : ''}>zcode</option>
      </select></label>
      <label class="cfg-check"><input type="checkbox" data-prev="exposed" ${d.exposed !== false ? 'checked' : ''}> 开放给其他机器调用（exposed；本机派单不受限）</label>
      <label class="cfg-check"><input type="checkbox" data-prev="allow_args" ${d.allow_args ? 'checked' : ''}> allow_args（允许任务传参）</label>
    </div>
    <div class="row spaced">${button(prevReg.busy ? '预览生成中…' : '按当前表单重新出预览', 'prev-refresh', '', prevReg.busy ? 'disabled' : '')}${button('返回发现结果', 'prev-back', 'link')}</div>
    <div id="prev-zone" class="spaced">${prevZoneHtml()}</div>
  </section></div>`;
  layerInert(true);
  $('#layer [role=dialog]')?.focus();
  playDrawerOpen();
  $('#layer').addEventListener('click', prevMaskClose);
}
function prevMaskClose(e) {
  if (!$('#prev-modal')) return;
  if (e.target.classList.contains('overlay')) closePreviewModal(true);
}
function closePreviewModal(back) {
  prevReg = null;
  if (back && disc) { paintDiscoverModal(); return; } // 返回发现结果（层重开）
  layerInert(false);
  $('#layer').innerHTML = '';
  const b = prevReturnFocus;
  prevReturnFocus = null;
  if (b?.isConnected) b.focus();
  else {
    const tab = document.querySelector('aside nav button.active') || document.querySelector('aside nav button');
    if (tab) tab.focus();
  }
}
async function prevRefresh(btn) {
  if (prevReg.busy) return;
  prevReg.busy = true;
  prevReg.err = '';
  if (btn) busyBtn(btn, '预览生成中…');
  try {
    const d = prevReg.draft;
    const r = await api('POST', '/api/v1/local/agents/registration-preview', { agent: {
      name: d.name, kind: d.kind || 'cli', command: d.command, workdir: d.workdir,
      agent_args: d.agent_args || [], timeout_sec: d.timeout_sec, allow_args: !!d.allow_args,
      allow_env: d.allow_env || [], config_home: d.config_home, description: d.description,
      permission_mode: d.permission_mode, exposed: d.exposed !== false, stdin_to_arg: !!d.stdin_to_arg,
      session_adapter: d.session_adapter || '',
    }, detect_host: disc?.results[prevReg.idx]?.host || '' });
    if (r.code === 200) prevReg.preview = r.data;
    else { prevReg.err = errLine(r.data); prevReg.preview = null; }
  } catch (e) {
    if (!e?.unauthorized) prevReg.err = '网络层失败：结果未知';
  }
  prevReg.busy = false;
  paintPreviewModal(); // 表单值从 prevReg.draft 回填（input 委托实时落 draft，输入不丢）
}
function prevZoneHtml() {
  if (prevReg.err) return `<div class="notice warn"><b>预览被拒</b>：${esc(prevReg.err)}<br>登记面未写盘，修正表单后可重新出预览。</div>`;
  const p = prevReg.preview;
  if (!p) return '<p class="muted small">点「按当前表单重新出预览」查看与现有配置的差异与本机检测（零写盘）。</p>';
  const diff = p.diff || {};
  let diffHtml = '';
  if (diff.kind === 'create') diffHtml = '<span class="badge online">新增</span> <small class="muted">保存后为结构性变更，需「应用并重启」生效。</small>';
  else if (diff.kind === 'none') diffHtml = '<span class="badge">无变化</span> <small class="muted">与现有配置完全一致。</small>';
  else diffHtml = '<span class="badge">修改</span><div class="table-wrap spaced"><table><tbody>' +
    (diff.changed_fields || []).map(f => `<tr><th>${esc(f.field)}</th><td>${esc(f.old || '（空）')} → <b>${esc(f.new || '（空）')}</b></td></tr>`).join('') +
    '</tbody></table></div>';
  const rep = p.detect || {};
  const lay = (label, lc) => {
    const st = lc?.status || 'not_checked';
    const word = st === 'pass' ? '通过' : st === 'fail' ? '未通过' : '未检测';
    const cls = st === 'pass' ? 'online' : st === 'fail' ? 'offline' : '';
    return `<tr><th>${esc(label)}</th><td><span class="badge ${cls}">${word}</span>${lc?.reason ? ` <small class="muted">${esc(lc.reason)}</small>` : ''}</td></tr>`;
  };
  return `<div class="spaced"><h3>与现有配置的差异</h3>${diffHtml}
    <h3 class="spaced">检测（即时四层 · 只读探测）</h3>
    <div class="table-wrap"><table><tbody>${lay('安装', rep.installed)}${lay('配置识别', rep.config_recognized)}${lay('执行就绪', rep.execution_ready)}${lay('本机调用', rep.call_allowed)}</tbody></table></div>
    ${rep.execution_ready?.status === 'pass' ? `<p class="muted small">${esc(rep.execution_ready.reason)}</p>` : ''}
    ${(p.structural || []).length ? `<div class="notice warn spaced"><b>结构性变更</b>：${esc((p.structural || []).join('；'))}——保存后需「应用并重启」生效。</div>` : ''}
    <p class="footer-note">${esc(p.note || '')}</p></div>`;
}
/* prevConfirm 确认登记：只把登记面写入能力配置编辑副本（同名替换防重名），保存
 * 仍由主人点「保存全部（校验+写盘）」走既有 PUT 整表链——前端零第二写路径。 */
function prevConfirm() {
  const d = prevReg.draft;
  if (!S.cfg.loaded || S.cfg.unavailable) { toast('能力配置未加载：先在「能力配置」点「加载配置」'); return; }
  const entry = {
    name: d.name, kind: d.kind || 'cli', command: d.command, workdir: d.workdir,
    agent_args: d.agent_args || [], timeout_sec: d.timeout_sec, allow_args: !!d.allow_args,
    allow_env: d.allow_env || [], config_home: d.config_home, description: d.description,
    permission_mode: d.permission_mode, exposed: d.exposed !== false, stdin_to_arg: !!d.stdin_to_arg,
    session_adapter: d.session_adapter || '',
  };
  const i = S.cfg.agents.findIndex(x => x.name === entry.name);
  if (i >= 0) S.cfg.agents[i] = entry; else S.cfg.agents.push(entry);
  closePreviewModal(true);
  render();
  toast('已加入能力配置表（未写盘）：点「保存全部（校验+写盘）」完成登记。' +
    (i >= 0 ? '这是对既有能力的修改。' : '新增能力保存后需「应用并重启」生效。'));
}

/* ---------- 页面③：派任务·我的任务（旅程②消费者） ---------- */
function tasksPage() {
  const c = S.f;
  // 建议修 10（2026-10-22）：standalone 本机主权——本机派单不经 hub，离线/信箱
  // 语义不适用，目录与选中提示不渲染「离线进信箱」误导文案；组网模式维持现状。
  const standalone = S.status?.mode === 'standalone';
  const head = `<div class="page-head"><div><h1>任务</h1>
    <p class="muted">从本机服务选目标 → 一段话描述要做的事 → 提交；结果在这里跟踪与取回。</p></div></div>`;
  /* 2026-10-23 同步 hub 端口径：下拉项只留名字（描述收进 title），智行管家
   * 单独 optgroup 分组加前缀，与普通智能体视觉分区。
   * A1-19（FE-POLISH 轮2）：管家判定与 hub isPrimeDir 同语义——能力卡
   * orchestrator 声明优先（AgentCard 自 2026-10-28 客户端声明制起携带本字段，
   * node 目录响应已透传），名字含 zcode-dev 降为向后兼容回退；不再仅凭名字
   * 判定（codex 六节对照表外项：回退识别不得说成用户已指定）。 */
  const isPrime = e => e.card.orchestrator === true || e.card.name.includes('zcode-dev');
  const optOf = e => {
    const v = e.node_id + '|' + e.card.name;
    const off = standalone || e.online ? '' : ' · 离线（任务先存着，上线就送）';
    const tip = `${e.card.name}${isPrime(e) ? ' · 智行管家：拆解复合目标并派发汇总' : ''} — ${e.card.description || '（无描述）'} · ${permLabel(e.card.permission_mode)}${off}`;
    const label = isPrime(e) ? `智行管家 · ${e.card.name}` : e.card.name;
    return `<option value="${esc(v)}" ${c.target === v ? 'selected' : ''} title="${esc(tip)}">${esc(label)}${off}</option>`;
  };
  const primeOpts = S.catalog.filter(isPrime), restOpts = S.catalog.filter(e => !isPrime(e));
  const group = (label, arr) => arr.length ? `<optgroup label="${esc(label)}">${arr.map(optOf).join('')}</optgroup>` : '';
  const opts = group('智行管家 · 复合目标拆解派发汇总', primeOpts) + group('智能体与工具', restOpts);
  const target = S.catalog.find(e => e.node_id + '|' + e.card.name === c.target);
  const stageView = {
    idle: '',
    submitting: `<div class="statusline"><span class="badge offered">提交中…</span></div>`,
    acked: `<div class="notice ok"><b>已确认</b>：任务已存好，任务号 <code>${esc(c.taskId || '')}</code>。${button('查看任务', 'open-submitted', 'link')}</div>`,
    failed: `<div class="notice warn"><b>提交被拒绝</b>：${c.message}${button('修正后重试', 'form-retry', 'link')}</div>`,
    unknown: `<div class="notice warn"><b>结果未知 · 表单已锁定</b>：${esc(c.message)}<br>同一恢复密钥（<code>${esc(c.idem)}</code>）重试不会重复执行：${button('同键重试', 'form-resend', 'link')} · ${button('放弃这单', 'form-abandon', 'link')}</div>`,
  }[c.stage] || '';
  const formLocked = c.stage === 'submitting' || c.stage === 'unknown' || c.stage === 'acked';
  const targetNote = target ? `<b>${esc(target.card.name)}</b> · ${esc(target.card.description || '（无描述）')} · ${esc(permLabel(target.card.permission_mode))} · ${standalone ? '<span class="badge online">本机直执行</span>' : target.online ? '<span class="badge online">在线</span>' : '<span class="badge offline">离线 · 排队等它回来</span>'}` : '';
  return head + `
  <section class="card" id="task-form"><h2>派任务</h2>
    ${S.catalog.length ? `
    <label>本机服务${standalone ? '（本机主权：未开放的能力也可本机派单）' : '（已开放给其他机器调用；派单目标只能是本机）'}<select id="f-target" ${formLocked ? 'disabled' : ''}><option value="" ${!c.target ? 'selected' : ''} disabled>选择一个本机服务…</option>${opts}</select></label>
    <div class="notice" ${target ? '' : 'hidden'}>${targetNote}</div>
    <label>任务描述（一段话讲清要做什么；以 prompt_text 交付给对方 agent）<textarea id="f-stdin" placeholder="例：审查这段代码的并发安全性，指出竞态并给出修复建议…" ${formLocked ? 'disabled' : ''}>${esc(c.stdin)}</textarea></label>
    <div class="row"><small class="muted">恢复密钥 <code>${esc(c.idem)}</code> · 结果未知时可同键重试，不会重复执行</small>
    ${c.stage === 'acked' ? button('再派一单', 'form-retry', 'primary') : button(c.stage === 'submitting' ? '提交中…' : '确认并提交', 'form-submit', 'primary', c.stage === 'submitting' ? 'disabled' : '')}</div>
    <div class="statusline">${stageView}</div>`
      : `<div class="empty"><h2>本机还没有可派发的服务</h2><p class="muted">先在「服务」登记本机能力（standalone 下未开放也能本机派单）；跨机派单请经 hub 管理面/MCP 发起。</p>${button('去看服务', 'go-services', 'primary')}</div>`}
  </section>
  ${myTasksCard()}${sessMigrateCard()}`;
}
// listStatusBadge 列表状态徽章：行内 status 直出（账本全量驱动，2026-10-26 全量
// 审计裁决——无需点击补查）；留痕补尾行（账本中已不存在的本机派单历史）无 status，
// 按「仅存留痕」呈现（详情查询即 404。具体保留窗口待产品裁决，D2——文案不写时长）。
function listStatusBadge(e) {
  return e.status ? badge(e.status) : '<span class="badge offline" title="账本中已不存在（本机派单历史留痕）">仅存留痕</span>';
}
// originBadge 任务来源徽标：词自 ORIGIN_WORDS 词典（shared block 双端同文），不再复制字面量。
function originBadge(origin) {
  const w = ORIGIN_WORDS[origin === 'hub' ? 'hub' : 'local'];
  return `<span class="badge" title="${w}">${w}</span>`;
}
// originOf 行来源归一（与 taskOrigin 回退同口径）：origin 恒有值；缺省/留痕行按 local
// 计（本机控制台默认本机语义，hub 任务误归本机只会误导产物指引，反向无此风险）。
function originOf(e) { return e?.origin === 'hub' ? 'hub' : 'local'; }
// myTasksCard 任务卡壳（R2F-P2-01 壳/内容分层）：section 与唯一 id 只在整页
// render 时创建一次；卡内内容 myTasksZone 为 P1-03 槽位局部更新单元（paintTasksCard
// 共用同一函数，初始渲染与局部更新 DOM 层级同构，不再把含 id 的完整壳写回自身）。
function myTasksCard() {
  return `<section class="card spaced" id="my-tasks-card">${myTasksZone()}</section>`;
}
// myTasksZone 任务列表 + 来源筛选内容区（TASK-nav-filter A1）：卡头部三枚 chip（全部/本机
// 派单/hub 下发，带已载计数；视觉对齐 hub .filters 形态，零计数退灰）。筛选为渲染层
// 过滤已载入条目（陷阱④：轮询只刷首页、翻页保序并入都在数据层，筛选随每次渲染从
// 全量已载列表现算，不缓存筛选结果）。零命中出空态+清除筛选，但「加载更早」翻页以
// 全量载入数计、入口照常（codex 必须修项）。终审必修2：筛选行恒在（不随 all.length
// 消失——空快照+激活筛选时控件必须在位可清除，S.tf 不自动重置）；空态二分：激活筛选
// →筛选空态，未激活+真无任务→首次派单空态。终审必修3：footer 口径三数分离——
// 「命中 X 条；筛选作用于已载入的 N / 共 M 条」（X=rows，N=all，M=tasksTotal）。
// 头部行在本区（P1-03 残余 A 同批裁定）：恢复路径 paintTasksCard 带数据变化重写
// 本区时「已载 X / 共 Y」计数随之更新，不滞留整页 render 之前的旧值。
function myTasksZone() {
  const all = S.tasks;
  const origin = S.tf.origin;
  const rows = origin ? all.filter(e => originOf(e) === origin) : all;
  const cnt = k => k ? all.filter(e => originOf(e) === k).length : all.length;
  const more = all.length < S.tasksTotal;
  const loaded = more ? `已载 ${all.length} / 共 ${S.tasksTotal} 条` : `${S.tasksTotal} 条`;
  const chips = [['', '全部'], ['local', ORIGIN_WORDS.local], ['hub', ORIGIN_WORDS.hub]].map(([k, label]) =>
    `<button data-act="tf-origin" data-origin="${k}" class="${origin === k ? 'on' : ''}${cnt(k) ? '' : ' zero'}">${esc(label)} ${cnt(k)}</button>`).join('');
  /* A4 五态（FE-POLISH 轮2）：加载失败不冒充空列表——S.loadError 且尚无已载任务
   * ⇒ 失败态（就地重试）；S.status 缺失 ⇒ 首刷未落定加载中；其余按既有空态。 */
  const failed = !!S.loadError && !all.length;
  const loading = !S.status && !all.length;
  return `<div class="row"><h2>我的任务</h2><small>${loaded} · 本机执行过的全部任务（本机派单 + hub 下发）</small></div>
  <div class="filters">${chips}</div>
  ${failed ? listErrorBox('reload-all', '任务列表')
  : loading ? listLoadingBox('任务列表')
  : (S.loadError && all.length ? listStaleBar('reload-all', '任务列表') : '')
    + (rows.length ? `<div class="table-wrap"><table><thead><tr><th>时刻</th><th class="task-title">目标</th><th>来源</th><th>状态</th><th></th></tr></thead><tbody>
    ${rows.map(e => `<tr data-task="${esc(e.task_id)}" tabindex="0" aria-label="查看任务 ${esc(e.task_id)}"><td>${timeShort(e.created_at)}</td><td><strong>${esc(e.target_agent)}</strong> @ ${esc(e.target_node)}</td><td>${originBadge(e.origin)}</td><td>${listStatusBadge(e)}</td><td class="table-action">${button('详情', 'open-task:' + esc(e.task_id))}</td></tr>`).join('')}
  </tbody></table></div>`
    : origin ? `<div class="empty"><h2>没有符合筛选的任务</h2><p class="muted">当前筛选下已载入的任务无命中；清除筛选，或用下方入口加载更早的任务。</p>${button('清除筛选', 'tf-clear')}</div>`
    : `<div class="empty"><h2>本机还没有执行过任务</h2><p class="muted">上方表单派第一单；hub 下发任务入账后同样出现在这里。</p>${button('＋ 派单', 'focus-form', 'primary')}</div>`)}
  ${more ? `<div class="row">${button(S.tasksMoreBusy ? '加载中…' : `加载更早的任务（还有 ${S.tasksTotal - all.length} 条）`, 'tasks-more', '', S.tasksMoreBusy ? 'disabled' : '')}</div>` : ''}
  ${(all.length || origin) ? `<p class="footer-note">${origin ? `命中 ${rows.length} 条；筛选作用于已载入的 ${all.length} / 共 ${S.tasksTotal} 条 · ` : ''}列表即本机账本全量——本机派单与 hub 下发一律在列（全量审计，更早历史翻页载入）；状态随列表直出。「仅存留痕」= 账本中已不存在的本机派单历史，详情不再可查。</p>` : ''}`;
}

/* ---------- 派单表单动作 ---------- */
async function submitForm() {
  const [targetNode, targetAgent] = (S.f.target || '').split('|');
  if (!targetNode || !S.f.stdin.trim()) { toast('先选目标并填写任务描述'); return; }
  S.f.stage = 'submitting'; render();
  try {
    const r = await api('POST', '/api/v1/tasks', {
      target_node: targetNode, target_agent: targetAgent,
      input: { stdin: S.f.stdin }, submitted_by: 'console',
      idem_key: S.f.idem,
    });
    if (r.code === 202 || r.code === 200) {
      S.f.stage = 'acked'; S.f.taskId = r.data?.task_id || '';
      toast(r.data?.dup ? '已确认（恢复密钥命中原任务）' : '已确认');
      try { S.snap = ''; await loadAll(); } catch (e) { /* 刷新失败交给轮询 */ }
      render(); return;
    }
    S.f.stage = 'failed';
    S.f.message = errLine(r.data);
    if (r.data?.error === 'hub_unreachable') { S.f.stage = 'unknown'; S.f.message = 'hub 暂不可达，提交是否生效不确定。'; }
  } catch (e) {
    if (e.unauthorized) return; // 401 已回令牌门
    S.f.stage = 'unknown';
    S.f.message = '网络层失败，提交是否生效不确定。';
  }
  render();
}

/* ---------- 连接向导 / 管家钦定与轮换 / 断开组网动作 ----------
 * 端点契约（localapi，均 Bearer local_token）：POST /api/v1/local/setup/parse|apply|
 * disconnect（setup.go）、POST /api/v1/local/prime/rotate|declare（prime.go）。
 * 全部走 api() 封装与 toast 通道；向导失败信息在页内 stageView 呈现（可反复重试）。 */

/* parseJoin 连接串前端先验（即时人话反馈）；后端 ParseJoinString 同源白名单复核
 * （protocol/validate.go：node_id 2-32 位小写、token 64hex、hub_url ws(s)://）。 */
function parseJoin(raw) {
  const s = (raw || '').trim();
  const PFX = 'swarmmind://join?';
  if (!s.startsWith(PFX)) throw new Error('格式须为 swarmmind://join?hub=…&node=…&token=…（从 hub 控制台「添加机器向导」复制）');
  const q = new URLSearchParams(s.slice(PFX.length));
  const hub = q.get('hub'), node = q.get('node'), token = q.get('token');
  if (!hub || !node || !token) throw new Error('hub / node / token 三个参数缺一不可');
  if (!/^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$/.test(node)) throw new Error('node_id 须为 2-32 位小写字母/数字/连字符');
  if (!/^[0-9a-f]{64}$/.test(token)) throw new Error('token 须为 64 位十六进制');
  if (!/^wss?:\/\/\S+$/.test(hub)) throw new Error('hub_url 须为可解析的 ws:// 或 wss:// 地址');
  return { hub, node, token };
}

async function wizPreview() {
  S.wiz.join = $('#wiz-join')?.value ?? S.wiz.join; // 粘贴后未触发 input 时的兜底同步
  try {
    parseJoin(S.wiz.join);
  } catch (e) {
    S.wiz.preview = null; S.wiz.stage = 'failed'; S.wiz.msg = e.message; render(); return;
  }
  S.wiz.stage = 'previewing'; render();
  try {
    const r = await api('POST', '/api/v1/local/setup/parse', { join_string: S.wiz.join });
    if (r.code === 200) {
      S.wiz.preview = r.data; S.wiz.stage = 'idle';
    } else {
      S.wiz.preview = null; S.wiz.stage = 'failed'; S.wiz.msg = errLine(r.data);
    }
  } catch (e) {
    if (e.unauthorized) return;
    S.wiz.preview = null; S.wiz.stage = 'failed'; S.wiz.msg = e.network ? '本机 API 不可达' : '解析失败';
  }
  render();
}

async function wizApply() {
  if (!S.wiz.preview) return;
  S.wiz.join = $('#wiz-join')?.value ?? S.wiz.join;
  let v;
  try {
    v = parseJoin(S.wiz.join);
  } catch (e) {
    S.wiz.preview = null; S.wiz.stage = 'failed'; S.wiz.msg = e.message; render(); return;
  }
  // 预览后接入串被改过 → 作废预览，防「所见非所存」（后端仍会复核，这里管体验）
  if (v.hub !== S.wiz.preview.hub_url || v.node !== S.wiz.preview.node_id) {
    S.wiz.preview = null; S.wiz.stage = 'failed'; S.wiz.msg = '连接串在预览后被修改，请重新预览'; render(); return;
  }
  confirmModal('保存并重启', '将写入 node.yaml 并重启 node 进入组网模式。', '保存并重启', async () => {
    S.wiz.stage = 'saving'; render();
    try {
      const r = await api('POST', '/api/v1/local/setup/apply', { join_string: S.wiz.join });
      if (r.code === 200) {
        S.wiz.stage = 'done'; S.wiz.written = r.data?.written || '';
        toast('node.yaml 已写入，节点即将重启');
        // 后端 1.5s 后退出重启（setupRestartDelay）+ 进程拉起窗口；主动补两刷，
        // 错过由 5s 轮询兜底
        setTimeout(() => { S.snap = ''; pollTick(); }, 6000);
        setTimeout(() => { S.snap = ''; pollTick(); }, 12000);
      } else {
        S.wiz.stage = 'failed'; S.wiz.msg = errLine(r.data);
      }
    } catch (e) {
      if (e.unauthorized) return;
      S.wiz.stage = 'failed';
      S.wiz.msg = e.network ? '网络层失败，是否已写入不确定——勿重复提交，稍后刷新本页确认' : '保存失败';
    }
    render();
  });
}

function primeRotate(btn) {
  confirmModal('轮换管家凭证', '旧凭证将立即失效；管家宿主配置若可自动接线会随轮换刷新（重连生效），否则需改配新凭证（SWARM_NODE_PRIME_TOKEN）。', '确认轮换', async () => {
    if (btn) btn.disabled = true; // 300ms 反馈：立即锁按钮
    const epoch = S.sessionEpoch; // 收敛 3：请求发起时捕获会话代次
    try {
      const r = await api('POST', '/api/v1/local/prime/rotate');
      if (S.sessionEpoch !== epoch) return; // 迟到应答：会话已换代（logout/401/换令牌），一次性凭证不写回内存
      if (r.code === 200) {
        S.primeNew = r.data?.prime_token || '';
        S.primeNewWire = r.data?.wired ? { target: r.data?.wired_target || '' } : null;
        toast(r.data?.wired ? '已轮换 · 新凭证已自动写入管家宿主配置' : '已轮换 · 新凭证仅此一次展示，请立即复制');
      } else {
        toast('轮换失败 · ' + errLine(r.data));
      }
    } catch (e) {
      if (e.unauthorized) return;
      toast(e.network ? '本机 API 不可达' : '轮换失败');
    }
    render();
    if (btn && document.contains(btn)) btn.disabled = false;
  });
}

// primeDeclare 网页钦定（方案 3.2.1 UX 收口）：confirmModal 确认（含凭据不变说明）→
// declare → 钦定完成视图（凭据事实块 + MCP 配置片段）。成功后主动刷新 status
// （prime_agent 即时反映，不等 5s 轮询）。
function primeDeclare(btn) {
  const name = $('#prime-select')?.value;
  if (!name) { toast('先选择一个本机 agent'); return; }
  const run = async () => {
  S.primeDecl.busy = true;
  busyBtn(btn, '钦定中…');
  const epoch = S.sessionEpoch; // 收敛 3：请求发起时捕获会话代次
  try {
    const r = await api('POST', '/api/v1/local/prime/declare', { prime_agent: name });
    if (S.sessionEpoch !== epoch) return; // 迟到应答：会话已换代（logout/401/换令牌），一次性凭证不写回状态（busy 已随 resetPrimeState 复位）
    if (r.code === 200) {
      // 声明联动三字段如实保存（P1-02）：orch/orchErr/synced 原始值透传完成页
      // 三态渲染；字段缺省存 null（测试构造未装配联动 / standalone 无上报环节），
      // 完成页对 null 不作任何声明承诺。
      S.primeDecl.done = { agent: name, token: r.data?.prime_token || '', mcp: r.data?.mcp_config || null,
        wired: !!r.data?.wired, target: r.data?.wired_target || '',
        orch: typeof r.data?.orchestrator === 'boolean' ? r.data.orchestrator : null,
        orchErr: r.data?.orchestrator_error || '',
        synced: typeof r.data?.hub_synced === 'boolean' ? r.data.hub_synced : null };
      S.primeDecl.picking = false;
      S.primeNew = ''; // 轮换提示与钦定完成视图不同时堆叠
      S.primeNewWire = null;
      toast(r.data?.wired ? '钦定完成 · MCP 连接已自动写入管家宿主配置（凭据不变）' : '钦定完成 · 凭据不变；未接线宿主请轮换获取一次性凭据后配置');
      S.snap = '';
      await loadAll().catch(() => {});
    } else {
      toast('钦定失败 · ' + errLine(r.data));
    }
  } catch (e) {
    if (e.unauthorized) { S.primeDecl.busy = false; return; } // 401 已回令牌门
    toast(e.network ? '本机 API 不可达' : '钦定失败');
  }
  S.primeDecl.busy = false;
  render();
  };
  confirmModal('钦定本机管家', `钦定「${esc(name)}」为本机管家：写入 prime_agent 并即时生效；不换发共享编排凭据，现有 MCP 连接持续有效（作废凭据请用「轮换管家凭证」）。`, '确认钦定', run);
}

/* retryPrimeDeclare 声明失败补写（P1-02 重试入口）：不重换凭证，走既有单翻转
 * PATCH 端点——先按本机实况撤其余有效声明位（含 YAML 基线旧持有人的覆盖压制，
 * SetAgentOrchestrator 互斥按有效真相判定），再授本次钦定名。每步原子落盘；任一
 * 步失败如实提示，完成页失败态保留可再次点击（幂等收敛：重跑时其余位已空）。
 * hub_synced 消费同 declare 口径（fix2）：字段为 boolean 才写 synced（组网实况）；
 * 缺省（standalone 未组网，PATCH 应答已改缺省）存 null——完成页随之呈现
 * 「standalone 未组网」态，不冒充「已提交上报 hub」。 */
async function retryPrimeDeclare(btn) {
  const agent = btn?.dataset?.agent || S.primeDecl.done?.agent;
  if (!agent) return;
  if (btn) btn.disabled = true;
  try {
    for (const a of S.agents.filter(x => x.orchestrator && x.name !== agent)) {
      const r = await api('PATCH', '/api/v1/local/agents/' + encodeURIComponent(a.name) + '/orchestrator', { orchestrator: false });
      if (r.code !== 200) { toast('撤旧声明失败 · ' + errLine(r.data)); return; }
    }
    const r = await api('PATCH', '/api/v1/local/agents/' + encodeURIComponent(agent) + '/orchestrator', { orchestrator: true });
    if (r.code === 200 && r.data?.orchestrator) {
      if (S.primeDecl.done && S.primeDecl.done.agent === agent) {
        S.primeDecl.done.orch = true; S.primeDecl.done.orchErr = '';
        S.primeDecl.done.synced = typeof r.data?.hub_synced === 'boolean' ? r.data.hub_synced : null;
      }
      toast('声明已补写生效');
    } else {
      toast('补写被拒 · ' + errLine(r.data));
    }
  } catch (e) {
    if (!e?.unauthorized) toast(e.network ? '本机 API 不可达' : '重试失败');
  } finally {
    if (btn && document.contains(btn)) btn.disabled = false;
  }
  await loadAll().catch(() => {});
  render();
}

function netDisconnect(btn) {
  confirmModal('断开组网', '重启进入 standalone（本机自治）：hub 离线暂存里未送达的任务将留在 hub 等过期；本机历史任务不受影响，本机派单照常可用。', '确认断开', async () => {
  if (btn) btn.disabled = true;
  try {
    const r = await api('POST', '/api/v1/local/setup/disconnect');
    if (r.code === 200) {
      toast(r.data?.note || '已清空 hub_url，node 即将重启进入 standalone');
      setTimeout(() => { S.snap = ''; pollTick(); }, 6000);
      setTimeout(() => { S.snap = ''; pollTick(); }, 12000);
    } else {
      toast('断开失败 · ' + errLine(r.data));
    }
  } catch (e) {
    if (e.unauthorized) return;
    toast(e.network ? '本机 API 不可达' : '断开失败');
  }
  if (btn && document.contains(btn)) btn.disabled = false;
  });
}

async function copyPrimeToken() {
  await copyText(S.primeNew, '新管家凭证', '新管家凭证已复制 · 旧凭证已失效，请立即配给管家的 MCP 连接');
}

// 钦定完成视图的复制：凭证 + MCP 配置片段（JSON.stringify(mcp, null, 2) 同展示口径）。
async function copyPrimeDeclare() {
  await copyText(S.primeDecl.done?.token || '', '管家凭证', '管家凭证已复制 · 请立即配给管家的 MCP 连接（SWARM_NODE_PRIME_TOKEN）');
}
async function copyPrimeMcp() {
  const d = S.primeDecl.done;
  if (!d?.mcp) { toast('没有可复制的 MCP 配置'); return; }
  /* W02-01 解耦后 mcp_config 凭据位恒为脱敏占位（wired=宿主内已是当前凭据，片段
   * 仅供迁移其他宿主）——复制出的片段直接贴用会认证失败，toast 必须如实告知须
   * 替换，不得宣称「即完成授权」。 */
  await copyText(JSON.stringify(d.mcp, null, 2), 'MCP 配置', d.wired
    ? '已复制（凭据位为脱敏占位）——本片段用于迁移其他宿主，贴用前须替换为「轮换管家凭证」获取的一次性明文'
    : '已复制（凭据位为脱敏占位）——贴用前须替换为「轮换管家凭证」获取的一次性明文，否则认证失败');
}

/* ---------- 任务详情抽屉 ----------
 * 稳定 DOM（D22 硬验收，SPEC 六.五 第三条）：openDrawer 建壳（role=dialog + 稳定
 * 分区容器，对齐 hub 壳层无障碍基线），此后刷新一律 refreshDrawer → drawerZonesApply
 * 分区 diff 局部写入；details 开合/原文模式/焦点/滚动随未动分区保持，数据无变化
 * 不碰 DOM；迟到请求按抽屉会话序号（drawerSeq）+ 请求序号（drawerReqSeq）丢弃；
 * 入场动画只随壳播放一次。终态停轮询是数据面事实（node 账本终态不可变、review 不
 * 回流），非 DOM 稳定性的替代方案。 */
let drawerSeq = 0;       // 抽屉会话序号：防被关/被换（codex 第三轮复测）
let drawerReqSeq = 0;    // 抽屉内请求序号：同抽屉多次刷新的乱序应答守卫（同上）
async function openDrawer(taskID) {
  closeDrawer();
  if (S.sessDrawer) closeSessionDrawer();   // 同一 #layer 层互斥：会话抽屉让位（closeSessionDrawer 递增序号作废在途应答）
  const seq = ++drawerSeq;
  const back = document.activeElement && document.activeElement !== document.body ? document.activeElement : null; // 关闭焦点返回（D1 对齐 hub）
  $('#layer').innerHTML = `<div class="overlay drawer-overlay"><section class="drawer" id="drawer" role="dialog" aria-modal="true" aria-labelledby="drawer-title" tabindex="-1">${drawerShellHtml()}</section></div>`;
  layerInert(true);   // A2 键盘基线：背景隔离（任务详情抽屉此前无 inert，键盘可操作被遮页面）
  $('#drawer').focus();
  playDrawerOpen();   // 入场动画只随壳播放一次；分区刷新不重放（D22）
  $('#layer').addEventListener('click', drawerMaskClose); // 具名引用重复注册幂等去重（与 cfg 抽屉遮罩关闭同机制）
  // 来源随抽屉持久化（codex 审查⑤）：开抽屉时刻从列表行捕获并存入 S.drawer.origin
  // ——之后轮询/翻页使该行离开当前列表态也不再丢来源（列表缺失行缺省 local 会把
  // hub 任务误标成本机派单、给出错误产物指引）。open-submitted 直开（本机派单）
  // 与列表暂无该行的兜底为空串，taskOrigin 再逐级回退。
  S.drawer = { id: taskID, origin: S.tasks.find(x => x.task_id === taskID)?.origin || '', timer: null, seq, zones: {}, returnFocus: back };
  await refreshDrawer(seq);
  // 迟到守卫：首刷返回前抽屉被关闭（S.drawer=null）或被更快的新抽屉替换（seq 不符）
  if (!S.drawer || S.drawer.seq !== seq) return;
  S.drawer.timer = setInterval(() => {
    const d = S.drawer;
    if (!d || d.seq !== seq) { clearInterval(S.drawer.timer); return; } // 被替换：自清，防泄漏
    const t = d.task;
    if (S.drawer && (!t || !TERMINAL.has(t.status))) refreshDrawer(seq);
  }, 5000);
}
function drawerMaskClose(e) {
  if (!S.drawer) return;   // 抽屉已关/换成 cfg 抽屉时本监听不动作（免隐式耦合）
  if (e.target.classList.contains('overlay')) closeDrawer();
}
function closeDrawer() {
  /* 关闭为直清、无 hub 的 280ms 出场动画——阶段 D 建议修 8 明确豁免（SPEC 六.五
   * 第六条白名单）：node #layer 是任务/能力配置/通知三抽屉共用的直清家族，
   * openDrawer/openCfgDrawer/openNotifyForm 让位路径要求立即清场，未引入 hub
   * layerGen 世代机制前不做关闭动画；直清同时使 drawerSeq++ 即刻作废在途应答，
   * 不存在 closing 窗口期的迟到应答竞态面。 */
  if (S.drawer?.timer) clearInterval(S.drawer.timer);
  const back = S.drawer?.returnFocus;
  S.drawer = null;
  drawerSeq++;   // 在途 refreshDrawer 应答全部作废
  layerInert(false);   // A2：释放背景隔离
  $('#layer').innerHTML = '';
  /* 关闭返焦（A2 与 hub restoreLayerFocus 同构）：触发元素仍在文档则回它；
   * 失存（轮询重绘使行消失等）回落侧栏当前页签按钮——hub 同兜底，焦点不落 body。 */
  if (back && back.isConnected) back.focus();
  else {
    const tab = document.querySelector('aside nav button.active') || document.querySelector('aside nav button');
    if (tab) tab.focus();
  }
}
// taskOrigin 任务来源判定（功能性修复 7，2026-10-22；2026-10-26 随列表账本全量
// 驱动校准；codex 审查⑤改为抽屉持久化优先）：① 开抽屉时捕获并保存在
// S.drawer.origin 的来源——列表分页/刷新后行可能不在当前列表态，缺失行不再默认
// local（hub 任务误标本机派单会给出错误的产物指引）；② 回退当前列表行 origin；
// ③ 仍无信息（open-submitted 直开 = 本机派单 by construction，或列表暂无该行）
// 缺省 local——本机控制台默认本机语义。
function taskOrigin(taskID) {
  if (S.drawer && S.drawer.id === taskID && S.drawer.origin) return S.drawer.origin;
  const e = S.tasks.find(x => x.task_id === taskID);
  return e && e.origin === 'hub' ? 'hub' : 'local';
}

async function refreshDrawer(seq) {
  const drawer = S.drawer;   // 捕获会话引用：seq 不符（被关/被换）则迟到应答丢弃
  if (!drawer || drawer.seq !== seq) return;
  const reqSeq = ++drawerReqSeq;   // 请求级序号：同抽屉多次刷新，仅最新应答可渲染
  const r = await api('GET', '/api/v1/tasks/' + encodeURIComponent(drawer.id)).catch(() => null);
  if (!S.drawer || S.drawer.seq !== seq || reqSeq !== drawerReqSeq) return;
  if (!r || r.code !== 200) {
    // 查询失败整层错误壳（白名单三处层容器级重建之一）：重置分区缓存后仍走分区写入
    const root = $('#drawer');
    if (!root) return;
    S.drawer.zones = {};
    drawerZonesApply(root, {
      head: `<div class="row"><h2 id="drawer-title">任务详情 · ${esc(drawer.id)}</h2>${button('×', 'close-drawer', 'close', '', 'aria-label="关闭抽屉"')}</div>`,
      intro: `<div class="notice warn">任务查询失败${r ? ' · ' + errLine(r.data) : '（网络）'}。任务可能不在本机账本（未在本机执行或已超出账本保留窗口）。</div>
      <p class="footer-note">下一步：回到${button('我的任务', 'close-drawer', 'link')}列表。</p>`,
    }, S.drawer.zones);
    return;
  }
  const t = r.data;
  S.drawer.task = t;
  const root = $('#drawer');
  if (root) drawerZonesApply(root, taskDrawerZones(t), S.drawer.zones);
}

// syncDrawerZones 以当前 S.drawer.task 重算分区并 diff 写入（log-raw 切换等
// 本地状态变化共用此入口；数据无变化时零 DOM 写）。
function syncDrawerZones() {
  const root = $('#drawer');
  if (root && S.drawer?.task) drawerZonesApply(root, taskDrawerZones(S.drawer.task), S.drawer.zones);
}

// taskDrawerZones 任务抽屉分区内容（D22：壳由 drawerShellHtml 建一次，本函数只
// 产出各分区 HTML，经 drawerZonesApply 与缓存 diff 后局部写入）。区块次序为
// FE-POLISH 轮2 H13 五段化（与 hub 同序，SPEC 六.五 第二条）：①我让它做什么
//（prompt）→ ②当前事实与下一步（状态介绍/核对指引+失败时「回表单重新派单」行动
// 按钮，intro）→ ③结果与可取文件（最终答复+产物，answer/artifacts）→ ④异常核查
//（fail/review 两分区为 hub 独有白名单，node 恒空零占位）→ ⑤折叠证据（段头身份行
// trace/提交人/创建/恢复密钥/来源 + 诊断日志/时间线 details 默认收起，evidence/
// logs/timeline）→ 归档/下一步（next）。头部只留标题与 agent@node+状态徽标，
// 身份技术态沉到证据段；来源标注随行（node 白名单：产物本机路径指引的判定依据）。
// 头部/文案/时间口径与 hub 同源（D2/D17/D18/D21）。
function taskDrawerZones(t) {
  const res = t.result;
  // 双路径语义（功能性修复 7）：本机任务（origin=local，本机账本真相源）产物区
  // 从本机账本直出——产物文件就在本机 workdir/outputs 下，展示本机绝对路径；
  // 仅 hub 下发任务保留「经 hub 取回」指引。
  const local = taskOrigin(t.id) === 'local';
  const agent = S.agents.find(x => x.name === t.target_agent);
  const absPath = a => (agent?.workdir ? agent.workdir.replace(/\/+$/, '') + '/' : '') + a.path;
  // 产物说明段仅在有产物时计算（第六轮修复 3）：无产物完成/运行中/needs_review 的
  // res.artifacts 为 null/空数组——原实现无条件读 res.artifacts[0] 直接 TypeError，
  // 抽屉卡「加载中」；空串不渲染。
  const artifactNote = !res?.artifacts?.length ? '' : local
    ? `<p class="footer-note">产物在本机：<code>${esc(absPath(res.artifacts[0]))}</code>${
        res.artifacts.length > 1 ? `（共 ${res.artifacts.length} 个，均在本机 outputs 目录）` : '（workdir/outputs 下，本机直接打开即可，无需经 hub 取回）'}</p>`
    : `<p class="footer-note">产物内容保留在执行机本机；经 hub 管理面 <code>GET /api/v1/tasks/{id}/artifacts/{index}</code> 取回。</p>`;
  // 状态介绍条（D4 统一：五态人话双端同有；needs_review 由 D18 主文案+D19 如实
  // 指引的 reviewNote 承担，不再叠第二条告警）。
  const statusIntro = t.status === 'needs_review' ? ''
    : t.status === 'completed' ? '任务已完成：可阅读最终答复并下载产物文件。'
    : t.status === 'failed' ? '本次执行未完成：先看下方说明，再决定是否重新派任务。'
    : t.status === 'cancelled' ? '已登记取消，执行机停止情况待确认：hub 已发出停止指令（离线机重连后补发），不会自动重跑。'
    : '任务仍在进行，抽屉每 5 秒自动刷新。';
  // D19 文案如实修正（阶段 B 裁决）：node 无核对登记端点（localapi 未提供），
  // 旧文案「核对结论由本机主人在本控制台确认」承诺了不存在的能力——改为如实
  // 指路 hub；主文案与 hub 版逐字同源（D18）。
  const reviewNote = `<div class="notice warn"><b>这个任务结果拿不准，等你确认：执行到一半机器重启了，请核对后登记结论，不要重新提交同一任务。</b>${
    local ? '<br>本控制台暂不支持登记核对结论，请人工核查实际结果，并到 hub 管理台登记。' : '<br>核对结论请到 hub 管理台登记。'}</div>`;
  return {
    head: `<div class="row"><h2 id="drawer-title">任务详情 · ${esc(t.id)}</h2>${button('×', 'close-drawer', 'close', '', 'aria-label="关闭抽屉"')}</div>
    <div class="row"><h2>${esc(t.target_agent)} @ ${esc(t.target_node)} ${badge(t.status)}</h2></div>`,
    intro: `${t.status === 'needs_review' ? reviewNote : `<div class="notice">${esc(statusIntro)}</div>`}
    ${taskErrorNotice(res, t.status === 'needs_review')}
    ${t.status === 'failed' ? `<div class="actions spaced">${button('回表单重新派单', 'reopen-form', 'link')}</div>` : ''}`,
    prompt: promptZoneHtml(t),
    answer: drawerAnswerHtml(t, res, t.status === 'needs_review'),
    artifacts: `<h3>产物文件</h3>${artifactsHtml(t)}${artifactNote}`,
    evidence: `<h3 class="spaced">证据与记录</h3>
    <p class="muted small">追踪 <code>${esc(t.trace_id || '—')}</code> · ${esc(t.submitted_by || '—')} 提交 · 创建于 ${esc(fmtUTC(t.created_at) || '—')} · 恢复密钥 ${esc(t.idem || '—')} · 来源 ${ORIGIN_WORDS[local ? 'local' : 'hub']}</p>
    <p class="muted small">下方完整证据（诊断日志 / 时间线）默认折叠——普通成功任务不用展开；需要核对异常时再打开。</p>`,
    logs: res && (res.stdout_tail || res.stderr_tail) ? `<details><summary>诊断日志（stdout / stderr 尾部）</summary><div id="log-wrap">${drawerLogInner(res, !!S.drawer?.logRaw)}</div><small>最多保留各 64KiB。</small></details>` : '',
    timeline: `<details class="spaced"><summary>时间线（受理 / 执行 / 终态事件）</summary>${drawerTimelineHtml(t)}</details>`,
    next: TERMINAL.has(t.status) && t.status !== 'needs_review'
      ? `<div class="notice">本机账本保留此次记录（已确认结果超保留期自动清理：默认 24 小时，node.yaml terminal_retention 可配；0s=永不清理）。${button('查看全部任务 →', 'close-drawer', 'link')}</div>`
      : '',
  };
}

// artifactsHtml 产物区（D13/D14 统一：恒渲染 + 空态文案双端同文；标题「产物文件」
// 同文。H15（FE-POLISH 轮2）：前置文件名+易读大小（fmtSize shared）；完整路径/
// 原始字节/SHA 收进行内 details。行内形态差异为 SPEC 六.五 白名单——node 本机
// 绝对路径指引进详情折叠，无取回按钮（本机直接打开）。）
function artifactsHtml(t) {
  const arts = t.result?.artifacts || [];
  if (!arts.length) return '<p class="muted">暂无已登记产物。中断任务不推断存在可交付产物。</p>';
  const local = taskOrigin(t.id) === 'local';
  const agent = S.agents.find(x => x.name === t.target_agent);
  const absPath = a => (agent?.workdir ? agent.workdir.replace(/\/+$/, '') + '/' : '') + a.path;
  return arts.map(a => `<div class="artifact"><b>${esc(String(a.path).split('/').pop() || a.path)}</b> <small class="artifact-size">${fmtSize(a.size)}</small>
  <details class="artifact-detail"><summary>完整路径与校验</summary><small>路径 <code>${esc(a.path)}</code>${local && agent?.workdir ? `<br>本机绝对路径：<code>${esc(absPath(a))}</code>` : ''}<br>sha256 <code>${esc(String(a.sha256 || '—'))}</code> · 原始 ${a.size} 字节</small></details></div>`).join('');
}

/* ---------- 复制诊断信息 ---------- */
async function copyDiagnostics() {
  const text = JSON.stringify({ node_id: S.nodeID, copied_at: new Date().toISOString(), ...S.status }, null, 2);
  await copyText(text, '诊断信息', '诊断信息已复制');
}

/* ---------- toast（FE-POLISH B3，2026-10-29：移植 hub .show 双向过渡） ----------
 * hidden 属性退役（[hidden]{display:none} 杀死过渡）；出现/消失走 .show 类，
 * 单实例 clearTimeout 与 hub toast() 同机制；停留时长 3200→3400 对齐 hub
 *（FE-POLISH 轮2 A1-1：同反馈同时长；底部黑条与位置属 SPEC 六.五白名单差异不动）；
 * reduced-motion 降级见 node style.css 媒体查询。 */
let toastTimer = null;
function toast(msg) {
  const el = $('#toast');
  el.textContent = msg;
  el.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.classList.remove('show'); }, 3400);
}
/* busyBtn/idleBtn 写动作进行时样板（FE-POLISH B6，与 hub 同实现）：原文案记忆在
 * dataset.label，恢复取记忆值；锁宽防抖；.loading 保持可读。spinner 复用 .spin
 * （node style.css 本批补齐定义）。 */
function busyBtn(btn, label) {
  if (!btn) return;
  btn.dataset.label = btn.textContent;
  btn.style.minWidth = btn.offsetWidth + 'px';
  btn.disabled = true;
  btn.classList.add('loading');
  btn.innerHTML = '<span class="spin"></span>' + esc(label);
}
function idleBtn(btn) {
  if (!btn) return;
  btn.disabled = false;
  btn.classList.remove('loading');
  if (btn.dataset.label) btn.textContent = btn.dataset.label;
  delete btn.dataset.label;
  btn.style.minWidth = '';
}
/* copyText 复制助手（FE-POLISH 轮2 A1-18，对齐 hub 手动复制弹层）：剪贴板被拒绝
 * 时不再只 toast（提示消失即无处复制）——落手动复制弹层（#layer 居中 modal，与
 * 通知编辑同家族），敏感值只在当前会话内展示、不落存储。 */
async function copyText(value, label, okMsg) {
  try {
    await navigator.clipboard.writeText(value);
    toast(okMsg || label + '已复制');
  } catch {
    openPlainModal(`${modalHead('手动复制' + label)}<p class="muted">浏览器未允许剪贴板访问，可选中以下内容复制。</p><textarea readonly rows="10">${esc(value)}</textarea>${button('完成，返回 →', 'copy-modal-close', 'link')}`);
  }
}
/* openPlainModal 普通弹层（node #layer 直清家族）：居中 modal 无障碍基线与通知
 * 编辑弹层同构（role/aria/focus/入场）；层间互斥沿用既有让位语义——开层即替换
 * #layer 内容（copyText 失败兜底单用途，让位可接受）。P2-03：记录触发元素，
 * 关层返焦（与任务/cfg/通知关闭同机制，Esc/完成/遮罩三入口同经 closeCopyModal）。 */
let copyReturnFocus = null;
function openPlainModal(content) {
  if (S.drawer) closeDrawer();       // 同一 #layer 互斥：任务详情抽屉让位
  if (cfgEdit) closeCfgDrawer(true); // 能力配置抽屉让位（还原快照）
  const a = document.activeElement;
  copyReturnFocus = a && a !== document.body ? a : null;
  $('#layer').innerHTML = `<div class="overlay"><section class="modal" id="copy-modal" role="dialog" aria-modal="true" aria-labelledby="layer-title" tabindex="-1">${content}</section></div>`;
  layerInert(true);
  $('#layer [role=dialog]')?.focus();
  playDrawerOpen();
}
/* ---------- 确认弹层（FE-POLISH B5，2026-10-29：window.confirm 全量退役） ----------
 * hub slim modal 同形态（居中 modal + danger 描边确认键 + modalHead 同词 ×）。
 * confirmModal(title, bodyHtml, okLabel, onOk)：onOk 在用户确认后执行（闭包自带
 * 上下文），取消/遮罩/× 即弃（confirmCb 置空）。可逆操作不进此层（不做清单 16）。
 * P1-02（2026-10-29）：能力配置抽屉不再让位——确认层作为 #layer 第二个子 overlay
 * 叠加（同 z-index DOM 序后者在上），取消只摘确认层，抽屉草稿/焦点原样保留；
 * 先关抽屉会经 closeCfgDrawer(true) 用快照覆盖未保存草稿（原 window.confirm 取消
 * 语义是保留编辑）。遮罩点击在确认层自身监听并 stopPropagation：不冒泡到 #layer
 * 触发下层 cfgMaskClose。任务详情抽屉（只读视图）让位不变。 */
let confirmCb = null;
let confirmReturnFocus = null;   // 确认层弹出前的焦点元素，取消后还给该元素
let confirmInertEls = [];        // 确认层存续期间被设 inert 的下层容器（R2-P2-01）

/* ---------- 弹层键盘基线（FE-POLISH 轮2 A2，任务/能力配置抽屉+通知/复制弹层） ----------
 * 与 hub 层生命周期键盘闭环同构（codex I01 漂移项收口）：开层时页面主体 inert
 *（背景移出 Tab 序与指针面）、Tab 陷阱（含 R3-P2-01 容器获焦分支）、Esc 关最上层、
 * 关闭返焦。层间互斥（#layer 直清家族）+ 确认层可叠加（R2-P2-01 机制不变）；
 * Esc 次序=确认层 > 通知/复制弹层 > 能力配置抽屉 > 任务抽屉。选择器口径与 hub
 * layerFocusables 同文（两端各自实现不入 shared block，改一处须核对另一处）。 */
function layerInert(on) {
  for (const el of [document.getElementById('app'), document.querySelector('.topbar')]) {
    if (!el) continue;
    if (on) el.setAttribute('inert', ''); else el.removeAttribute('inert');
  }
}
/* topOverlay 当前最上层 overlay（确认层叠加时取末位——DOM 序后者在上）。 */
function topOverlay() {
  const ovs = [...$('#layer').children].filter(el => el.classList.contains('overlay'));
  return ovs[ovs.length - 1] || null;
}
function nodeLayerFocusables() {
  const ov = topOverlay();
  if (!ov) return { ov: null, items: [] };
  const items = [...ov.querySelectorAll('a[href],button:not([disabled]),input:not([disabled]):not([type=hidden]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])')]
    .filter(el => el.offsetParent !== null);
  return { ov, items };
}
function closeCopyModal() {
  if (!$('#copy-modal')) return;
  layerInert(false);
  $('#layer').innerHTML = '';
  /* P2-03 关闭返焦（A2 同构）：触发元素仍在文档则回它；失存回落侧栏当前页签
   *（与任务/cfg/通知/确认关闭同一兜底），焦点不落 body。 */
  const back = copyReturnFocus;
  copyReturnFocus = null;
  if (back?.isConnected) back.focus();
  else {
    const tab = document.querySelector('aside nav button.active') || document.querySelector('aside nav button');
    if (tab) tab.focus();
  }
}
/* confirmSetInert 确认层键盘隔离（R2-P2-01，2026-10-29）：确认层是视觉模态，键盘
 * 不得操作被遮住的下层（此前 Shift+Tab 可落回下层「保存」钮，Enter 触发保存）。
 * 与 hub openLayerPlain 同策略（层开时 #app/顶栏 inert）：面向页面主体与 #layer 内
 * 先于确认层的旧 overlay（cfg 抽屉叠层等），确认层自身不 inert。关闭（确认或取消）
 * 先解除再返焦——inert 元素 focus() 静默失败，顺序不能反。 */
function confirmSetInert(on) {
  if (on) {
    const cm = $('#confirm-modal');
    confirmInertEls = [document.getElementById('app'), document.querySelector('.topbar'),
      ...[...$('#layer').children].filter(el => !el.contains(cm))].filter(Boolean);
    for (const el of confirmInertEls) el.setAttribute('inert', '');
  } else {
    for (const el of confirmInertEls) el.removeAttribute('inert');
    confirmInertEls = [];
  }
}
function confirmModal(title, bodyHtml, okLabel, onOk) {
  if (S.drawer) closeDrawer();       // 任务详情抽屉让位（只读视图，无草稿）
  confirmCb = onOk;
  const a = document.activeElement;
  confirmReturnFocus = a && a !== document.body ? a : null;
  const ov = document.createElement('div');
  ov.className = 'overlay';
  ov.innerHTML = `<section class="modal slim" id="confirm-modal" role="dialog" aria-modal="true" aria-labelledby="layer-title" tabindex="-1">${modalHead(title)}
  <div class="notice warn">${bodyHtml}</div>
  <div class="actions spaced"><button type="button" class="danger" data-act="confirm-ok">${esc(okLabel)}</button>${button('取消', 'close', 'link')}</div></section>`;
  ov.addEventListener('click', e => {
    if (e.target !== ov) return;     // 层内点击交给文档级委托；遮罩点击才归此层
    e.stopPropagation();             // 不上抛 #layer：下层抽屉的 cfgMaskClose 不误触发
    closeConfirm();
  });
  $('#layer').appendChild(ov);
  confirmSetInert(true);
  ov.querySelector('[role=dialog]')?.focus();
  void ov.offsetWidth;               // 先落初始帧再放行 .open，入场过渡可播（playDrawerOpen 同口径）
  requestAnimationFrame(() => ov.classList.add('open'));
}
function closeConfirm() {
  confirmCb = null;
  $('#confirm-modal')?.closest('.overlay')?.remove();   // P1-01：先移除当前确认层——剩余 overlay 判定不得把确认层自身算进去
  confirmSetInert(false);            // 再解除下层 inert（inert 内 focus() 无效，先解除）
  /* A2：确认层关闭后**剩余**下层抽屉/弹层若仍在场，恢复其背景 inert——confirmSetInert
   * 把 app/顶栏与下层 overlay 一并解除，下层还开着时主体必须补回隔离。判定必须在
   * 移除确认层之后：此前先判后删，确认层自身必然在场，页面+确认层场景（无下层）
   * 也误判「有 overlay」→ 页面恒 inert、焦点落 body，通知渠道删除/钦定/凭证轮换/
   * 重载配置等页面确认入口关闭后整页锁死。页面+确认层场景关闭即解除隔离，按钮可点、
   * Tab 可达；cfg 抽屉+确认层场景抽屉隔离保持、背景仍 inert。 */
  if ($('#layer')?.querySelector('.overlay')) layerInert(true);
  const back = confirmReturnFocus;
  confirmReturnFocus = null;
  if (back?.isConnected) back.focus();   // 取消回弹层前焦点（抽屉字段或页面触发钮）
  else {   // P1-01：触发元素失存（轮询重绘使行消失等）回落侧栏当前页签——与任务/cfg/通知关闭同构兜底，焦点不落 body
    const tab = document.querySelector('aside nav button.active') || document.querySelector('aside nav button');
    if (tab) tab.focus();
  }
}

/* ---------- 事件委托 ---------- */
document.addEventListener('click', e => {
  /* 复制兜底弹层遮罩关闭（A1-18）：与通知弹层遮罩同语义（点遮罩=取消）。 */
  if (e.target.classList && e.target.classList.contains('overlay') && $('#copy-modal')) { closeCopyModal(); return; }
  /* 会话表单 modal 遮罩关闭（W01-05）：点遮罩=关闭，草稿留 S.sess 重开恢复。 */
  if (e.target.classList && e.target.classList.contains('overlay') && $('#sess-modal')) { closeSessModal(); return; }
  const btn = e.target.closest('button');
  const tr = e.target.closest('tr[data-task]');
  if (btn) {
    if (btn.dataset.go) { location.hash = '#/' + btn.dataset.go; return; } // nav 导航按钮（data-go）
    const [act, arg] = (btn.dataset.act || '').split(':');
    switch (act) {
      case 'gate-enter': tryToken($('#gate-token').value.trim()); return;
      case 'go': location.hash = '#/' + arg; return;
      case 'go-tasks': S.focusForm = true; location.hash = '#/tasks'; route(); if (S.page === 'tasks') focusForm(); return;
      case 'go-overview': location.hash = '#/overview'; return;
      case 'go-services': location.hash = '#/services'; return;
      case 'focus-form': focusForm(); return;
      case 'copy-diag': copyDiagnostics(); return;
      case 'wiz-preview': wizPreview(); return;
      case 'wiz-apply': wizApply(); return;
      case 'prime-rotate': primeRotate(btn); return;
      case 'prime-pick': S.primeDecl.done = null; S.primeDecl.picking = true; render(); return;
      case 'prime-pick-cancel': S.primeDecl.picking = false; render(); return;
      case 'prime-declare': primeDeclare(btn); return;
      case 'prime-retry-orch': retryPrimeDeclare(btn); return;
      case 'net-disconnect': netDisconnect(btn); return;
      /* P08 决策设置（W06-03）：三区深链 + MCP 编排授权启停/重读 */
      case 'goto': gotoAnchor(arg); return;
      case 'mcp-toggle': mcpAccessToggle(btn); return;
      case 'mcp-reload': loadMcpAccess(); return;
      /* ---------- MCP 接入子页（W03-04 P07）：六分区 + 配置弹层 + 诊断抽屉 ---------- */
      case 'go-mcp': location.hash = '#/mcp'; return;
      case 'mcp-cfg-open': openMcpCfgModal(); return;
      case 'mcp-modal-close': closeMcpModal(true); return;
      case 'mcp-preview': mcpPreview(btn); return;
      case 'mcp-apply': mcpApply(btn); return;
      case 'mcp-suggest-name': mcpSuggestName(); return;
      case 'mcp-revert': mcpRevert(btn); return;
      case 'mcp-copy-snippet': mcpCopySnippet(); return;
      case 'mcp-download-snippet': mcpDownloadSnippet(); return;
      case 'mcp-board-reload': mcpBoardLoad(); return;
      case 'mcp-v0': mcpRunV0(btn); return;
      case 'mcp-v2': mcpRunV2(btn); return;
      case 'mcp-confirm': mcpConfirmLayer(arg); return;
      case 'mcp-drawer': openMcpDrawer(arg); return;
      case 'mcp-drawer-close': closeMcpDrawer(); return;
      case 'mcp-copy-prompt-v4': copyText(MCP_PROMPT_V4, 'V4 提示词', 'V4 提示词已复制（零副作用：未发起任何验证）'); return;
      case 'mcp-copy-prompt-v5': copyText(MCP_PROMPT_V5, 'V5 提示词', 'V5 提示词已复制（零副作用：未发起任何验证或任务）'); return;
      case 'copy-prime-token': copyPrimeToken(); return;
      case 'copy-prime-declare': copyPrimeDeclare(); return;
      case 'copy-prime-mcp': copyPrimeMcp(); return;
      case 'cfg-load': loadCfg(); return;
      case 'cfg-open': openCfgDrawer(parseInt(arg, 10)); return;
      case 'cfg-close': case 'cfg-cancel': closeCfgDrawer(true); return; // 取消语义：还原快照
      case 'cfg-save-row': cfgDrawerSave(btn); return;
      case 'cfg-add': {
        S.cfg.agents.push({ name: '', kind: 'cli', command: '', workdir: '', agent_args: [],
          timeout_sec: 600, allow_args: false, allow_env: [], config_home: '',
          description: '', permission_mode: '', exposed: true });
        render(); return;
      }
      case 'cfg-del': {
        if (cfgSaving) return; // 保存 PUT 在途禁删：splice 会让 cfgEdit.idx 错位，抽屉续改将写串行
        const di = parseInt(arg, 10);
        if (!Number.isNaN(di) && S.cfg.agents[di]) {
          confirmModal('删除 agent · ' + esc(S.cfg.agents[di].name || '（未命名）'),
            `从能力配置移除「${esc(S.cfg.agents[di].name || '（未命名）')}」；保存后写入 node.yaml（结构性变更需重启生效）。`, '确认删除', () => {
              S.cfg.agents.splice(di, 1);
              if (cfgEdit) closeCfgDrawer(false); // 抽屉内删除：行已移除即关抽屉（closeCfgDrawer 内含 render）
              else render();
            });
        }
        return;
      }
      case 'cfg-save': cfgSave(btn); return;
      /* ---------- 本机 agent 发现与登记预览（W06-02） ---------- */
      case 'discover-open': openDiscoverModal(); return;
      case 'disc-run': discRun(btn); return;
      case 'disc-clear': if (disc) { disc.results = []; disc.note = ''; disc.err = ''; paintDiscoverModal(); } return;
      case 'disc-preview': openPreviewModal(parseInt(arg, 10)); return;
      case 'prev-refresh': prevRefresh(btn); return;
      case 'prev-back': closePreviewModal(true); return;
      case 'prev-confirm': prevConfirm(); return;
      case 'notify-add': openNotifyForm(null); return;
      case 'notify-retry': loadNotifyChannels(); return;
      case 'notify-edit': {
        const ch = S.notifyChannels.find(x => x.id === arg);
        if (ch) openNotifyForm(ch);
        return;
      }
      case 'notify-save': saveNotifyChannel(); return;
      case 'confirm-ok': {   // 确认弹层（B5）：先关层再执行回调——回调可能开新层/重绘
        const cb = confirmCb;
        closeConfirm();
        if (cb) {
          /* P1-01：确认路径的回调多为异步写动作，完成后 render 重建 #app 会把
           * closeConfirm 刚返还的焦点冲到 body——动作落定后若焦点无处安放，按 A2
           * 同款兜底回侧栏当前页签（回调自会管理焦点时此兜底不动作）。 */
          Promise.resolve(cb()).catch(() => {}).finally(() => {
            if (!document.activeElement || document.activeElement === document.body) {
              const tab = document.querySelector('aside nav button.active') || document.querySelector('aside nav button');
              if (tab) tab.focus();
            }
          });
        }
        return;
      }
      case 'close':   // shared modalHead ×/取消 同词关闭动作（与 hub 同词，notify-polish 改动三）；守护防误清其他层
        if ($('#prev-modal')) closePreviewModal(true);   // 预览层 × = 返回发现结果（无发现会话则全关）
        else if ($('#disc-modal')) closeDiscoverModal();
        else if ($('#mcp-modal')) closeMcpModal(true);   // MCP 配置弹层（W03-04）
        else if ($('#mcp-drawer')) closeMcpDrawer();     // MCP 诊断抽屉（W03-04）
        else if ($('#notify-modal')) closeNotifyForm();
        else if ($('#confirm-modal')) closeConfirm();
        else if ($('#copy-modal')) closeCopyModal();
        else if ($('#sess-modal')) closeSessModal();   // 会话表单 modal（W01-05；Esc/×=关闭不弃草稿）
        return;
      case 'copy-modal-close': closeCopyModal(); return;
      case 'notify-toggle': toggleNotifyChannel(arg, btn); return;
      case 'notify-test': testNotifyChannel(arg, btn); return;
      case 'notify-del': deleteNotifyChannel(arg, btn.dataset.name || arg); return;
      case 'cfg-reload': {
        confirmModal('从文件重载', '放弃当前编辑，从 node.yaml 重新读取（未保存的修改将丢失）。', '放弃并重载', () => cfgReload(btn));
        return;
      }
      case 'cfg-restart': {
        confirmModal('应用并重启 node', '在途任务将被中断并进入「等你确认」。', '确认重启', () => cfgRestart(btn));
        return;
      }
      case 'logout':
        localStorage.removeItem(TOKEN_KEY); S.token = ''; S.authed = false; S.snap = '';
        resetPrimeState(); // 修复 5：登出清一次性凭证
        render(); return;
      case 'expose-toggle': {
        const a = S.agents.find(x => x.name === arg);
        setExposed(arg, !(a && a.exposed), btn); return;
      }
      case 'orch-off': setOrchestrator(arg, false, btn); return;
      case 'form-submit': submitForm(); return;
      case 'form-retry': // 新幂等键开新单
        S.f = { stage: 'idle', target: S.f.target, stdin: S.f.stdin, idem: newIdem(), message: '' };
        render(); focusForm(); return;
      case 'form-resend': submitForm(); return; // 同键重试
      case 'form-abandon':
        S.f = { stage: 'idle', target: S.f.target, stdin: S.f.stdin, idem: newIdem(), message: '' };
        toast('已放弃 · 恢复密钥作废，不会再提交这一单'); render(); return;
      case 'open-submitted': {
        const id = S.f.taskId; if (id) openDrawer(id); return;
      }
      case 'open-task': openDrawer(arg); return;
      case 'tasks-more': loadMoreTasks(); return;
      case 'reload-all': {   // A4：列表加载失败就地重试
        (async () => {
          try { await loadAll(); } catch (e) { if (!e.unauthorized) S.loadError = '本机 API 暂不可达'; }
          render();
        })();
        return;
      }
      case 'tf-origin': S.tf.origin = btn.dataset.origin || ''; render(); return;   // 来源筛选 chip（A1；零命中空态的清除同源）
      case 'tf-clear': S.tf = { origin: '' }; render(); return;
      case 'reopen-form': closeDrawer(); route(); focusForm(); return;
      case 'close-drawer': closeDrawer(); return;
      case 'log-raw': {   // 诊断日志 查看原文/查看渲染 切换：状态落 S.drawer 后分区重算
        if (!S.drawer?.task?.result) return;
        S.drawer.logRaw = !S.drawer.logRaw;
        syncDrawerZones();   // 仅 logs 分区内容变化：缓存 diff 后只重写该分区，details 开合经 drawerZoneSwap 回放
        return;
      }
      case 'prompt-raw': {   // 任务指令 查看原文/查看渲染 切换（2026-10-26 改动四，与 hub 同机制）
        if (!S.drawer?.task) return;
        S.drawer.promptRaw = !S.drawer.promptRaw;
        syncDrawerZones();   // 仅 prompt 分区内容变化：缓存 diff 后只重写该分区
        return;
      }
      case 'prompt-expand': {   // 任务指令 预览折叠 展开/收起（t20261026-prompt-preview，与 hub 同机制：展开态落 S.drawer 后分区重算）
        if (!S.drawer?.task) return;
        S.drawer.promptExpanded = !S.drawer.promptExpanded;
        syncDrawerZones();   // 仅 prompt 分区内容变化：缓存 diff 后只重写该分区
        return;
      }
      /* ---------- 会话（W01-05）：列表/详情页/modal/取消/关闭动作 ---------- */
      case 'sess-submit': submitSess(); return;
      case 'sess-retry': S.sess.stage = 'idle'; submitSess(S.sess.snap); return;   // 同参重试：先落 idle 过 submitSess 守卫，携提交参数快照重入（R1 必修4 + 同一请求 ID 幂等兜底）
      case 'sess-abandon': S.sess.stage = 'idle'; S.sess.message = ''; toast('已放弃等待 · 表单解锁'); syncSessModal(); return;
      /* sess-new 新建入口（页头/空态 CTA）：草稿保护——未受理的新建草稿原样带回
       * modal；acked 态再点视为开新一轮（fresh）。 */
      case 'sess-new': if (S.sess.stage === 'acked') S.sess = freshSessForm(); openSessModal(); return;
      case 'sess-again': S.sess = freshSessForm(); syncSessModal(); syncSessModalHead(); requestAnimationFrame(() => $('#sess-title')?.focus()); return;
      case 'sess-switch-new': S.sess = freshSessForm(); syncSessModal(); syncSessModalHead(); requestAnimationFrame(() => $('#sess-title')?.focus()); return;
      case 'sess-reload': loadSessions().then(paintSessionsView).catch(() => { }); return;
      case 'sess-chip': {
        const c = btn.dataset.chip || '';
        S.sessFilter = c !== '' && S.sessFilter === c ? '' : c;   // 终审必修3：选中/再点复位（点同值回全部）
        paintSessionsView();
        return;
      }
      case 'sess-clear-filter': S.sessFilter = ''; S.sessQuery = ''; const q = $('#sess-query'); if (q) q.value = ''; paintSessionsView(); toast('已清除筛选'); return;
      case 'sess-back': location.hash = '#/sessions'; return;
      case 'sess-open': closeSessModal(); goSessionDetail(arg); return;   // 旧入口兼容跳转（受理回执/列表行查看 → 详情页）
      case 'sess-continue': sessContinue(arg); return;
      case 'close-sess-drawer': closeSessionDrawer(); return;
      case 'close-sess-modal': closeSessModal(); return;
      case 'sess-turn-detail': openSessTurnDrawer(parseInt(btn.dataset.seq, 10)); return;
      case 'sess-turn-cancel': {
        /* 轮次取消确认（confirmModal 追加式叠加层；W01-03 人工专属语义——回执三态
         * 如实，unknown 待核对绝不虚报已停止）。目标身份取 data-id/data-seq（act
         * 词无冒号参数——split(':') 的 arg 为空，不得用作会话 ID）。 */
        const id = btn.dataset.id, seq = parseInt(btn.dataset.seq, 10);
        confirmModal('取消第 ' + seq + ' 轮 · ' + sessIdShort(id), '取消是终局干预：排队轮将被确定性撤销（不再执行）；执行中的轮次将被终止（进程组整组回收）。取消结果按证据如实记账——停止事实未证实时该轮标记「待核对」，绝不虚报已停止。已留痕的轮次与答复继续可查。', '确认取消', async () => {
          const r = await api('POST', `/api/v1/sessions/${encodeURIComponent(id)}/turns/${seq}/cancel`, {}).catch(() => null);
          if (r && (r.code === 200 || r.code === 202)) {
            const c = r.data?.cancel || {};
            toast(r.code === 202 ? '取消任务已投递：结果随详情自动刷新收敛'
              : c.outcome === 'queued' ? '已撤销排队轮：本轮未执行（确定性终止）'
              : c.outcome === 'executed' ? '已终止执行中的轮次：进程组已整组回收'
              : c.outcome === 'unknown' ? '取消已触发，停止事实未证实：该轮标记待核对——请核对本机实际状态'
              : '取消回执已收到');
          } else if (r && r.code === 409) toast('取消未生效：' + sessErrHuman(r.data));
          else toast(r ? '取消失败：' + sessErrHuman(r.data) : '取消结果未知 · 网络异常：取消意图可能已生效，请稍后查询核对，勿盲目重试');
          refreshSessionsAfterWrite();
        });
        return;
      }
      case 'sess-close': {
        /* 关闭确认（confirmModal 追加式叠加层，hub slim modal 为白名单形态差异）：
         * 详情页/执行明细抽屉之上叠加，取消即回原视图；确认后按 ID 刷新。
         * W01-03 close_in_flight：在途轮存在时服务端 409，人话指向取消/等待。 */
        const id = arg;
        const name = (S.sessPage && S.sessPage.id === id ? S.sessPage.session?.agent_name : null) || S.sessions.find(x => x.id === id)?.agent_name || '';
        confirmModal('关闭会话 · ' + sessIdShort(id), `关闭${name ? `「${esc(name)}」的` : '此'}会话后不再接受续轮；已留痕的轮次与答复继续可查。此操作不可撤销。会话仍有在途轮次时关闭会被拒绝——请先取消或等待在途轮次完成。`, '确认关闭', async () => {
          const r = await api('POST', '/api/v1/local/sessions/' + encodeURIComponent(id) + '/close', '{}').catch(() => null);
          if (r && r.code === 200) {
            toast(r.data?.dup ? '会话本就是关闭状态（幂等回执，零写入）' : '会话已关闭');
          } else if (r && r.code === 409) toast(sessErrHuman(r.data));
          else toast('关闭失败：' + (r ? sessErrHuman(r.data) : '网络异常'));
          refreshSessionsAfterWrite();
        });
        return;
      }
    }
  }
  if (tr && !e.target.closest('button')) openDrawer(tr.dataset.task);
  const trs = e.target.closest('tr[data-sess]');
  if (trs && !e.target.closest('button')) goSessionDetail(trs.dataset.sess);   // W01-05：行点击进独立详情页（旧 700px 会话抽屉退役）
  const trt = e.target.closest('tr[data-turn]');
  if (trt && S.page === 'sessions' && S.sessId && !e.target.closest('button')) { S.sessPage.sel = parseInt(trt.dataset.turn, 10); syncSessPageZones(); }   // 轮次时间线行：选中→页内答复分区
});
document.addEventListener('input', e => {
  if (e.target.id === 'f-target') { S.f.target = e.target.value; render(); }
  if (e.target.id === 'f-stdin') S.f.stdin = e.target.value;
  if (e.target.id === 'wiz-join') S.wiz.join = e.target.value;
  if (e.target.id === 'sess-title') S.sess.title = e.target.value;   // 会话标题草稿镜像（modal 内；W01-05）
  if (e.target.id === 'sess-cwd') S.sess.cwd = e.target.value;   // 草稿镜像（modal；输入不重绘）
  if (e.target.id === 'sess-prompt') S.sess.prompt = e.target.value;
  if (e.target.id === 'sess-query') { S.sessQuery = e.target.value; paintSessionsView(); }   // P01 搜索：镜像 + 列表/chips 槽位局部刷新（输入框自身不受触碰）
  // 发现候选表单（data-disc）：直写 disc.form 不重绘（保输入焦点），探测时统一提交
  const dk = e.target.dataset?.disc;
  if (dk && disc) { disc.form[dk] = e.target.value; return; }
  // MCP 接入配置表单（data-mcp，W03-04）：直写 mcpModal.draft 不重绘（保输入焦点）；
  // 范围/调用环境变化即失效在途旧预览（预览应答携表单指纹，不符丢弃）。
  const mk = e.target.dataset?.mcp;
  if (mk && mcpModal) { mcpModal.draft[mk] = e.target.value; return; }
  // 登记预览表单（data-prev）：直写 prevReg.draft 不重绘（重出预览后回填不丢输入）
  const pv = e.target.dataset?.prev;
  if (pv && prevReg) {
    if (e.target.type === 'checkbox') { prevReg.draft[pv] = e.target.checked; return; }
    if (pv === 'timeout_sec') { prevReg.draft.timeout_sec = parseInt(e.target.value, 10) || 0; return; }
    if (pv === 'agent_args' || pv === 'allow_env') { prevReg.draft[pv] = e.target.value.split(',').map(x => x.trim()).filter(Boolean); return; }
    prevReg.draft[pv] = e.target.value;
    return;
  }
  // 能力配置编辑（data-cfg="<idx>.<field>"）：直写副本不重绘（保输入焦点），保存时统一提交
  const cfgT = e.target.dataset?.cfg;
  if (cfgT) {
    const [si, field] = cfgT.split('.');
    const a = S.cfg.agents[parseInt(si, 10)];
    if (!a) return;
    if (e.target.type === 'checkbox') { a[field] = e.target.checked; return; }
    if (field === 'timeout_sec') { a.timeout_sec = parseInt(e.target.value, 10) || 0; return; }
    if (field === 'agent_args' || field === 'allow_env') {
      a[field] = e.target.value.split(',').map(x => x.trim()).filter(Boolean);
      return;
    }
    a[field] = e.target.value;
  }
});
document.addEventListener('keydown', e => {
  /* A2 键盘基线（FE-POLISH 轮2）：Esc 只关最上层——确认层 > 通知弹层 > 复制弹层
   * > 能力配置抽屉（Esc=取消语义，还原快照）> 任务详情抽屉。此前仅确认层响应
   * Esc，抽屉键盘可绕到被遮页面。 */
  if (e.key === 'Escape') {
    if ($('#confirm-modal')) { e.preventDefault(); closeConfirm(); return; }
    if ($('#prev-modal')) { e.preventDefault(); closePreviewModal(true); return; }
    if ($('#disc-modal')) { e.preventDefault(); closeDiscoverModal(); return; }
    if ($('#mcp-modal')) { e.preventDefault(); closeMcpModal(true); return; }   // MCP 配置弹层（W03-04）
    if ($('#mcp-drawer')) { e.preventDefault(); closeMcpDrawer(); return; }     // MCP 诊断抽屉（W03-04）
    if ($('#notify-modal')) { e.preventDefault(); closeNotifyForm(); return; }
    if ($('#copy-modal')) { e.preventDefault(); closeCopyModal(); return; }
    if (cfgEdit) { e.preventDefault(); closeCfgDrawer(true); return; }
    if ($('#sess-modal')) { e.preventDefault(); closeSessModal(); return; }
    if (S.drawer) { e.preventDefault(); closeDrawer(); return; }
    if (S.sessDrawer) { e.preventDefault(); closeSessionDrawer(); return; }
    return;
  }
  /* Tab 焦点循环：确认层分支沿用 R2-P2-01/R3-P2-01 机制；其余层（任务/能力配置
   * 抽屉、通知/复制弹层）走同款陷阱——inert 已把背景移出 Tab 序，焦点意外落在
   * 层外或层内非 items 节点（开层初始焦点=tabindex=-1 容器）一律消费按键入层，
   * items 上首尾循环。选择器口径与 hub layerFocusables 同文。 */
  if (e.key === 'Tab' && $('#layer')?.querySelector('.overlay')) {
    const { ov, items } = nodeLayerFocusables();
    if (ov && items.length) {
      const first = items[0], last = items[items.length - 1];
      const active = document.activeElement;
      if (!ov.contains(active)) { e.preventDefault(); (e.shiftKey ? last : first).focus(); }
      else if (!items.includes(active)) { e.preventDefault(); (e.shiftKey ? first : last).focus(); }
      else if (!e.shiftKey && active === last) { e.preventDefault(); first.focus(); }
      else if (e.shiftKey && active === first) { e.preventDefault(); last.focus(); }
    }
    return;
  }
  if (e.key === 'Enter' && e.target.id === 'gate-token') tryToken(e.target.value.trim());
  /* A1-10 任务行键盘可达：行有 tabindex 后 Enter/Space 等价点击（与 hub 同分支
   * 语义；此前最近任务/列表行仅点击，键盘不可开抽屉——codex I01 漂移项）。 */
  const tr = e.target.closest?.('tr[data-task]');
  if (tr && e.target === tr && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); openDrawer(tr.dataset.task); }
  /* 会话行/轮次行键盘可达（A1-10 同款）：Enter/Space 等价点击。 */
  const trse = e.target.closest?.('tr[data-sess]');
  if (trse && e.target === trse && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); goSessionDetail(trse.dataset.sess); }
  const trtu = e.target.closest?.('tr[data-turn]');
  if (trtu && e.target === trtu && (e.key === 'Enter' || e.key === ' ')) { e.preventDefault(); if (S.page === 'sessions' && S.sessId && S.sessPage) { S.sessPage.sel = parseInt(trtu.dataset.turn, 10); syncSessPageZones(); } }
});
/* 通知渠道弹层：类型切换只重绘 #nf-hint 与 #nf-type-fields 两槽（shared
 * nfRepaint），名称与事件勾选不被冲掉（局部刷新纪律，与 hub 控制台同机制）；
 * 类型切走格式重置通用。消息格式切换同槽重绘，已输入地址与请求头经
 * nfSecFromDom 回填不丢（t20261026-notify-polish 恒定骨架）。 */
document.addEventListener('change', e => {
  if (e.target.id === 'notify-also') { saveAlsoNotify(e.target); return; }
  if (e.target.id === 'sess-agent') { S.sess.agent = e.target.value; paintSessBindZoneNode(); return; }   // 目标切换只重写绑定摘要区（W01-05：适配器/权限只读声明随能力卡，输入 DOM 不受触碰）
  if (e.target.id === 'nf-type') {
    nfRepaint(e.target.value, 'generic', {});
    return;
  }
  if (e.target.id === 'nf-format') {
    nfRepaint($('#nf-type')?.value || 'webhook', e.target.value, nfSecFromDom());
    return;
  }
  /* MCP 接入配置表单 select 切换（W03-04）：范围/调用环境/宿主变化即失效旧预览
   *（换范围先失效旧响应）并整层重绘（select 切换无输入焦点损失）。 */
  if (e.target.dataset?.mcp === 'scope' && mcpModal) {
    mcpModal.draft.scope = e.target.value;
    if (e.target.value === 'hub' && mcpModal.draft.server_url === 'http://127.0.0.1:8445') mcpModal.draft.server_url = '';
    if (e.target.value === 'node' && !mcpModal.draft.server_url) mcpModal.draft.server_url = 'http://127.0.0.1:8445';
    mcpModal.preview = null; mcpModal.previewErr = ''; mcpModal.applied = null;
    paintMcpModal();
    return;
  }
  if (e.target.dataset?.mcp === 'caller_env' && mcpModal) {
    mcpModal.draft.caller_env = e.target.value;
    mcpModal.preview = null; mcpModal.previewErr = '';
    paintMcpModal();
    return;
  }
  if (e.target.dataset?.mcp === 'host' && mcpModal) {
    mcpModal.draft.host = e.target.value;
    mcpModal.preview = null; mcpModal.previewErr = '';
    paintMcpModal();
    return;
  }
});
$('#logout').addEventListener('click', () => {
  localStorage.removeItem(TOKEN_KEY); S.token = ''; S.authed = false; S.snap = '';
  resetPrimeState(); // 修复 5：登出清一次性凭证
  render();
});
function focusForm() {
  requestAnimationFrame(() => $('#f-stdin')?.focus());
}

/* ---------- 启动 ---------- */
async function boot() {
  /* 顶栏条件发丝线（A1-12，与 hub 同条件边界同机制）：topbar 在 index.html 常驻，
   * 绑一次；passive 只读监听不阻塞滚动线程；初值立即同步（reload 中段刷新场景）。 */
  const topbar = document.querySelector('.topbar');
  if (topbar) {
    const onWinScroll = () => topbar.classList.toggle('scrolled', window.scrollY > 8);
    window.addEventListener('scroll', onWinScroll, { passive: true });
    onWinScroll();
  }
  /* 5s 轮询注册恒定前置（A4 验收矩阵实证缺口）：原注册点在本函数末尾，首访会话
   * （无 localStorage 令牌，走令牌门 tryToken 登录）在 `if (!S.token) return` 早退，
   * 整页轮询从未注册——任务列表/状态永不自动刷新，五态的「失败自动恢复」也不成立。
   * 轮询自身有 S.authed/可见性守卫，未登录时空转，恒注册安全。 */
  setInterval(pollTick, 5000);
  if (!S.token) { render(); return; }
  const epochAtReq = S.sessionEpoch; // 启动验证同样遵守会话边界（迟到 200 不得恢复 authed）
  try {
    await api('GET', '/api/v1/local/status', undefined, { noGate: true });
    if (S.sessionEpoch !== epochAtReq) return;
    S.authed = true;
    await loadAll();
  } catch (e) { /* 401 → 门；网络异常 → 空数据页，轮询会补 */
    if (!e.unauthorized) { S.authed = true; S.loadError = '本机 API 暂不可达'; }   // A4：启动失败落态，列表呈失败态可重试
  }
  route();
}
boot();
