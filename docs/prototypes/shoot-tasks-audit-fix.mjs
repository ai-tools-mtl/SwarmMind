// 任务列表账本全量驱动批 codex 审查五项修复 harness（docs/TESTPLAN-tasks-audit.md
// 审查节闭合复验）：起真实 hub + nodeA，账本注入 205 条 hub 来源终态种子（C7 认可
// 的账本注入法；清理未接线故全部留存）+ hub/本机各真实派一单，playwright + API 断言——
//   ①快照顺序：提交穿插读取窗口由单测钉死（TestLocalTasksSubmitInterleave），此处
//     验真实链路下「本机派单行带状态出列、来源徽标正确」；
//   ④容量边界：/api/v1/local/tasks 默认 200 条 + total=207；limit/offset 切片连续、
//     越界空页、非法参数 400；控制台「已载 200 / 共 207 条」+「加载更早」追加翻页；
//   ⑤抽屉来源持久化：列表行移除后 syncDrawerZones 重算，抽屉头部仍「hub 下发」
//     （不回退缺省 local 的错误产物指引）；
//   ②事实纠正：任务页/总览页文案无「24h / 24 小时」具体保留窗口；
//   复审节①②（跨轮询场景）：加载 2 页→轮询 2 轮历史保持+无漏条（DOM 行序===
//     服务端全序）；轮询间新任务入账合并无缺口；翻页应答延迟落地+轮询先行完成
//     的确定性交错下，迟到应答作废（不拼接）、在途 busy 不被轮询提前解除；
//   另验：来源列/状态徽标直出（hub 下发+本机派单双行）、总览最近任务卡、无 JS 错误。
// 截图 docs/prototypes/shots-tasks-audit/ 留本地。
import { chromium } from 'playwright';
import { execSync, spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync, openSync, existsSync } from 'node:fs';
import net from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const ROOT = '/Users/tl.m/coding/personal/sandbox/SwarmMind';
const OUT = join(ROOT, 'docs/prototypes/shots-tasks-audit');
mkdirSync(OUT, { recursive: true });
const TMP = mkdtempSync(join(tmpdir(), 'swarmmind-tasksaudit-'));

const ADMIN = 'walk-admin-token-0123456789abcdef';
const TOK_A = 'walk-token-node-a-aaaaaaaaaaaa';
const LOCAL_A = 'walk-local-token-a-aaaaaaaaaa';
const SEED_N = 205;

const freePort = () => new Promise(res => {
  const s = net.createServer(); s.listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => res(p)); });
});
const HUB_NODE_PORT = await freePort(), API_PORT = await freePort(), LA_PORT = await freePort();

const sleep = ms => new Promise(r => setTimeout(r, ms));
const kids = [];
const spawnBin = (bin, cfg, log) => {
  const fd = openSync(join(TMP, log + '.log'), 'a');
  const p = spawn(join(TMP, 'bin', bin), ['-config', join(TMP, cfg)], { stdio: ['ignore', fd, fd] });
  p._log = log; kids.push(p); return p;
};
const cleanup = () => { for (const p of kids) { try { p.kill('SIGKILL'); } catch { } } };
process.on('exit', cleanup);
process.on('SIGINT', () => { cleanup(); process.exit(130); });

const HUB = `http://127.0.0.1:${API_PORT}`;
let cookie = '';
async function hub(method, path, body) {
  const r = await fetch(HUB + path, {
    method, headers: { 'Content-Type': 'application/json', ...(cookie ? { cookie } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const sc = r.headers.get('set-cookie'); if (sc) cookie = sc.split(';')[0];
  return { code: r.status, data: await r.json().catch(() => null) };
}
async function local(port, token, method, path, body) {
  const r = await fetch(`http://127.0.0.1:${port}${path}`, {
    method, headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + token },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  return { code: r.status, data: await r.json().catch(() => null) };
}
async function waitStatus(get, want, limitMs) {
  const t0 = Date.now();
  while (Date.now() - t0 < limitMs) {
    try { const { code, data } = await get(); if (code === 200 && new RegExp(want).test(data.status)) return data; } catch { }
    await sleep(150);
  }
  throw new Error(`任务未到达 /${want}/`);
}
async function waitNodeOnline(limitMs) {
  const t0 = Date.now();
  while (Date.now() - t0 < limitMs) {
    try {
      const { data } = await hub('GET', '/api/v1/nodes');
      if ((data?.nodes || []).some(x => x.node_id === 'nodeA' && x.online)) return;
    } catch { }
    await sleep(200);
  }
  throw new Error('nodeA 未上线');
}

console.log('── 构建 hub / node');
mkdirSync(join(TMP, 'bin'), { recursive: true });
execSync('go build -o ' + JSON.stringify(join(TMP, 'bin')) + ' ./cmd/hub ./cmd/node', { cwd: ROOT, stdio: 'inherit' });

const INSPECT = join(TMP, 'inspect.sh');
writeFileSync(INSPECT, `#!/bin/sh
mkdir -p "\$SWARM_OUTPUTS_DIR" || exit 1
printf '巡检原始记录\\n' > "\$SWARM_OUTPUTS_DIR/巡检记录.txt"
printf '%s\\n' '# 巡检报告' '' '**结论**：一切正常。'
`);
execSync('chmod +x ' + JSON.stringify(INSPECT));

const agentInspect = `  - name: inspect-demo
    kind: cli
    command: ${INSPECT}
    workdir: /tmp
    timeout_sec: 10
    allow_args: true
    allow_env: ["TMPDIR"]
    description: 巡检演示
    permission_mode: no-shell
`;

writeFileSync(join(TMP, 'hub.yaml'), `node_listen: 127.0.0.1:${HUB_NODE_PORT}
api_listen: 127.0.0.1:${API_PORT}
tls:
  insecure_loopback: true
nodes:
  nodeA: ${TOK_A}
admin_token: ${ADMIN}
data_dir: ${TMP}/data-hub
`);
writeFileSync(join(TMP, 'nodeA.yaml'), `node_id: nodeA
hub_url: ws://127.0.0.1:${HUB_NODE_PORT}/ws/node
register_token: ${TOK_A}
hub_pubkey_fp: ""
local_listen: 127.0.0.1:${LA_PORT}
local_token: ${LOCAL_A}
prime_agent: inspect-demo
concurrency: 4
data_dir: ${TMP}/data-a
base_env:
  PATH: /usr/bin:/bin
  TMPDIR: /tmp
agents:
${agentInspect}
`);

console.log('── 账本注入 %d 条 hub 来源终态种子（node 启动前写 ledger.json）', SEED_N);
mkdirSync(join(TMP, 'data-a'), { recursive: true });
{
  const iso = ms => new Date(ms).toISOString();
  const base = Date.now() - 26 * 3600 * 1000;
  const seed = {};
  for (let i = 0; i < SEED_N; i++) {
    const id = `t-seed-${String(i).padStart(3, '0')}`;
    const at = iso(base + i * 5 * 60 * 1000);
    seed[id] = {
      task: {
        id, trace_id: 'tr-' + id, target_node: 'nodeA', target_agent: 'inspect-demo',
        input: { args: [] }, submitted_by: 'seed', status: 'completed',
        created_at: at, updated_at: at, expire_at: iso(base + i * 5 * 60 * 1000 + 7 * 24 * 3600 * 1000),
      },
      state: 'finished', origin: 'hub',
      result: { status: 'completed', exit_code: 0, final_answer: 'seed ' + id, stdout_tail: '', stderr_tail: '', artifacts: [], finished_at: at },
      result_acked: true, first_acked_at: at,
    };
  }
  writeFileSync(join(TMP, 'data-a', 'ledger.json'), JSON.stringify(seed, null, 2));
}

console.log('── 启动 hub + nodeA');
spawnBin('hub', 'hub.yaml', 'hub');
spawnBin('node', 'nodeA.yaml', 'nodeA');
{
  const t0 = Date.now();
  while (Date.now() - t0 < 15000) {
    try { if ((await hub('POST', '/api/v1/login', { token: ADMIN })).code === 200) break; } catch { }
    await sleep(200);
  }
  if (!cookie) throw new Error('hub 管理面未就绪');
}
await waitNodeOnline(15000);
console.log('  在线');

// ---------- 播种：hub 下发一单 + 本机派单一单（都等终态） ----------
const hubTask = (await hub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'inspect-demo', input: { args: [], stdin: '巡检-hub', env: {} }, submitted_by: 'demo' })).data.task_id;
await waitStatus(() => hub('GET', '/api/v1/tasks/' + hubTask), 'completed', 15000);
const localTask = (await local(LA_PORT, LOCAL_A, 'POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'inspect-demo', input: { args: [], stdin: '巡检-local', env: {} }, submitted_by: 'console' })).data.task_id;
await waitStatus(() => local(LA_PORT, LOCAL_A, 'GET', '/api/v1/tasks/' + localTask), 'completed', 15000);
const TOTAL = SEED_N + 2;
console.log(`  播种完成（种子 ${SEED_N} + hub ${hubTask} + 本机 ${localTask}，共 ${TOTAL}）`);

// ---------- 断言助手 ----------
let pass = 0, fail = 0;
const check = (name, ok, extra) => { console.log(`  [${ok ? 'PASS' : 'FAIL'}] ${name}${ok || extra === undefined ? '' : ' — ' + extra}`); ok ? pass++ : fail++; };

/* ===== API 层：分页与轻量投影（④） ===== */
{
  const r = await local(LA_PORT, LOCAL_A, 'GET', '/api/v1/local/tasks');
  const rows = r.data?.tasks || [];
  check(`API 默认参数：前 200 条 + total=${TOTAL}`, r.code === 200 && rows.length === 200 && r.data?.total === TOTAL,
    `code=${r.code} rows=${rows.length} total=${r.data?.total}`);
  check('本机派单最新行在列首且带状态/来源（①真实链路）',
    rows[0]?.task_id === localTask && rows[0]?.origin === 'local' && rows[0]?.status === 'completed',
    JSON.stringify(rows[0]));
  check('hub 下发行在列且 origin=hub（全量审计）',
    rows.some(x => x.task_id === hubTask && x.origin === 'hub' && x.status === 'completed'));
  check('轻量投影：行含 trace_id 不含 submitted_by（最小字段）',
    'trace_id' in rows[0] && !('submitted_by' in rows[0]) && !('submitted_by' in (rows.find(x => x.task_id === hubTask) || {})));
  const seam = await local(LA_PORT, LOCAL_A, 'GET', '/api/v1/local/tasks?limit=5&offset=5');
  const srows = seam.data?.tasks || [];
  const orderOk = r.data.total === TOTAL && srows.length === 5 &&
    rows[5].task_id === srows[0].task_id;   // 切片连续：offset=5 首行恰为全序第 6 行
  check('API limit/offset 切片连续（排序在切片前，跨页次序一致）', orderOk, JSON.stringify({ rows5: rows[5], seam0: srows[0] }));
  const tail = await local(LA_PORT, LOCAL_A, 'GET', `/api/v1/local/tasks?limit=10&offset=${TOTAL - 2}`);
  check('尾页：最后两行为最旧种子（排序新→旧，真实任务为最新在列首）',
    tail.data?.total === TOTAL && (tail.data?.tasks || []).length === 2 &&
    [tail.data.tasks[0].task_id, tail.data.tasks[1].task_id].join(',') === 't-seed-001,t-seed-000',
    JSON.stringify(tail.data));
  const beyond = await local(LA_PORT, LOCAL_A, 'GET', '/api/v1/local/tasks?offset=9999');
  check('offset 越界：空页 + total 恒全量', beyond.data?.total === TOTAL && (beyond.data?.tasks || []).length === 0);
  for (const bad of ['limit=0', 'limit=-1', 'limit=abc', 'offset=-1', 'offset=abc']) {
    const b = await local(LA_PORT, LOCAL_A, 'GET', '/api/v1/local/tasks?' + bad);
    check(`非法参数 ${bad} → 400`, b.code === 400, `code=${b.code}`);
  }
}

/* ===== 控制台（headless）：列表分页 / 加载更早 / 来源与状态直出 / 抽屉来源持久化 / 文案 ===== */
{
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 1200 } });
  const jsErrors = [];
  page.on('pageerror', e => jsErrors.push(String(e).slice(0, 300)));
  await page.goto(`http://127.0.0.1:${LA_PORT}/#/tasks`, { waitUntil: 'networkidle' });
  await page.waitForSelector('#gate-token');
  await page.fill('#gate-token', LOCAL_A);
  await page.click('[data-act="gate-enter"]');
  await page.waitForSelector('.shell', { timeout: 10000 });
  // gate 登录后停在总览（tryToken 不重路由，既有行为）——显式切任务页再断言
  await page.evaluate(() => { location.hash = '#/tasks'; route(); });
  await page.waitForSelector('tr[data-task]');

  const rowCount = () => page.evaluate(() => document.querySelectorAll('tr[data-task]').length);
  const tasksCount = () => page.evaluate(() => {
    const sec = [...document.querySelectorAll('section')].find(s => s.querySelector('h2')?.textContent === '我的任务');
    return sec?.querySelector('.row small')?.textContent || '';
  });
  check('控制台首屏 200 行（分页首页）', (await rowCount()) === 200, `rows=${await rowCount()}`);
  check('计数「已载 200 / 共 207 条」', (await tasksCount()).includes(`已载 200 / 共 ${TOTAL} 条`), await tasksCount());
  const moreBtn = await page.evaluate(() => !!document.querySelector('[data-act="tasks-more"]'));
  check('「加载更早的任务」按钮在（还有 7 条）', moreBtn && (await page.evaluate(() => document.querySelector('[data-act="tasks-more"]').textContent.includes('还有 7 条'))));
  await page.screenshot({ path: `${OUT}/tasks-list-paged.png`, fullPage: true });

  await page.click('[data-act="tasks-more"]');
  await page.waitForFunction(n => document.querySelectorAll('tr[data-task]').length === n, TOTAL, { timeout: 5000 });
  check('加载更早：追至全量 207 行、按钮退场、计数收口',
    (await rowCount()) === TOTAL && !(await page.evaluate(() => document.querySelector('[data-act="tasks-more"]')))
    && (await tasksCount()).startsWith(`${TOTAL} 条`));
  await page.screenshot({ path: `${OUT}/tasks-loadmore-full.png`, fullPage: true });

  check('来源列直出：hub 下发 / 本机派单 双徽标',
    await page.evaluate(({ hub, loc }) => {
      const htr = document.querySelector(`tr[data-task="${hub}"]`), ltr = document.querySelector(`tr[data-task="${loc}"]`);
      return !!htr && !!ltr && htr.textContent.includes('hub 下发') && ltr.textContent.includes('本机派单')
        && htr.textContent.includes('已完成') && ltr.textContent.includes('已完成');
    }, { hub: hubTask, loc: localTask }));
  {
    const hit = await page.evaluate(() => {
      const t = document.body.textContent;
      // 断言文案形态（「保留 24h」「保留 24 小时」）而非裸 "24h"——任务号 hex 与
      // 徽标词连排可能凑出 "…24h…" 假阳性（t-seed-124 + hub 下发 实测）。
      for (const pat of ['保留 24', '24 小时']) {
        const i = t.indexOf(pat);
        if (i >= 0) return pat + ' @…' + t.slice(Math.max(0, i - 40), i + 40) + '…';
      }
      return '';
    });
    check('任务页文案无具体保留窗口（②事实纠正）', hit === '', hit);
  }

  // ⑤ 抽屉来源持久化：hub 任务抽屉打开后把该行从列表态移除并强制分区重算——
  // 头部来源必须保持「hub 下发」（缺省 local 回退会给错误产物指引）
  await page.click(`tr[data-task="${hubTask}"]`);
  await page.waitForSelector('#drawer');
  await page.waitForTimeout(400);
  check('hub 任务抽屉：头部来源「hub 下发」',
    await page.evaluate(() => document.querySelector('#drawer [data-zone="head"]').textContent.includes('来源 hub 下发')));
  await page.screenshot({ path: `${OUT}/drawer-hub-origin.png`, fullPage: true });
  await page.evaluate(id => {
    S.tasks = S.tasks.filter(x => x.task_id !== id);   // 列表缺失行（翻页/刷新失行的代偿构造）
    syncDrawerZones();                                  // 分区重算：来源判定不得回退缺省 local
  }, hubTask);
  await page.waitForTimeout(300);
  check('列表缺失行后重算：抽屉来源仍「hub 下发」（⑤持久化，不回退 local）',
    await page.evaluate(() => document.querySelector('#drawer [data-zone="head"]').textContent.includes('来源 hub 下发')
      && !document.querySelector('#drawer [data-zone="head"]').textContent.includes('来源 本机派单')));
  await page.evaluate(() => { window.__shell = document.getElementById('drawer'); });
  await page.waitForTimeout(6000);                      // 熬过一个轮询周期（列表态恢复）
  check('跨轮询后抽屉来源保持 + 壳未重建',
    await page.evaluate(id => document.getElementById('drawer') === window.__shell
      && document.querySelector('#drawer [data-zone="head"]').textContent.includes('来源 hub 下发')
      && S.drawer?.id === id && S.drawer?.origin === 'hub', hubTask));
  await page.click('#drawer [data-act="close-drawer"]');
  await page.waitForTimeout(400);

  await page.click(`tr[data-task="${localTask}"]`);
  await page.waitForSelector('#drawer');
  await page.waitForTimeout(400);
  check('本机任务抽屉：头部来源「本机派单」',
    await page.evaluate(() => document.querySelector('#drawer [data-zone="head"]').textContent.includes('来源 本机派单')));
  await page.screenshot({ path: `${OUT}/drawer-local-origin.png`, fullPage: true });
  await page.click('#drawer [data-act="close-drawer"]');
  await page.waitForTimeout(400);

  /* ===== 复审①②跨轮询场景：历史保持 / 无漏条 / 迟到翻页应答作废 ===== */
  let TOTAL_NOW = TOTAL;   // 场景一b 追派一单后 +1（供后续总览计数断言取当前真相）
  {
    const domSeq = () => page.evaluate(() => [...document.querySelectorAll('tr[data-task]')].map(tr => tr.dataset.task));
    const apiAll = async () => (await local(LA_PORT, LOCAL_A, 'GET', '/api/v1/local/tasks?limit=1000')).data?.tasks?.map(t => t.task_id) || [];
    const resetToFirstPage = () => page.evaluate(async () => { S.tasks = []; S.tasksTotal = 0; S.snap = ''; await pollTick(); });

    // 场景一：加载 2 页 → 轮询 2 轮 → 历史保持 + 无漏条（复审①）
    await resetToFirstPage();
    await page.waitForTimeout(300);
    check('跨轮询基线：复位后首屏 200 行、按钮在', (await rowCount()) === 200
      && !!(await page.evaluate(() => document.querySelector('[data-act="tasks-more"]'))), `rows=${await rowCount()}`);
    await page.evaluate(() => loadMoreTasks());
    await page.waitForFunction(n => document.querySelectorAll('tr[data-task]').length === n, TOTAL, { timeout: 5000 });
    const order1 = await apiAll();
    await page.evaluate(() => pollTick());
    await page.waitForTimeout(300);
    await page.evaluate(() => pollTick());
    await page.waitForTimeout(300);
    check('加载 2 页→轮询 2 轮：历史保持（仍全量行、按钮退场、计数收口）',
      (await rowCount()) === TOTAL && !(await page.evaluate(() => document.querySelector('[data-act="tasks-more"]')))
      && (await tasksCount()).startsWith(`${TOTAL} 条`), `rows=${await rowCount()} count=${await tasksCount()}`);
    check('轮询 2 轮无漏条：DOM 行序 === 服务端全序（老历史行不丢、无重复）',
      JSON.stringify(await domSeq()) === JSON.stringify(order1));

    // 场景一b：轮询间新任务入账——合并路径无缺口（新任务插队首部，老历史保序接尾）
    const extraTask = (await local(LA_PORT, LOCAL_A, 'POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'inspect-demo', input: { args: [], stdin: '巡检-local-轮询合并', env: {} }, submitted_by: 'console' })).data.task_id;
    await waitStatus(() => local(LA_PORT, LOCAL_A, 'GET', '/api/v1/tasks/' + extraTask), 'completed', 15000);
    const TOTAL2 = TOTAL + 1;
    TOTAL_NOW = TOTAL2;
    await page.evaluate(() => pollTick());
    await page.waitForTimeout(300);
    const order2 = await apiAll();
    check('轮询间新任务入账：合并后仍全量无缺口（行数+1、行序===服务端全序）',
      (await rowCount()) === TOTAL2 && JSON.stringify(await domSeq()) === JSON.stringify(order2)
      && (await domSeq())[0] === extraTask, `rows=${await rowCount()}`);
    check('合并后计数「已载 X / 共 Y 条」收口', (await tasksCount()).startsWith(`${TOTAL2} 条`), await tasksCount());

    // 场景二（复审②）：翻页应答延迟落地 + 轮询先行完成的确定性交错——
    // 迟到应答必须作废（不拼接），在途 busy 不被轮询提前解除
    await resetToFirstPage();
    await page.waitForTimeout(300);
    await page.evaluate(() => {
      window.__origFetch = window.fetch;
      window.fetch = (u, o) => {
        const s = String(u);
        if (s.includes('/api/v1/local/tasks') && s.includes('offset=')) {
          return window.__origFetch(u, o).then(r => new Promise(res => setTimeout(() => res(r), 500)));
        }
        return window.__origFetch(u, o);
      };
    });
    const moreP = page.evaluate(() => loadMoreTasks());
    await page.waitForTimeout(150);            // 翻页请求已发出（应答被延迟 500ms）
    await page.evaluate(() => pollTick());     // 轮询先行完成（旧实现此处提前解除 busy + 应答拼接污染）
    const busyDuringStale = await page.evaluate(() => S.tasksMoreBusy);
    await moreP;                               // 迟到翻页应答落地
    const afterStale = await page.evaluate(() => ({ n: S.tasks.length, busy: S.tasksMoreBusy }));
    await page.evaluate(() => { window.fetch = window.__origFetch; });
    await page.waitForTimeout(300);
    check('在途翻页不被轮询提前解除 busy（轮询完成后仍 true）', busyDuringStale === true, `busy=${busyDuringStale}`);
    check('迟到翻页应答作废：不拼接、busy 收口、列表仍首页',
      afterStale.n === 200 && afterStale.busy === false && (await rowCount()) === 200,
      JSON.stringify(afterStale));
    check('作废后 DOM 无污染：行序 === 服务端首页序',
      JSON.stringify(await domSeq()) === JSON.stringify((await apiAll()).slice(0, 200)));
    await page.screenshot({ path: `${OUT}/tasks-cross-polling.png`, fullPage: false });
  }

  await page.evaluate(() => { location.hash = '#/overview'; });
  await page.waitForTimeout(600);
  check('总览最近任务卡渲染 + 计数用 total（非已载行数）',
    await page.evaluate(n => {
      const card = [...document.querySelectorAll('section h2')].find(h => h.textContent.includes('最近任务'));
      return !!card && card.closest('section').textContent.includes(`${n} 条`);
    }, TOTAL_NOW));
  await page.screenshot({ path: `${OUT}/overview-recent.png`, fullPage: true });

  check('全程无 JS 错误', jsErrors.length === 0, jsErrors.join(' | '));
  await browser.close();
}

console.log(`\n结果：${pass} PASS / ${fail} FAIL`);
cleanup();
process.exit(fail ? 1 : 0);
