// 文案清扫 codex 复审闭合补拍（shoot-copy-sweep.mjs 的复审帧）：总览恢复卡
// 三态计数收进诊断折叠 + 形态文案人话前置（技术代号进悬停）。起 hub +
// nodeA(组网) + nodeC(standalone)，重拍受影响的总览帧（1440/1276）覆盖
// shots-copy-sweep/ 同名文件，另加折叠展开态帧 + 悬停代号 DOM 断言。
import { chromium } from 'playwright';
import { execSync, spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync, openSync } from 'node:fs';
import net from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const ROOT = '/Users/tl.m/coding/personal/sandbox/SwarmMind';
const OUT = join(ROOT, 'docs/prototypes/shots-copy-sweep');
const TMP = mkdtempSync(join(tmpdir(), 'swarmmind-reviewfix-'));

const ADMIN = 'rvfix-admin-token-0123456789abcdef';
const TOK_A = 'rvfix-token-node-a';
const LOCAL_A = 'rvfix-local-token-a';
const LOCAL_C = 'rvfix-local-token-c';

const freePort = () => new Promise(res => {
  const s = net.createServer(); s.listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => res(p)); });
});
const HUB_NODE_PORT = await freePort(), API_PORT = await freePort(), LA_PORT = await freePort(), LC_PORT = await freePort();
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

console.log('── 构建 + 启动 hub + nodeA(组网) + nodeC(standalone)');
mkdirSync(join(TMP, 'bin'), { recursive: true });
execSync(`go build -o ${JSON.stringify(join(TMP, 'bin'))} ./cmd/hub ./cmd/node`, { cwd: ROOT, stdio: 'inherit' });
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
prime_agent: echo-demo
concurrency: 2
data_dir: ${TMP}/data-a
base_env:
  PATH: /usr/bin:/bin
  TMPDIR: /tmp
agents:
  - name: echo-demo
    kind: cli
    command: /bin/echo
    workdir: /tmp
    timeout_sec: 10
    allow_args: true
    allow_env: ["TMPDIR"]
    description: 回声演示：原样输出任务参数
    permission_mode: no-shell
`);
writeFileSync(join(TMP, 'nodeC.yaml'), `node_id: nodeC
local_listen: 127.0.0.1:${LC_PORT}
local_token: ${LOCAL_C}
concurrency: 2
data_dir: ${TMP}/data-c
base_env:
  PATH: /usr/bin:/bin
  TMPDIR: /tmp
agents:
  - name: echo-local
    kind: cli
    command: /bin/echo
    workdir: /tmp
    timeout_sec: 10
    allow_args: true
    allow_env: ["TMPDIR"]
    description: 本机回声（standalone）
    permission_mode: no-shell
`);
spawnBin('hub', 'hub.yaml', 'hub');
spawnBin('node', 'nodeA.yaml', 'nodeA');
spawnBin('node', 'nodeC.yaml', 'nodeC');

{
  const t0 = Date.now();
  while (Date.now() - t0 < 15000) {
    try { if ((await hub('POST', '/api/v1/login', { token: ADMIN })).code === 200) break; } catch { }
    await sleep(200);
  }
  if (!cookie) throw new Error('hub 未就绪');
}
let onlineA = false;
{
  const t0 = Date.now();
  while (Date.now() - t0 < 15000) {
    try {
      const n = (await hub('GET', '/api/v1/nodes')).data?.nodes?.find(x => x.node_id === 'nodeA');
      if (n?.online) { onlineA = true; break; }
    } catch { }
    await sleep(200);
  }
  if (!onlineA) throw new Error('nodeA 未上线');
}
// nodeC standalone 需钦定管家激活编排接口（走控制台同一 declare 协议），再派一单
// 本机任务让账本计数非零（折叠展开帧出真实数字）
{
  const d = await local(LC_PORT, LOCAL_C, 'POST', '/api/v1/local/prime/declare', { prime_agent: 'echo-local' });
  if (d.code !== 200) throw new Error('nodeC 钦定失败 ' + d.code + ' ' + JSON.stringify(d.data));
  const r = await local(LC_PORT, LOCAL_C, 'POST', '/api/v1/tasks', { target_node: 'nodeC', target_agent: 'echo-local', input: { args: ['review-fix'] }, submitted_by: 'console' });
  if (r.code !== 202 && r.code !== 200) throw new Error('nodeC 本机提交失败 ' + r.code);
}
// nodeA 派两单（一完成一运行中）让组网总览账本计数有内容
{
  const r = await hub('POST', '/api/v1/tasks', { target_node: 'nodeA', target_agent: 'echo-demo', input: { args: ['review-fix'] }, submitted_by: 'demo' });
  const t0 = Date.now();
  while (Date.now() - t0 < 15000) {
    const d = await hub('GET', '/api/v1/tasks/' + r.data.task_id).catch(() => null);
    if (d?.code === 200 && d.data.status === 'completed') break;
    await sleep(200);
  }
}
console.log('  环境就绪');

const browser = await chromium.launch();
async function newPage(width = 1440, height = 900) {
  return browser.newPage({ viewport: { width, height } });
}
const shot = (page, name) => page.screenshot({ path: `${OUT}/${name}.png`, fullPage: true });

async function openConsole(port, token) {
  const page = await newPage();
  await page.goto(`http://127.0.0.1:${port}/#/overview`, { waitUntil: 'networkidle' });
  await page.waitForSelector('#gate-token');
  await page.fill('#gate-token', token);
  await page.click('[data-act="gate-enter"]');
  await page.waitForSelector('.shell', { timeout: 10000 });
  await page.waitForTimeout(700);
  return page;
}

/* ===== nodeA 总览（组网模式） ===== */
{
  const page = await openConsole(LA_PORT, LOCAL_A);
  // 契约断言：主面无三态裸词卡；诊断折叠存在且默认收起；形态代号进悬停
  const chk = await page.evaluate(() => ({
    ledgerHeadGone: !document.body.textContent.includes('执行账本（三态）'),
    diagFold: !!document.querySelector('main details summary')?.textContent.includes('诊断：账本计数'),
    foldClosedByDefault: !document.querySelector('main details').open,
    hoverTitle: document.querySelector('main [title^="形态代号："]')?.title || '',
  }));
  if (!chk.ledgerHeadGone || !chk.diagFold || !chk.foldClosedByDefault || chk.hoverTitle !== '形态代号：networked') {
    throw new Error('组网总览契约断言失败: ' + JSON.stringify(chk));
  }
  await shot(page, 'node-02-overview');
  await page.click('main details summary');
  await page.waitForTimeout(300);
  await shot(page, 'node-12-overview-diag-open');
  await page.setViewportSize({ width: 1276, height: 900 });
  await page.waitForTimeout(400);
  await shot(page, 'node-40-narrow-overview');
  await page.close();
}

/* ===== nodeC 总览（standalone 模式） ===== */
{
  const page = await openConsole(LC_PORT, LOCAL_C);
  const chk = await page.evaluate(() => ({
    hoverTitle: document.querySelector('main [title^="形态代号："]')?.title || '',
    plainMode: !!document.querySelector('main .mini-stat b') && !document.body.textContent.includes('standalone（本机自治'),
  }));
  if (chk.hoverTitle !== '形态代号：standalone' || !chk.plainMode) {
    throw new Error('standalone 总览契约断言失败: ' + JSON.stringify(chk));
  }
  await shot(page, 'nodec-01-overview-standalone');
  await page.setViewportSize({ width: 1276, height: 900 });
  await page.waitForTimeout(400);
  await shot(page, 'nodec-40-narrow-overview');
  await page.close();
}

await browser.close();
console.log('复审补拍完成 → ' + OUT);
process.exit(0);
