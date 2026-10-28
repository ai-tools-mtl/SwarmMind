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
  primeDecl: { picking: false, busy: false, done: null },  // 网页钦定流程（方案 3.2.1）：picking=选择视图展开；done={agent,token,mcp,wired,target} 钦定完成一次性展示
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
  S.primeDecl = { picking: false, busy: false, done: null };
  S.primeNew = '';
  S.primeNewWire = null;
  S.tf = { origin: '' };   // 来源筛选随会话边界重置（TASK-nav-filter A1 codex 必须修项）
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
  const changed = snap !== S.snap;
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
      S.tasks = S.tasks.concat(r.data?.tasks || []);
      S.tasksTotal = r.data?.total ?? S.tasksTotal;
    }
  } catch (e) {
    if (!e.unauthorized) toast('加载更早任务失败');
  }
  S.tasksMoreBusy = false;
  S.snap = ''; render();
}
async function pollTick() {
  if (!S.authed || document.visibilityState !== 'visible') return;
  try {
    if (await loadAll()) {
      if (cfgEdit) return; // 能力配置编辑抽屉开着：数据照常刷，表格 DOM 不动（编辑会话不受重绘干扰）
      render();
    }
  } catch (e) {
    /* 401 已由 api() 处理；其余落失败态（A4 五态：列表区分「失败可重试」与
     * 「真的没有」，不再静默冒充空列表），下轮轮询自动恢复。 */
    if (!e.unauthorized) S.loadError = '本机 API 暂不可达';
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
const PAGES = [['overview', '总览'], ['services', '服务'], ['tasks', '任务'], ['wizard', '连接']];
// setup 模式只开向导 + 有限本机浏览（方案 3.3：可看本机环境，不能派单）。
const SETUP_PAGES = [['wizard', '连接向导'], ['overview', '本机环境']];
function route() {
  const h = (location.hash || '#/overview').replace(/^#\//, '');
  const allowed = S.authed && S.status?.mode === 'setup' ? SETUP_PAGES : PAGES;
  S.page = allowed.some(p => p[0] === h) ? h : allowed[0][0];
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
  render();
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
function render() {
  $('#conn-chip').hidden = !S.authed;
  $('#logout').hidden = !S.authed;
  $('#who').textContent = S.authed ? 'Bearer 令牌已就绪' : '';
  if (S.authed && S.status?.online != null) {
    const chip = $('#conn-chip');
    chip.className = 'badge ' + (S.status.online ? 'online' : 'offline');
    chip.textContent = S.status.online ? '中心端在线 · 重连 ' + S.status.epoch + ' 次' : 'hub 离线';
  }
  $('#app').innerHTML = !S.authed ? gateView() : layout(pageView());
  if (S.drawer) refreshDrawer(S.drawer.seq); // 第六轮修复 3：抽屉打开时随全局数据刷新重取内容（传 seq：非当前抽屉会话的迟到应答由守卫丢弃）
}
// pageView 按路由出页面（setup 模式下 route() 已把页面收敛到 SETUP_PAGES）。
function pageView() {
  if (S.page === 'wizard') return wizardPage();
  if (S.page === 'services') return servicesPage();
  if (S.page === 'tasks') return tasksPage();
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
      + (st?.mode === 'setup' ? primeCard() : '');
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
  ${primeCard()}${recentTasksCard()}`;
}
/* 本机管家 · 形态（方案 3.1/3.2.1）：网页钦定闭环——未钦定/换管家走选择视图
 * → declare → 钦定完成视图（一次性凭证 + MCP 配置片段）→ 已钦定常态（轮换 +
 * 换管家入口）；setup 模式只提示不开放（矩阵 #9/#18）。全部动态值过 esc()。 */
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
    return `<section class="card spaced"><h2>本机管家 · 形态</h2>
    <div class="mini-stat spaced"><span><b>未钦定</b><small>本机管家</small></span>${modeStat}</div>
    <small class="muted">先完成连接并配置能力后再钦定管家。</small></section>`;
  }
  const head = `<section class="card spaced"><div class="row"><h2>本机管家 · 形态</h2>
    <div class="actions">${st?.mode === 'networked' ? button('断开组网', 'net-disconnect') : ''}</div></div>
  <div class="mini-stat spaced"><span><b>${pa ? esc(pa) : '未钦定'}</b><small>本机管家</small></span>${modeStat}</div>`;
  const newTok = S.primeNew ? `<div class="notice warn spaced"><b>新管家凭证（仅此一次展示，立即复制）</b>：<code>${esc(S.primeNew)}</code> ${button('复制', 'copy-prime-token', 'link')}<br><small>旧凭证已失效；${S.primeNewWire ? `新凭证已自动写入管家的 MCP 配置（<code>${esc(S.primeNewWire.target)}</code>，重连后生效），或「换一个管家」重新走钦定。` : '请立即配给管家的 MCP 连接（SWARM_NODE_PRIME_TOKEN），或「换一个管家」重新走钦定。'}</small></div>` : '';
  if (S.primeDecl.done) return head + primeDoneView() + foot + '</section>';
  if (!pa || S.primeDecl.picking) return head + primePickView(pa) + foot + '</section>';
  return head + `<small class="muted">智行管家经 MCP（prime_token）获得的是<b>本机管家权限</b>：只管本机派单；本机主人（local_token）在网页派单同样放行。钦定/轮换/配置管理等管理操作仅主人可用。</small>
  <div class="row spaced">${button('轮换管家凭证', 'prime-rotate', 'link')}${button('换一个管家', 'prime-pick', 'link')}</div>${newTok}${foot}</section>`;
}
/* 钦定选择视图（未钦定引导态与「换一个管家」共用；数据源本机 agents 列表）。 */
function primePickView(pa) {
  const opts = S.agents.map(a => `<option value="${esc(a.name)}">${esc(a.name)}${a.description ? ' — ' + esc(a.description) : ''}</option>`).join('');
  return `<small class="muted">${pa ? '当前管家将被替换：钦定即换凭证，旧凭证立即失效。' : '钦定一个本机 agent 担任智行管家：它经 MCP（prime_token）获得本机编排权（本机管家权限）；本机主人网页派单不依赖管家凭证。'}</small>
  ${S.agents.length ? `<label>本机 agent<select id="prime-select">${opts}</select></label>
  <div class="row spaced">${button(S.primeDecl.busy ? '钦定中…' : '钦定为智行管家', 'prime-declare', 'primary', S.primeDecl.busy ? 'disabled' : '')}${pa ? button('取消', 'prime-pick-cancel', 'link') : ''}</div>`
    : `<div class="empty"><p class="muted">本机还没有可钦定的 agent：先在「服务 → 能力配置」登记并重启 node。</p>${button('去看服务', 'go-services', '')}</div>`}`;
}
/* 钦定完成视图：成功提示 + 一次性凭证 + MCP 配置片段。wired=true（t20261023-62a2
   自动接线）：明示已写入的影子 home 配置路径，零手贴；降级：保留手动粘贴指引。 */
function primeDoneView() {
  const d = S.primeDecl.done;
  if (!d) return '';
  const mcpJSON = d.mcp ? JSON.stringify(d.mcp, null, 2) : '（应答未含 mcp_config，请点「换一个管家」重新钦定）';
  const wiredNote = d.wired
    ? `<div class="notice ok spaced"><b>已自动接线</b>：管家 MCP 连接已写入 <code>${esc(d.target)}</code>（mcpServers.swarmmind）——零手贴，agent 重连后即可本机派单；原配置备份在同目录 <code>setting.json.bak-prime</code>。</div>`
    : '';
  const tokTail = d.wired ? '凭证已随接线写入宿主配置，此处展示仅供留档备份。' : '请立即配给管家的 MCP 连接（SWARM_NODE_PRIME_TOKEN）。';
  return `<div class="notice ok spaced"><b>钦定完成</b>：「${esc(d.agent)}」已是本机管家——已写入 node.yaml 的 prime_agent 并即时生效（无需重启），旧凭证已随本次钦定失效。</div>${wiredNote}
  <div class="notice warn spaced"><b>管家凭证（仅此一次展示，立即复制）</b>：<code>${esc(d.token)}</code> ${button('复制凭证', 'copy-prime-declare', 'link')}<br><small>${tokTail}</small></div>
  <label>MCP 配置片段（${d.wired ? '已自动写入——如需手工迁移其他宿主可复制' : '加入管家 agent 的 mcpServers 即完成授权——贴入即生效'}）<pre><code>${esc(mcpJSON)}</code></pre></label>
  <div class="row spaced">${button('复制 MCP 配置', 'copy-prime-mcp', '')}${button('换一个管家', 'prime-pick', 'link')}</div>
  <p class="footer-note">${d.wired ? '接线发生在钦定事务内（走本机主人授权）；轮换管家凭证时会自动刷新同一配置，无需重复操作。' : '把上方 JSON 片段粘进管家 agent 的 MCP 服务器配置（SWARM_NODE_PRIME_TOKEN 已随片段带出）；重连后它即可本机派单。'}</p>`;
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
function normalizeProfile(p) {
  return {
    name: p.name || '', kind: p.kind || 'cli', command: p.command || '', workdir: p.workdir || '',
    agent_args: p.agent_args || [], timeout_sec: p.timeout_sec || 600,
    allow_args: !!p.allow_args, allow_env: p.allow_env || [], config_home: p.config_home || '',
    description: p.description || '', permission_mode: p.permission_mode || '', exposed: p.exposed !== false, orchestrator: !!p.orchestrator,
    stdin_to_arg: !!p.stdin_to_arg,
  };
}

/* ---------- 页面②：我的服务（旅程①服务提供者：开关三态 + 只读配置） ---------- */
function servicesPage() {
  const head = `<div class="page-head"><div><h1>服务</h1>
    <p class="muted">本机 agent 即服务：是否开放给其他机器调用，由本机决定；关闭后其他机器的<b>新任务</b>不再接收，本机派单不受影响，已在执行/排队的任务照常跑到终态。</p></div></div>`;
  // 功能性修复 2（2026-10-22）：空态不再提前 return——能力配置区（新增按钮）
  // 恒渲染，网页即可登记第一个 agent；去掉「只引导手编 YAML」的死路文案。
  const empty = S.agents.length ? '' : `<section class="card"><div class="empty"><h2>还没有 agent</h2>
    <p class="muted">在下方「能力配置」点「＋ 新增 agent」登记本机服务（名称 / command / workdir 均填绝对路径），保存后按提示应用并重启生效；习惯手编的也可编辑 node.yaml 后点「从文件重载」。</p></div></section>`;
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
      ${button('加载配置', 'cfg-load', '')}</div><p class="muted">读取 node.yaml 的 agents 表进行网页编辑。</p></section>`;
  }
  if (c.unavailable) {
    return `<section class="card spaced"><h2>能力配置</h2><p class="muted">配置编辑不可用（节点版本未装配配置端点，或接口不可达）。</p></section>`;
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
    ${pending}${msg}
    <div class="table-wrap"><table class="cfg-table"><thead><tr><th>名称</th><th>命令</th><th>超时</th><th>开放</th><th></th></tr></thead>
    <tbody>${rows}</tbody></table></div>
    <div class="row spaced">
      ${button('＋ 新增 agent', 'cfg-add', '')}
      ${button('保存全部（校验+写盘）', 'cfg-save', 'primary')}
      ${button('从文件重载', 'cfg-reload', '')}
    </div>
    <details ${c.agents.length ? '' : 'open'}><summary>编辑字段说明</summary><p class="muted small">
    command / workdir / config_home 必须绝对路径；timeout_sec 正数（秒）；allow_env 仅环境变量键名或 <code>*</code> 全开；
    关闭开放（exposed=false）只影响其他机器调用，本机派单不受限。结构性变更（增删 / command / agent_args / kind）保存后需「应用并重启」。</p></details></section>`;
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
  if (back?.isConnected) back.focus();
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
    <div class="row"><div><h2 style="margin-bottom:4px">${esc(a.name)} <span class="badge">${esc(a.kind)}</span> <span class="badge">${on ? '网内可调用' : '仅本机'}</span></h2>
      <small class="muted">${esc(a.description || '（无描述）')} · ${esc(permLabel(a.permission_mode))} · 今日已执行 <b>${esc(a.today_tasks ?? 0)}</b> 次</small></div>
    <div class="actions"><div class="toggle" role="group" aria-label="开放给其他机器调用" title="开放给其他机器调用：关闭后只停止接收外部新任务——不停已在执行/排队的任务（本控制台暂无停止入口，hub 来源任务可在 hub 管理台取消）">
      ${button('开放', `expose-on:${esc(a.name)}`, 'on-btn' + (on ? ' selected' : ''), '', `data-agent="${esc(a.name)}"`)}
      ${button('停止接受新任务', `expose-off:${esc(a.name)}`, 'off-btn' + (on ? '' : ' selected'), '', `data-agent="${esc(a.name)}"`)}
    </div><div class="toggle" role="group" aria-label="智行管家声明" title="智行管家：接收复合目标拆解派发汇总（全网同一时刻至多一个，由各机器客户端声明、hub 汇聚）">
    ${/* 管家开关状态化单按钮（DESIGN-butler v4 试用反馈二，2026-10-28）：已声明
        只留绿徽标+弱化文字链（「设为管家」不再出现，双按钮并排视觉混乱退役）；
        未声明只留一颗中性灰按钮（非实心蓝主操作，与开放/停止开放区分）。 */
    a.orchestrator
    ? `<span class="badge online">管家 · 本机</span>${button('取消管家', `orch-off:${esc(a.name)}`, 'link', '', `data-agent="${esc(a.name)}"`)}`
    : button('设为管家', `orch-on:${esc(a.name)}`, '', '', `data-agent="${esc(a.name)}"`)}
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
      const a = S.agents.find(x => x.name === name);
      if (a) { a.orchestrator = r.data.orchestrator; a._synced = !!r.data.hub_synced; }
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
  if (back?.isConnected) back.focus();
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
  ${myTasksCard()}`;
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
// myTasksCard 任务列表 + 来源筛选（TASK-nav-filter A1）：卡头部三枚 chip（全部/本机
// 派单/hub 下发，带已载计数；视觉对齐 hub .filters 形态，零计数退灰）。筛选为渲染层
// 过滤已载入条目（陷阱④：轮询只刷首页、翻页保序并入都在数据层，筛选随每次渲染从
// 全量已载列表现算，不缓存筛选结果）。零命中出空态+清除筛选，但「加载更早」翻页以
// 全量载入数计、入口照常（codex 必须修项）。终审必修2：筛选行恒在（不随 all.length
// 消失——空快照+激活筛选时控件必须在位可清除，S.tf 不自动重置）；空态二分：激活筛选
// →筛选空态，未激活+真无任务→首次派单空态。终审必修3：footer 口径三数分离——
// 「命中 X 条；筛选作用于已载入的 N / 共 M 条」（X=rows，N=all，M=tasksTotal）。
function myTasksCard() {
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
  return `<section class="card spaced"><div class="row"><h2>我的任务</h2><small>${loaded} · 本机执行过的全部任务（本机派单 + hub 下发）</small></div>
  <div class="filters">${chips}</div>
  ${failed ? listErrorBox('reload-all', '任务列表')
  : loading ? listLoadingBox('任务列表')
  : rows.length ? `<div class="table-wrap"><table><thead><tr><th>时刻</th><th class="task-title">目标</th><th>来源</th><th>状态</th><th></th></tr></thead><tbody>
    ${rows.map(e => `<tr data-task="${esc(e.task_id)}" tabindex="0" aria-label="查看任务 ${esc(e.task_id)}"><td>${timeShort(e.created_at)}</td><td><strong>${esc(e.target_agent)}</strong> @ ${esc(e.target_node)}</td><td>${originBadge(e.origin)}</td><td>${listStatusBadge(e)}</td><td class="table-action">${button('详情', 'open-task:' + esc(e.task_id))}</td></tr>`).join('')}
  </tbody></table></div>`
    : origin ? `<div class="empty"><h2>没有符合筛选的任务</h2><p class="muted">当前筛选下已载入的任务无命中；清除筛选，或用下方入口加载更早的任务。</p>${button('清除筛选', 'tf-clear')}</div>`
    : `<div class="empty"><h2>本机还没有执行过任务</h2><p class="muted">上方表单派第一单；hub 下发任务入账后同样出现在这里。</p>${button('＋ 派单', 'focus-form', 'primary')}</div>`}
  ${more ? `<div class="row">${button(S.tasksMoreBusy ? '加载中…' : `加载更早的任务（还有 ${S.tasksTotal - all.length} 条）`, 'tasks-more', '', S.tasksMoreBusy ? 'disabled' : '')}</div>` : ''}
  ${(all.length || origin) ? `<p class="footer-note">${origin ? `命中 ${rows.length} 条；筛选作用于已载入的 ${all.length} / 共 ${S.tasksTotal} 条 · ` : ''}列表即本机账本全量——本机派单与 hub 下发一律在列（全量审计，更早历史翻页载入）；状态随列表直出。「仅存留痕」= 账本中已不存在的本机派单历史，详情不再可查。</p>` : ''}</section>`;
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

// primeDeclare 网页钦定（方案 3.2.1 UX 收口）：confirmModal 确认（含旧凭证失效警示）→
// declare → 钦定完成视图（一次性凭证 + MCP 配置片段）。成功后主动刷新 status
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
      S.primeDecl.done = { agent: name, token: r.data?.prime_token || '', mcp: r.data?.mcp_config || null,
        wired: !!r.data?.wired, target: r.data?.wired_target || '' };
      S.primeDecl.picking = false;
      S.primeNew = ''; // 轮换提示与钦定完成视图不同时堆叠
      S.primeNewWire = null;
      toast(r.data?.wired ? '钦定完成 · MCP 连接已自动写入管家宿主配置' : '钦定完成 · 管家凭证仅此一次展示，请立即复制');
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
  confirmModal('钦定本机管家', `钦定「${esc(name)}」为本机管家：旧管家凭证将立即失效，正在使用旧凭证的 MCP 连接需改配新凭证。`, '确认钦定', run);
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
  await copyText(JSON.stringify(d.mcp, null, 2), 'MCP 配置', 'MCP 配置已复制 · 粘贴进管家 agent 的 mcpServers 即完成授权');
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
  if (back && back.isConnected) back.focus();   // 失存（轮询重建使行消失等）不强求
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
 * #layer 内容（copyText 失败兜底单用途，让位可接受）。 */
function openPlainModal(content) {
  if (S.drawer) closeDrawer();       // 同一 #layer 互斥：任务详情抽屉让位
  if (cfgEdit) closeCfgDrawer(true); // 能力配置抽屉让位（还原快照）
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
  confirmSetInert(false);            // 先解除下层 inert 再返焦（inert 内 focus() 无效）
  /* A2：确认层关闭后下层抽屉/弹层若仍在场，恢复其背景 inert——confirmSetInert
   * 把 app/顶栏与下层 overlay 一并解除，下层还开着时主体必须补回隔离。 */
  if ($('#layer')?.querySelector('.overlay')) layerInert(true);
  $('#confirm-modal')?.closest('.overlay')?.remove();
  const back = confirmReturnFocus;
  confirmReturnFocus = null;
  if (back?.isConnected) back.focus();   // 取消回弹层前焦点（抽屉字段或页面触发钮）
}

/* ---------- 事件委托 ---------- */
document.addEventListener('click', e => {
  /* 复制兜底弹层遮罩关闭（A1-18）：与通知弹层遮罩同语义（点遮罩=取消）。 */
  if (e.target.classList && e.target.classList.contains('overlay') && $('#copy-modal')) { closeCopyModal(); return; }
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
      case 'net-disconnect': netDisconnect(btn); return;
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
        if (cb) cb();
        return;
      }
      case 'close':   // shared modalHead ×/取消 同词关闭动作（与 hub 同词，notify-polish 改动三）；守护防误清其他层
        if ($('#notify-modal')) closeNotifyForm();
        else if ($('#confirm-modal')) closeConfirm();
        else if ($('#copy-modal')) closeCopyModal();
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
      case 'expose-on': case 'expose-off': setExposed(arg, act === 'expose-on', btn); return;
      case 'orch-on': case 'orch-off': setOrchestrator(arg, act === 'orch-on', btn); return;
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
    }
  }
  if (tr && !e.target.closest('button')) openDrawer(tr.dataset.task);
});
document.addEventListener('input', e => {
  if (e.target.id === 'f-target') { S.f.target = e.target.value; render(); }
  if (e.target.id === 'f-stdin') S.f.stdin = e.target.value;
  if (e.target.id === 'wiz-join') S.wiz.join = e.target.value;
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
    if ($('#notify-modal')) { e.preventDefault(); closeNotifyForm(); return; }
    if ($('#copy-modal')) { e.preventDefault(); closeCopyModal(); return; }
    if (cfgEdit) { e.preventDefault(); closeCfgDrawer(true); return; }
    if (S.drawer) { e.preventDefault(); closeDrawer(); return; }
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
});
/* 通知渠道弹层：类型切换只重绘 #nf-hint 与 #nf-type-fields 两槽（shared
 * nfRepaint），名称与事件勾选不被冲掉（局部刷新纪律，与 hub 控制台同机制）；
 * 类型切走格式重置通用。消息格式切换同槽重绘，已输入地址与请求头经
 * nfSecFromDom 回填不丢（t20261026-notify-polish 恒定骨架）。 */
document.addEventListener('change', e => {
  if (e.target.id === 'notify-also') { saveAlsoNotify(e.target); return; }
  if (e.target.id === 'nf-type') {
    nfRepaint(e.target.value, 'generic', {});
    return;
  }
  if (e.target.id === 'nf-format') {
    nfRepaint($('#nf-type')?.value || 'webhook', e.target.value, nfSecFromDom());
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
  setInterval(pollTick, 5000);
}
boot();
