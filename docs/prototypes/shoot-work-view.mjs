// 工作单管理台三层走查（P5 W3，TESTPLAN-W3-work 支撑 harness 行）：起真实 hub+node，
// 播种五态工作单（planning/executing/blocked/done/草稿保护单），playwright 走查——
//   ①列表：#works 表格徽标/进度/任期呈现；
//   ②详情抽屉：六分区渲染；打开 ≥2 轮询周期（12s）数据无变化 → 分区节点身份不变
//     （不碰 DOM）、details 开合与表单草稿跨轮询保持（D22 纪律，动作区一次建壳）；
//   ③动作三步全链：接管（expected 预填、成功后任期上屏）→ 续派（步骤声明目标预填、
//     成功后尝试表 +1）→ 处置（abandoned 关闭、终态徽标、动作区收起、报告区浮现）。
//   ④审查修复场景（W3 审查④⑤）：未选中步骤状态变化不重建续派下拉（草稿保持）；
//     接管响应落地后被延迟的旧读档到达不得回退任期（三重迟到防护）。
// 截图 docs/prototypes/shots-work-view/ 留本地；关键断言结果输出 stdout。
import { chromium } from 'playwright';
import { execSync, spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync, openSync } from 'node:fs';
import net from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const ROOT = '/Users/tl.m/coding/personal/sandbox/SwarmMind';
const OUT = join(ROOT, 'docs/prototypes/shots-work-view');
mkdirSync(OUT, { recursive: true });
const TMP = mkdtempSync(join(tmpdir(), 'swarmmind-workview-'));

const ADMIN = 'walk-admin-token-0123456789abcdef';
const TOK_A = 'walk-token-node-a-aaaaaaaaaaaa';

const freePort = () => new Promise(res => {
  const s = net.createServer(); s.listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => res(p)); });
});
const HUB_NODE_PORT = await freePort(), API_PORT = await freePort();

const sleep = ms => new Promise(r => setTimeout(r, ms));
const kids = [];
const spawnBin = (bin, cfg, log) => {
  const fd = openSync(join(TMP, log + '.log'), 'a');
  const p = spawn(join(TMP, 'bin', bin), ['-config', join(TMP, cfg)], { stdio: ['ignore', fd, fd] });
  kids.push(p); return p;
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
async function waitStatus(get, want, limitMs) {
  const t0 = Date.now();
  let last = '';
  while (Date.now() - t0 < limitMs) {
    try {
      const { code, data } = await get();
      if (code === 200) { last = data.status; if (new RegExp(want).test(data.status)) return data; }
    } catch { }
    await sleep(150);
  }
  throw new Error(`未到达 /${want}/（当前 ${last}）`);
}

console.log('── 构建 hub / node');
mkdirSync(join(TMP, 'bin'), { recursive: true });
execSync('go build -o ' + JSON.stringify(join(TMP, 'bin')) + ' ./cmd/hub ./cmd/node', { cwd: ROOT, stdio: 'inherit' });

const INSPECT = join(TMP, 'inspect.sh');
writeFileSync(INSPECT, `#!/bin/sh
printf '%s\\n' '# 巡检报告' '' '**结论**：一切正常。'
`);
execSync('chmod +x ' + JSON.stringify(INSPECT));
mkdirSync(join(TMP, 'wd-inspect'), { recursive: true });
mkdirSync(join(TMP, 'wd-sleep'), { recursive: true });

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
local_listen: 127.0.0.1:0
local_token: walk-local-token-a-aaaaaaaaaa
concurrency: 4
data_dir: ${TMP}/data-a
base_env:
  PATH: /usr/bin:/bin
  TMPDIR: /tmp
agents:
  - name: inspect-demo
    kind: cli
    command: ${INSPECT}
    workdir: ${TMP}/wd-inspect
    timeout_sec: 10
    allow_args: true
    allow_env: ["TMPDIR"]
    description: 巡检演示
    permission_mode: no-shell
  - name: stat-missing
    kind: cli
    command: /usr/bin/false
    workdir: /tmp
    timeout_sec: 5
    allow_args: true
    allow_env: []
    description: 失败演示
    permission_mode: no-shell
  - name: sleeper-long
    kind: cli
    command: /bin/sleep
    workdir: ${TMP}/wd-sleep
    timeout_sec: 60
    allow_args: true
    allow_env: []
    description: 长任务演示
    permission_mode: no-shell
`);

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
{
  const t0 = Date.now();
  while (Date.now() - t0 < 15000) {
    try {
      const { data } = await hub('GET', '/api/v1/nodes');
      if ((data?.nodes || []).some(x => x.node_id === 'nodeA' && x.online)) break;
    } catch { }
    await sleep(200);
  }
}

// ---------- 播种四态工作单 ----------
const createWork = async (goal, steps) => {
  const r = await hub('POST', '/api/v1/works', { goal, prime_term: 'prime-1', steps });
  if (r.code !== 201) throw new Error('建单失败 ' + r.code + ' ' + JSON.stringify(r.data));
  return r.data.work.id;
};
const dispatch = async (wid, step, agent, key, args = [], term = 'prime-1') => {
  const r = await hub('POST', '/api/v1/tasks', {
    target_node: 'nodeA', target_agent: agent, input: { args, stdin: '' },
    idem_key: key, work_ref: wid, step_seq: step, prime_term: term,
  });
  if (r.code !== 202) throw new Error('派发失败 ' + r.code + ' ' + JSON.stringify(r.data));
  return r.data.task_id;
};

const wDone = await createWork('W3 走查·已完成单', [
  { desc: '巡检一', target_node: 'nodeA', target_agent: 'inspect-demo' },
  { desc: '巡检二', target_node: 'nodeA', target_agent: 'inspect-demo' },
]);
for (const [i, key] of [['1', 'wd1'], ['2', 'wd2']]) {
  const tid = await dispatch(wDone, Number(i), 'inspect-demo', key);
  await waitStatus(() => hub('GET', '/api/v1/tasks/' + tid), 'completed', 20000);
}
{
  const r = await hub('POST', `/api/v1/works/${wDone}/collect`, { prime_term: 'prime-1', report: '两步巡检全部完成，结论正常。' });
  if (r.code !== 200) throw new Error('收集失败 ' + r.code);
}

const wBlocked = await createWork('W3 走查·受阻单', [
  { desc: '磁盘巡检（会失败）', target_node: 'nodeA', target_agent: 'stat-missing' },
  { desc: '内存巡检', target_node: 'nodeA', target_agent: 'inspect-demo' },
]);
{
  const tid = await dispatch(wBlocked, 1, 'stat-missing', 'wb1');
  await waitStatus(() => hub('GET', '/api/v1/tasks/' + tid), 'failed', 20000);
}

const wExec = await createWork('W3 走查·执行中单', [
  { desc: '长任务', target_node: 'nodeA', target_agent: 'sleeper-long' },
]);
{
  const tid = await dispatch(wExec, 1, 'sleeper-long', 'we1', ['300']);
  await waitStatus(() => hub('GET', '/api/v1/tasks/' + tid), '^running$', 20000);
}

const wPlan = await createWork('W3 走查·计划中单', [
  { desc: '还没派', target_node: 'nodeA', target_agent: 'inspect-demo' },
]);

// 草稿保护单（W3 审查⑤场景）：步骤 2 长任务在选中步骤 1 期间改变状态，
// 续派下拉不得重建（用户改过的执行者草稿保持）
const wDraft = await createWork('W3 走查·草稿保护单', [
  { desc: '要改草稿的一步', target_node: 'nodeA', target_agent: 'inspect-demo' },
  { desc: '后台长任务', target_node: 'nodeA', target_agent: 'sleeper-long' },
]);
console.log('  播种完成（done/blocked/executing/planning/draft 五单）');

// ---------- 断言助手 ----------
let pass = 0, fail = 0;
const check = (name, ok) => { console.log(`  [${ok ? 'PASS' : 'FAIL'}] ${name}`); ok ? pass++ : fail++; };

const browser = await chromium.launch();
{
  const page = await browser.newPage({ viewport: { width: 1440, height: 1200 } });
  page.on('pageerror', e => console.log('[pageerror]', String(e).slice(0, 400)));
  await page.goto(HUB + '/#/', { waitUntil: 'networkidle' });
  await page.click('[data-act="switch-login"]');
  await page.fill('#token', ADMIN);
  await page.click('#login-form button[type=submit]');
  await page.waitForSelector('.shell', { timeout: 10000 });

  // ─── 层 1：列表 ───
  await page.evaluate(() => { location.hash = '#/works'; });
  await page.waitForSelector('tr[data-work]');
  await page.waitForTimeout(300);
  check('列表：五单齐出（含 ID 行）', await page.evaluate(() => document.querySelectorAll('tr[data-work]').length === 5));
  {
    const missing = await page.evaluate(() => {
      const txt = document.body.textContent;
      return ['已完成', '受阻', '执行中', '计划中'].filter(w => !txt.includes(w));
    });
    check('列表：徽标呈现（已完成/受阻/执行中/计划中）缺 ' + JSON.stringify(missing), missing.length === 0);
  }
  check('列表：进度分式（已完成单 2/2）', await page.evaluate(() => document.body.textContent.includes('2/2')));
  await page.screenshot({ path: `${OUT}/01-work-list.png`, fullPage: true });

  // ─── 层 2：详情抽屉（稳定 DOM）───
  await page.click(`tr[data-work="${wBlocked}"]`);
  await page.waitForSelector('.drawer');
  await page.waitForTimeout(600);
  check('抽屉：头部含目标与受阻徽标', await page.evaluate(() => {
    const h = document.querySelector('.drawer [data-zone="head"]');
    return h && h.textContent.includes('W3 走查·受阻单') && h.textContent.includes('受阻');
  }));
  check('抽屉：步骤表 2 行 + 尝试表 1 行', await page.evaluate(() => {
    const steps = document.querySelectorAll('.drawer [data-zone="steps"] tbody tr');
    const atts = document.querySelectorAll('.drawer [data-zone="attempts"] tbody tr');
    return steps.length === 2 && atts.length === 1;
  }));
  check('抽屉：事件时间线含派发登记与步骤回写', await page.evaluate(() => {
    const ev = document.querySelector('.drawer [data-zone="events"]');
    return ev && ev.textContent.includes('派发登记') && ev.textContent.includes('步骤回写');
  }));
  check('抽屉：无报告区（未关闭单不产出 report 分区内容）', await page.evaluate(() => {
    const r = document.querySelector('.drawer [data-zone="report"]');
    return r && r.textContent.trim() === '';
  }));
  // 稳定 DOM：开续派 details、留草稿、标记分区子节点 → 熬 2 个轮询周期
  await page.click('#wa-dispatch-box summary');
  await page.fill('#wa-stdin', '重试磁盘巡检（草稿）');
  await page.evaluate(() => {
    window.__zones = {};
    for (const z of ['head', 'steps', 'attempts', 'events']) {
      const el = document.querySelector(`.drawer [data-zone="${z}"]`);
      if (el?.firstElementChild) el.firstElementChild.__w3marker = z;
    }
    window.__det = document.querySelector('#wa-dispatch-box');
  });
  await page.waitForTimeout(12000);
  check('抽屉：数据无变化分区子节点身份不变（12s ≥2 轮询，不碰 DOM）', await page.evaluate(() => {
    for (const z of ['head', 'steps', 'attempts', 'events']) {
      const el = document.querySelector(`.drawer [data-zone="${z}"]`);
      if (!el?.firstElementChild || el.firstElementChild.__w3marker !== z) return false;
    }
    return true;
  }));
  check('抽屉：续派 details 开合跨轮询保持（动作区一次建壳恒定节点）', await page.evaluate(() => {
    const d = document.querySelector('#wa-dispatch-box');
    return d === window.__det && d.open;
  }));
  check('抽屉：表单草稿跨轮询保持（不在分区模板串）', await page.evaluate(() => document.querySelector('#wa-stdin').value === '重试磁盘巡检（草稿）'));
  await page.screenshot({ path: `${OUT}/02-work-drawer-stable.png`, fullPage: true });

  // ─── 层 2.5：审查⑤场景——未选中步骤状态变化不重建下拉（草稿保护）───
  // 选步骤 1、改执行者草稿；随后步骤 2 派发并运行（状态 pending→已派发→执行中），
  // 轮询刷新后选中与草稿都必须保持（旧实现全量签名会重建选项并重预填冲掉草稿）
  await page.click('.drawer .close');
  await page.waitForTimeout(400);
  await page.click(`tr[data-work="${wDraft}"]`);
  await page.waitForSelector('.drawer');
  await page.waitForFunction(() => document.querySelector('#wa-step')?.options?.length === 2, null, { timeout: 8000 });
  await page.click('#wa-dispatch-box summary');   // 展开续派折叠区（动作区一次建壳，details 默认收起）
  await page.selectOption('#wa-step', '1');
  await page.fill('#wa-agent', 'draft-keep-agent');
  await dispatch(wDraft, 2, 'sleeper-long', 'wd-slow', ['15']);
  await page.waitForFunction(() => {
    const rows = document.querySelectorAll('.drawer [data-zone="steps"] tbody tr');
    return [...rows].some(r => r.textContent.includes('已派发') || r.textContent.includes('执行中'));
  }, null, { timeout: 15000 });
  await page.waitForTimeout(6000);   // 熬过至少一个轮询周期（5s）：重建若发生必在此窗口内
  check('审查⑤：未选中步骤状态变化不重建下拉（选中保持）', await page.evaluate(() => document.querySelector('#wa-step').value === '1'));
  check('审查⑤：未选中步骤状态变化不重预填（执行者草稿保持）', await page.evaluate(() => document.querySelector('#wa-agent').value === 'draft-keep-agent'));
  await page.screenshot({ path: `${OUT}/07-work-draft-guard.png`, fullPage: true });
  await page.click('.drawer .close');
  await page.waitForTimeout(400);

  // ─── 层 3：动作三步（接管 → 续派 → 处置）───
  // 重开受阻单抽屉（层 2.5 换单后此处恢复上下文）
  await page.click(`tr[data-work="${wBlocked}"]`);
  await page.waitForSelector('.drawer');
  await page.waitForFunction(() => document.querySelector('#wa-term-now')?.textContent === 'prime-1', null, { timeout: 8000 });
  // 接管：expected 预填当前任期，成功后任期上屏（正控：分区 diff 写入也得到验证）
  check('接管前：任期展示位为 prime-1', await page.evaluate(() => document.querySelector('#wa-term-now').textContent === 'prime-1'));
  await page.click('#wa-takeover-box summary');   // 展开接管折叠区
  await page.fill('#wa-new-term', 'prime-7');
  await page.click('[data-act="work-takeover"]');
  await page.waitForFunction(() => document.querySelector('#wa-term-now')?.textContent === 'prime-7', null, { timeout: 8000 });
  check('接管：任期展示位与头部同步为 prime-7', await page.evaluate(() => document.querySelector('.drawer [data-zone="head"]').textContent.includes('prime-7')));
  await page.screenshot({ path: `${OUT}/03-work-takeover.png`, fullPage: true });

  // 续派：步骤下拉 2 项（失败 + 待派发），选中失败步 → 执行者预填声明目标 →
  // 改为可成功的能力 → 提交后尝试表 +1（重开抽屉后续派折叠区需先展开）
  check('续派：可派步骤下拉含 2 项', await page.evaluate(() => document.querySelectorAll('#wa-step option').length === 2));
  await page.click('#wa-dispatch-box summary');
  await page.selectOption('#wa-step', '1');
  check('续派：选步后执行者按声明预填', await page.evaluate(() => document.querySelector('#wa-node').value === 'nodeA' && document.querySelector('#wa-agent').value === 'stat-missing'));
  await page.fill('#wa-agent', 'inspect-demo');
  await page.fill('#wa-stdin', '重试磁盘巡检');
  await page.click('[data-act="work-dispatch"]');
  await page.waitForFunction(() => document.querySelectorAll('.drawer [data-zone="attempts"] tbody tr').length === 2, null, { timeout: 8000 });
  check('续派：尝试表 +1（新尝试新键）', true);
  await page.screenshot({ path: `${OUT}/04-work-dispatch.png`, fullPage: true });

  // 步骤 2 走 REST 派发并到终态 → 全部步骤终态 → 处置关闭（abandoned）
  {
    const tid = await dispatch(wBlocked, 2, 'inspect-demo', 'wb2', [], 'prime-7');   // 接管后旧任期派发被拒（409），须携当前任期
    await waitStatus(() => hub('GET', '/api/v1/tasks/' + tid), 'completed', 20000);
  }
  await page.waitForFunction(() => {
    const rows = document.querySelectorAll('.drawer [data-zone="steps"] tbody tr');
    return rows.length === 2 && [...rows].every(r => r.textContent.includes('完成'));
  }, null, { timeout: 15000 });
  check('关闭前：生命周期介绍提示可收集/可处置', await page.evaluate(() => {
    const t = document.querySelector('.drawer [data-zone="intro"]').textContent;
    return t.includes('可收集关闭') || t.includes('处置放弃');
  }));
  await page.click('#wa-collect-box summary');   // 展开处置折叠区
  await page.selectOption('#wa-outcome', 'abandoned');
  await page.fill('#wa-report', '巡检结果已由人工复核，本单结论不再采用，处置放弃。');
  await page.click('[data-act="work-collect"]');
  await page.waitForFunction(() => document.querySelector('.drawer [data-zone="head"]')?.textContent.includes('已放弃'), null, { timeout: 8000 });
  check('处置：终态徽标（已放弃）上屏', true);
  check('处置：动作区收起（终态不可再接管/续派/关闭）', await page.evaluate(() => document.querySelector('#work-actions').hidden));
  check('处置：报告区浮现（md 渲染）', await page.evaluate(() => document.querySelector('.drawer [data-zone="report"]').textContent.includes('处置放弃')));
  check('处置：时间线出现收集关闭事件', await page.evaluate(() => document.querySelector('.drawer [data-zone="events"]').textContent.includes('收集关闭')));
  await page.screenshot({ path: `${OUT}/05-work-collect-abandoned.png`, fullPage: true });

  // 已完成单：动作区同样收起（终态一致纪律）
  await page.click('.drawer .close');
  await page.waitForTimeout(400);
  await page.click(`tr[data-work="${wDone}"]`);
  await page.waitForSelector('.drawer');
  await page.waitForTimeout(500);
  check('done 单：动作区收起 + 报告区呈现', await page.evaluate(() => document.querySelector('#work-actions').hidden && document.querySelector('.drawer [data-zone="report"]').textContent.includes('结论正常')));
  await page.screenshot({ path: `${OUT}/06-work-done-drawer.png`, fullPage: true });

  // ─── 层 4：审查④场景——写动作三重迟到防护（真实竞态）───
  // 构造真实的「迟到旧读档」：先取接管前快照，显式触发一次详情读档并让该请求
  // 在 2.5s 后以旧快照应答（应答已在途、服务端随后变更的迟到形态）；期间接管
  // 响应先落地（任期 prime-7 上屏）——旧读档随后到达，任期展示与头部都不得回退
  // prime-1（写前 seq 作废 + 本地回写版本 rev 双守卫；二者任一缺失即回退）
  await page.click('.drawer .close');
  await page.waitForTimeout(400);
  await page.click(`tr[data-work="${wExec}"]`);
  await page.waitForSelector('.drawer');
  await page.waitForFunction(() => document.querySelector('#wa-term-now')?.textContent === 'prime-1', null, { timeout: 8000 });
  const { data: staleWork } = await hub('GET', '/api/v1/works/' + wExec);   // 接管前快照
  if (staleWork?.work?.prime_term !== 'prime-1') throw new Error('快照任期应为 prime-1');
  let staleServed = false;
  const delayWorkGet = u => u.pathname === `/api/v1/works/${wExec}`;
  await page.route(delayWorkGet, async route => {
    if (!staleServed) {
      staleServed = true;
      await sleep(2500);
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(staleWork) });
      return;
    }
    await route.continue();
  });
  await page.evaluate(() => fetchWorkDetail());   // 显式发出将迟到的那次读档
  await page.click('#wa-takeover-box summary');
  await page.fill('#wa-new-term', 'prime-7');
  await page.click('[data-act="work-takeover"]');
  await page.waitForFunction(() => document.querySelector('#wa-term-now')?.textContent === 'prime-7', null, { timeout: 8000 });
  check('迟到防护：接管响应先行落地（任期展示位 prime-7）', true);
  await page.waitForTimeout(3200);   // 等旧快照应答抵达并被守卫处置
  check('迟到防护：迟到旧读档不回退任期（展示位仍 prime-7）', await page.evaluate(() => document.querySelector('#wa-term-now').textContent === 'prime-7'));
  check('迟到防护：头部任期同步不回退（分区不被旧快照覆盖）', await page.evaluate(() => document.querySelector('.drawer [data-zone="head"]').textContent.includes('prime-7')));
  await page.unroute(delayWorkGet);
  await page.screenshot({ path: `${OUT}/08-work-late-guard.png`, fullPage: true });

  await browser.close();
}

console.log(`\n四层走查完成：${pass} PASS / ${fail} FAIL（截图 ${OUT}/）`);
cleanup();
process.exit(fail ? 1 : 0);
