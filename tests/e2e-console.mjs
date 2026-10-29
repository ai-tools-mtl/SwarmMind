// SwarmMind 控制台 E2E 测试（Playwright，真实浏览器交互）
// 运行：node tests/e2e-console.mjs
//   hub 模式（默认）：E2E_BASE 指向 hub 管理面，账户密码登录，跑 E01-E20 系列。
//   node 模式（终审必修4）：E2E_BASE 指向 node local API 且设置 local_token 环境变量
//     （本机管理令牌，令牌门登录）→ 跑 N01-N03/E-NAV3/E-N1~N3 系列：来源筛选行恒在、
//     轮询保持筛选、翻页叠加筛选、登出后 S.tf 重置。
//   会话套件（T3 双端会话界面；R1 必修6 重写）：E2E_SUITE=sessions 切换——hub 模式跑
//     S01-S10，node 模式跑 SN01-SN06。安全门（R1 必修1）：会话套件会创建/关闭真实
//     会话，E2E_BASE 必须显式指向批准的本地目标（localhost/127.0.0.1/[::1]），缺失
//     或非法在启动浏览器前立即非零退出，绝不发起任何网络请求。
//     受控数据：全部经 REST 构造并绑定创建回执的唯一 session ID，所有行/详情/关闭
//     断言按该 ID 定位；unverified（真实成功轮次）与 failed（坏工作目录）分别覆盖；
//     实按键断言 activeElement/Esc/返焦；DOM 功能、整页视觉、深层交互三层分开报告
//     （record 第 6 参 layer∈DOM|VISUAL|DEEP，汇总分层计数——DOM 存在不替代像素与
//     深层交互合格）。截图矩阵（双端五视口+深层态）写入 E2E_SHOTS（默认
//     /tmp/agent-session/t3-shots），走查记录写 walkthrough-log.txt。
// 覆盖：首次初始化/密码登录/总览/节点/任务/运维控制（含总理模式派发）/策略/设置（账户+令牌）/退出
// 语义：needs_review 提示、抽屉详情、批次报告、令牌明文一次性展示
// 汇总：PASS / SKIP / FAIL 三态分开计（终审必修4：跳过不再冒充 PASS）；仅 FAIL 判败退出。

import { chromium } from 'playwright';
import fs from 'node:fs';

let BASE = process.env.E2E_BASE || 'http://1.13.158.180:8444';
const USER = process.env.E2E_USER || 'admin';
const PASS = process.env.E2E_PASS || 'Swarm@2026';
const NODE_TOKEN = process.env.local_token || '';
const NODE_MODE = !!NODE_TOKEN;
const SUITE = process.env.E2E_SUITE || 'full';   // full=既有 E/N 系列；sessions=T3 会话系列

// ---------- 会话套件安全门（R1 必修1）----------
// 会话套件会登录、创建会话并关闭会话（真实注册表写入）。E2E_BASE 必须显式设置且
// 指向批准的本地测试目标；缺失/非法在 chromium.launch() 之前非零退出——浏览器都
// 不启动，保证缺参路径零网络请求。既有 full/N 套件行为不变（历史默认仅其沿用）。
const APPROVED_LOCAL_HOSTS = new Set(['localhost', '127.0.0.1', '::1']);
function assertLocalBase(suite) {
  const raw = process.env.E2E_BASE || '';
  let u = null;
  try { u = new URL(raw); } catch { }
  const host = u ? u.hostname.toLowerCase() : '';
  const ok = !!u && (u.protocol === 'http:' || u.protocol === 'https:') && APPROVED_LOCAL_HOSTS.has(host);
  if (!ok) {
    console.error(`[会话套件安全门] E2E_SUITE=${suite} 拒绝启动：E2E_BASE 必须显式指向批准的本地目标（http(s)://localhost|127.0.0.1|[::1]:端口），当前值：${raw || '（未设置）'}。会话套件会创建/关闭真实会话，禁止默认线上 hub。`);
    process.exit(1);
  }
  return raw.replace(/\/+$/, '');
}

// ---------- 会话套件公共（R1 必修6）----------
const SESS_SIX = ['pending', 'awaiting_node', 'running', 'unverified', 'done', 'failed'];
const SESS_WORD = { pending: '排队中', awaiting_node: '已投递', running: '执行中', done: '已完成', unverified: '工作结果未证实', failed: '执行失败' };
const VIEWPORTS = [[1440, 900], [1280, 800], [1024, 768], [680, 700], [390, 800]];
const SHOTS = (process.env.E2E_SHOTS || '/tmp/agent-session/t3-shots').replace(/\/+$/, '');
const walk = [];
const walkLog = line => { walk.push(line); console.log('  [走查] ' + line); };
const flushWalk = suite => { try { fs.mkdirSync(SHOTS, { recursive: true }); fs.appendFileSync(`${SHOTS}/walkthrough-log.txt`, `===== ${suite} 会话套件走查（${new Date().toISOString()}）=====\n` + walk.join('\n') + '\n'); console.log(`走查记录已追加至 ${SHOTS}/walkthrough-log.txt（${suite}，${walk.length} 条）`); } catch { } };
/* shot 整页截图（R2 必修6 残余）：先回页顶（吸顶/固定元素按顶部姿态入图），再
 * fullPage 取完整文档——视口截图停在长页中段＝假整页证据。 */
const shot = async (page, name) => {
  fs.mkdirSync(SHOTS, { recursive: true });
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({ path: `${SHOTS}/${name}.png`, fullPage: true });
  walkLog(`截图 ${name}.png（fullPage 整页）`);
};
/* pngSize 读 PNG IHDR 像素尺寸（整页视觉判据，R2 必修6 残余：fullPage 截图必须
 * 覆盖文档完整高度——「整页视觉」不凭截图调用成功判 PASS，逐张核对像素）。 */
const pngSize = name => {
  const b = fs.readFileSync(`${SHOTS}/${name}.png`);
  return { w: b.readUInt32BE(16), h: b.readUInt32BE(20) };
};
/* 页内取焦点元素链条（实按键断言用）：activeElement 的定位描述 */
const activeDesc = () => {
  const a = document.activeElement;
  if (!a || a === document.body) return 'body';
  return (a.id ? '#' + a.id : '') + (a.dataset?.act ? `[data-act=${a.dataset.act}]` : '') + (a.dataset?.sess ? `[data-sess=${a.dataset.sess}]` : '') + (a.className ? '.' + String(a.className).split(' ').join('.') : '');
};
/* 字号地板核查（R1 必修7 浏览器实证）：作用域内 small/.small/footer-note/code 的
 * computed font-size 一律 ≥12.5px（0.01 容差），返回违例清单。 */
async function cssFloorCheck(page, scopeSel) {
  return page.evaluate(sel => {
    const bad = [];
    for (const scope of document.querySelectorAll(sel)) {
      for (const el of scope.querySelectorAll('small, .small, .footer-note, code')) {
        const fsz = parseFloat(getComputedStyle(el).fontSize);
        if (!(fsz >= 12.49)) bad.push(`${el.tagName.toLowerCase()}.${el.className}=${fsz}`);
      }
    }
    return bad;
  }, scopeSel);
}
/* reduced-motion 降级实证（R1 必修8）：reduce 页注入 sst-running 徽标，伪元素
 * animationName 必须为 none；普通页对照应为 pulse-dot（注入同一文档即读，无需交互）。 */
async function reducedMotionCheck(browser, base) {
  const p = await browser.newPage({ reducedMotion: 'reduce', ignoreHTTPSErrors: true });
  await p.goto(base, { waitUntil: 'domcontentloaded' });
  const reduced = await p.evaluate(() => {
    const s = document.createElement('span');
    s.className = 'badge sst-running';
    document.body.appendChild(s);
    return getComputedStyle(s, ':before').animationName;
  });
  await p.close();
  return reduced;
}
/* chipMatches 页内判据（S02/S06 共用）：chips 计数与 zero 色态逐项匹配 API 推导期望。 */
const chipMatches = e => {
  const chips = [...document.querySelectorAll('[data-act="sess-chip"]')];
  const all = chips.find(c => c.dataset.chip === '');
  if (!all || !all.textContent.includes(`(${e.total})`)) return false;
  for (const s of e.six) {
    const c = chips.find(x => x.dataset.chip === s);
    if (!c) return false;
    const n = e.counts[s] || 0;
    if (c.textContent.trim().split(/\s+/).pop() !== String(n)) return false;
    if (c.classList.contains('zero') !== (n === 0)) return false;
  }
  return true;
};

const results = [];
const p_sleep = ms => new Promise(r => setTimeout(r, ms));
function record(id, name, ok, note = '', skip = false, layer = 'DOM') {
  results.push({ id, name, ok: skip ? false : ok, skip, note, layer });
  console.log(`${skip ? '⏭️ SKIP' : ok ? '✅ PASS' : '❌ FAIL'}  ${id}  ${name}${note ? '  — ' + note : ''}`);
}
function summarize() {
  const pass = results.filter(r => r.ok && !r.skip).length;
  const skip = results.filter(r => r.skip).length;
  const fail = results.filter(r => !r.ok && !r.skip).length;
  console.log(`\n========== E2E 汇总：PASS ${pass} / SKIP ${skip} / FAIL ${fail}（共 ${results.length} 条）==========`);
  /* 分层报告（R1 必修6）：DOM 功能 / 整页视觉 / 深层交互三层分开计数——DOM 断言
   * 存在不替代像素合格与深层交互合格，任一层 FAIL 同样判败。 */
  for (const layer of ['DOM', 'VISUAL', 'DEEP']) {
    const rs = results.filter(r => (r.layer || 'DOM') === layer);
    if (!rs.length) continue;
    const lp = rs.filter(r => r.ok && !r.skip).length, lf = rs.filter(r => !r.ok && !r.skip).length, ls = rs.filter(r => r.skip).length;
    console.log(`  [${layer}] PASS ${lp} / SKIP ${ls} / FAIL ${lf}（共 ${rs.length} 条）`);
  }
  if (fail) {
    console.log('FAIL 明细：');
    results.filter(r => !r.ok && !r.skip).forEach(r => console.log(`  ${r.id} ${r.name} ${r.note}`));
    process.exit(1);
  }
}

// ---------- node 模式（终审必修4）：令牌门登录 + 来源筛选浏览器级流程 ----------
async function nodeFlow(browser) {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  globalThis.__page = page;
  page.setDefaultTimeout(15000);
  console.log(`node 模式：${BASE}（令牌门登录，local_token 经环境变量传入）`);
  await page.goto(BASE, { waitUntil: 'networkidle' });
  if (await page.locator('#gate-token').count()) {
    await page.fill('#gate-token', NODE_TOKEN);
    await page.click('[data-act="gate-enter"]');
    record('N01', 'node 令牌门登录进入控制台', true);
  } else {
    record('N01', 'node 令牌门登录进入控制台', true, '浏览器存有上轮令牌自动进入（登出环节仍会重走令牌门）');
  }
  await page.waitForSelector('#logout:not([hidden])', { timeout: 15000 }); // 登录终态信号（避开启动轮询重绘）

  // node 导航词表（TASK-nav-filter B：总览/服务/任务/连接；aside 内结构化定位）
  const NODE_NAV = ['总览', '服务', '任务', '连接'];
  const nodeNav = (await page.locator('aside nav button').allTextContents())
    .map(s => s.replace(/^0\d/, '').trim());
  record('E-NAV3', 'node 导航词表：4 项名称与顺序', JSON.stringify(nodeNav) === JSON.stringify(NODE_NAV), nodeNav.join('/'));

  await page.click('aside nav button[data-go="tasks"]');
  await page.waitForSelector('[data-act="tf-origin"]', { timeout: 15000 }); // 筛选行恒在（空快照也有，必修2）；表格仅在有行时渲染
  const chipCount = await page.locator('[data-act="tf-origin"]').count();
  record('N02', 'node 任务页来源筛选行恒在（3 chip，空快照也不消失）', chipCount === 3, `chip ${chipCount}`);

  // E-N1 轮询保持筛选：选计数非零的来源 chip（全零选 local）→ 跨一个轮询周期（5s）
  // → chip 选中态与行集不变；来源列全部命中所选 origin
  const chipTexts = await page.locator('[data-act="tf-origin"]').allTextContents();
  const pick = /本机派单\s*[1-9]/.test(chipTexts[1] || '') ? 'local' : /hub 下发\s*[1-9]/.test(chipTexts[2] || '') ? 'hub' : 'local';
  await page.click(`[data-act="tf-origin"][data-origin="${pick}"]`);
  await page.waitForTimeout(300);
  const rowsBefore = await page.evaluate(() => [...document.querySelectorAll('tr[data-task]')].map(r => r.dataset.task).join(','));
  await p_sleep(6500);   // 轮询 5s + 余量：至少跨一个轮询周期
  const chipOn = await page.evaluate(o => document.querySelector(`[data-act="tf-origin"][data-origin="${o}"]`)?.classList.contains('on'), pick);
  const rowsAfterPoll = await page.evaluate(() => [...document.querySelectorAll('tr[data-task]')].map(r => r.dataset.task).join(','));
  const scopeOk = await page.evaluate(() =>
    /命中 \d+ 条；筛选作用于已载入的 \d+ \/ 共 \d+ 条/.test(document.querySelector('.footer-note')?.textContent || ''));
  const hasRows = rowsBefore.length > 0;
  record('E-N1', '轮询刷新后筛选保持（chip 选中态+行集不变+footer 三数口径）', chipOn && rowsBefore === rowsAfterPoll && scopeOk, hasRows ? `行 ${rowsBefore.split(',').length} origin=${pick}` : '无已载任务', !hasRows);
  const cellsOk = !hasRows || await page.evaluate(o => {
    const want = o === 'hub' ? 'hub 下发' : '本机派单';
    const tds = [...document.querySelectorAll('tr[data-task] td:nth-child(4)')].map(td => td.textContent.trim());
    return tds.length > 0 && tds.every(t => t === want);
  }, pick);
  record('E-N1b', '来源筛选后行全部命中所选来源', cellsOk, `origin=${pick}`, !hasRows);

  // E-N2 翻页叠加筛选：「加载更早」后 chip 仍选中、行集增长、新增行仍命中来源
  const moreBtn = page.locator('[data-act="tasks-more"]');
  if (await moreBtn.count()) {
    const before = rowsBefore ? rowsBefore.split(',').length : 0;
    await moreBtn.click();
    await page.waitForFunction(n => document.querySelectorAll('tr[data-task]').length > n, before, { timeout: 20000 }).catch(() => { });
    await page.waitForTimeout(500);
    const stillOn = await page.evaluate(o => document.querySelector(`[data-act="tf-origin"][data-origin="${o}"]`)?.classList.contains('on'), pick);
    const grown = await page.evaluate(() => document.querySelectorAll('tr[data-task]').length);
    const scopeN = await page.evaluate(() => {
      const m = (document.querySelector('.footer-note')?.textContent || '').match(/已载入的 (\d+) \/ 共 (\d+)/);
      return m ? +m[1] : 0;
    });
    const newCellsOk = await page.evaluate(o => {
      const want = o === 'hub' ? 'hub 下发' : '本机派单';
      const tds = [...document.querySelectorAll('tr[data-task] td:nth-child(4)')].map(td => td.textContent.trim());
      return tds.length > 0 && tds.every(t => t === want);
    }, pick);
    // 合法形态二选一：可见行增长；或新页全属另一来源被筛光（可见行不变）但 footer 已载数增长——
    // 不把「可见行增长」当唯一翻页成功判据（codex 建议：否则下一页全属另一来源时误报 FAIL）
    const pass = stillOn && newCellsOk && (grown > before || scopeN > before);
    record('E-N2', '翻页载入后筛选叠加保持（chip 选中+已载增长+行仍命中）', pass, `可见行 ${before}→${grown} 已载 ${scopeN} origin=${pick}`);
  } else {
    record('E-N2', '翻页载入后筛选叠加保持', false, '账本已全量载入，无「加载更早」入口', true);
  }

  // E-N3 登出后 S.tf 重置：带筛选登出 → 令牌门 → 再登录 → 任务页「全部」chip 选中
  await page.click('#logout');
  await page.waitForSelector('#gate-token', { timeout: 15000 });
  await page.fill('#gate-token', NODE_TOKEN);
  await page.click('[data-act="gate-enter"]');
  await page.waitForSelector('#logout:not([hidden])', { timeout: 15000 });
  await page.click('aside nav button[data-go="tasks"]');
  await page.waitForSelector('[data-act="tf-origin"]', { timeout: 15000 });
  const allOn = await page.evaluate(() => document.querySelector('[data-act="tf-origin"][data-origin=""]')?.classList.contains('on'));
  record('E-N3', '登出→再登录后 S.tf 重置（「全部」chip 选中）', allOn === true);
}

// ---------- hub 会话套件（T3，R1 必修6 重写）：S01-S10 ----------
// 受控种子经 REST 构造并绑定创建回执 ID：seedA（无轮次）/ seedB（真实轮次→
// unverified）/ seedC（坏工作目录→failed，可控失败）。所有行/详情/关闭断言按 ID
// 定位，不吃历史数据。分层：S01-S03/S08=DOM，S10/S09=VISUAL，S04-S07=DEEP。
const hubApi = (page, path, opt = {}) => page.evaluate(async ({ path, opt }) => {
  const r = await fetch(path, {
    method: opt.method || 'GET',
    headers: opt.body ? { 'Content-Type': 'application/json' } : {},
    credentials: 'same-origin',
    body: opt.body,
  });
  return { code: r.status, body: await r.json().catch(() => null) };
}, { path, opt });
/* hubWaitTerminal 轮询会话详情直至首轮落终态，返回终态值（超时 'timeout'）。 */
async function hubWaitTerminal(page, id, timeoutMs = 300000) {
  const t0 = Date.now();
  while (Date.now() - t0 < timeoutMs) {
    const st = await page.evaluate(async id => {
      const r = await fetch(`/api/v1/sessions/${id}`, { credentials: 'same-origin' }).then(x => x.json()).catch(() => null);
      const t = (r?.session?.turns || [])[0];
      return t ? t.status : '';
    }, id);
    if (st && !['pending', 'awaiting_node', 'running'].includes(st)) return st;
    await p_sleep(3000);
  }
  return 'timeout';
}
async function hubSessFlow(browser) {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, ignoreHTTPSErrors: true });
  globalThis.__page = page;
  page.setDefaultTimeout(15000);
  console.log(`hub 会话套件：${BASE}`);
  await page.goto(BASE, { waitUntil: 'networkidle' });
  let initialized = false;
  try {
    const r = await page.request.get(`${BASE}/api/v1/auth/account`);
    initialized = (await r.json()).initialized === true;
  } catch { }
  if (!initialized) {
    await page.fill('#su-user', USER);
    await page.fill('#su-pass', PASS);
    await page.fill('#su-pass2', PASS);
    await page.click('#setup-form button[type=submit]');
  } else {
    await page.fill('#login-user', USER);
    await page.fill('#login-pass', PASS);
    await page.click('#pw-login-form button[type=submit]');
  }
  await page.waitForSelector('#logout:not([hidden])', { timeout: 15000 }); // 登录终态信号（避开启动轮询重绘）
  await p_sleep(1200);

  // ---- 受控种子（REST 构造，绑定创建回执唯一 ID；创建仅登记不派发，首轮显式提交）----
  const mkSeed = cwd => page.evaluate(async cwd => {
    const dir = await fetch('/api/v1/directory', { credentials: 'same-origin' }).then(r => r.json()).catch(() => null);
    const d = (dir?.directory || []).find(x => x.card?.session_adapter);
    if (!d) return { err: '目录中无声明 session_adapter 的能力' };
    const r = await fetch('/api/v1/sessions', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, credentials: 'same-origin',
      body: JSON.stringify({ node_id: d.node_id, agent: d.card.name, cwd }),
    });
    const j = await r.json().catch(() => ({}));
    return { code: r.status, id: j?.session?.id, adapter: j?.session?.adapter, node: d.node_id, agent: d.card.name };
  }, cwd);
  const stamp = Date.now();
  const seedA = await mkSeed('/tmp/agent-session/work');
  if (!seedA.id) {
    record('S01', '会话列表渲染（列名顺序/行内容/待首轮徽标）', false, '种子失败：' + (seedA.err || seedA.code));
    flushWalk('hub');
    summarize();
    return;
  }
  const seedC = await mkSeed(`/nonexistent-e2e-failed-${stamp}`);   // 坏 cwd：轮次执行必失败（可控 failed）
  const seedB = await mkSeed('/tmp/agent-session/work');
  const turn = (id, prompt) => hubApi(page, `/api/v1/sessions/${encodeURIComponent(id)}/turns`, { method: 'POST', body: JSON.stringify({ prompt, turn_seq: 1 }) });
  await turn(seedB.id, 'E2E 受控轮次：请只回复 OK 两个字母，不要读取文件，不要执行任何命令。');
  await turn(seedC.id, 'E2E 失败覆盖：工作目录不存在，预期执行失败。');
  walkLog(`种子绑定：A=${seedA.id}（无轮次） B=${seedB.id}（真实轮次→unverified） C=${seedC.id}（坏 cwd→failed）`);

  // 进入会话页
  await page.click('aside nav button[data-page="sessions"]');
  await page.waitForSelector(`#sess-list-zone tr[data-sess="${seedA.id}"]`, { timeout: 15000 });

  // S01 列表渲染（绑定 seedA 唯一 ID；不写死 agent 名——断言行含种子 agent）
  const heads = await page.evaluate(() => [...document.querySelectorAll('#sess-list-zone thead th')].map(th => th.textContent.trim()));
  const headsOk = JSON.stringify(heads) === JSON.stringify(['会话', '目标机器', '绑定', '状态', '最近活动（UTC）', '操作']);
  const rowOk = await page.evaluate(id => {
    const tr = document.querySelector(`tr[data-sess="${id}"]`);
    return !!tr && tr.getAttribute('tabindex') === '0' && tr.textContent.includes('待首轮');
  }, seedA.id);
  const agentOk = await page.evaluate(({ id, agent }) => document.querySelector(`tr[data-sess="${id}"]`)?.textContent.includes(agent), { id: seedA.id, agent: seedA.agent });
  record('S01', '会话列表渲染（列名顺序/种子行含种子 agent/待首轮徽标/行 tabindex）', headsOk && rowOk && agentOk, heads.join('|'));

  // S02 状态筛选（环境无关口径）：chips 计数与 zero 色态逐项匹配 API 推导期望
  //（推导口径=前端 sessStateCounts 同款：按 last_turn.status 计数，关闭会话的轮次
  // 同样计入六态 chip；轮询 5s 节拍内状态可能推进，失败带新期望重试收敛）；
  // 点零计数 chip → 空态 → 清除恢复
  const deriveExp = sessions => {
    const counts = {};
    for (const s of sessions) { const st = s.last_turn?.status || ''; if (st) counts[st] = (counts[st] || 0) + 1; }
    return { total: sessions.length, counts, six: SESS_SIX };
  };
  let s02ok = false, expNow = null;
  for (let i = 0; i < 4 && !s02ok; i++) {
    const sessNow = (await hubApi(page, '/api/v1/sessions')).body?.sessions || [];
    expNow = deriveExp(sessNow);
    await page.waitForFunction(chipMatches, expNow, { timeout: 7000 }).catch(() => { });
    s02ok = await page.evaluate(chipMatches, expNow);
  }
  const counts = expNow?.counts || {};
  const zeroChip = SESS_SIX.find(s => (counts[s] || 0) === 0);
  let filterOk = false;
  if (zeroChip) {
    await page.click(`[data-act="sess-chip"][data-chip="${zeroChip}"]`);
    await page.waitForSelector('#sess-list-zone .empty', { timeout: 5000 });
    const emptyTxt = await page.textContent('#sess-list-zone .empty');
    const hasClear = await page.locator('[data-act="sess-clear-filter"]').count();
    await page.click('[data-act="sess-clear-filter"]');
    await page.waitForSelector('#sess-list-zone tr[data-sess]', { timeout: 5000 });
    filterOk = emptyTxt.includes('没有符合筛选的会话') && hasClear === 1;
  }
  record('S02', '状态筛选 chips 计数与 zero 色态逐一匹配 API 数据（全部+6 态）', s02ok, JSON.stringify(expNow));
  record('S02b', `零计数 chip（${zeroChip || '无'}）点击 → 筛选空态 → 清除恢复`, filterOk, zeroChip ? `点 ${SESS_WORD[zeroChip]}` : '环境数据无零计数状态，点击路径未覆盖', !zeroChip);

  // S03 新建表单联动：选机器 → 能力联动（未声明者禁选+注明）→ 摘要行+绑定区 → 权限档无全权 → 空消息校验。
  // 适配器只读值断言创建回执的 seedA.adapter（R1 建议2：不再 zcode 兜底）
  await page.selectOption('#sess-node', seedA.node);
  await page.waitForTimeout(400);
  const agentOpts = await page.evaluate(() => {
    const sel = document.querySelector('#sess-agent');
    return sel ? [...sel.options].map(o => ({ v: o.value, t: o.textContent, disabled: o.disabled })) : [];
  });
  const optCap = agentOpts.find(o => o.v === seedA.agent);
  // 无适配器对照按目录实际 session_adapter 能力分类（R1 建议2 残余：不得取「第一个
  // 不同于 seedA 的 agent」充对照——目录存在多个合法会话能力时后者会被误断 disabled
  // 而假败）。期望分类：disabled ⇔ 该能力卡未声明 session_adapter；禁选项注明文案；
  // 选项值必须都能与目录对账（对不上=FAIL，不静默放过）。
  const dirCaps = await page.evaluate(async nodeId => {
    const dir = await fetch('/api/v1/directory', { credentials: 'same-origin' }).then(r => r.json()).catch(() => null);
    const caps = {};
    for (const d of (dir?.directory || [])) if (d.node_id === nodeId && d.card?.name) caps[d.card.name] = !!d.card.session_adapter;
    return caps;
  }, seedA.node);
  const optsAll = agentOpts.filter(o => o.v);
  const noAdapt = optsAll.filter(o => dirCaps[o.v] === false);
  const unknownCap = optsAll.filter(o => !(o.v in dirCaps));
  const noOk = unknownCap.length === 0 && optsAll.every(o => o.disabled === !dirCaps[o.v]) && noAdapt.every(o => o.t.includes('未声明会话适配器'));
  await page.selectOption('#sess-agent', seedA.agent);
  await page.waitForTimeout(400);
  const summary = await page.textContent('#sess-summary-line');
  const adapterRo = await page.getAttribute('#sess-bind-zone input[readonly]', 'value');
  const permVals = await page.evaluate(() => [...document.querySelectorAll('#sess-perm option')].map(o => o.value));
  await page.fill('#sess-cwd', '/tmp/agent-session/work');
  await page.click('#sess-form button[type="submit"]');
  await p_sleep(400);
  const toastTxt = await page.textContent('#toast').catch(() => '');
  const s03ok = optCap && !optCap.disabled && noOk
    && summary.includes('建立持久会话') && adapterRo === seedA.adapter
    && !permVals.includes('danger-full-access') && !permVals.includes('yolo')
    && toastTxt.includes('首轮消息');
  record('S03', '新建表单联动（选项按目录 session_adapter 能力分类禁选并注明/摘要行/适配器只读=创建回执声明/权限档剔除全权/空消息拦截）', s03ok,
    `cap=${JSON.stringify(optCap)} 禁选对照=${noAdapt.length}/${optsAll.length}${noAdapt.length ? '' : '（该节点开放能力均声明适配器——按分类全检，无禁选对照属环境事实）'} unknown=${unknownCap.length} adapterRo=${adapterRo} toast=${toastTxt.slice(0, 20)}`);

  // S10 hub 五视口整页截图（视觉层留档；此时三个种子会话都在列）。整页视觉判据
  // （R2 必修6 残余）：fullPage 截图逐张核对 PNG 像素尺寸——宽=视口宽、高≥页面
  // scrollHeight−2px 容差；不凭截图调用成功判「整页视觉」PASS。
  let shotFail = '', shotNotes = [];
  try {
    for (const [w, h] of VIEWPORTS) {
      await page.setViewportSize({ width: w, height: h });
      await p_sleep(450);
      const fullH = await page.evaluate(() => document.documentElement.scrollHeight);
      await shot(page, `hub-sessions-${w}x${h}`);
      const px = pngSize(`hub-sessions-${w}x${h}`);
      const dimOk = px.w === w && px.h >= fullH - 2;
      shotNotes.push(`${w}x${h} 页面${fullH}/图${px.h}${dimOk ? '' : '✗'}`);
      if (!dimOk) shotFail = shotFail || `${w}x${h} 图高${px.h} < 页面高${fullH}`;
    }
    await page.setViewportSize({ width: 1440, height: 900 });
  } catch (e) { shotFail = shotFail || e.message.slice(0, 80); }
  walkLog(`S10 整页视觉核对：${shotNotes.join(' ')}（判据=图高≥文档高；人眼核查入口=PNG 留档）`);
  record('S10', 'hub 会话页五视口整页截图（fullPage 覆盖完整文档高度：1440/1280/1024/680/390）', !shotFail, shotFail || shotNotes.join(' '), false, 'VISUAL');

  // S07 抽屉有轮次深层态（seedB，唯一 ID 定位）：焦点入层 → Tab 陷阱 → 轮次行实键
  // Enter 选中 → detail 警示条；unverified 徽标绝不渲染为成功；Esc 关层返焦
  await page.click(`tr[data-sess="${seedB.id}"]`);
  await page.waitForSelector('#layer .drawer', { timeout: 5000 });
  await page.waitForSelector('#layer .drawer tr[data-turn="1"]', { timeout: 15000 });
  await page.waitForFunction(() => document.querySelector('#layer .drawer tr[data-turn="1"] .badge')?.classList.contains('sst-unverified'), null, { timeout: 300000 }).catch(() => { });
  const bBadgeUnv = await page.evaluate(() => document.querySelector('#layer .drawer tr[data-turn="1"] .badge')?.classList.contains('sst-unverified'));
  const focusInDrawer = await page.evaluate(() => document.activeElement === document.querySelector('#layer .drawer'));
  /* Tab 陷阱断言（对齐 hub R3-P2-01 锚点语义）：容器（tabindex=-1，不在 items）上
   * 正向 Tab → 末位；再 Tab → 首位（循环）；首位 Shift+Tab → 末位。 */
  const drawerItems = () => page.evaluate(sel => {
    const items = [...document.querySelector(sel).querySelectorAll('a[href],button:not([disabled]),input:not([disabled]):not([type=hidden]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])')].filter(el => el.offsetParent !== null);
    return { n: items.length, activeIsFirst: document.activeElement === items[0], activeIsLast: document.activeElement === items[items.length - 1] };
  }, '#layer .drawer');
  await page.keyboard.press('Tab');   // 容器 → 末位（正向锚点）
  const tabToLast = (await drawerItems()).activeIsLast;
  await page.keyboard.press('Tab');   // 末位 → 首位（循环）
  const tabWrapFirst = (await drawerItems()).activeIsFirst;
  await page.keyboard.press('Shift+Tab');   // 首位 → 末位（反向循环）
  const tabShiftLast = (await drawerItems()).activeIsLast;
  let trapOk = tabToLast && tabWrapFirst && tabShiftLast;
  for (let i = 0; i < 10 && trapOk; i++) {   // 连按 Tab 焦点不出层
    await page.keyboard.press('Tab');
    trapOk = await page.evaluate(() => !!document.activeElement?.closest?.('#layer'));
  }
  // 轮次行实键盘选中（行 tabindex + Enter 等价点击，R1 必修6：tabindex 存在不证明键盘可达）
  await page.focus('#layer .drawer tr[data-turn="1"]');
  await page.keyboard.press('Enter');
  await p_sleep(500);
  const selOn = await page.evaluate(() => document.querySelector('#layer .drawer tr[data-turn="1"]')?.classList.contains('on'));
  const detailTxt = await page.textContent('#layer .drawer [data-zone="detail"]');
  const detailOk = detailTxt.includes('工作结果未证实') && !detailTxt.includes('已完成');
  await shot(page, 'hub-drawer-timeline');
  await shot(page, 'hub-drawer-turn-unverified');
  await page.keyboard.press('Escape');
  /* 返焦断言在 closeLayer 同步返焦后立即读（120ms）：再晚观察窗可能撞上轮询
   * 列表重绘——重绘换掉返焦落点的行节点会把焦点一并带到 body（负载相关时序，
   * 与关层返焦本身无关，S04 静置后的严格断言承担确定性验收）。 */
  await p_sleep(120);
  const refocusLoose = await page.evaluate(() => {
    const a = document.activeElement;
    return !!(a && (a.closest?.('tr[data-sess]') || a.closest?.('aside nav')));
  });
  walkLog(`S07 Esc 关层走查：返焦（宽松，+120ms）=${refocusLoose}（activeElement=${await page.evaluate(activeDesc)}）`);
  await p_sleep(500);
  const drawerGone = await page.locator('#layer .drawer').count() === 0;
  // chip 计数 0→1 跨越（R1 必修3 局部更新实证）：unverified 由零态跨到非零并退灰
  let chipSync = false;
  {
    const sessNow = (await hubApi(page, '/api/v1/sessions')).body?.sessions || [];
    const exp = deriveExp(sessNow);
    await page.waitForFunction(chipMatches, exp, { timeout: 12000 }).catch(() => { });
    chipSync = (exp.counts.unverified || 0) >= 1 && await page.evaluate(chipMatches, exp);
  }
  record('S07', '有轮次抽屉深层态（焦点入层/Tab 首末循环 10 连按不出层/实键 Enter 选中轮次/unverified 警示条且绝不渲染为成功/Esc 关层返焦/chip 零→非零同步变色）',
    focusInDrawer && trapOk && bBadgeUnv && selOn && detailOk && drawerGone && refocusLoose && chipSync,
    `badge=${bBadgeUnv} trap=${trapOk}(末${tabToLast}/循环首${tabWrapFirst}/反向末${tabShiftLast}) sel=${selOn} detail=${detailTxt.slice(0, 30).replace(/\s+/g, ' ')}… escGone=${drawerGone} refocus=${refocusLoose} chipSync=${chipSync}`, false, 'DEEP');

  // S08 失败轮次覆盖（seedC，可控数据）：等待终态应为 failed；抽屉徽标+详情错误条
  const cTerm = await hubWaitTerminal(page, seedC.id);
  await page.click(`tr[data-sess="${seedC.id}"]`);
  await page.waitForSelector('#layer .drawer', { timeout: 5000 });
  await page.waitForSelector('#layer .drawer tr[data-turn="1"]', { timeout: 10000 });   // 分区异步填充：轮次行就位再读徽标
  const cBadge = await page.evaluate(() => document.querySelector('#layer .drawer tr[data-turn="1"] .badge')?.classList.contains('sst-failed'));
  await page.click('#layer .drawer tr[data-turn="1"]');
  await p_sleep(500);
  const cDetail = await page.textContent('#layer .drawer [data-zone="detail"]');
  await page.keyboard.press('Escape');
  await p_sleep(500);
  record('S08', '失败轮次覆盖（坏工作目录可控 failed：时间线徽标 sst-failed/详情执行失败错误条）',
    cTerm === 'failed' && cBadge && cDetail.includes('执行失败'), `终态=${cTerm} badge=${cBadge} detail=${cDetail.slice(0, 30).replace(/\s+/g, ' ')}…`);

  // S04 空轮次抽屉 + 严格键盘闭环（seedA；B/C 已终态、静置一轮轮询让列表落定，行节点稳定）
  await p_sleep(5500);
  const rowA = await page.$(`tr[data-sess="${seedA.id}"]`);
  await rowA.click();
  await page.waitForSelector('#layer .drawer', { timeout: 5000 });
  const zones = await page.evaluate(() => [...document.querySelectorAll('#layer .drawer [data-zone]')].map(z => z.dataset.zone));
  const drawerTxt = await page.textContent('#layer .drawer');
  const zonesOk = JSON.stringify(zones) === JSON.stringify(['head', 'binding', 'turns', 'detail', 'actions'])
    && drawerTxt.includes('绑定与状态') && drawerTxt.includes('轮次（0）')
    && drawerTxt.includes('还没有轮次记录。') && drawerTxt.includes('轮次详情') && drawerTxt.includes('动作');
  const focusContainer = await page.evaluate(() => document.activeElement === document.querySelector('#layer .drawer'));
  /* Tab 陷阱断言（对齐 hub 四分支语义）：容器正向 Tab → 末位（R3-P2-01 正向锚点）；
   * 末位再正向 Tab → 首位（处理器 wrap）；首位 Shift+Tab → 末位（处理器反向 wrap）。
   * 中段位移走浏览器原生遍历，不出层即可。 */
  const s4Items = () => page.evaluate(sel => {
    const items = [...document.querySelector(sel).querySelectorAll('a[href],button:not([disabled]),input:not([disabled]):not([type=hidden]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])')].filter(el => el.offsetParent !== null);
    return { n: items.length, isFirst: document.activeElement === items[0], isLast: document.activeElement === items[items.length - 1] };
  }, '#layer .drawer');
  await page.keyboard.press('Tab');
  const firstOk = (await s4Items()).isLast;
  await page.keyboard.press('Tab');
  const wrapOk = (await s4Items()).isFirst;
  await page.keyboard.press('Shift+Tab');
  const lastOk = (await s4Items()).isLast;
  await page.keyboard.press('Escape');
  await p_sleep(600);
  const goneOk = await page.locator('#layer .drawer').count() === 0;
  const refocusRow = await page.evaluate(el => document.activeElement === el, rowA);
  walkLog(`S04 键盘走查：开层焦点=容器 ${focusContainer}；Tab→末 ${firstOk}；末→首循环 ${wrapOk}；首→末反向 ${lastOk}；Esc 关层 ${goneOk}；返焦原会话行 ${refocusRow}（activeElement=${await page.evaluate(activeDesc)}）`);
  record('S04', '空轮次抽屉分区（五分区同构/空轮次词典）+严格键盘（焦点入层/Tab 末→首→末循环/Esc 关层返焦原行）',
    zonesOk && focusContainer && firstOk && wrapOk && lastOk && goneOk && refocusRow,
    `zones=${zones.join(',')} focus=${focusContainer} tab=${firstOk}/${wrapOk}/${lastOk} esc=${goneOk} refocus=${refocusRow}`, false, 'DEEP');

  // S05 继续预填（列表行「继续」→ continue 形态：只读绑定/轮号/cwd 预填；改回新建）
  await page.click(`tr[data-sess="${seedA.id}"] [data-act="sess-continue"]`);
  await page.waitForSelector('text=继续会话', { timeout: 5000 });
  const s05 = await page.evaluate(() => {
    const ro = [...document.querySelectorAll('#sess-form input[readonly]')].length;
    const label = [...document.querySelectorAll('#sess-form label')].map(l => l.textContent).join('');
    return {
      ro: ro >= 4,   // 两级目标只读 + 适配器 + 权限只读
      label: label.includes('续轮消息（第 1 轮）'),
      cwd: document.querySelector('#sess-cwd')?.value === '/tmp/agent-session/work',
    };
  });
  await shot(page, 'hub-continue-prefill');
  await page.click('[data-act="sess-new"]');
  await page.waitForSelector('text=新建会话', { timeout: 5000 });
  record('S05', '继续预填（continue 形态只读绑定/第 1 轮标目/cwd 预填/改回新建）', s05.ro && s05.label && s05.cwd, JSON.stringify(s05));

  // S06 关闭确认（slim modal 整层语义，R1 必修6 深层验收：层可见/背景 inert/焦点入层/
  // 覆盖关系实证；「再想想」=整层关会话保活；确认后列表徽标按 ID 刷新为已关闭）
  await page.click(`tr[data-sess="${seedA.id}"]`);
  await page.waitForSelector('#layer .drawer', { timeout: 5000 });
  await page.click('[data-act="sess-close"]');
  await page.waitForSelector('#layer .modal', { timeout: 5000 });
  await p_sleep(650);   // 入场动画落定再量几何（modal 400ms spring 中途态给假差）
  const modalTxt = await page.textContent('#layer .modal');
  const modalDeep = await page.evaluate(() => {
    const m = document.querySelector('#layer .modal');
    const r = m.getBoundingClientRect();
    const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    return {
      visible: !!m && r.width > 0 && r.height > 0,
      cover: !!hit && !!hit.closest('#layer .modal'),
      noDrawer: !document.querySelector('#layer .drawer'),   // hub 整层语义：确认层打开时抽屉不在层内
      inert: document.getElementById('app')?.hasAttribute('inert'),
      focusIn: !!document.activeElement?.closest?.('#layer .modal'),
    };
  });
  await shot(page, 'hub-close-confirm-modal');
  walkLog(`S06 确认层走查：visible=${modalDeep.visible} 覆盖(elementFromPoint 命中 modal)=${modalDeep.cover} 整层语义(抽屉不在层)=${modalDeep.noDrawer} 背景 inert=${modalDeep.inert} 焦点入层=${modalDeep.focusIn}`);
  await page.click('#layer .modal [data-act="close"]');   // 再想想：整层关（task-cancel 同族）
  await page.waitForSelector('#layer .modal', { state: 'detached', timeout: 5000 });
  const stillActive = await page.evaluate(id => !document.querySelector(`tr[data-sess="${id}"]`)?.textContent.includes('已关闭'), seedA.id);
  await page.click(`tr[data-sess="${seedA.id}"]`);
  await page.waitForSelector('#layer .drawer', { timeout: 5000 });
  await page.click('[data-act="sess-close"]');
  await page.waitForSelector('#layer .modal [data-act="sess-close-confirm"]', { timeout: 5000 });
  await page.click('[data-act="sess-close-confirm"]');
  await page.waitForSelector('#layer .drawer', { state: 'detached', timeout: 8000 });
  let closedBadge = false;
  for (let i = 0; i < 8 && !closedBadge; i++) {   // 列表刷新走统一槽位入口（R1 必修3）
    await p_sleep(1200);
    closedBadge = await page.evaluate(id => document.querySelector(`tr[data-sess="${id}"]`)?.textContent.includes('已关闭'), seedA.id);
  }
  // 关闭后 chips/页脚随统一入口刷新（与 API 数据保持一致；关闭会话仍计数在「全部」）
  const sessAfter = (await hubApi(page, '/api/v1/sessions')).body?.sessions || [];
  await page.waitForFunction(chipMatches, deriveExp(sessAfter), { timeout: 12000 }).catch(() => { });
  const afterOk = await page.evaluate(chipMatches, deriveExp(sessAfter));
  const footOk = await page.evaluate(n => document.getElementById('sess-foot-zone')?.textContent.includes(`${n} 个会话`), sessAfter.length);
  await shot(page, 'hub-list-after-close');
  record('S06', '关闭确认深层态（slim modal 警示词/层可见+背景 inert+覆盖关系+焦点入层/再想想整层关且会话保活/确认后列表徽标+chips+页脚按 ID 刷新）',
    modalTxt.includes('不可撤销') && modalTxt.includes('确认关闭') && modalDeep.visible && modalDeep.cover && modalDeep.noDrawer && modalDeep.inert && modalDeep.focusIn && stillActive && closedBadge && afterOk && footOk,
    `modal=${modalTxt.slice(0, 30)}… deep=${JSON.stringify(modalDeep)} closed=${closedBadge} chips=${afterOk} foot=${footOk}`, false, 'DEEP');

  // S09 会话展示面字号地板 + running 脉冲 reduced-motion 降级（视觉层实证）
  await page.click(`tr[data-sess="${seedB.id}"]`);
  await page.waitForSelector('#layer .drawer', { timeout: 5000 });
  const floorBad = await cssFloorCheck(page, '.sess-scope');   // 表单卡+列表卡+打开中的会话抽屉
  const animNormal = await page.evaluate(() => {
    const s = document.createElement('span');
    s.className = 'badge sst-running';
    document.querySelector('#layer').appendChild(s);
    const an = getComputedStyle(s, ':before').animationName;
    s.remove();
    return an;
  });
  await page.keyboard.press('Escape');
  const animReduced = await reducedMotionCheck(browser, BASE);
  record('S09', '会话展示面字号地板（small/.small/footer-note/code computed ≥12.5px，含抽屉）+sst-running 脉冲 reduced-motion 停',
    floorBad.length === 0 && animNormal === 'pulse-dot' && animReduced === 'none',
    `违例=${JSON.stringify(floorBad)} anim=${animNormal}/reduce=${animReduced}`, false, 'VISUAL');
  flushWalk('hub');
  summarize();
}

// ---------- node 会话套件（T3，R1 必修6 重写）：SN01-SN06（真实 zcode 轮次全链） ----------
// 受控数据经 localapi REST（Bearer local_token）构造并绑定创建回执唯一 session ID；
// SN01 绑定 UI 提交的网络应答；unverified/failed 分别覆盖；实按键 + 叠加层几何/inert/
// 焦点陷阱深层断言；截图矩阵与走查记录分层留档。
const nodeApi = async (path, opt = {}) => {
  const r = await fetch(BASE + path, {
    method: opt.method || 'GET',
    headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + NODE_TOKEN },
    body: opt.body,
  });
  return { code: r.status, body: await r.json().catch(() => ({})) };
};
async function nodeSessFlow(browser) {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, ignoreHTTPSErrors: true });
  globalThis.__page = page;
  page.setDefaultTimeout(15000);
  console.log(`node 会话套件：${BASE}（令牌门登录，local_token 经环境变量传入）`);
  await page.goto(BASE, { waitUntil: 'networkidle' });
  if (await page.locator('#gate-token').count()) {
    await page.fill('#gate-token', NODE_TOKEN);
    await page.click('[data-act="gate-enter"]');
  }
  await page.waitForSelector('#logout:not([hidden])', { timeout: 15000 });

  // SN01 本机会话区块+新建全链：区块渲染（禁选注明）→ 表单真实建会话投首轮 →
  // 受理网络应答绑定唯一 session ID → 列表行按该 ID 出现
  await page.click('aside nav button[data-go="tasks"]');
  await page.waitForSelector('#sess-card', { timeout: 15000 });
  const cardTxt = await page.textContent('#sess-card');
  const optNo = await page.evaluate(() => {
    const o = [...document.querySelectorAll('#sess-agent option')].find(o => o.disabled && o.value);
    return o ? o.textContent.includes('未声明会话适配器') : false;
  });
  const capName = await page.evaluate(() => {
    const o = [...document.querySelectorAll('#sess-agent option')].find(o => !o.disabled && o.value);
    return o ? o.value : '';
  });
  if (!capName) {
    record('SN01', '本机会话新建全链（区块渲染/禁选注明/受理回执/列表行）', false, '本机无可会话能力——SN 系列未验收，环境缺声明 session_adapter 的能力', true);
    flushWalk('node');
    summarize();
    return;
  }
  await page.selectOption('#sess-agent', capName);
  await page.fill('#sess-cwd', '/tmp/agent-session/work');
  await page.fill('#sess-prompt', 'E2E 冒烟：请只回复 OK 两个字母，不要读取文件，不要执行任何命令。');
  const [resp] = await Promise.all([
    page.waitForResponse(r => r.url().includes('/api/v1/local/sessions') && !r.url().includes('/turns') && r.request().method() === 'POST'),
    page.click('[data-act="sess-submit"]'),
  ]);
  const ackBody = await resp.json().catch(() => ({}));
  const sid = ackBody?.turn?.session_id || '';   // 创建回执唯一 ID：后续全部断言按它定位
  const ackTxt = await page.textContent('#sess-card .notice.ok').catch(() => '');
  await page.waitForSelector(`tr[data-sess="${sid}"]`, { timeout: 15000 });
  record('SN01', '本机会话新建全链（区块渲染/未声明禁选注明/受理回执/网络应答绑定唯一 session ID/列表行按 ID 出现）',
    cardTxt.includes('本机会话') && optNo && !!sid && ackTxt.includes('已受理'), `cap=${capName} sid=${sid || '（未取得）'}`);

  // SN05 node 五视口整页截图（视觉层留档；会话已在列）。整页判据同 S10（R2 必修6
  // 残余）：fullPage 像素尺寸逐张核对，不凭截图调用成功判 PASS。
  let snShotFail = '', snShotNotes = [];
  try {
    for (const [w, h] of VIEWPORTS) {
      await page.setViewportSize({ width: w, height: h });
      await p_sleep(450);
      const fullH = await page.evaluate(() => document.documentElement.scrollHeight);
      await shot(page, `node-tasks-${w}x${h}`);
      const px = pngSize(`node-tasks-${w}x${h}`);
      const dimOk = px.w === w && px.h >= fullH - 2;
      snShotNotes.push(`${w}x${h} 页面${fullH}/图${px.h}${dimOk ? '' : '✗'}`);
      if (!dimOk) snShotFail = snShotFail || `${w}x${h} 图高${px.h} < 页面高${fullH}`;
    }
    await page.setViewportSize({ width: 1440, height: 900 });
  } catch (e) { snShotFail = snShotFail || e.message.slice(0, 80); }
  walkLog(`SN05 整页视觉核对：${snShotNotes.join(' ')}（判据=图高≥文档高；人眼核查入口=PNG 留档）`);
  record('SN05', 'node 任务页五视口整页截图（fullPage 覆盖完整文档高度：1440/1280/1024/680/390）', !snShotFail, snShotFail || snShotNotes.join(' '), false, 'VISUAL');

  // SN02 抽屉四分区+真实轮次终态（unverified 严格覆盖，failed 拆到 SN04）：
  // 焦点入层 → 等轮次 unverified → 实键 Enter 选中轮次 → 警示条 → Esc 返焦原行
  await page.click(`tr[data-sess="${sid}"]`);
  await page.waitForSelector('#sess-drawer', { timeout: 5000 });
  const zones = await page.evaluate(() => [...document.querySelectorAll('#sess-drawer [data-zone]')].map(z => z.dataset.zone));
  const focusInDrawer = await page.evaluate(() => document.activeElement === document.querySelector('#sess-drawer'));
  await page.waitForFunction(() => {
    const b = document.querySelector('#sess-drawer tr[data-turn="1"] .badge');
    return b && (b.classList.contains('sst-unverified') || b.classList.contains('sst-failed'));
  }, null, { timeout: 300000 }).catch(() => { });
  const bBadgeUnv = await page.evaluate(() => document.querySelector('#sess-drawer tr[data-turn="1"] .badge')?.classList.contains('sst-unverified'));
  await page.focus('#sess-drawer tr[data-turn="1"]');
  await page.keyboard.press('Enter');   // 行键盘可达：实键选中 → detail 分区
  await p_sleep(500);
  const selOn = await page.evaluate(() => document.querySelector('#sess-drawer tr[data-turn="1"]')?.classList.contains('on'));
  const detailTxt = await page.textContent('#sess-drawer [data-zone="detail"]');
  const detailOk = bBadgeUnv && detailTxt.includes('工作结果未证实') && !detailTxt.includes('已完成');
  await shot(page, 'node-drawer-timeline');
  await shot(page, 'node-drawer-turn-unverified');
  await page.keyboard.press('Escape');   // 此刻列表刚随终态刷新过（原返焦节点可能失存）：只断言焦点未落 body
  await p_sleep(600);
  const drawerGone1 = await page.locator('#sess-drawer').count() === 0;
  const refocusLoose = await page.evaluate(() => {
    const a = document.activeElement;
    return !!(a && (a.closest?.('tr[data-sess]') || a.closest?.('aside nav')));
  });
  // 静置一轮轮询（列表已稳定）→ 重开抽屉 → Esc：严格断言返焦原会话行（建议1 的实键盘回归）
  await p_sleep(5500);
  await page.click(`tr[data-sess="${sid}"]`);
  await page.waitForSelector('#sess-drawer', { timeout: 5000 });
  await p_sleep(600);
  await page.keyboard.press('Escape');
  await p_sleep(600);
  const refocusRow = await page.evaluate(id => document.activeElement?.closest?.('tr[data-sess]')?.getAttribute('data-sess') === id, sid);
  walkLog(`SN02 键盘走查：开层焦点=抽屉容器 ${focusInDrawer}；实键 Enter 选中轮次 sel=${selOn}；Esc 关层 ${drawerGone1} 返焦（宽松）=${refocusLoose}；静置重开再 Esc 严格返焦原行=${refocusRow}（activeElement=${await page.evaluate(activeDesc)}）`);
  record('SN02', '抽屉四分区+真实轮次 unverified 终态（严格：绝不接受 failed 冒充；焦点入层/实键 Enter 选中/警示条且绝不渲染为成功/Esc 关层返焦原行）',
    zones.join(',') === 'head,binding,turns,detail,actions' && focusInDrawer && bBadgeUnv && selOn && detailOk && drawerGone1 && refocusLoose && refocusRow,
    `zones=${zones.length} unverified=${bBadgeUnv} sel=${selOn} detail=${detailTxt.slice(0, 30).replace(/\s+/g, ' ')}… escGone=${drawerGone1} refocus=${refocusLoose}/${refocusRow}`, false, 'DEEP');

  // SN07 整页轮询豁免（R1 必修10 行为验证；R2 必修10 收敛新判据）。两相位各经 API
  // 建一个临时会话制造常规数据变化（其首轮即真实任务行——本地派单需钦定统筹，不可
  // 用）：相位A 抽屉开放、相位B 焦点在会话表单，断言页面 DOM 节点身份/焦点/光标/
  // 滚动在数据变化期保持，任务列表照常刷新，且会话列表照常跟进。
  // 判据与内核语义对齐（sessionx createWithID：create+首轮同一执行体，首轮 CLI 执
  // 行完成才发布映射——新建会话 ID 在首轮完成前结构性不在 GET /sessions 列表，实测
  // 实证）。因此「新建会话 ID 进入列表」以首轮完成上限 300s 等待（与 SN02 轮次终态
  // 同上限，超时=FAIL 不吞）；等待期间该轮任务行状态多轮推进使 loadAll 连续多轮
  // changed=true，豁免分支被反复执行——正是被测路径。「状态推进可见」用关闭翻转：
  // 已发布会话的 close 同步即时，下一轮询徽标必翻 sst-closed（仍处豁免态，列表刷
  // 新恰经修复后的公共加载路径——R2 N1 回归面）。
  const nodeCount = () => page.evaluate(() => document.querySelectorAll('tr[data-task]').length);
  const scratchSession = () => nodeApi('/api/v1/local/sessions', {
    method: 'POST',
    body: JSON.stringify({ agent: capName.split('|')[1] || capName, cwd: '/tmp/agent-session/work', prompt: 'E2E 轮询豁免验证：请只回复 OK 两个字母，不要读取文件，不要执行任何命令。' }),
  });
  const closeScratch = id => nodeApi(`/api/v1/local/sessions/${encodeURIComponent(id)}/close`, { method: 'POST', body: '{}' });
  /* 行/徽标等待都返回布尔（超时=FAIL 不吞）；列表行按 data-sess 属性值比对，不拼选择器。 */
  const rowInList = (id, timeout) => page.waitForFunction(sid => [...document.querySelectorAll('#sess-list-zone tr[data-sess]')].some(t => t.getAttribute('data-sess') === sid), id, { timeout }).then(() => true).catch(() => false);
  const badgeFlip = (id, cls, timeout) => page.waitForFunction(({ sid, c }) => [...document.querySelectorAll('#sess-list-zone tr[data-sess] .badge')].some(b => b.closest('tr')?.getAttribute('data-sess') === sid && b.classList.contains(c)), { sid: id, c: cls }, { timeout }).then(() => true).catch(() => false);
  // 相位A：抽屉开放态
  await page.click(`tr[data-sess="${sid}"]`);
  await page.waitForSelector('#sess-drawer', { timeout: 5000 });
  await page.evaluate(() => { window.__markA = [document.querySelector('main h1'), document.querySelector('#sess-form-zone'), document.querySelector('#sess-list-zone')]; });
  const cntA0 = await nodeCount();
  const scratchA = await scratchSession();
  const sidA = scratchA?.body?.turn?.session_id || '';
  const grewA = await page.waitForFunction(n => document.querySelectorAll('tr[data-task]').length > n, cntA0, { timeout: 30000 }).then(() => true).catch(() => false);
  const inListA = sidA ? await rowInList(sidA, 300000) : false;
  const phaseA = await page.evaluate(() => {
    const now = [document.querySelector('main h1'), document.querySelector('#sess-form-zone'), document.querySelector('#sess-list-zone')];
    return {
      identityKept: window.__markA.every((el, i) => el && el === now[i]),
      tasksRefreshed: document.querySelectorAll('tr[data-task]').length > 0,
    };
  });
  const cntA1 = await nodeCount();
  let closedA = false;
  if (inListA) { await closeScratch(sidA); closedA = await badgeFlip(sidA, 'sst-closed', 30000); }
  await page.keyboard.press('Escape');   // 关抽屉
  await p_sleep(500);
  // 相位B：焦点在会话表单（先复位 SN01 的 acked 锁定态 → 新建表单；cwd 输入 + 光标定位 + 页面滚动）
  await page.click('#sess-card [data-act="sess-new"]');
  await page.waitForSelector('text=新建会话', { timeout: 5000 });
  await page.fill('#sess-cwd', '/tmp/agent-session/work/keep-focus');
  await page.evaluate(() => {
    const inp = document.querySelector('#sess-cwd');
    inp.focus();
    inp.setSelectionRange(6, 6);
    window.scrollTo(0, 260);
    window.__markB = document.querySelector('#sess-form-zone');
  });
  const cntB0 = await nodeCount();
  const scratchB = await scratchSession();
  const sidB = scratchB?.body?.turn?.session_id || '';
  const grewB = await page.waitForFunction(n => document.querySelectorAll('tr[data-task]').length > n, cntB0, { timeout: 30000 }).then(() => true).catch(() => false);
  const inListB = sidB ? await rowInList(sidB, 300000) : false;
  const phaseB = await page.evaluate(() => {
    const inp = document.querySelector('#sess-cwd');
    return {
      identityKept: window.__markB && document.querySelector('#sess-form-zone') === window.__markB,
      focusKept: document.activeElement === inp,
      caretKept: inp?.selectionStart === 6,
      scrollKept: Math.abs(window.scrollY - 260) < 40,
    };
  });
  const cntB1 = await nodeCount();
  let closedB = false;
  if (inListB) { await closeScratch(sidB); closedB = await badgeFlip(sidB, 'sst-closed', 30000); }
  walkLog(`SN07 轮询豁免走查：抽屉开放态 节点身份=${phaseA.identityKept} 任务数 ${cntA0}→${cntA1} 增长=${grewA} 临时会话 ${sidA || '（创建失败）'} 入列表=${inListA} 关闭徽标翻转=${closedA}；表单焦点态 DOM=${phaseB.identityKept} 焦点=${phaseB.focusKept} 光标=${phaseB.caretKept} 滚动=${phaseB.scrollKept} 任务数 ${cntB0}→${cntB1} 增长=${grewB} 临时会话 ${sidB || '（创建失败）'} 入列表=${inListB} 关闭徽标翻转=${closedB}（入列表等待跨首轮完成上限 300s，其间任务状态多轮推进连续驱动豁免分支）`);
  record('SN07', '会话表单/抽屉开放态整页轮询豁免（数据变化期 DOM 身份/焦点/光标/滚动保持；等待超时不吞：任务真实增长、新建会话 ID 在首轮完成后进入列表、豁免态下关闭徽标翻转可见）',
    phaseA.identityKept && grewA && cntA1 > cntA0 && inListA && closedA
    && phaseB.identityKept && phaseB.focusKept && phaseB.caretKept && phaseB.scrollKept && grewB && cntB1 > cntB0 && inListB && closedB,
    `A=${JSON.stringify(phaseA)} 任务 ${cntA0}→${cntA1} 增长=${grewA} 入列表=${inListA} 关闭翻转=${closedA}；B=${JSON.stringify(phaseB)} 任务 ${cntB0}→${cntB1} 增长=${grewB} 入列表=${inListB} 关闭翻转=${closedB}`, false, 'DEEP');

  // 继续预填留档（node 形态：单下拉 continue，只读绑定/轮号/cwd 预填；轮号按实际
  // 轮数推进——SN02 后该会话已 1 轮，continue 应预填第 2 轮）
  const turnCount = (await nodeApi('/api/v1/local/sessions/' + encodeURIComponent(sid))).body?.session?.turns?.length ?? 1;
  await page.click(`tr[data-sess="${sid}"] [data-act^="sess-continue"]`);
  await page.waitForSelector('text=继续会话', { timeout: 5000 });
  const prefill = await page.evaluate(n => ({
    ro: [...document.querySelectorAll('#sess-form-zone input[readonly]')].length >= 3,
    label: [...document.querySelectorAll('#sess-form-zone label')].map(l => l.textContent).join('').includes(`续轮消息（第 ${n} 轮）`),
    cwd: document.querySelector('#sess-cwd')?.value === '/tmp/agent-session/work',
  }), turnCount + 1);
  await shot(page, 'node-continue-prefill');
  await page.click('[data-act="sess-new"]');
  await page.waitForSelector('text=新建会话', { timeout: 5000 });
  walkLog(`继续预填走查：readonly=${prefill.ro} 续轮标目=第 ${turnCount + 1} 轮 ${prefill.label} cwd=${prefill.cwd}（截图 node-continue-prefill.png）`);

  // SN04 失败轮次覆盖（可控数据，node 形态）：create+首轮为同一执行体——首轮失败即
  // 创建失败、会话不发布（T2 创建门语义，与 hub「先注册后异步轮」不同），故坏 cwd 不
  // 能落在已存在会话上。改为：合法目录建会话（首轮成功）→ 删除目录 → 续轮 spawn 失败
  // → 轮 2 落 failed（会话仍在，失败轮可查）。
  const failDir = `/tmp/agent-session/e2e-turnfail-${Date.now()}`;
  fs.mkdirSync(failDir, { recursive: true });
  const fSeed = await nodeApi('/api/v1/local/sessions', {
    method: 'POST',
    body: JSON.stringify({ agent: capName.split('|')[1] || capName, cwd: failDir, prompt: 'E2E 失败覆盖第 1 步：请只回复 OK，不要读取文件，不要执行任何命令。' }),
  });
  const fId = fSeed.body?.turn?.session_id || '';
  let fTerm1 = '', fTerm2 = '';
  if (fId) {
    const t0 = Date.now();
    while (Date.now() - t0 < 300000) {   // 等首轮终态（应 unverified；重负载下任务队列可能排队）
      const d = await nodeApi('/api/v1/local/sessions/' + encodeURIComponent(fId));
      const t = (d.body?.session?.turns || [])[0];
      if (t && !['pending', 'awaiting_node', 'running'].includes(t.status)) { fTerm1 = t.status; break; }
      await p_sleep(3000);
    }
    fs.rmSync(failDir, { recursive: true, force: true });   // 摘掉工作目录
    if (fTerm1 === 'ok' || fTerm1 === 'unverified') {   // node 内核轮行终态 ok/failed（work_result 恒 unverified，SN02 已严格覆盖）
      await nodeApi(`/api/v1/local/sessions/${encodeURIComponent(fId)}/turns`, {
        method: 'POST', body: JSON.stringify({ prompt: 'E2E 失败覆盖第 2 步：工作目录已删除，预期执行失败。' }),
      });
      const t1 = Date.now();
      while (Date.now() - t1 < 300000) {
        const d = await nodeApi('/api/v1/local/sessions/' + encodeURIComponent(fId));
        const turns = d.body?.session?.turns || [];
        const t2 = turns.find(t => t.seq === 2);
        if (t2 && !['pending', 'awaiting_node', 'running'].includes(t2.status)) { fTerm2 = t2.status; break; }
        await p_sleep(3000);
      }
    }
  }
  let badBadge = false, badDetail = false;
  if (fId && fTerm2 === 'failed') {
    await page.waitForSelector(`tr[data-sess="${fId}"]`, { timeout: 15000 });
    await page.click(`tr[data-sess="${fId}"]`);
    await page.waitForSelector('#sess-drawer', { timeout: 5000 });
    await page.waitForSelector('#sess-drawer tr[data-turn="2"]', { timeout: 10000 });   // 分区异步填充：轮 2 行就位再读徽标
    badBadge = await page.evaluate(() => document.querySelector('#sess-drawer tr[data-turn="2"] .badge')?.classList.contains('sst-failed'));
    await page.click('#sess-drawer tr[data-turn="2"]');
    await p_sleep(500);
    badDetail = (await page.textContent('#sess-drawer [data-zone="detail"]')).includes('执行失败');
    await page.keyboard.press('Escape');
    await p_sleep(500);
  }
  record('SN04', '失败轮次覆盖（删目录可控续轮 failed：会话仍在/轮 2 徽标 sst-failed/详情执行失败错误条）',
    fId && (fTerm1 === 'ok' || fTerm1 === 'unverified') && fTerm2 === 'failed' && badBadge && badDetail,
    `sid=${fId || '（创建失败）'} 轮1=${fTerm1 || 'timeout'} 轮2=${fTerm2 || 'timeout'} badge=${badBadge} detail=${badDetail}`);

  // SN03 关闭确认（confirmModal 追加式叠加层，R1 必修6 深层验收）：叠加层下抽屉原位
  // 保留；层可见/覆盖关系/背景 inert/焦点陷阱实键；Esc 只摘确认层并返焦「关闭会话」钮；
  // 确认后抽屉头部与列表按 ID 双双刷新为已关闭
  await page.click(`tr[data-sess="${sid}"]`);
  await page.waitForSelector('#sess-drawer', { timeout: 5000 });
  await page.click('#sess-drawer [data-act^="sess-close:"]');
  await page.waitForSelector('#confirm-modal', { timeout: 5000 });
  await p_sleep(650);   // 入场动画落定再量几何
  const confirmDeep = await page.evaluate(() => {
    const m = document.querySelector('#confirm-modal');
    const r = m.getBoundingClientRect();
    const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    return {
      visible: !!m && r.width > 0 && r.height > 0,
      cover: !!hit && !!hit.closest('#confirm-modal'),   // 覆盖关系：命中点在最上层确认层
      drawerKept: !!document.querySelector('#sess-drawer'),   // 叠加层下抽屉原位保留（node 形态）
      overlays: document.querySelectorAll('#layer > .overlay').length,
      inert: document.getElementById('app')?.hasAttribute('inert'),
      focusIn: !!document.activeElement?.closest?.('#confirm-modal'),
    };
  });
  let trapOk = true;
  for (let i = 0; i < 6 && trapOk; i++) {   // Tab 连按不出确认层（焦点陷阱）
    await page.keyboard.press('Tab');
    trapOk = await page.evaluate(() => !!document.activeElement?.closest?.('#confirm-modal'));
  }
  await shot(page, 'node-close-confirm-overlay');
  walkLog(`SN03 确认层走查：visible=${confirmDeep.visible} 覆盖(elementFromPoint 命中确认层)=${confirmDeep.cover} 抽屉原位保留=${confirmDeep.drawerKept} 叠加 overlay 数=${confirmDeep.overlays} 背景 inert=${confirmDeep.inert} 焦点入层=${confirmDeep.focusIn} Tab×6 陷阱=${trapOk}`);
  await page.keyboard.press('Escape');   // 顶层关 Esc：只摘确认层，抽屉保留，返焦「关闭会话」钮
  await p_sleep(500);
  const escConfirm = await page.evaluate(() => ({
    confirmGone: !document.querySelector('#confirm-modal'),
    drawerKept: !!document.querySelector('#sess-drawer'),
    focusBack: (document.activeElement?.dataset?.act || '').startsWith('sess-close'),
  }));
  // 再次打开确认层并确认关闭
  await page.click('#sess-drawer [data-act^="sess-close:"]');
  await page.waitForSelector('#confirm-modal', { timeout: 5000 });
  await page.click('#confirm-modal [data-act="confirm-ok"]');
  await p_sleep(800);
  let closedOk = false;
  for (let i = 0; i < 6 && !closedOk; i++) {
    await p_sleep(1500);
    closedOk = await page.evaluate(id => {
      const headBadge = document.querySelector('#sess-drawer [data-zone="head"] .badge')?.classList.contains('sst-closed');
      const listBadge = document.querySelector(`tr[data-sess="${id}"] .badge`)?.classList.contains('sst-closed');
      return headBadge && listBadge;
    }, sid);
  }
  await shot(page, 'node-after-close');
  record('SN03', '关闭确认深层态（叠加层下抽屉原位保留/层可见+覆盖关系+背景 inert+焦点陷阱实键/Esc 只摘确认层返焦关闭钮/确认后头部与列表按 ID 双刷新已关闭）',
    confirmDeep.visible && confirmDeep.cover && confirmDeep.drawerKept && confirmDeep.overlays === 2 && confirmDeep.inert && confirmDeep.focusIn && trapOk
    && escConfirm.confirmGone && escConfirm.drawerKept && escConfirm.focusBack && closedOk,
    `deep=${JSON.stringify(confirmDeep)} trap=${trapOk} esc=${JSON.stringify(escConfirm)} closed=${closedOk}`, false, 'DEEP');

  // SN06 会话展示面字号地板 + running 脉冲 reduced-motion 降级（视觉层实证；抽屉仍开）
  const floorBad = await cssFloorCheck(page, '.sess-scope');   // #sess-card + 打开中的 #sess-drawer
  const animNormal = await page.evaluate(() => {
    const s = document.createElement('span');
    s.className = 'badge sst-running';
    document.querySelector('#layer').appendChild(s);
    const an = getComputedStyle(s, ':before').animationName;
    s.remove();
    return an;
  });
  const animReduced = await reducedMotionCheck(browser, BASE);
  record('SN06', '会话展示面字号地板（small/.small/footer-note/code computed ≥12.5px，含卡与抽屉）+sst-running 脉冲 reduced-motion 停',
    floorBad.length === 0 && animNormal === 'pulse-dot' && animReduced === 'none',
    `违例=${JSON.stringify(floorBad)} anim=${animNormal}/reduce=${animReduced}`, false, 'VISUAL');
  flushWalk('node');
  summarize();
}

async function main() {
  if (SUITE === 'sessions') BASE = assertLocalBase(SUITE);   // R1 必修1：会话套件先过安全门，浏览器启动前拦截
  const browser = await chromium.launch();
  if (NODE_MODE) {
    if (SUITE === 'sessions') await nodeSessFlow(browser);   // 套件内自调 summarize
    else await nodeFlow(browser);
    await browser.close();
    if (SUITE !== 'sessions') summarize();
    return;
  }
  if (SUITE === 'sessions') {
    await hubSessFlow(browser);   // 套件内自调 summarize
    await browser.close();
    return;
  }
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, acceptDownloads: true });
  globalThis.__page = page;
  page.setDefaultTimeout(15000);

  // ---------- 账户状态探测：走 setup 或密码登录 ----------
  let initialized = false;
  try {
    const r = await page.request.get(`${BASE}/api/v1/auth/account`);
    initialized = (await r.json()).initialized === true;
  } catch { }
  console.log(`账户状态: ${initialized ? '已初始化（走密码登录）' : '未初始化（走首次设置）'}`);

  await page.goto(BASE, { waitUntil: 'networkidle' });

  // TC-E01 首次初始化（仅未初始化时）
  if (!initialized) {
    await page.fill('#su-user', USER);
    await page.fill('#su-pass', PASS);
    await page.fill('#su-pass2', PASS);
    await page.click('#setup-form button[type=submit]');
    await page.waitForSelector('text=总览', { timeout: 15000 });
    record('E01', '首次初始化：创建账户并自动登录进入总览', true);
  } else {
    // TC-E02 密码登录
    await page.fill('#login-user', USER);
    await page.fill('#login-pass', PASS);
    await page.click('#pw-login-form button[type=submit]');
    await page.waitForSelector('text=总览', { timeout: 15000 });
    record('E02', '密码登录进入总览', true);
  }

  // TC-E-NAV 导航统一（TASK-nav-filter B）：aside 内定位一律用结构化选择器
  //（aside nav button[data-page=…]），词表逐项断言（:has-text("任务") 会宽泛命中，
  // codex 必须修项）；aria-current 指向当前页。
  const HUB_NAV = ['总览', '机器', '任务', '会话', '派发', '工作单', '群聊', '策略容量', '设置'];
  const hubNav = (await page.locator('aside nav button').allTextContents())
    .map(s => s.replace(/^0\d/, '').replace(/\s*\(\d+\)$/, '').trim());
  record('E-NAV', 'hub 导航词表：9 项名称与顺序（T3 起「会话」顺延第 4 位）', JSON.stringify(hubNav) === JSON.stringify(HUB_NAV), hubNav.join('/'));
  const curNav = ((await page.locator('aside nav button[aria-current="page"]').textContent()) || '').replace(/^0\d/, '').trim();
  record('E-NAV2', 'hub 导航 aria-current 指向当前页（总览）', curNav === '总览', curNav);

  // ---------- 总览页 ----------
  // TC-E03 统计卡与中心端状态
  await page.waitForSelector('text=中心端状态');
  const metricsOk = await page.locator('.metric').count() >= 3;
  const focusOk = await page.locator('text=需要你关注').count() >= 1;
  record('E03', '总览：统计卡与「需要你关注」卡渲染', metricsOk && focusOk);

  // TC-E04 状态分布下钻 chips（2026-10-22 总览下钻重构：七态 chip 直达任务页对应筛选，无「全部」chip）
  const chips = await page.locator('.chip-link').count();
  record('E04', '总览：状态分布下钻 chips（八态：七态+取消后新增 cancelled）', chips === 8, `实际 ${chips}`);

  // ---------- 节点接入页 ----------
  await page.click('aside nav button[data-page="nodes"]'); // E05 结构化定位（总览入口现词「＋ 添加机器」，「节点接入」为旧文案断言漂移，顺修
  await page.waitForSelector('text=已登记机器'); // 现词（旧「已登记节点」为既有漂移，顺修）
  const bodyText = await page.textContent('body');
  const hasMac = bodyText.includes('mac-mini'), hasBaidu = bodyText.includes('baidu-node');
  const onlineCount = (bodyText.match(/在线/g) || []).length;
  record('E05', '节点接入：双节点在列且在线', hasMac && hasBaidu && onlineCount >= 2);

  // TC-E06 接入向导 4 步走通（2026-10-22 向导重构后适配；2026-10-26 nav-filter 验收顺修——
  // 「生成接入配置」为更早期流程的过时断言，现流程：机器页「添加机器」开向导 Step1）
  // Step1 命名 → Step2 token 生成与登记片段 → Step3 部署方式 → Step4 关闭
  await page.click('button:has-text("添加机器")');
  await page.waitForSelector('#enroll-id');
  await page.fill('#enroll-id', 'e2e-drill');
  await page.click('[data-act="enroll-next1"]');
  await page.waitForSelector('[data-act="enroll-pasted"]');
  const hubSnap = await page.textContent('.modal');
  record('E06', '接入向导：Step2 生成令牌+hub 登记片段（真实值非占位）', hubSnap.includes('e2e-drill') && hubSnap.includes('hub.yaml') && hubSnap.includes('复制令牌'));
  await page.click('[data-act="enroll-pasted"]');
  await page.waitForSelector('[data-act="enroll-next3"]');
  await page.click('[data-act="enroll-next3"]');
  await page.waitForSelector('[data-act="enroll-later"]');
  await page.click('[data-act="enroll-later"]');
  await page.waitForSelector('#enroll-id', { state: 'detached' });

  // ---------- 任务页（导航定位用结构化选择器：aside nav button[data-page]） ----------
  await page.click('aside nav button[data-page="tasks"]');
  await page.waitForSelector('th:has-text("提交时间")', { timeout: 15000 }); // 任务表头唯一，option 不会误匹配
  const taskRows = await page.locator('tr[data-task]').count();
  record('E07', '任务：历史任务列表渲染', taskRows >= 1, `行数 ${taskRows}`);

  // TC-E20 目标机器筛选（TASK-nav-filter A2）：选项来自快照去重；选中后行全部命中
  // 该机器。导出与批量共用 filteredTasks——终审必修4：名称声称同源过滤就必须实测，
  // E20x 真实触发下载并解析导出 JSON。清除路径见 E20b/E20c。
  const nodeVals = await page.evaluate(() => {
    const sel = document.querySelector('select[data-f="node"]');
    return sel ? [...sel.options].map(o => o.value).filter(Boolean) : [];
  });
  if (nodeVals.length) {
    await page.selectOption('select[data-f="node"]', nodeVals[0]);
    await page.waitForTimeout(300);
    const rowNodes = await page.evaluate(() =>
      [...document.querySelectorAll('tr[data-task] td:nth-child(2)')].map(td => td.textContent.trim()));
    const allHit = rowNodes.length > 0 && rowNodes.every(t => t === nodeVals[0]);
    record('E20', '目标机器筛选：行全部命中所选机器', allHit, `机器 ${nodeVals[0]} 行 ${rowNodes.length}`);
    // E20x 导出同源过滤实测：触发导出按钮 → 捕获下载 → 解析 JSON → 仅含所选机器且条数与列表一致
    try {
      const [download] = await Promise.all([
        page.waitForEvent('download', { timeout: 15000 }),
        page.click('[data-act="export"]'),
      ]);
      const exported = JSON.parse(fs.readFileSync(await download.path(), 'utf8'));
      const exportOk = Array.isArray(exported) && exported.length === rowNodes.length
        && exported.every(t => t.target_node === nodeVals[0]);
      record('E20x', '导出 JSON 下载解析：同源过滤（仅所选机器，条数与列表一致）', exportOk, `导出 ${Array.isArray(exported) ? exported.length : '非数组'} 条 / 列表 ${rowNodes.length} 条`);
    } catch (e) {
      record('E20x', '导出 JSON 下载解析：同源过滤', false, '导出下载失败：' + e.message.slice(0, 80));
    }
  // 清除路径 A（非空筛选态真实用户路径）：select 改回「全部机器」，行数恢复
  await page.selectOption('select[data-f="node"]', '');
  await page.waitForTimeout(300);
  const restoredRows = await page.locator('tr[data-task]').count();
  const clearedVal = await page.evaluate(() => document.querySelector('select[data-f="node"]')?.value === '');
  record('E20b', '清除路径 A：select 改回「全部机器」，列表恢复', clearedVal && restoredRows === taskRows, `行 ${restoredRows}/${taskRows}`);
  // 清除路径 B（零命中空态，clear-filters 按钮的设计场景）：trace 无命中 + 叠加机器
  // 筛选（终审必修4：E20c 必须覆盖机器维度）→ 空态点清除 → trace 与机器 select 双复位、列表全恢复
  await page.fill('input[data-f="trace"]', 'no-such-trace-id');
  await page.selectOption('select[data-f="node"]', nodeVals[0]);
  await page.waitForTimeout(400);
  const emptyCleared = await page.locator('[data-act="clear-filters"]').count();
  if (emptyCleared) {
    await page.click('[data-act="clear-filters"]');
    await page.waitForTimeout(300);
    const afterClear = await page.locator('tr[data-task]').count();
    const fieldsCleared = await page.evaluate(() =>
      document.querySelector('input[data-f="trace"]')?.value === '' &&
      document.querySelector('select[data-f="node"]')?.value === '');
    record('E20c', '清除路径 B：零命中空态点「清除筛选」，trace+机器 select 双复位、列表恢复', fieldsCleared && afterClear === taskRows, `行 ${afterClear}/${taskRows}`);
  } else {
    record('E20c', '清除路径 B：零命中空态清除按钮', false, '空态未出现 clear-filters 按钮');
  }
  } else {
    record('E20', '目标机器筛选：行全部命中所选机器', false, '快照无目标机器，无法构造筛选场景', true);
  }

  // TC-E08 任务详情抽屉（最终答复/产物/时间线区块）
  if (taskRows >= 1) {
    await page.locator('[data-act="detail"]').first().click();
    await page.waitForSelector('.drawer');
    const drawerText = await page.textContent('.drawer');
    const blocksOk = drawerText.includes('任务输入') && drawerText.includes('最终答复') && drawerText.includes('产物') && drawerText.includes('时间线');
    record('E08', '任务详情抽屉：四区块渲染（输入/答复/产物/时间线）', blocksOk);
    await page.keyboard.press('Escape');
  }

  // ---------- 运维控制页：真实派单（机器优先两级选择）----------
  await page.click('aside nav button[data-page="control"]');
  await page.waitForSelector('#ctrl-node');
  const nodeOptions = await page.locator('#ctrl-node option').count();
  const agentDisabledBefore = await page.locator('#ctrl-agent').isDisabled();
  record('E09', '派单：机器下拉可选，未选机器时智能体禁用', nodeOptions >= 1 && agentDisabledBefore, `机器选项 ${nodeOptions}`);

  // 选机器 → 智能体列表联动非空 → 选智能体 → 摘要行出现
  await page.selectOption('#ctrl-node', { index: 1 }); // index 0 为占位
  await page.waitForTimeout(300);
  const agentOptionsAfter = await page.locator('#ctrl-agent option').count();
  await page.selectOption('#ctrl-agent', { index: 1 });
  const summary = await page.textContent('#ctrl-summary-line, body');
  record('E09b', '选机器后智能体联动非空，摘要行出现', agentOptionsAfter >= 1 && summary.includes('将在'), `智能体选项 ${agentOptionsAfter}`);

  // TC-E10 统筹模式（2026-10-23 定案：总理→统筹；勾选框改为智能体列表置顶标记项）：
  // 定位统筹载体所在机器 → 选机器 → 首个真实选项应为「统筹 · zcode-dev」→ 选中切 placeholder
  // → 选普通项恢复 → 再选统筹提交复合目标 → 批次报告
  const PRIME_GOAL = 'E2E 统筹模式链路验证：查询网内能力目录并回报在线节点数量，无需派发子任务';
  let e10ok = false, e10note = '';
  const primeNode = await page.evaluate(() => {
    /* 顶层 const S 不在 window 上：经动态脚本桥取（new Function 在全局作用域看不到
     * 模块内 const——改从 DOM 拉：能力目录无直读 DOM，退回 API 拉取） */
    return fetch('/api/v1/directory', { credentials: 'same-origin' })
      .then(r => r.json()).then(j => {
        const list = j?.directory || [];
        const d = list.find(x => (x.card?.name || '').includes('zcode-dev') && x.online);
        return d ? (d.node_id || '') : '';
      }).catch(() => '');
  });
  if (!primeNode) {
    record('E10', '统筹置顶项与 placeholder 联动', false, '网内无在线 zcode-dev，跳过提交流程', true);
  } else {
    await page.selectOption('#ctrl-node', primeNode);
    await page.waitForTimeout(400);
    const firstReal = await page.evaluate(() => {
      const sel = document.querySelector('#ctrl-agent');
      const o = [...sel.options].find(x => x.value);
      return o ? { v: o.value, t: o.textContent } : null;
    });
    const pinnedOk = !!firstReal && firstReal.v.includes('zcode-dev') && firstReal.t.includes('智行管家 ·'); // 置顶标记现词（旧「统筹 ·」为既有漂移，顺修）
    await page.selectOption('#ctrl-agent', firstReal.v);
    await page.waitForTimeout(300);
    const phOn = await page.getAttribute('#ctrl-stdin', 'placeholder');
    // 切回普通智能体验证恢复
    const normal = await page.evaluate(() => {
      const sel = document.querySelector('#ctrl-agent');
      const o = [...sel.options].find(x => x.value && !x.value.includes('zcode-dev'));
      return o ? o.value : '';
    });
    let phOff = null;
    if (normal) { await page.selectOption('#ctrl-agent', normal); await page.waitForTimeout(300); phOff = await page.getAttribute('#ctrl-stdin', 'placeholder'); }
    const toggleOk = pinnedOk && phOn.startsWith('描述复合目标') && (!normal || phOff.startsWith('用自然语言描述'));
    if (!toggleOk) e10note = `置顶=${pinnedOk} first=${JSON.stringify(firstReal)} phOn=${String(phOn).slice(0, 10)}… phOff=${String(phOff).slice(0, 10)}…`;
    // 选回统筹项提交复合目标
    await page.selectOption('#ctrl-agent', firstReal.v);
    await page.fill('#ctrl-stdin', PRIME_GOAL);
    await page.click('button:has-text("提交任务")');
    await page.waitForSelector('[data-act="detail"]', { timeout: 30000 }); // 提交后任务行出现
    const reportOk = (await page.textContent('body')).includes('已提交');
    e10ok = toggleOk && reportOk;
    record('E10', '统筹置顶项选中即统筹模式：placeholder 切换+提交复合目标', e10ok, e10note);
  }

  // TC-E10b 总理模式任务入参：经任务详情 API 断言 args[0] 以 PRIME 提示词开头、
  // 含「# 本次目标」段与用户输入的目标文本
  const primeTaskId = await page.locator('[data-act="detail"]').first().getAttribute('data-id');
  const primeTask = await page.evaluate(async id => {
    const r = await fetch('/api/v1/tasks/' + encodeURIComponent(id), { credentials: 'same-origin' });
    return r.status === 200 ? r.json() : null;
  }, primeTaskId);
  const a0 = primeTask?.input?.args?.[0] || '';
  const primeArgsOk = a0.startsWith('# SwarmMind 统筹智能体（Prime）')
    && a0.includes('# 本次目标') && a0.includes(PRIME_GOAL);
  record('E10b', '统筹模式任务 args[0] 含 PRIME 提示词特征与目标文本（任务详情 API）', primeArgsOk,
    primeArgsOk ? `任务 ${primeTaskId}` : `PENDING/异常：任务 ${primeTaskId} args[0] 头 40 字=${a0.slice(0, 40)}`);

  // TC-E11 批次报告「查看任务」直达详情（BUG 回归：提交后快照即时刷新）
  await page.locator('[data-act="detail"]').first().click();
  await page.waitForSelector('.drawer', { timeout: 20000, state: 'visible' }).catch(async () => {
    console.log('  [调试] drawer 未出现；S.tasks=', await p.evaluate(() => S.tasks.length), 'hash=', p.url());
    throw new Error('drawer not visible');
  });
  const detailText = await page.textContent('.drawer');
  const notFound = detailText.includes('已归档') || detailText.includes('不存在');
  record('E11', '批次报告→查看任务：直达详情不 404（BUG 回归）', !notFound, notFound ? '出现归档提示' : '详情正常打开');
  await page.keyboard.press('Escape');

  // TC-E19 失败任务：失败原因（进程错误输出）前置可见（不再折叠）
  await page.click('aside nav button[data-page="tasks"]');
  await page.waitForSelector('th:has-text("提交时间")');
  // 从 API 找一个 stderr 非空的失败任务（超时类失败无 stderr，不适用本断言）
  const stderrTask = await page.evaluate(async () => {
    const r = await fetch('/api/v1/tasks', { credentials: 'same-origin' }).then(x => x.json());
    const hit = (r.tasks || []).find(t => t.status === 'failed' && t.result?.stderr_tail);
    return hit ? hit.id : null;
  });
  if (stderrTask) {
    await page.evaluate(id => openTask(id), stderrTask);
    await page.waitForSelector('.drawer', { timeout: 15000 });
    const dt = await page.textContent('.drawer');
    const stderrShown = dt.includes('失败原因（进程错误输出）');
    record('E19', '失败任务详情：stderr 前置展示', stderrShown, stderrShown ? '' : '未见前置 stderr 块');
    await page.keyboard.press('Escape');
  } else {
    record('E19', '失败任务详情：stderr 前置展示', false, '当前无带 stderr 的失败任务', true);
  }

  // ---------- 策略容量页 ----------
  await page.click('aside nav button[data-page="policy"]');
  await page.waitForSelector('text=离线暂存条数', { timeout: 15000 }); // 策略页独有首行（旧「信箱条数」为既有漂移，顺修）
  const policyRows = await page.locator('table tbody tr').count();
  record('E12', '策略容量：额度表渲染', policyRows >= 8, `行数 ${policyRows}`);

  // ---------- 设置页：账户信息 + 令牌 ----------
  await page.click('aside nav button[data-page="tokens"]');
  await page.waitForSelector('text=账户信息');
  let acctOk = false;
  try {
    await page.waitForFunction(() => document.body.innerText.includes('admin'), { timeout: 10000 });
    acctOk = true;
  } catch { }
  record('E13', '设置：账户信息显示当前用户名', acctOk);

  // TC-E14 令牌创建：明文一次性展示 → 保存后列表可见
  await page.fill('#token-name', 'E2E 自动化验证令牌');
  await page.click('#token-form button[type=submit]');
  await page.waitForSelector('#new-token-plain', { timeout: 15000 });
  const plain = await page.textContent('#new-token-plain');
  const tokenOk = plain.startsWith('smsw_');
  record('E14', '设置：创建令牌并一次性展示明文（smsw_ 前缀）', tokenOk);
  await page.click('[data-act="token-done"]');
  await page.waitForSelector('#new-token-plain', { state: 'detached' });
  const listOk = (await page.textContent('body')).includes('E2E 自动化验证令牌');
  record('E15', '设置：令牌列表出现新令牌', listOk);

  // TC-E16 退出登录 → 回登录页（密码模式）
  await page.click('#logout');
  await page.waitForSelector('.login', { timeout: 15000 });
  const backToLogin = (await page.textContent('body')).includes('管理员入口');
  record('E16', '退出登录回到登录页', backToLogin);

  // TC-E17 修改密码后旧密码失效 + 新密码可登录（改回原密码闭环）
  // 先重新登录（等退出后的渲染稳定：密码表单可见且探询完成）
  await page.waitForSelector('#pw-login-form', { state: 'visible', timeout: 15000 });
  await p_sleep(1200);
  await page.fill('#login-user', USER);
  await page.fill('#login-pass', PASS);
  await page.click('#pw-login-form button[type=submit]');
  await page.waitForSelector('text=总览', { timeout: 15000 });
  await page.click('aside button:has-text("设置")');
  await page.waitForSelector('#acc-cur');
  await p_sleep(1500); // 等进页异步重渲（loadTokens 完成后 render 会重建 DOM，提前填值会被清掉）
  await page.fill('#acc-cur', PASS);
  await page.fill('#acc-new', PASS + 'x');
  await page.click('[data-act="account-update"]');
  await p_sleep(2000);
  const accAfter = await page.evaluate(([u, pw]) => fetch('/api/v1/auth/login-password', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: u, password: pw }) }).then(r => r.status), [USER, PASS + 'x']);
  record('E17', '设置：修改密码成功（新密码可换会话验证）', accAfter === 200, `新密码登录态 ${accAfter}`);
  // 改回原密码（保持环境稳定）
  await page.fill('#acc-cur', PASS + 'x');
  await page.fill('#acc-new', PASS);
  await page.click('[data-act="account-update"]');
  await p_sleep(2000);
  const accBack = await page.evaluate(([u, pw]) => fetch('/api/v1/auth/login-password', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: u, password: pw }) }).then(r => r.status), [USER, PASS]);
  record('E18', '设置：密码改回原值（环境闭环）', accBack === 200);

  // TC-E-401（终审必修1 回归）：登录→退出→登录表单出现→再登录已由 E16/E17 覆盖；
  // 此处补 401 路径——清会话 cookie 等价会话失效，轮询打 401 后登录表单必须出现
  //（登录页指纹缓存不得残留业务 DOM，否则退出/401 后永远停在业务页出不来登录框）。
  await page.context().clearCookies();
  await page.waitForSelector('.login', { timeout: 20000 });   // 5s 轮询内 401 → sessionExpired → 登录分支
  record('E-401', '会话失效（401）后登录表单出现', (await page.textContent('body')).includes('管理员入口'));

  await browser.close();

  // ---------- 汇总 ----------
  summarize();
}

main().catch(async e => {
  console.error('E2E 异常终止:', e.message);
  try {
    const page = globalThis.__page;
    if (page) {
      console.log('[dump] URL:', page.url());
      console.log('[dump] body:', (await page.textContent('body')).replace(/\s+/g, ' ').slice(0, 400));
      await page.screenshot({ path: '/tmp/e2e-fail.png' });
    }
  } catch { }
  process.exit(2);
});
