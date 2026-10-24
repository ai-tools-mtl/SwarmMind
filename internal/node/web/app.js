'use strict';
/* SwarmMind Node 本地控制台（Loop 7，INTERACTION 客户端三旅程）。
 * 数据面：本机回环 /api/v1/*（Bearer local_token，令牌持于 localStorage）。
 * 页面：总览（旅程③运维）· 我的服务（旅程①服务提供者：exposed 开关三态）·
 * 派任务·我的任务（旅程②消费者：本机服务派单 + 留痕跟踪；跨机协作经 hub，
 * node 控制台不含全网目录——方案 v2 边界）。与 hub 控制台同交互闭环
 * （INTERACTION 四：300ms 反馈、终态带下一步、结果未知锁表单、技术态配人话双语）。
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

/* ---------- 展示词典：技术态永远配人话（INTERACTION 四.5） ---------- */
const STATES = {
  submitted: '已提交', offered: '已投递', acknowledged: '已接收',
  running: '执行中', completed: '已完成', failed: '执行失败', needs_review: '待人工核对',
};
const ERRORS = {
  timeout: '执行超过本机配置的超时上限，进程组已整组终止',
  policy_denied: '参数或环境变量超出本机白名单，未执行',
  expired: '离线任务超过 7 天保留期，已判失败',
  agent_not_found: '执行节点配置已变化，找不到该 agent',
  agent_not_exposed: '目标未暴露该能力，任务未准入',
  node_restarted_midway: '执行到一半机器重启，结果不确定',
  truncated: '结果超过编码预算，答复或诊断日志已截断',
  mailbox_full: '目标信箱已满（条数或字节预算）',
  storage_full: 'hub 存储全局预算已满',
  frame_too_large: '提交体超过单消息 1MiB 上限',
};
const TERMINAL = new Set(['completed', 'failed', 'needs_review']);

/* ---------- 全局状态 ---------- */
const TOKEN_KEY = 'swarmmind_node_token';
const S = {
  token: localStorage.getItem(TOKEN_KEY) || '',
  authed: false,
  page: 'overview',
  nodeID: '',
  status: null,            // GET /api/v1/local/status
  agents: [],              // GET /api/v1/local/agents（本机 ExecProfile 视图）
  tasks: [],               // GET /api/v1/local/tasks（我的任务留痕）
  live: {},                // task_id → 最近一次实况状态（详情/列表徽章）
  catalog: [],             // 派单目录（本机 exposed 能力；node 无全网视图）
  snap: '',                // 轮询数据指纹：未变化不重绘
  drawer: null,            // 打开的任务详情 {id, timer}
  f: { stage: 'idle', target: '', stdin: '', idem: newIdem(), message: '' },   // 派单表单状态机
  focusForm: false,
  wiz: { join: '', preview: null, stage: 'idle', msg: '', written: '' },       // 接入向导状态机（方案 3.3）
  primeNew: '',            // 统筹凭证轮换后的新凭证（仅此一次展示），
  primeNewWire: null,      // 轮换自动接线结果（t20261023-62a2）：{target} 命中 zcode 布局；null=降级手动配给
  primeDecl: { picking: false, busy: false, done: null },  // 网页钦定流程（方案 3.2.1）：picking=选择视图展开；done={agent,token,mcp,wired,target} 钦定完成一次性展示
  cfg: { loaded: false, unavailable: false, agents: [], pending: false, supervised: true,
         restart_cmd: '', config_path: '', structural: [], msg: '' },  // 能力配置编辑（方案 3.5）；编辑会话在模块级 cfgEdit（t20261023-a4b1 抽屉化）
  sessionEpoch: 0,         // 会话代次（第五轮收敛 3）：login/logout/401/换令牌时 +1（resetPrimeState 统一挂点）；
                           // 在途请求发起时捕获，应答返回时代次不一致 = 迟到应答，丢弃不写状态
};
function newIdem() {
  return 'web-' + (globalThis.crypto?.randomUUID?.() || Date.now() + '-' + Math.random().toString(36).slice(2));
}
/* 一次性统筹凭证内存清理（安全审查修复 5）：登出/401/换令牌时清空——退出登录后
   prime_token 不得残留在 JS 内存（下个会话重新进页可见）。同时递增会话代次
  （第五轮收敛 3）：清理前已发出的在途请求，其迟到应答凭代次不一致被丢弃，
   不得把已清理的一次性凭证写回 S.primeNew / S.primeDecl.done。 */
function resetPrimeState() {
  S.sessionEpoch++;
  S.primeDecl = { picking: false, busy: false, done: null };
  S.primeNew = '';
  S.primeNewWire = null;
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
    api('GET', '/api/v1/local/tasks'),
    api('GET', '/api/v1/agents'),
  ]);
  S.nodeID = st.data?.node_id || '';
  S.status = st.data || null;
  S.agents = ag.data?.agents || [];
  S.tasks = tk.data?.tasks || [];
  // 派单目录只含本机 agent（/api/v1/agents 无 network 键——node 无全网视图）。
  // 建议修 10（2026-10-22）：standalone 本机主权——private（未暴露）能力也可本机
  // 派单（exposed 只约束 hub 准入），目录不过滤；组网模式维持只列 exposed。
  S.catalog = (dir.data?.local || []).filter(e => e.card?.exposed || S.status?.mode === 'standalone');
  // 列表徽章用实况：最近 8 条留痕并发补查（hub 是状态真相源，日志不缓存状态）
  const top = S.tasks.slice(0, 8);
  const live = await Promise.all(top.map(e =>
    api('GET', '/api/v1/tasks/' + encodeURIComponent(e.task_id)).then(r => r.data).catch(() => null)));
  top.forEach((e, i) => { if (live[i]) S.live[e.task_id] = live[i]; });
  const snap = JSON.stringify([S.status, S.agents, S.tasks, S.catalog, Object.keys(S.live).length]);
  const changed = snap !== S.snap;
  S.snap = snap;
  return changed;
}
async function pollTick() {
  if (!S.authed || document.visibilityState !== 'visible') return;
  try {
    if (await loadAll()) {
      if (cfgEdit) return; // 能力配置编辑抽屉开着：数据照常刷，表格 DOM 不动（编辑会话不受重绘干扰）
      render();
    }
  } catch (e) { /* 401 已由 api() 处理；其余静默，下轮再试 */ }
}

/* ---------- 令牌门（本机管理令牌 = 本机控制台的登录） ---------- */
async function tryToken(token) {
  const epochAtReq = S.sessionEpoch; // 捕获登录发起时代次
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
    toast('令牌未被接受 · ' + errLine(r.data));
  } catch (e) {
    if (S.sessionEpoch !== myEpoch) return; // 迟到失败同理不碰新会话
    toast('本机 API 不可达，确认 node 已启动');
  }
  if (S.sessionEpoch === myEpoch) {
    S.token = localStorage.getItem(TOKEN_KEY) || '';
  }
}

/* ---------- 路由（hash SPA） ---------- */
const PAGES = [['overview', '总览'], ['services', '我的服务'], ['tasks', '派任务·我的任务'], ['wizard', '接入']];
// setup 模式只开向导 + 有限本机浏览（方案 3.3：可看本机环境，不能派单）。
const SETUP_PAGES = [['wizard', '接入向导'], ['overview', '本机环境']];
function route() {
  const h = (location.hash || '#/overview').replace(/^#\//, '');
  const allowed = S.authed && S.status?.mode === 'setup' ? SETUP_PAGES : PAGES;
  S.page = allowed.some(p => p[0] === h) ? h : allowed[0][0];
  if (S.page === 'services' && S.authed && S.status?.mode !== 'setup' && !S.cfg.loaded && !S.cfg._loading) {
    S.cfg._loading = true;
    loadCfg().finally(() => { S.cfg._loading = false; });
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
  const nums = ['①', '②', '③', '④'];
  return `<div class="shell"><aside><div class="workspace"><strong>${esc(S.nodeID || 'node')}</strong><small class="muted">本机控制台</small></div>
  <nav>${pages.map(([id, label]) => `<button data-go="${id}" class="${S.page === id ? 'active' : ''}"><span class="navnum">${nums[pages.findIndex(p => p[0] === id)] || ''}</span>${label}</button>`).join('')}</nav>
  <div class="aside-bottom">连接 hub：${online ? '<span class="badge online">在线</span>' : '<span class="badge offline">离线</span>'}<br>
  <span style="opacity:.75">本机控制台<br>仅本机可访问</span></div></aside>
  <main>${content}</main></div>`;
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
  return `<main><div class="card login"><div class="mark">⌘</div><h1>SwarmMind Node 本地控制台</h1>
  <p class="muted">本机管理令牌鉴权（Bearer）。令牌来自 node.yaml 的 <code>local_token</code>；未配置时 node 首次启动已生成，见启动日志或 <code>data_dir/local_token</code> 文件。</p>
  <label>本机管理令牌<input id="gate-token" type="password" placeholder="local_token" autocomplete="off"></label>
  ${button('进入控制台', 'gate-enter', 'primary')}
  <p class="footer-note">仅本机回环可达；令牌只存本浏览器（localStorage），不出本机。</p></div></main>`;
}

/* ---------- 接入向导（方案 3.3：粘贴接入串 → 预览 node.yaml → 保存落盘重启；
 * setup 模式的首启页，standalone/组网经「接入」页可重新接入/切换 hub） ---------- */
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
  const preview = w.preview ? `<label>预览：将写入的 node.yaml（agents 段留空，接入后在网页/手编添加）<textarea readonly rows="12" class="mono">${esc(w.preview.yaml_preview)}</textarea></label>` : '';
  return `<div class="page-head"><div><h1>接入中心端</h1>
    <p class="muted">从 hub 控制台「接入向导 → 复制接入串」拿到 <code>swarmmind://join?…</code>，粘贴到下面完成接入（写入前可反复预览）。</p></div></div>
  <section class="card">
    <label>接入串<textarea id="wiz-join" rows="3" placeholder="swarmmind://join?hub=wss%3A%2F%2F…&node=…&token=…" ${locked ? 'disabled' : ''}>${esc(w.join)}</textarea></label>
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
    return head + `<section class="card"><h2>三步把本机接入协作网</h2>
      <p class="muted">本机还没有任何 agent。推荐网页登记（也可手编 node.yaml）：</p>
      <div class="step"><span>1</span><div><b>登记能力</b><div class="muted">去「我的服务 → 能力配置」点「＋ 新增 agent」（command / workdir 绝对路径）；习惯手编的编辑 <code>node.yaml</code> 的 <code>agents:</code> 段，完整字段见 <code>configs/node.example.yaml</code>。</div></div></div>
      <div class="step"><span>2</span><div><b>应用并重启</b><div class="muted">网页保存结构性变更（新增 agent）后点「应用并重启」；手编的重启 node。重启不丢已确认的任务结果。</div></div></div>
      <div class="step"><span>3</span><div><b>钦定统筹并派单</b><div class="muted">在「本机统筹」区钦定一个 agent，随后即可在本机派单；需要网内调用时再开 exposed 开关。</div></div></div></section>`
      + (st?.mode === 'setup' ? primeCard() : '');
  }
  return head + `
  <div class="grid cols3 spaced">
    <section class="card"><h3>连接状态</h3><div class="metric"><span class="badge ${st?.online ? 'online' : 'offline'}">${st?.online ? '在线' : '离线'}</span></div>
      <small class="muted">epoch ${esc(st?.epoch ?? 0)} · 掉线自动重连，重连后代次 +1</small></section>
    <section class="card"><h3>执行账本（三态）</h3><div class="mini-stat"><span><b>${esc(led.accepted ?? 0)}</b><small>accepted</small></span><span><b>${esc(led.started ?? 0)}</b><small>started</small></span><span><b>${esc(led.finished ?? 0)}</b><small>finished</small></span></div>
      <small class="muted">重启恢复：started 未完 → needs_review，绝不自动重跑</small></section>
    <section class="card"><h3>最近同步</h3><div class="metric">${st?.last_sync_at ? timeShort(st.last_sync_at) : '从未'}</div>
      <small class="muted">${st?.last_sync_at ? '最近一次注册成功 · ' + esc(timeFull(st.last_sync_at)) : '注册成功后这里显示时间'}</small></section>
  </div>
  ${primeCard()}${recentTasksCard()}`;
}
/* 本机统筹 · 形态（方案 3.1/3.2.1）：网页钦定闭环——未钦定/换统筹走选择视图
 * → declare → 钦定完成视图（一次性凭证 + MCP 配置片段）→ 已钦定常态（轮换 +
 * 换统筹入口）；setup 模式只提示不开放（矩阵 #9/#18）。全部动态值过 esc()。 */
function primeCard() {
  const st = S.status;
  const pa = st?.prime_agent || '';
  const modeTxt = st?.mode === 'standalone' ? 'standalone（本机自治，不连 hub）'
    : st?.mode === 'setup' ? 'setup（未接入）' : '组网在线';
  const foot = `<p class="footer-note">断开组网后：hub 信箱内未收任务将待过期；本机历史任务不受影响，本机派单照常可用。</p>`;
  if (st?.mode === 'setup') {
    return `<section class="card spaced"><h2>本机统筹 · 形态</h2>
    <div class="mini-stat spaced"><span><b>未钦定</b><small>本机统筹</small></span><span><b>${esc(modeTxt)}</b><small>当前形态</small></span></div>
    <small class="muted">先完成接入并配置能力后再钦定统筹。</small></section>`;
  }
  const head = `<section class="card spaced"><div class="row"><h2>本机统筹 · 形态</h2>
    <div class="actions">${st?.mode === 'networked' ? button('断开组网', 'net-disconnect') : ''}</div></div>
  <div class="mini-stat spaced"><span><b>${pa ? esc(pa) : '未钦定'}</b><small>本机统筹</small></span><span><b>${esc(modeTxt)}</b><small>当前形态</small></span></div>`;
  const newTok = S.primeNew ? `<div class="notice warn spaced"><b>新统筹凭证（仅此一次展示，立即复制）</b>：<code>${esc(S.primeNew)}</code> ${button('复制', 'copy-prime-token', 'link')}<br><small>旧凭证已失效；${S.primeNewWire ? `新凭证已自动写入统筹的 MCP 配置（<code>${esc(S.primeNewWire.target)}</code>，重连后生效），或「换一个统筹」重新走钦定。` : '请立即配给统筹的 MCP 连接（SWARM_NODE_PRIME_TOKEN），或「换一个统筹」重新走钦定。'}</small></div>` : '';
  if (S.primeDecl.done) return head + primeDoneView() + foot + '</section>';
  if (!pa || S.primeDecl.picking) return head + primePickView(pa) + foot + '</section>';
  return head + `<small class="muted">统筹经 MCP 用 prime_token 本机派单；本机主人（local_token）在网页派单同样放行（2026-10-22 鉴权裁决）。declare/轮换/配置管理等管理面仅主人可操作。</small>
  <div class="row spaced">${button('轮换统筹凭证', 'prime-rotate', 'link')}${button('换一个统筹', 'prime-pick', 'link')}</div>${newTok}${foot}</section>`;
}
/* 钦定选择视图（未钦定引导态与「换一个统筹」共用；数据源本机 agents 列表）。 */
function primePickView(pa) {
  const opts = S.agents.map(a => `<option value="${esc(a.name)}">${esc(a.name)}${a.description ? ' — ' + esc(a.description) : ''}</option>`).join('');
  return `<small class="muted">${pa ? '当前统筹将被替换：钦定即换凭证，旧凭证立即失效。' : '钦定一个本机 agent 作为统筹，它将经 MCP（prime_token）获得本机编排权；本机主人网页派单不依赖统筹凭证。'}</small>
  ${S.agents.length ? `<label>本机 agent<select id="prime-select">${opts}</select></label>
  <div class="row spaced">${button(S.primeDecl.busy ? '钦定中…' : '钦定为统筹', 'prime-declare', 'primary', S.primeDecl.busy ? 'disabled' : '')}${pa ? button('取消', 'prime-pick-cancel', 'link') : ''}</div>`
    : `<div class="empty"><p class="muted">本机还没有可钦定的 agent：先在「我的服务 → 能力配置」登记并重启 node。</p>${button('去看我的服务', 'go-services', '')}</div>`}`;
}
/* 钦定完成视图：成功提示 + 一次性凭证 + MCP 配置片段。wired=true（t20261023-62a2
   自动接线）：明示已写入的影子 home 配置路径，零手贴；降级：保留手动粘贴指引。 */
function primeDoneView() {
  const d = S.primeDecl.done;
  if (!d) return '';
  const mcpJSON = d.mcp ? JSON.stringify(d.mcp, null, 2) : '（应答未含 mcp_config，请点「换一个统筹」重新钦定）';
  const wiredNote = d.wired
    ? `<div class="notice ok spaced"><b>已自动接线</b>：统筹 MCP 连接已写入 <code>${esc(d.target)}</code>（mcpServers.swarmmind）——零手贴，agent 重连后即可本机派单；原配置备份在同目录 <code>setting.json.bak-prime</code>。</div>`
    : '';
  const tokTail = d.wired ? '凭证已随接线写入宿主配置，此处展示仅供留档备份。' : '请立即配给统筹的 MCP 连接（SWARM_NODE_PRIME_TOKEN）。';
  return `<div class="notice ok spaced"><b>钦定完成</b>：「${esc(d.agent)}」已是本机统筹——已写入 node.yaml prime_agent 并即时生效（无需重启），旧凭证已随本次钦定失效。</div>${wiredNote}
  <div class="notice warn spaced"><b>统筹凭证（仅此一次展示，立即复制）</b>：<code>${esc(d.token)}</code> ${button('复制凭证', 'copy-prime-declare', 'link')}<br><small>${tokTail}</small></div>
  <label>MCP 配置片段（${d.wired ? '已自动写入——如需手工迁移其他宿主可复制' : '加入统筹 agent 的 mcpServers 即完成授权——接入即统筹'}）<pre><code>${esc(mcpJSON)}</code></pre></label>
  <div class="row spaced">${button('复制 MCP 配置', 'copy-prime-mcp', '')}${button('换一个统筹', 'prime-pick', 'link')}</div>
  <p class="footer-note">${d.wired ? '接线发生在钦定事务内（走本机主人授权）；轮换统筹凭证时会自动刷新同一配置，无需重复操作。' : '把上方 JSON 片段粘进统筹 agent 的 MCP 服务器配置（SWARM_NODE_PRIME_TOKEN 已随片段带出）；重连后它即可本机派单。'}</p>`;
}
function recentTasksCard() {
  const rows = S.tasks.slice(0, 8);
  return `<section class="card spaced"><div class="row"><h2>最近任务（本机派出的）</h2><small>${S.tasks.length} 条留痕</small></div>
  ${rows.length ? `<div class="table-wrap"><table><thead><tr><th>时刻</th><th class="task-title">目标</th><th>任务</th><th>状态</th></tr></thead><tbody>
    ${rows.map(e => { const t = S.live[e.task_id]; return `<tr data-task="${esc(e.task_id)}"><td>${timeShort(e.created_at)}</td><td><strong>${esc(e.target_agent)}</strong> @ ${esc(e.target_node)}<small>${esc(e.task_id)}</small></td><td>${esc((e.idem_key || '—')).slice(0, 24)}</td><td>${t ? badge(t.status) : '<span class="badge offline">状态待查</span>'}</td></tr>`; }).join('')}
  </tbody></table></div><p class="footer-note">点击行查看详情；任务真相在本机账本（本页实时补查），终态保留 24h。</p>`
    : `<div class="empty"><h2>还没有派过任务</h2><p class="muted">去「派任务」选一个本机服务，描述要做的事即可。</p>${button('＋ 派单', 'go-tasks', 'primary')}</div>`}</section>`;
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
async function cfgSave(btn, label = '保存全部（校验+写盘）') {
  if (btn) { btn.disabled = true; btn.textContent = '保存中…'; }
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
  } finally { if (btn) { btn.disabled = false; btn.textContent = label; } }
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
  if (btn) { btn.disabled = true; btn.textContent = '重启中…'; }
  try {
    const r = await api('POST', '/api/v1/local/restart');
    if (r.code === 200) {
      const d = r.data || {};
      S.cfg.msg = d.note || 'node 重启中，稍后自动恢复';
      render();
      // 保活拉起后自动恢复：6s/12s 两次补拉 + 5s 轮询兑底
      setTimeout(() => { S.cfg.loaded = false; loadCfg(); }, 6000);
      setTimeout(() => { S.cfg.loaded = false; loadCfg(); }, 12000);
    } else { alert('重启失败：' + errLine(r.data)); if (btn) { btn.disabled = false; btn.textContent = '应用并重启'; } }
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
    description: p.description || '', permission_mode: p.permission_mode || '', exposed: p.exposed !== false,
    stdin_to_arg: !!p.stdin_to_arg,
  };
}

/* ---------- 页面②：我的服务（旅程①服务提供者：开关三态 + 只读配置） ---------- */
function servicesPage() {
  const head = `<div class="page-head"><div><h1>我的服务</h1>
    <p class="muted">本机 agent 即服务：暴露开关主权在本机；关闭后<b>新任务</b>不再准入（404），已在执行/排队的任务照常跑到终态。</p></div></div>`;
  // 功能性修复 2（2026-10-22）：空态不再提前 return——能力配置区（新增按钮）
  // 恒渲染，网页即可登记第一个 agent；去掉「只引导手编 YAML」的死路文案。
  const empty = S.agents.length ? '' : `<section class="card"><div class="empty"><h2>还没有 agent</h2>
    <p class="muted">在下方「能力配置」点「＋ 新增 agent」登记本机服务（名称 / command / workdir 均填绝对路径），保存后按提示应用并重启生效；习惯手编的也可编辑 node.yaml 后点「从文件重载」。</p></div></section>`;
  return head + empty + S.agents.map(a => svcCard(a)).join('') +
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
    <div class="table-wrap"><table class="cfg-table"><thead><tr><th>名称</th><th>命令</th><th>超时</th><th>暴露</th><th></th></tr></thead>
    <tbody>${rows}</tbody></table></div>
    <div class="row spaced">
      ${button('＋ 新增 agent', 'cfg-add', '')}
      ${button('保存全部（校验+写盘）', 'cfg-save', 'primary')}
      ${button('从文件重载', 'cfg-reload', '')}
    </div>
    <details ${c.agents.length ? '' : 'open'}><summary>编辑字段说明</summary><p class="muted small">
    command / workdir / config_home 必须绝对路径；timeout_sec 正数（秒）；allow_env 仅环境变量键名或 <code>*</code> 全开；
    exposed=false 仅关闭网内准入，本机统筹派单不受限。结构性变更（增删 / command / agent_args / kind）保存后需「应用并重启」。</p></details></section>`;
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
function openCfgDrawer(idx) {
  const a = S.cfg.agents[idx];
  if (!a) return;
  if (S.drawer) closeDrawer(); // 同一 #layer 互斥：任务详情抽屉让位
  if (cfgEdit && S.cfg.agents[cfgEdit.idx]) S.cfg.agents[cfgEdit.idx] = cfgEdit.snap; // 换行编辑（UI 不可达，防御）：旧行先还原
  cfgEdit = { idx, snap: JSON.parse(JSON.stringify(a)) };
  $('#layer').innerHTML = `<div class="overlay drawer-overlay"><div class="drawer" id="cfg-drawer">${cfgDrawerView(a, idx)}</div></div>`;
  $('#layer').addEventListener('click', cfgMaskClose); // 具名引用重复注册幂等去重（与任务抽屉遮罩关闭同机制）
}
function cfgMaskClose(e) {
  if (!cfgEdit) return; // 抽屉已关后残留监听不动作（免与任务抽屉遮罩隐式耦合）
  if (e.target.classList.contains('overlay')) closeCfgDrawer(true); // 遮罩关闭=取消语义（还原单行）
}
// closeCfgDrawer 关编辑抽屉并重绘表格；restore=true 还原快照（取消/遮罩/×），
// false 保留编辑副本（保存成功/删除——内容已是意图终态）。
function closeCfgDrawer(restore) {
  if (cfgSaving) return;
  if (cfgEdit && restore && S.cfg.agents[cfgEdit.idx]) S.cfg.agents[cfgEdit.idx] = cfgEdit.snap;
  cfgEdit = null;
  $('#layer').innerHTML = '';
  render();
}
// cfgDrawerSave 抽屉保存：同一 PUT 整表协议，口径「保存（校验+写盘）」只落盘本行
// 的编辑意图；成功关抽屉，被拒/网络未知保持打开（副本保留，现口径）。
async function cfgDrawerSave(btn) {
  cfgSaving = true;
  let ok = false;
  try { ok = await cfgSave(btn, '保存（校验+写盘）'); } finally { cfgSaving = false; }
  if (ok || !S.authed) closeCfgDrawer(false); // 401 已回令牌门：抽屉随会话收起
}
function cfgDrawerView(a, i) {
  return `<div class="row"><h2>编辑 agent · ${esc(a.name) || '（未命名）'}</h2>${button('×', 'cfg-close', 'close')}</div>
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
      <label class="cfg-check"><input type="checkbox" data-cfg="${i}.exposed" ${a.exposed ? 'checked' : ''}> exposed（对网内暴露；本机派单不受限）</label>
    </div>
    <div class="cfg-drawer-foot">
      ${button('取消', 'cfg-cancel', '')}
      <span class="actions">${button('删除此 agent', `cfg-del:${i}`, 'off-btn')}${button('保存（校验+写盘）', 'cfg-save-row', 'primary')}</span>
    </div>`;
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
      <small class="muted">${esc(a.description || '（无描述）')} · 权限模式 <code>${esc(a.permission_mode || '—')}</code> · 今日已执行 <b>${esc(a.today_tasks ?? 0)}</b> 次</small></div>
    <div class="actions"><div class="toggle" role="group" aria-label="暴露开关">
      ${button('暴露', `expose-on:${esc(a.name)}`, 'on-btn' + (on ? ' selected' : ''), '', `data-agent="${esc(a.name)}"`)}
      ${button('停止暴露', `expose-off:${esc(a.name)}`, 'off-btn' + (on ? '' : ' selected'), '', `data-agent="${esc(a.name)}"`)}
    </div></div></div>${syncNote}
    <details><summary>配置详情（只读 · 来自本机 ExecProfile）</summary>
      <div class="table-wrap"><table><tbody>
        <tr><th>command</th><td><code>${esc(a.command)}</code> ${a.allow_args ? '' : '<span class="readonly">allow_args=false：任务参数将被拒绝</span>'}</td></tr>
        <tr><th>workdir</th><td><code>${esc(a.workdir)}</code></td></tr>
        <tr><th>timeout_sec</th><td>${esc(a.timeout_sec)}s<small>唯一超时源；超时整组终止进程组</small></td></tr>
        <tr><th>allow_args</th><td>${a.allow_args ? 'true · 接受任务参数（解释器类=全参数授权，选用即知情）' : 'false · 非空参数 → policy_denied'}</td></tr>
        <tr><th>allow_env</th><td>${(a.allow_env || []).length ? '<code>' + a.allow_env.map(esc).join('</code> <code>') + '</code>' : '（空 · 任务不可覆盖环境变量）'}</td></tr>
        <tr><th>config_home</th><td><code>${esc(a.config_home || '（空 = data_dir/home）')}</code><small>进程 HOME 指此，与 workdir 分离；凭证不出本机</small></td></tr>
        <tr><th>agent_args</th><td>${(a.agent_args || []).length ? '<code>' + a.agent_args.map(esc).join(' ') + '</code>' : '（无固定附加参数）'}<small>固定拼在任务参数前，任务不可改</small></td></tr>
        <tr><th>permission_mode</th><td>${esc(a.permission_mode || '—')}<small>映射 agent 自带权限机制，调用方以此为准</small></td></tr>
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
      toast(exposed ? '已暴露 · 即时生效' : '已停止暴露 · 新任务不再准入');
    } else {
      toast('开关失败 · ' + errLine(r.data));
    }
  } catch (e) {
    toast(e.network ? '本机 API 不可达' : '开关失败');
  }
  render();
  pollTick();
}

/* ---------- 页面③：派任务·我的任务（旅程②消费者） ---------- */
function tasksPage() {
  const c = S.f;
  // 建议修 10（2026-10-22）：standalone 本机主权——本机派单不经 hub，离线/信箱
  // 语义不适用，目录与选中提示不渲染「离线进信箱」误导文案；组网模式维持现状。
  const standalone = S.status?.mode === 'standalone';
  const head = `<div class="page-head"><div><h1>派任务 · 我的任务</h1>
    <p class="muted">从本机服务选目标 → 一段话描述要做的事 → 提交；结果在这里跟踪与取回。</p></div></div>`;
  /* 2026-10-23 同步 hub 端口径：下拉项只留名字（描述收进 title），统筹载体
   * （zcode-dev）单独 optgroup 分组加前缀，与普通智能体视觉分区 */
  const isPrime = e => e.card.name.includes('zcode-dev');
  const optOf = e => {
    const v = e.node_id + '|' + e.card.name;
    const off = standalone || e.online ? '' : ' · 离线（任务进信箱，重连后执行）';
    const tip = `${e.card.name}${isPrime(e) ? ' · 统筹：拆解复合目标并派发汇总' : ''} — ${e.card.description || '（无描述）'} · 权限 ${e.card.permission_mode || '—'}${off}`;
    const label = isPrime(e) ? `统筹 · ${e.card.name}` : e.card.name;
    return `<option value="${esc(v)}" ${c.target === v ? 'selected' : ''} title="${esc(tip)}">${esc(label)}${off}</option>`;
  };
  const primeOpts = S.catalog.filter(isPrime), restOpts = S.catalog.filter(e => !isPrime(e));
  const group = (label, arr) => arr.length ? `<optgroup label="${esc(label)}">${arr.map(optOf).join('')}</optgroup>` : '';
  const opts = group('统筹 · 复合目标拆解派发汇总', primeOpts) + group('智能体与工具', restOpts);
  const target = S.catalog.find(e => e.node_id + '|' + e.card.name === c.target);
  const stageView = {
    idle: '',
    submitting: `<div class="statusline"><span class="badge offered">提交中…</span></div>`,
    acked: `<div class="notice ok"><b>已确认 · acknowledged</b>：任务已持久化，任务号 <code>${esc(c.taskId || '')}</code>。${button('查看任务', 'open-submitted', 'link')}</div>`,
    failed: `<div class="notice warn"><b>提交被拒绝</b>：${c.message}${button('修正后重试', 'form-retry', 'link')}</div>`,
    unknown: `<div class="notice warn"><b>结果未知 · 表单已锁定</b>：${esc(c.message)}<br>同一幂等键（<code>${esc(c.idem)}</code>）重试不会重复执行：${button('同键重试', 'form-resend', 'link')} · ${button('放弃这单', 'form-abandon', 'link')}</div>`,
  }[c.stage] || '';
  const formLocked = c.stage === 'submitting' || c.stage === 'unknown' || c.stage === 'acked';
  const targetNote = target ? `<b>${esc(target.card.name)}</b> · ${esc(target.card.description || '（无描述）')} · 权限模式 <code>${esc(target.card.permission_mode || '—')}</code> · ${standalone ? '<span class="badge online">本机直执行</span>' : target.online ? '<span class="badge online">在线</span>' : '<span class="badge offline">离线 · 排队等它回来</span>'}` : '';
  return head + `
  <section class="card" id="task-form"><h2>派任务</h2>
    ${S.catalog.length ? `
    <label>本机服务${standalone ? '（本机主权：未暴露的能力也可本机派单）' : '（已暴露；派单目标只能是本机）'}<select id="f-target" ${formLocked ? 'disabled' : ''}><option value="" ${!c.target ? 'selected' : ''} disabled>选择一个本机服务…</option>${opts}</select></label>
    <div class="notice" ${target ? '' : 'hidden'}>${targetNote}</div>
    <label>任务描述（一段话讲清要做什么；以 prompt_text 交付给对方 agent）<textarea id="f-stdin" placeholder="例：审查这段代码的并发安全性，指出竞态并给出修复建议…" ${formLocked ? 'disabled' : ''}>${esc(c.stdin)}</textarea></label>
    <div class="row"><small class="muted">幂等键 <code>${esc(c.idem)}</code> · 结果未知时可同键重试，不会重复执行</small>
    ${c.stage === 'acked' ? button('再派一单', 'form-retry', 'primary') : button(c.stage === 'submitting' ? '提交中…' : '确认并提交', 'form-submit', 'primary', c.stage === 'submitting' ? 'disabled' : '')}</div>
    <div class="statusline">${stageView}</div>`
      : `<div class="empty"><h2>本机还没有可派发的服务</h2><p class="muted">先在「我的服务」登记本机能力（standalone 下未暴露也能本机派单）；跨机派单请经 hub 管理面/MCP 发起。</p>${button('去看我的服务', 'go-services', 'primary')}</div>`}
  </section>
  ${myTasksCard()}`;
}
function myTasksCard() {
  const rows = S.tasks;
  return `<section class="card spaced"><div class="row"><h2>我的任务</h2><small>${rows.length} 条留痕 · 本机派出的全部任务</small></div>
  ${rows.length ? `<div class="table-wrap"><table><thead><tr><th>时刻</th><th class="task-title">目标</th><th>任务号</th><th>状态</th><th></th></tr></thead><tbody>
    ${rows.map(e => { const t = S.live[e.task_id]; return `<tr data-task="${esc(e.task_id)}"><td>${timeShort(e.created_at)}</td><td><strong>${esc(e.target_agent)}</strong> @ ${esc(e.target_node)}</td><td><code>${esc(e.task_id)}</code></td><td>${t ? badge(t.status) : '<span class="badge offline">点击查询</span>'}</td><td class="table-action">${button('详情', 'open-task:' + esc(e.task_id))}</td></tr>`; }).join('')}
  </tbody></table></div><p class="footer-note">状态实时补查自本机账本；终态保留 24h，过期任务号查询即 404。</p>`
    : `<div class="empty"><h2>还没有任务记录</h2><p class="muted">上方表单派第一单。</p>${button('＋ 派单', 'focus-form', 'primary')}</div>`}</section>`;
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
      toast(r.data?.dup ? '已确认（幂等命中原任务）' : '已确认 · acknowledged');
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

/* ---------- 接入向导 / 统筹钦定与轮换 / 断开组网动作 ----------
 * 端点契约（localapi，均 Bearer local_token）：POST /api/v1/local/setup/parse|apply|
 * disconnect（setup.go）、POST /api/v1/local/prime/rotate|declare（prime.go）。
 * 全部走 api() 封装与 toast 通道；向导失败信息在页内 stageView 呈现（可反复重试）。 */

/* parseJoin 接入串前端先验（即时人话反馈）；后端 ParseJoinString 同源白名单复核
 * （protocol/validate.go：node_id 2-32 位小写、token 64hex、hub_url ws(s)://）。 */
function parseJoin(raw) {
  const s = (raw || '').trim();
  const PFX = 'swarmmind://join?';
  if (!s.startsWith(PFX)) throw new Error('格式须为 swarmmind://join?hub=…&node=…&token=…（从 hub 控制台接入向导复制）');
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
    S.wiz.preview = null; S.wiz.stage = 'failed'; S.wiz.msg = '接入串在预览后被修改，请重新预览'; render(); return;
  }
  if (!confirm('保存将写入 node.yaml 并重启 node 进入组网模式，确认保存？')) return;
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
}

async function primeRotate(btn) {
  if (!confirm('轮换统筹凭证？旧凭证将立即失效；统筹宿主配置若可自动接线会随轮换刷新（重连生效），否则需改配新凭证（SWARM_NODE_PRIME_TOKEN）。')) return;
  if (btn) btn.disabled = true; // 300ms 反馈：立即锁按钮
  const epoch = S.sessionEpoch; // 收敛 3：请求发起时捕获会话代次
  try {
    const r = await api('POST', '/api/v1/local/prime/rotate');
    if (S.sessionEpoch !== epoch) return; // 迟到应答：会话已换代（logout/401/换令牌），一次性凭证不写回内存
    if (r.code === 200) {
      S.primeNew = r.data?.prime_token || '';
      S.primeNewWire = r.data?.wired ? { target: r.data?.wired_target || '' } : null;
      toast(r.data?.wired ? '已轮换 · 新凭证已自动写入统筹宿主配置' : '已轮换 · 新凭证仅此一次展示，请立即复制');
    } else {
      toast('轮换失败 · ' + errLine(r.data));
    }
  } catch (e) {
    if (e.unauthorized) return;
    toast(e.network ? '本机 API 不可达' : '轮换失败');
  }
  render();
  if (btn && document.contains(btn)) btn.disabled = false;
}

// primeDeclare 网页钦定（方案 3.2.1 UX 收口）：confirm（含旧凭证失效警示）→
// declare → 钦定完成视图（一次性凭证 + MCP 配置片段）。成功后主动刷新 status
// （prime_agent 即时反映，不等 5s 轮询）。
async function primeDeclare(btn) {
  const name = $('#prime-select')?.value;
  if (!name) { toast('先选择一个本机 agent'); return; }
  if (!confirm(`钦定「${name}」为本机统筹？旧统筹凭证将立即失效，正在使用旧凭证的 MCP 连接需改配新凭证。`)) return;
  S.primeDecl.busy = true;
  if (btn) { btn.disabled = true; btn.textContent = '钦定中…'; }
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
      toast(r.data?.wired ? '钦定完成 · MCP 连接已自动写入统筹宿主配置' : '钦定完成 · 统筹凭证仅此一次展示，请立即复制');
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
}

async function netDisconnect(btn) {
  if (!confirm('断开组网并重启进入 standalone（本机自治）？\n\nhub 信箱内未收任务将留在 hub 待过期；本机历史任务不受影响，本机派单照常可用。')) return;
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
}

async function copyPrimeToken() {
  try {
    await navigator.clipboard.writeText(S.primeNew);
    toast('新统筹凭证已复制 · 旧凭证已失效，请立即配给统筹的 MCP 连接');
  } catch (e) {
    toast('复制失败——浏览器未授权剪贴板');
  }
}

// 钦定完成视图的复制：凭证 + MCP 配置片段（JSON.stringify(mcp, null, 2) 同展示口径）。
async function copyPrimeDeclare() {
  try {
    await navigator.clipboard.writeText(S.primeDecl.done?.token || '');
    toast('统筹凭证已复制 · 请立即配给统筹的 MCP 连接（SWARM_NODE_PRIME_TOKEN）');
  } catch (e) {
    toast('复制失败——浏览器未授权剪贴板');
  }
}
async function copyPrimeMcp() {
  const d = S.primeDecl.done;
  if (!d?.mcp) { toast('没有可复制的 MCP 配置'); return; }
  try {
    await navigator.clipboard.writeText(JSON.stringify(d.mcp, null, 2));
    toast('MCP 配置已复制 · 粘贴进统筹 agent 的 mcpServers 即完成授权');
  } catch (e) {
    toast('复制失败——浏览器未授权剪贴板');
  }
}

/* ---------- 任务详情抽屉 ---------- */
let drawerSeq = 0;       // 抽屉会话序号：防被关/被换（codex 第三轮复测）
let drawerReqSeq = 0;    // 抽屉内请求序号：同抽屉多次刷新的乱序应答守卫（同上）
async function openDrawer(taskID) {
  closeDrawer();
  const seq = ++drawerSeq;
  $('#layer').innerHTML = `<div class="overlay drawer-overlay"><div class="drawer" id="drawer"><p class="muted">加载中…</p></div></div>`;
  $('#layer').addEventListener('click', e => { if (e.target.classList.contains('overlay')) closeDrawer(); });
  S.drawer = { id: taskID, timer: null, seq };
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
function closeDrawer() {
  if (S.drawer?.timer) clearInterval(S.drawer.timer);
  S.drawer = null;
  drawerSeq++;   // 在途 refreshDrawer 应答全部作废
  $('#layer').innerHTML = '';
}
// taskOrigin 任务来源判定（功能性修复 7，2026-10-22）：以「我的任务」留痕的
// origin 字段为准（后端自本地账本补）；留痕仅由本机派单写入，无 origin 的旧
// 缓存/无账本条目按 local 处理——本机控制台列表里的任务默认走本机语义。
function taskOrigin(taskID) {
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
    $('#drawer').innerHTML = `<div class="row"><h2>${esc(S.drawer.id)}</h2>${button('×', 'close-drawer', 'close')}</div>
      <div class="notice warn">任务查询失败${r ? ' · ' + errLine(r.data) : '（网络）'}。可能已过保留期（终态 24h / 离线 7 天）。</div>
      <p class="footer-note">下一步：回到${button('我的任务', 'close-drawer', 'link')}列表。</p>`;
    return;
  }
  const t = r.data;
  S.drawer.task = t;
  S.live[t.id] = t;
  $('#drawer').innerHTML = taskDrawerHtml(t);
}

// taskDrawerHtml 任务抽屉正文（refreshDrawer 渲染用；「查看原文」切换只重绘
// #log-wrap 不走全量，见 logSectionHtml）。读 t 与全局 S（taskOrigin/S.agents）。
function taskDrawerHtml(t) {
  const res = t.result;
  const errTxt = res?.error ? ERRORS[res.error.split(';')[0]] || res.error : '';
  // 双路径语义（功能性修复 7）：本机任务（origin=local，本机账本真相源）产物区
  // 从本机账本直出——产物文件就在本机 workdir/outputs 下，展示本机绝对路径；
  // 仅 hub 下发任务保留「经 hub 取回」指引。
  const local = taskOrigin(t.id) === 'local';
  const agent = S.agents.find(x => x.name === t.target_agent);
  const absPath = a => (agent?.workdir ? agent.workdir.replace(/\/+$/, '') + '/' : '') + a.path;
  // 产物说明段仅在有产物时计算（第六轮修复 3）：无产物完成/运行中/needs_review 的
  // res.artifacts 为 null/空数组——原实现无条件读 res.artifacts[0] 直接 TypeError，
  // 抽屉卡「加载中」；下方模板只在 res.artifacts?.length 为真时插值本段，空串不渲染。
  const artifactNote = !res?.artifacts?.length ? '' : local
    ? `<p class="muted small">产物在本机：<code>${esc(absPath(res.artifacts[0]))}</code>${
        res.artifacts.length > 1 ? `（共 ${res.artifacts.length} 个，均在本机 outputs 目录）` : '（workdir/outputs 下，本机直接打开即可，无需经 hub 取回）'}</p>`
    : `<p class="muted small">产物内容保留在执行机本机；经 hub 管理面 <code>GET /api/v1/tasks/{id}/artifacts/{index}</code> 取回。</p>`;
  const reviewNote = local
    ? `<div class="notice warn"><b>执行到一半机器重启了，结果不确定——等待本机主人核对，请勿重新提交</b>（needs_review 是独立终态，不是失败；核对结论由本机主人在本控制台确认）。</div>`
    : `<div class="notice warn"><b>执行到一半机器重启了，结果不确定——等待本机主人核对，请勿重新提交</b>（needs_review 是独立终态，不是失败；核对记录在 hub 控制台登记）。</div>`;
  return `<div class="row"><h2>${esc(t.target_agent)} @ ${esc(t.target_node)} ${badge(t.status)}</h2>${button('×', 'close-drawer', 'close')}</div>
    <p class="muted small">任务号 <code>${esc(t.id)}</code> · 追踪 <code>${esc(t.trace_id || '—')}</code> · 提交方 ${esc(t.submitted_by || '—')} · 幂等键 ${esc(t.idem || '—')} · 来源 ${local ? '本机派单' : 'hub 下发'}</p>
    <ul class="timeline" style="list-style:none;padding:0;margin:14px 0">
      <li>创建 ${esc(timeFull(t.created_at))} · 更新 ${esc(timeFull(t.updated_at))}</li>
      ${res ? `<li>开始 ${esc(timeFull(res.started_at))} · 结束 ${esc(timeFull(res.finished_at))} · 退出码 ${esc(res.exit_code)}</li>` : ''}
    </ul>
    ${t.status === 'needs_review' ? reviewNote : ''}
    ${errTxt || res?.error ? `<div class="notice warn"><b>${esc(errTxt || res.error)}</b> · <code>${esc(res.error)}</code></div>` : ''}
    ${!res ? `<p class="muted">尚未到终态（${esc(STATES[t.status] || t.status)}）。抽屉每 5s 自动刷新，可先去忙别的。</p>`
      : `<h3>最终答复 · FinalAnswer</h3>${mdAnswer(res.final_answer)}
         ${res.artifacts?.length ? `<h3>产物（${res.artifacts.length} 个，保留至确认后 24h）</h3>${res.artifacts.map(a => `<div class="artifact"><code>${esc(a.path)}</code> <small>${a.size} 字节 · sha256 ${esc(a.sha256.slice(0, 16))}…${local && agent?.workdir ? `<br>本机绝对路径：<code>${esc(absPath(a))}</code>` : ''}</small></div>`).join('')}${artifactNote}` : ''}
         <details><summary>诊断日志（stdout / stderr 尾部）</summary><div id="log-wrap">${logSectionHtml(res)}</div></details>`}
    <div class="row spaced"><small class="muted">终态自带下一步：</small>
      <span class="actions">${t.status === 'completed' ? button('查看全部任务', 'close-drawer', 'link') : t.status === 'failed' ? button('回表单重新派单', 'reopen-form', 'link') + button('查看全部任务', 'close-drawer', 'link') : button('查看全部任务', 'close-drawer', 'link')}</span></div>`;
}

// mdAnswer 最终答复渲染态（t20261023-969e：agent 产出正文按 Markdown 渲染）；
// mdToHtml 对空白输入返回空串，兜底「（空）」人话。
function mdAnswer(text) {
  return `<div class="md-body md-answer">${mdToHtml(text) || '<p class="muted">（空）</p>'}</div>`;
}

// logSectionHtml 诊断日志区（stdout / stderr 尾部）：默认 md 渲染 + 「查看原文」
// 切换（df -h 类空格对齐的进程输出被 md 解析会散架，agent 的 md 结构化日志又常
// 在——默认渲染满足主诉求，误渲染一键回原文）。切换态存 S.drawer.logRaw；切换
// 只重绘 #log-wrap，details 开合态不被整抽屉重绘冲掉。原文视图沿用单 pre 双段。
function logSectionHtml(res) {
  const raw = !!S.drawer?.logRaw;
  const toggle = `<div class="md-log-head"><span class="muted small">${raw ? '当前为原始输出。' : '默认按 Markdown 渲染；空格对齐的进程输出请切原文。'}</span>${button(raw ? '查看渲染' : '查看原文', 'log-raw', 'link')}</div>`;
  if (raw) return `${toggle}<pre>stdout:\n${esc(res.stdout_tail || '（空）')}\n\nstderr:\n${esc(res.stderr_tail || '（空）')}</pre>`;
  const sec = (label, tail) => `<b class="md-log-label">${label}</b><div class="md-body md-log">${mdToHtml(tail || '') || '<p class="muted">（空）</p>'}</div>`;
  return `${toggle}${sec('stdout', res.stdout_tail)}${sec('stderr', res.stderr_tail)}`;
}

/* ---------- 复制诊断信息 ---------- */
async function copyDiagnostics() {
  const text = JSON.stringify({ node_id: S.nodeID, copied_at: new Date().toISOString(), ...S.status }, null, 2);
  try {
    await navigator.clipboard.writeText(text);
    toast('诊断信息已复制');
  } catch (e) {
    toast('复制失败——浏览器未授权剪贴板');
  }
}

/* ---------- toast ---------- */
let toastTimer = null;
function toast(msg) {
  const el = $('#toast');
  el.textContent = msg;
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, 3200);
}

/* ---------- 事件委托 ---------- */
document.addEventListener('click', e => {
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
          if (confirm(`确定删除 agent「${S.cfg.agents[di].name}」？保存后写入 node.yaml（结构性变更需重启生效）。`)) {
            S.cfg.agents.splice(di, 1);
            if (cfgEdit) closeCfgDrawer(false); // 抽屉内删除：行已移除即关抽屉（closeCfgDrawer 内含 render）
            else render();
          }
        }
        return;
      }
      case 'cfg-save': cfgSave(btn); return;
      case 'cfg-reload': {
        if (!confirm('放弃当前编辑，从 node.yaml 文件重新读取？（未保存的修改将丢失）')) return;
        cfgReload(btn); return;
      }
      case 'cfg-restart': {
        if (!confirm('应用并重启 node？在途任务将被中断并进入「待人工核对」，确认继续？')) return;
        cfgRestart(btn); return;
      }
      case 'logout':
        localStorage.removeItem(TOKEN_KEY); S.token = ''; S.authed = false; S.snap = '';
        resetPrimeState(); // 修复 5：登出清一次性凭证
        render(); return;
      case 'expose-on': case 'expose-off': setExposed(arg, act === 'expose-on', btn); return;
      case 'form-submit': submitForm(); return;
      case 'form-retry': // 新幂等键开新单
        S.f = { stage: 'idle', target: S.f.target, stdin: S.f.stdin, idem: newIdem(), message: '' };
        render(); focusForm(); return;
      case 'form-resend': submitForm(); return; // 同键重试
      case 'form-abandon':
        S.f = { stage: 'idle', target: S.f.target, stdin: S.f.stdin, idem: newIdem(), message: '' };
        toast('已放弃 · 幂等键作废，不会再提交这一单'); render(); return;
      case 'open-submitted': {
        const id = S.f.taskId; if (id) openDrawer(id); return;
      }
      case 'open-task': openDrawer(arg); return;
      case 'reopen-form': closeDrawer(); route(); focusForm(); return;
      case 'close-drawer': closeDrawer(); return;
      case 'log-raw': {   // 诊断日志 查看原文/查看渲染 切换：只重绘 #log-wrap
        if (!S.drawer?.task?.result) return;
        S.drawer.logRaw = !S.drawer.logRaw;
        const wrap = $('#log-wrap');
        if (wrap) wrap.innerHTML = logSectionHtml(S.drawer.task.result);
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
  if (e.key === 'Enter' && e.target.id === 'gate-token') tryToken(e.target.value.trim());
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
  if (!S.token) { render(); return; }
  const epochAtReq = S.sessionEpoch; // 启动验证同样遵守会话边界（迟到 200 不得恢复 authed）
  try {
    await api('GET', '/api/v1/local/status', undefined, { noGate: true });
    if (S.sessionEpoch !== epochAtReq) return;
    S.authed = true;
    await loadAll();
  } catch (e) { /* 401 → 门；网络异常 → 空数据页，轮询会补 */
    if (!e.unauthorized) S.authed = true;
  }
  route();
  setInterval(pollTick, 5000);
}
boot();
