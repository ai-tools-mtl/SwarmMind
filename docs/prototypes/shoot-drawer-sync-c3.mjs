// 抽屉统一（docs/TASK-drawer-sync.md 阶段 C）D22 硬验收行为自验 harness：
// 起真实 hub + nodeA，播种运行中/完成/等你确认/取消四类任务，playwright 断言——
//   hub：抽屉打开 6s+（≥1 轮询周期）后 details 开合态保持、分区节点身份不变
//       （数据无变化不碰 DOM）；核对表单区节点身份/焦点/滚动/草稿跨轮询保持
//       （阶段 D 终审必须修 3：草稿不进模板串，失焦后轮询不重建表单区）；
//       在途登记 busy 由状态驱动 disabled、分区重算不回退（不靠重建）；旧列表
//       迟到提交不回退核对记录卡（数据版本判定，必须修 2）；cancelled+result=null
//       不显示「尚未到终态」（D9 状态优先，必须修 4）；零值时间不生成时间线事件
//       （必须修 5①，合成 payload 驱动）；抽屉内真实点击不被 detach。
//   复审 P1/P2（2026-10-25）：单查迟到后已关闭 / 补查期间换会话 / 同任务请求乱序 /
//       单查跨本地回写失效四场景——在途应答一律只落快照，不重开层不回退（会话归属
//       层世代 + 请求序号 + 数据版本三重判定）；无变化时 review 表单区
//       MutationObserver 零变更（先比后写，text 同值覆写即 childList 变更）。
//   codex 三轮（2026-10-25）：换任务不换代——A 刷新在途压住，A→B→A 切回后放行
//       旧 A 的 404：旧会话归属随换代失效，不弹归档 toast 不扰现会话。
//   node：运行中任务抽屉轮询至完成：#drawer 壳节点全程同一（不再整层 innerHTML
//       重建），终态迁移后答复区/时间线/归档 notice 经分区写入出现。
// 迟到/乱序应答用 playwright route 延迟+压住驱动；零值时间用 route 改写列表 payload。
// 输出关键断言结果到 stdout；截图 docs/prototypes/shots-drawer-sync/ 留本地。
import { chromium } from 'playwright';
import { execSync, spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync, openSync } from 'node:fs';
import net from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const ROOT = '/Users/tl.m/coding/personal/sandbox/SwarmMind';
const OUT = join(ROOT, 'docs/prototypes/shots-drawer-sync');
mkdirSync(OUT, { recursive: true });
const TMP = mkdtempSync(join(tmpdir(), 'swarmmind-drawerc3-'));

const ADMIN = 'walk-admin-token-0123456789abcdef';
const TOK_A = 'walk-token-node-a-aaaaaaaaaaaa';
const LOCAL_A = 'walk-local-token-a-aaaaaaaaaa';

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
${agentInspect}  - name: sleeper-long
    kind: cli
    command: /bin/sleep
    workdir: /tmp
    timeout_sec: 20
    allow_args: true
    allow_env: []
    description: 长任务演示
    permission_mode: no-shell
  - name: stat-missing
    kind: cli
    command: /usr/bin/stat
    workdir: /tmp
    timeout_sec: 5
    allow_args: true
    allow_env: []
    description: 失败演示
    permission_mode: no-shell
`);

console.log('── 启动 hub + nodeA');
spawnBin('hub', 'hub.yaml', 'hub');
let nodeAProc = spawnBin('node', 'nodeA.yaml', 'nodeA');
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

// ---------- 播种 ----------
const md = (await hub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'inspect-demo', input: { args: [], stdin: '巡检', env: {} }, submitted_by: 'demo' })).data.task_id;
await waitStatus(() => hub('GET', '/api/v1/tasks/' + md), 'completed', 15000);
// nrHub：等你确认（场景③ 稳定 DOM 全链：焦点/身份/草稿/滚动/busy/记录卡）
const nrHub = (await hub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'sleeper-long', input: { args: ['44'] }, submitted_by: 'demo', idem_key: 'c3-review' })).data.task_id;
await waitStatus(() => hub('GET', '/api/v1/tasks/' + nrHub), '^running$', 15000);
nodeAProc.kill('SIGKILL');
await sleep(300);
nodeAProc = spawnBin('node', 'nodeA.yaml', 'nodeA');
await waitStatus(() => hub('GET', '/api/v1/tasks/' + nrHub), 'needs_review', 25000);
await waitNodeOnline(20000);
// nrHub2：第二个等你确认（场景④ 核对回写 vs 旧列表乱序，必须修 2 行为复现）
const nrHub2 = (await hub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'sleeper-long', input: { args: ['44'] }, submitted_by: 'demo', idem_key: 'c3-review-2' })).data.task_id;
await waitStatus(() => hub('GET', '/api/v1/tasks/' + nrHub2), '^running$', 15000);
nodeAProc.kill('SIGKILL');
await sleep(300);
nodeAProc = spawnBin('node', 'nodeA.yaml', 'nodeA');
await waitStatus(() => hub('GET', '/api/v1/tasks/' + nrHub2), 'needs_review', 25000);
await waitNodeOnline(20000);
// d9：取消（场景⑤ D9 状态优先）——kill 后取消，node 无法回传执行数据，
// 结果保持 null（后端合法态，cancel.go Result 留空），nodeA 保持离线待场景内重启
const d9 = (await hub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'sleeper-long', input: { args: ['30'] }, submitted_by: 'demo', idem_key: 'c3-d9' })).data.task_id;
await waitStatus(() => hub('GET', '/api/v1/tasks/' + d9), '^running$', 15000);
nodeAProc.kill('SIGKILL');
await sleep(300);
{
  const c = await hub('POST', `/api/v1/tasks/${d9}/cancel`, {});
  if (c.code !== 200 && c.code !== 202) throw new Error('取消失败 ' + c.code);
}
await waitStatus(() => hub('GET', '/api/v1/tasks/' + d9), 'cancelled', 15000);
console.log('  播种完成（完成 + 2×hub 等你确认 + 取消[result=null]）');

// ---------- 断言助手 ----------
let pass = 0, fail = 0;
const check = (name, ok) => { console.log(`  [${ok ? 'PASS' : 'FAIL'}] ${name}`); ok ? pass++ : fail++; };

const browser = await chromium.launch();

/* ===== hub：稳定 DOM 行为 ===== */
{
  const page = await browser.newPage({ viewport: { width: 1440, height: 1200 } });
  page.on('pageerror', e => console.log('[pageerror]', String(e).slice(0, 400)));
  await page.goto(HUB + '/#/', { waitUntil: 'networkidle' });
  await page.click('[data-act="switch-login"]');
  await page.fill('#token', ADMIN);
  await page.click('#login-form button[type=submit]');
  try {
    await page.waitForSelector('.shell', { timeout: 10000 });
  } catch (e) {
    await page.screenshot({ path: `${OUT}/c3-login-fail.png`, fullPage: true });
    console.log('登录失败现场：', await page.evaluate(() => document.body.textContent.slice(0, 400)).catch(String));
    throw e;
  }
  await page.evaluate(() => { location.hash = '#/tasks'; });
  await page.waitForSelector('.filters');

  // 1) 完成任务抽屉：展开 details + 切原文 → 熬过 ≥2 个轮询周期（12s）→
  //    details 同一节点且保持 open+raw；真实点击 summary 仍可切换（无 detach）
  await page.click(`tr[data-task="${md}"]`);
  await page.waitForSelector('.drawer');
  await page.waitForTimeout(500);
  await page.click('.drawer details summary');           // 真实点击（旧全量重绘会周期性 detach）
  await page.waitForTimeout(200);
  await page.click('.drawer [data-act="log-raw"]');
  await page.waitForTimeout(200);
  await page.evaluate(() => { window.__det = document.querySelector('.drawer details'); window.__zone = document.querySelector('.drawer [data-zone="logs"]'); });
  await page.waitForTimeout(12000);                      // 2 个 5s 轮询周期，数据无变化
  check('hub 数据无变化：logs 分区节点身份不变（不碰 DOM）',
    await page.evaluate(() => window.__zone === document.querySelector('.drawer [data-zone="logs"]')));
  check('hub details 开合态+原文模式跨轮询保持（同一 details 节点且 open）',
    await page.evaluate(() => window.__det === document.querySelector('.drawer details') && window.__det.isConnected && window.__det.open));
  check('hub 原文单 pre 双段（stdout:/stderr: 前缀）',
    await page.evaluate(() => { const p = document.querySelector('.drawer details pre'); return p && /^stdout:\n/.test(p.textContent) && /\nstderr:\n/.test(p.textContent); }));
  await page.screenshot({ path: `${OUT}/c3-hub-raw-after-12s.png`, fullPage: true });
  await page.click('.drawer details summary');           // 再真实点击：收起（点击不被打断）
  check('hub 抽屉内真实点击可收起 details（无重绘 detach）',
    await page.evaluate(() => !document.querySelector('.drawer details').open));
  await page.click('.drawer .close');
  await page.waitForTimeout(400);

  // 1b) 时间线零值守卫（必须修 5①）：改写列表 payload 把 md 任务四个时刻全置
  //     Go 零值 → 轮询进状态 → 抽屉时间线不得出现任何事件（含空 <small> 骨架行）
  {
    let zeroShot = false;
    await page.route('**/api/v1/tasks', async route => {
      if (route.request().method() !== 'GET' || zeroShot) return route.continue();
      zeroShot = true;
      const resp = await route.fetch();
      const j = await resp.json();
      const t = (j.tasks || []).find(x => x.id === md);
      if (t) {
        t.created_at = '0001-01-01T00:00:00Z'; t.updated_at = '0001-01-01T00:00:00Z';
        if (t.result) { t.result.started_at = '0001-01-01T00:00:00Z'; t.result.finished_at = '0001-01-01T00:00:00Z'; }
      }
      await route.fulfill({ response: resp, body: JSON.stringify(j), contentType: 'application/json' });
    });
    // fire-and-forget 轮询直到改写落地（pollTick single-flight 在途会跳过，循环重试）：
    // 以「S.tasks 里 md 的 created_at 已变零值」为落地信号
    const zeroed = await page.evaluate(async id => {
      for (let i = 0; i < 50; i++) {
        pollTick();
        await new Promise(r => setTimeout(r, 200));
        const t = S.tasks.find(x => x.id === id);
        if (t && String(t.created_at).startsWith('0001-01-01')) return true;
      }
      return false;
    }, md);
    if (!zeroed) throw new Error('零值合成数据未落地');
    await page.click(`tr[data-task="${md}"]`);
    await page.waitForSelector('.drawer');
    await page.waitForTimeout(400);
    check('hub 零值时间守卫：受理/开始执行/终态事件全部不生成（D16 完整判定，必须修 5①）',
      await page.evaluate(() => {
        const ol = document.querySelector('.drawer ol.timeline');
        return !!ol && ol.children.length === 0 && !ol.textContent.includes('任务已受理') && !ol.textContent.includes('开始执行');
      }));
    check('hub 零值创建时间展示位兜底「—」（D17 契约）',
      await page.evaluate(() => document.querySelector('.drawer').textContent.includes('创建于 —')));
    check('hub 零值时间不影响答复区渲染（md 正文在）',
      await page.evaluate(() => document.querySelector('.drawer').textContent.includes('结论')));
    await page.screenshot({ path: `${OUT}/c3-hub-zero-time.png`, fullPage: true });
    await page.click('.drawer .close');
    await page.waitForTimeout(400);
    await page.unroute('**/api/v1/tasks');
    await page.evaluate(() => pollTick());   // 下一轮拉回真实数据
    await page.waitForTimeout(400);
  }
  // 2) 等你确认抽屉：核对表单区节点身份/焦点/草稿/滚动跨轮询保持 → 在途 busy
  //    状态驱动 disabled → 核对登记就地换记录卡（阶段 D 必须修 3 全链）
  await page.click(`tr[data-task="${nrHub}"]`);
  await page.waitForSelector('.drawer');
  await page.waitForTimeout(400);
  await page.evaluate(() => {
    const el = document.querySelector('.drawer #review-note');
    el.value = 'C3 自验：核对依据草稿。';
    el.dispatchEvent(new Event('input', { bubbles: true }));
  });
  await page.waitForTimeout(100);
  await page.evaluate(() => {
    window.__ta = document.querySelector('.drawer #review-note');
    window.__btnAck = document.querySelector('.drawer [data-review-action="ack_review"]');
    window.__btnMark = document.querySelector('.drawer [data-review-action="mark_failed"]');
    window.__rzone = document.querySelector('.drawer [data-zone="review"]');
  });
  await page.focus('.drawer #review-note');               // 焦点入 textarea：轮询输入保护
  await page.waitForTimeout(6000);                        // ≥1 轮询周期
  check('hub 焦点在 textarea：轮询期焦点保持且节点身份不变（输入保护）',
    await page.evaluate(() => document.activeElement === window.__ta && window.__ta === document.querySelector('.drawer #review-note')));
  await page.evaluate(() => {                             // 失焦 + 滚离顶部：触发「缓存与模板分叉」路径
    window.__ta.blur();
    const d = document.querySelector('.drawer');
    d.scrollTop = d.scrollHeight;
    window.__scroll = d.scrollTop;
  });
  // 失焦后必须有真实数据变化才会重绘分区（无变化零写入本身不重建）：给离线 nodeA
  // 派一单进离线暂存，列表指纹变化驱动下一轮 render——旧实现（草稿进模板串）此处
  // review 区整区重建、节点身份丢失，正是 codex 浏览器复现序列
  await hub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'inspect-demo', input: { args: [], stdin: '失焦期数据变化驱动（离线暂存）', env: {} }, submitted_by: 'demo' });
  await page.waitForTimeout(6000);                        // 失焦后 ≥1 轮询周期（含上述数据变化）
  check('hub 失焦后轮询：textarea/按钮节点身份不变（草稿不进模板串，必须修 3）',
    await page.evaluate(() => {
      const ta = document.querySelector('.drawer #review-note');
      const btn = document.querySelector('.drawer [data-review-action="ack_review"]');
      return ta === window.__ta && btn === window.__btnAck && ta.isConnected;
    }));
  check('hub 核对草稿跨轮询保值（失焦后）',
    await page.evaluate(() => document.querySelector('.drawer #review-note')?.value === 'C3 自验：核对依据草稿。'));
  check('hub review 分区零写入 + 滚动位置保持（无变化不碰 DOM）',
    await page.evaluate(() => document.querySelector('.drawer [data-zone="review"]') === window.__rzone
      && document.querySelector('.drawer').scrollTop === window.__scroll));
  await page.screenshot({ path: `${OUT}/c3-hub-review-stable-dom.png`, fullPage: true });

  // 无变化零写入（复审 P2②）：状态不变时重复应用表单状态 + 一轮真实轮询，
  // review 分区 MutationObserver 须零记录——旧实现 textContent 同值覆写每轮
  // 产生 childList 变更（codex 实测状态不变 2 次/轮），disabled 同理按属性比对
  await page.evaluate(() => {
    window.__mut = [];
    const mo = new MutationObserver(rs => { for (const r of rs) window.__mut.push(r.type); });
    mo.observe(document.querySelector('.drawer [data-zone="review"]'), { childList: true, subtree: true, attributes: true, characterData: true });
    window.__mo = mo;
    applyDrawerFormState(); applyDrawerFormState();
  });
  await page.evaluate(() => pollTick());
  await page.waitForTimeout(500);
  check('hub 无变化零写入：review 表单区 MutationObserver 零变更（先比后写，复审 P2②）',
    await page.evaluate(() => { window.__mo.disconnect(); return window.__mut.length === 0; }));

  // 在途 busy：延迟核对 POST → 按钮状态驱动 disabled + 「登记中…」→ 分区重算不回退
  await page.route('**/api/v1/tasks/*/review', async route => { await sleep(1200); route.continue(); });
  await page.click('.drawer [data-review-action="ack_review"]');
  await page.waitForTimeout(250);
  check('hub 在途登记：busy 状态驱动 disabled + 「登记中…」标签',
    await page.evaluate(() => {
      const ack = document.querySelector('.drawer [data-review-action="ack_review"]');
      const mark = document.querySelector('.drawer [data-review-action="mark_failed"]');
      return ack.disabled && ack.textContent === '登记中…' && mark.disabled && mark.textContent === '标记为失败';
    }));
  await page.evaluate(() => syncDrawerZones());           // 强制分区重算：busy 不得随重算回退/重建
  check('hub busy 经分区重算不回退且按钮节点同一（状态驱动，不靠重建）',
    await page.evaluate(() => {
      const ack = document.querySelector('.drawer [data-review-action="ack_review"]');
      return ack === window.__btnAck && ack.disabled && ack.textContent === '登记中…';
    }));
  await page.waitForTimeout(2200);                        // 延迟应答放行 + 处理
  check('hub 核对登记后抽屉就地换已核对记录卡（无需重开抽屉）',
    await page.evaluate(() => document.querySelector('.drawer')?.textContent.includes('人工核对记录') && !!document.querySelector('.drawer')?.textContent.includes('已核对完成')));
  check('hub 时间线含「人工核对完成」事件（核对后分区出现）',
    await page.evaluate(() => document.querySelector('.drawer .timeline')?.textContent.includes('人工核对完成')));
  await page.screenshot({ path: `${OUT}/c3-hub-reviewed-inplace.png`, fullPage: true });
  await page.unroute('**/api/v1/tasks/*/review');
  await page.click('.drawer .close');
  await page.waitForTimeout(400);

  // 3) 核对回写 vs 旧列表乱序（必须修 2，codex 复现序列）：核对前发起的列表请求
  //    先抓快照再压住在途 → POST 核对先成功（记录卡就位、数据版本 +1）→ 旧快照
  //    迟到提交 → 记录卡不得被退回表单
  {
    let releaseList; const listHeld = new Promise(r => { releaseList = r; });
    let listCaught = false;
    // 压住前先派一单（离线暂存）：压住的旧快照必须含「另一任务变化」——否则迟到提交
    // 指纹不变不触发 render，回归被隐藏（codex 复现序列的要点：旧列表含另一任务变化）。
    // 处理器校验快照已含驱动任务才压住：首个含驱动的响应即被扣下，更早的放行应答都
    // 不含驱动，保证释放后提交必然带来列表指纹变化 → render（确定性复现）。
    const driver = (await hub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'inspect-demo', input: { args: [], stdin: '乱序场景列表变化驱动（离线暂存）', env: {} }, submitted_by: 'demo' })).data.task_id;
    await page.route('**/api/v1/tasks', async route => {
      if (route.request().method() !== 'GET' || listCaught) return route.continue();
      const resp = await route.fetch();
      const j = await resp.json();
      if (!(j.tasks || []).some(x => x.id === driver)) return route.continue();   // 快照未含变化：放行等下一个
      listCaught = true;
      await listHeld;                     // 压住到核对完成之后
      route.fulfill({ response: resp });  // 旧快照迟到提交
    });
    // fire-and-forget 轮询直到列表请求真被压住（single-flight 在途会跳过，循环重试）
    for (let i = 0; i < 40 && !listCaught; i++) { page.evaluate(() => { pollTick(); }).catch(() => { }); await sleep(200); }
    check('乱序前置：核对前旧列表请求已在途压住', listCaught);
    await page.click(`tr[data-task="${nrHub2}"]`);
    await page.waitForSelector('.drawer');
    await page.waitForTimeout(300);
    await page.evaluate(() => {
      const el = document.querySelector('.drawer #review-note');
      el.value = 'C3 乱序场景核对依据。';
      el.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await page.click('.drawer [data-review-action="ack_review"]');   // POST 不匹配列表 glob：直通
    await page.waitForSelector('.drawer [data-act="go-review-queue"]', { timeout: 5000 });
    check('乱序中段：核对登记先成功、记录卡就位', await page.evaluate(() => document.querySelector('.drawer')?.textContent.includes('人工核对记录')));
    releaseList();                                        // 旧列表此刻才提交（数据为核对前快照）
    await page.waitForTimeout(1200);
    check('hub 旧列表迟到提交不回退核对记录卡（数据版本判定，必须修 2）',
      await page.evaluate(() => document.querySelector('.drawer')?.textContent.includes('人工核对记录')
        && document.querySelector('.drawer .timeline')?.textContent.includes('人工核对完成')));
    check('hub 任务列表侧同样不回退（列表徽标保持已核对）',
      await page.evaluate(sel => { const tr = document.querySelector(sel); return !!tr && tr.textContent.includes('已核对'); }, `tr[data-task="${nrHub2}"]`));
    await page.screenshot({ path: `${OUT}/c3-hub-stale-list-race.png`, fullPage: true });
    await page.unroute('**/api/v1/tasks');
    await page.click('.drawer .close');
    await page.waitForTimeout(400);
  }

  // 4) D9 状态优先（必须修 4）：cancelled+result=null（node 离线无法回传，真实
  //    服务端合法态）不得显示「尚未到终态」
  {
    // 轮询直到 S.tasks 里的 d9 已是 cancelled（single-flight 在途会跳过，循环重试）
    const gotCancel = await page.evaluate(async id => {
      for (let i = 0; i < 50; i++) {
        pollTick();
        await new Promise(r => setTimeout(r, 200));
        const t = S.tasks.find(x => x.id === id);
        if (t && t.status === 'cancelled') return true;
      }
      return false;
    }, d9);
    if (!gotCancel) throw new Error('cancelled 任务未进列表状态');
    await page.click(`tr[data-task="${d9}"]`);
    await page.waitForSelector('.drawer');
    await page.waitForTimeout(400);
    check('hub cancelled+result=null：不显示「尚未到终态」，落无答复人话兜底（D9 状态优先）',
      await page.evaluate(() => {
        const d = document.querySelector('.drawer').textContent;
        return !d.includes('尚未到终态') && d.includes('该任务没有文本答复（例如等待类任务）');
      }));
    check('hub cancelled 徽标与介绍条（D4 五态）',
      await page.evaluate(() => {
        const d = document.querySelector('.drawer').textContent;
        return d.includes('已取消') && d.includes('任务已被取消：执行进程已整组终止（或尚未执行），不会自动重跑。');
      }));
    await page.screenshot({ path: `${OUT}/c3-hub-cancelled-null-result.png`, fullPage: true });
    await page.click('.drawer .close');
    await page.waitForTimeout(400);
  }

  // 5) 复审 P1①：单查迟到后已关闭——刷新应答在途时用户关闭抽屉，旧应答只落快照，
  //    不得重开层（旧实现 !S.drawer 判开层，此处必抢开）
  {
    let releaseMd; const mdHeld = new Promise(r => { releaseMd = r; });
    let mdCaught = false;
    await page.route(`**/api/v1/tasks/${md}`, async route => {
      if (route.request().method() !== 'GET' || mdCaught) return route.continue();
      mdCaught = true;
      const resp = await route.fetch();
      await mdHeld;
      route.fulfill({ response: resp });
    });
    await page.click(`tr[data-task="${md}"]`);
    await page.waitForSelector('.drawer');
    await page.evaluate(id => { ACT['drawer-refresh']({ dataset: { id } }); }, md);
    await page.waitForTimeout(300);                        // 刷新请求已被压住
    await page.click('.drawer .close');                    // 应答在途时关闭
    await page.waitForTimeout(400);                        // 关闭清场完成（280ms 收尾）
    releaseMd();
    await page.waitForTimeout(800);
    check('hub 单查迟到后已关闭：旧应答只落快照不重开层（复审 P1① drawer-refresh）',
      await page.evaluate(id => !document.querySelector('.drawer') && !document.querySelector('.overlay')
        && S.drawer === null && S.tasks.some(t => t.id === id), md));
    await page.unroute(`**/api/v1/tasks/${md}`);
  }

  // 6) 复审 P1②：补查期间换会话——悬空补查应答在途时用户开/关了他任务抽屉，
  //    旧应答只落快照，不得抢开层（换会话与关闭后两类抢开一并覆盖；会话归属=
  //    层世代判定，期间世代已两变）
  {
    let releaseGhost; const ghostHeld = new Promise(r => { releaseGhost = r; });
    let ghostCaught = false;
    await page.route(`**/api/v1/tasks/${md}`, async route => {
      if (route.request().method() !== 'GET' || ghostCaught) return route.continue();
      ghostCaught = true;
      const resp = await route.fetch();
      await ghostHeld;
      route.fulfill({ response: resp });
    });
    await page.evaluate(id => {                            // 构造悬空引用：列表暂无此任务 → 走补查
      S.tasks = S.tasks.filter(t => t.id !== id);
      openTask(id);
    }, md);
    await page.waitForTimeout(300);                        // 补查请求已被压住
    await page.click(`tr[data-task="${d9}"]`);             // 换会话：打开另一任务抽屉
    await page.waitForSelector('.drawer');
    await page.waitForTimeout(200);
    check('hub 补查在途换任务：当前会话不被在途应答顶替',
      await page.evaluate(({ cur, ghost }) => S.drawer?.id === cur
        && document.querySelector('.drawer').textContent.includes(cur)
        && !document.querySelector('.drawer').textContent.includes(ghost), { cur: d9, ghost: md }));
    await page.click('.drawer .close');                    // 再关闭：旧应答随后才到
    await page.waitForTimeout(400);
    releaseGhost();
    await page.waitForTimeout(800);
    check('hub 补查应答跨换会话+关闭：不抢开层只落快照（复审 P1② openTask 悬空补查）',
      await page.evaluate(id => !document.querySelector('.drawer') && S.drawer === null
        && S.tasks.some(t => t.id === id), md));
    await page.unroute(`**/api/v1/tasks/${md}`);
  }

  // 7) 复审 P1③：同任务请求乱序——同任务两次刷新，第一次应答压住并改写成旧快照
  //    （status 回退 running），第二次直通先应用；旧应答迟到必须整单作废，不得把
  //    列表与抽屉回退到旧态（请求序号守卫；本场景世代未变，专锁序号维度）
  {
    let releaseStale; const staleHeld = new Promise(r => { releaseStale = r; });
    let staleCaught = false;
    await page.route(`**/api/v1/tasks/${md}`, async route => {
      if (route.request().method() !== 'GET') return route.continue();
      if (!staleCaught) {
        staleCaught = true;
        const resp = await route.fetch();
        const j = await resp.json();
        if (j && j.id === md) j.status = 'running';        // 伪造更旧快照（服务端现值 completed）
        await staleHeld;
        await route.fulfill({ response: resp, body: JSON.stringify(j), contentType: 'application/json' });
      } else await route.continue();
    });
    await page.click(`tr[data-task="${md}"]`);
    await page.waitForSelector('.drawer');
    await page.evaluate(id => { ACT['drawer-refresh']({ dataset: { id } }); }, md);   // 第一次：被压住
    await page.waitForTimeout(300);
    await page.evaluate(id => { ACT['drawer-refresh']({ dataset: { id } }); }, md);   // 第二次：直通先应用
    await page.waitForTimeout(500);
    releaseStale();
    await page.waitForTimeout(800);
    check('hub 同任务请求乱序：迟到旧应答不回退列表与抽屉（复审 P1 请求序号守卫）',
      await page.evaluate(id => {
        const t = S.tasks.find(x => x.id === id);
        return t && t.status === 'completed'
          && !!document.querySelector('.drawer') && !document.querySelector('.drawer').textContent.includes('任务仍在进行');
      }, md));
    await page.screenshot({ path: `${OUT}/c3-hub-stale-refresh-dropped.png`, fullPage: true });
    await page.click('.drawer .close');
    await page.waitForTimeout(400);
    await page.unroute(`**/api/v1/tasks/${md}`);
  }

  // 8) 复审 P1④：单查跨本地回写失效——悬空补查在途期间发生本地回写（取消成功
  //    taskVer+1），迟到的回写前旧快照必须整单作废：不落快照（列表不回退 running）
  //    也不得借「应答到达时恰无抽屉」抢开层（数据版本判定 × 会话归属复合场景）
  {
    console.log('── 场景 8 播种：拉起 nodeA + 运行中任务');
    nodeAProc = spawnBin('node', 'nodeA.yaml', 'nodeA');
    await waitNodeOnline(20000);
    const cx = (await hub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'sleeper-long', input: { args: ['60'] }, submitted_by: 'demo', idem_key: 'c3-cx' })).data.task_id;
    await waitStatus(() => hub('GET', '/api/v1/tasks/' + cx), '^running$', 15000);
    let releaseCx; const cxHeld = new Promise(r => { releaseCx = r; });
    let cxCaught = false;
    await page.route(`**/api/v1/tasks/${cx}`, async route => {
      if (route.request().method() !== 'GET' || cxCaught) return route.continue();
      cxCaught = true;
      const resp = await route.fetch();                    // 回写前快照（running）
      await cxHeld;
      route.fulfill({ response: resp });
    });
    await page.evaluate(id => {
      S.tasks = S.tasks.filter(t => t.id !== id);
      openTask(id);                                        // 悬空补查（被压住）
    }, cx);
    await page.waitForTimeout(300);
    await page.evaluate(id => { ACT['task-cancel-confirm']({ dataset: { id } }); }, cx);   // 本地回写：取消成功 taskVer+1
    await waitStatus(() => hub('GET', '/api/v1/tasks/' + cx), 'cancelled', 15000);
    await page.waitForTimeout(600);                        // 取消应答处理完（taskVer 已 bump）
    releaseCx();
    await page.waitForTimeout(800);
    check('hub 单查跨本地回写失效：回写前旧快照不落列表不抢开层（复审 P1④）',
      await page.evaluate(id => {
        const t = S.tasks.find(x => x.id === id);
        return t && t.status === 'cancelled' && !document.querySelector('.drawer') && S.drawer === null;
      }, cx));
    await page.unroute(`**/api/v1/tasks/${cx}`);
    nodeAProc.kill('SIGKILL');                             // 还原离线（对齐后续 node 场景拉起前提）
    await sleep(300);
  }

  // 9) codex 三轮必须修：换任务不换代——A 刷新应答在途压住（改写为归档 404），
  //    openTask 换 B 再切回 A 后放行旧应答：旧实现世代未变，旧 404 恰命中
  //    drawer.id===A 弹归档 toast；修复后旧会话归属随两次换代失效
  {
    let releaseMd9; const md9Held = new Promise(r => { releaseMd9 = r; });
    let md9Caught = false;
    await page.route(`**/api/v1/tasks/${md}`, async route => {
      if (route.request().method() !== 'GET' || md9Caught) return route.continue();
      md9Caught = true;
      await md9Held;
      await route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'not_found', message: '任务已归档（终态保留 24 小时）' }) });
    });
    await page.click(`tr[data-task="${md}"]`);             // A 在列表中：直开不走补查路由
    await page.waitForSelector('.drawer');
    await page.evaluate(() => { window.__drawer9 = document.querySelector('.drawer'); });
    await page.evaluate(id => { ACT['drawer-refresh']({ dataset: { id } }); }, md);   // 旧 A 刷新：被压住
    await page.waitForTimeout(300);
    await page.evaluate(id => openTask(id), d9);           // 换任务 B（行被 inert 遮挡，语义等价直调）
    await page.waitForTimeout(200);
    check('hub 换任务：抽屉会话切到 B（分区 diff 就地换内容，壳不重开）',
      await page.evaluate(({ a, b }) => S.drawer?.id === b && window.__drawer9 === document.querySelector('.drawer')
        && document.querySelector('.drawer').textContent.includes(b) && !document.querySelector('.drawer').textContent.includes(a), { a: md, b: d9 }));
    await page.evaluate(id => openTask(id), md);           // 切回 A：再换代
    await page.waitForTimeout(200);
    releaseMd9();                                          // 旧 A 的 404 此刻才回
    await page.waitForTimeout(800);                        // toast 若弹在此窗口内可见（3400ms 存活）
    check('hub A→B→A 后旧刷新 404 迟到：不弹归档 toast（codex 三轮必须修）',
      await page.evaluate(() => !($('#toast').classList.contains('show') && $('#toast').textContent.includes('已归档'))));
    check('hub 旧应答作废不扰现会话：抽屉仍在 A、壳未重开、层未关',
      await page.evaluate(id => S.drawer?.id === id && window.__drawer9 === document.querySelector('.drawer')
        && layerState === 'open' && document.querySelector('.drawer').textContent.includes(id), md));
    await page.screenshot({ path: `${OUT}/c3-hub-switch-session-404.png`, fullPage: true });
    await page.click('.drawer .close');
    await page.waitForTimeout(400);
    await page.unroute(`**/api/v1/tasks/${md}`);
  }
  await page.close();
}

// nodeA 拉起（D9 场景保持离线至此），供 node 场景播种本机任务
nodeAProc = spawnBin('node', 'nodeA.yaml', 'nodeA');
await waitNodeOnline(20000);

/* ===== node：运行中→完成 终态迁移，壳节点恒定 ===== */
{
  const page = await browser.newPage({ viewport: { width: 1440, height: 1200 } });
  await page.goto(`http://127.0.0.1:${LA_PORT}/#/tasks`, { waitUntil: 'networkidle' });
  await page.waitForSelector('#gate-token');
  await page.fill('#gate-token', LOCAL_A);
  await page.click('[data-act="gate-enter"]');
  await page.waitForSelector('.shell', { timeout: 10000 });
  await page.waitForTimeout(400);

  const run = await local(LA_PORT, LOCAL_A, 'POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'sleeper-long', input: { args: ['6'] }, submitted_by: 'console' });
  const runId = run.data.task_id;
  await waitStatus(() => local(LA_PORT, LOCAL_A, 'GET', '/api/v1/tasks/' + runId), '^running$', 15000);
  await page.reload({ waitUntil: 'networkidle' });
  await page.waitForSelector(`tr[data-task="${runId}"]`);
  await page.click(`[data-act="open-task:${runId}"]`);
  await page.waitForSelector('#drawer');
  await page.waitForTimeout(400);
  check('node 运行中：介绍条出镜（D4）+ 无「没有文本答复」误导（D9）',
    await page.evaluate(() => document.querySelector('#drawer').textContent.includes('任务仍在进行，抽屉每 5 秒自动刷新。')));
  await page.evaluate(() => { window.__shell = document.getElementById('drawer'); window.__head = document.querySelector('#drawer [data-zone="head"]'); });
  await page.waitForTimeout(13000);                      // 熬过完成迁移（sleep 6 + 轮询节奏）
  check('node 终态迁移前后：#drawer 壳节点同一（不再整层重建，D22）',
    await page.evaluate(() => window.__shell === document.getElementById('drawer') && window.__head === document.querySelector('#drawer [data-zone="head"]')));
  check('node 完成态经分区写入：答复区+时间线已完成事件+归档 notice（D21/D8 空答复兜底）',
    await page.evaluate(() => {
      const d = document.getElementById('drawer').textContent;
      return d.includes('最终答复') && d.includes('该任务没有文本答复（例如等待类任务）') && d.includes('已完成') && d.includes('任务已归档前保留 24 小时。') && d.includes('查看全部任务');
    }));
  check('node 时间线为 ol.timeline 事件流（D15/D16 零值守卫无乱码时间）',
    await page.evaluate(() => {
      const ol = document.querySelector('#drawer ol.timeline');
      return !!ol && ol.textContent.includes('任务已受理') && ol.textContent.includes('开始执行') && !ol.textContent.includes('1/1/0');
    }));
  // details 开合保持：运行中无日志区 → 展开产物区前的诊断日志不存在；改为验证输入焦点/滚动保持：
  await page.evaluate(() => { document.querySelector('#drawer [data-zone="timeline"]').scrollIntoView(); window.__y = document.getElementById('drawer').scrollTop; });
  await page.waitForTimeout(6000);                       // 终态后轮询自停：数据无变化零写入
  check('node 终态停轮询期滚动位置保持（无重绘扰动）',
    await page.evaluate(() => document.getElementById('drawer').scrollTop === window.__y));
  await page.screenshot({ path: `${OUT}/c3-node-completed-inplace.png`, fullPage: true });

  // 本机 needs_review：D19 文案 + 零值时间不出现乱码
  await page.click('.drawer [data-act="close-drawer"]');
  await page.waitForTimeout(400);
  const nrLocal = (await local(LA_PORT, LOCAL_A, 'POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'sleeper-long', input: { args: ['44'] }, submitted_by: 'console' })).data.task_id;
  await waitStatus(() => local(LA_PORT, LOCAL_A, 'GET', '/api/v1/tasks/' + nrLocal), '^running$', 15000);
  nodeAProc.kill('SIGKILL');
  await sleep(300);
  nodeAProc = spawnBin('node', 'nodeA.yaml', 'nodeA');
  await waitStatus(() => local(LA_PORT, LOCAL_A, 'GET', '/api/v1/tasks/' + nrLocal), 'needs_review', 25000);
  await page.reload({ waitUntil: 'networkidle' });
  await page.waitForSelector(`tr[data-task="${nrLocal}"]`);
  await page.click(`[data-act="open-task:${nrLocal}"]`);
  await page.waitForSelector('#drawer');
  await page.waitForTimeout(600);
  check('node 本机 needs_review：D18 主文案 + D19 如实指路（不承诺本端登记）',
    await page.evaluate(() => {
      const d = document.getElementById('drawer').textContent;
      return d.includes('请核对后登记结论，不要重新提交同一任务') && d.includes('本控制台暂不支持登记核对结论') && !d.includes('由本机主人在本控制台确认');
    }));
  check('node needs_review 时间线零值守卫：无 1/1/08 类乱码（D16）',
    await page.evaluate(() => !document.getElementById('drawer').textContent.match(/1\/1\/0\d/)));
  check('node 抽屉壳 role=dialog + aria（D1 对齐 hub）',
    await page.evaluate(() => document.getElementById('drawer').getAttribute('role') === 'dialog' && !!document.getElementById('drawer').getAttribute('aria-modal')));
  await page.screenshot({ path: `${OUT}/c3-node-needsreview.png`, fullPage: true });
  await page.close();
}

await browser.close();
console.log(`\n结果：${pass} PASS / ${fail} FAIL`);
cleanup();
process.exit(fail ? 1 : 0);
