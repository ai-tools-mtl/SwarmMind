// SwarmMind 控制台 E2E 测试（Playwright，真实浏览器交互）
// 运行：node tests/e2e-console.mjs
// 覆盖：首次初始化/密码登录/总览/节点/任务/运维控制（含总理模式派发）/策略/设置（账户+令牌）/退出
// 语义：needs_review 提示、抽屉详情、批次报告、令牌明文一次性展示

import { chromium } from 'playwright';

const BASE = process.env.E2E_BASE || 'http://1.13.158.180:8444';
const USER = process.env.E2E_USER || 'admin';
const PASS = process.env.E2E_PASS || 'Swarm@2026';

const results = [];
const p_sleep = ms => new Promise(r => setTimeout(r, ms));
function record(id, name, ok, note = '') {
  results.push({ id, name, ok, note });
  console.log(`${ok ? '✅ PASS' : '❌ FAIL'}  ${id}  ${name}${note ? '  — ' + note : ''}`);
}

async function main() {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
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

  // ---------- 总览页 ----------
  // TC-E03 统计卡与中心端状态
  await page.waitForSelector('text=中心端状态');
  const metricsOk = await page.locator('.metric').count() >= 3;
  const focusOk = await page.locator('text=需要你关注').count() >= 1;
  record('E03', '总览：统计卡与「需要你关注」卡渲染', metricsOk && focusOk);

  // TC-E04 状态分布下钻 chips（2026-10-22 总览下钻重构：七态 chip 直达任务页对应筛选，无「全部」chip）
  const chips = await page.locator('.chip-link').count();
  record('E04', '总览：状态分布下钻 chips（7 枚七态直达）', chips === 7, `实际 ${chips}`);

  // ---------- 节点接入页 ----------
  await page.click('button:has-text("节点接入")');
  await page.waitForSelector('text=已登记节点');
  const bodyText = await page.textContent('body');
  const hasMac = bodyText.includes('mac-mini'), hasBaidu = bodyText.includes('baidu-node');
  const onlineCount = (bodyText.match(/在线/g) || []).length;
  record('E05', '节点接入：双节点在列且在线', hasMac && hasBaidu && onlineCount >= 2);

  // TC-E06 生成接入配置弹层
  await page.click('button:has-text("生成接入配置")');
  await page.waitForSelector('#enroll-snippet');
  const snippet = await page.textContent('#enroll-snippet');
  record('E06', '节点接入：接入配置片段生成（含 node_id 与令牌占位）', snippet.includes('node_id') && snippet.includes('hub.yaml'));
  await page.click('[data-act="close"]');
  await page.waitForSelector('#enroll-snippet', { state: 'detached' });

  // ---------- 任务监管页 ----------
  await page.click('aside button:has-text("任务监管")');
  await page.waitForSelector('th:has-text("提交时间")', { timeout: 15000 }); // 任务表头唯一，option 不会误匹配
  const taskRows = await page.locator('tr[data-task]').count();
  record('E07', '任务监管：历史任务列表渲染', taskRows >= 1, `行数 ${taskRows}`);

  // TC-E08 任务详情抽屉（最终答复/产物/时间线区块）
  if (taskRows >= 1) {
    await page.locator('[data-act="detail"]').first().click();
    await page.waitForSelector('.drawer');
    const drawerText = await page.textContent('.drawer');
    const blocksOk = drawerText.includes('任务输入') && drawerText.includes('最终答复') && drawerText.includes('产物') && drawerText.includes('时间线');
    record('E08', '任务详情抽屉：四区块渲染（输入/答复/产物/时间线）', blocksOk);
    await page.keyboard.press('Escape');
  }

  // ---------- 运维控制页：真实派单 ----------
  await page.click('button:has-text("任务派发")');
  await page.waitForSelector('#ctrl-agent');
  const agentOptions = await page.locator('#ctrl-agent option').count();
  record('E09', '运维控制：能力目录下拉非空', agentOptions >= 1, `选项 ${agentOptions}`);

  // 选能力（节点随之确定）
  await page.selectOption('#ctrl-agent', { index: 0 });
  const nodeHint = await page.textContent('#ctrl-node-hint');
  record('E09b', '选择能力后显示执行节点提示（不再单独选节点）', !!nodeHint, `节点 ${nodeHint}`);

  // TC-E10 总理模式（PRIME-DESIGN 8.1）：勾选联动（agent→zcode-dev、广播隐藏、
  // placeholder 切换、节点提示同步）→ 取消全恢复 → 再勾选提交复合目标 → 批次报告
  const PRIME_GOAL = 'E2E 总理模式链路验证：查询网内能力目录并回报在线节点数量，无需派发子任务';
  const primeOpt = page.locator('#ctrl-agent option[value*="zcode-dev"]');
  let e10ok = false, e10note = '';
  if (await primeOpt.count()) {
    const selBefore = await page.inputValue('#ctrl-agent');
    await page.check('#ctrl-prime');
    const selOn = await page.inputValue('#ctrl-agent');
    const hintOn = await page.textContent('#ctrl-node-hint');
    const bcHidden = await page.locator('#ctrl-broadcast-line').isHidden();
    const phOn = await page.getAttribute('#ctrl-stdin', 'placeholder');
    await page.uncheck('#ctrl-prime');
    const selOff = await page.inputValue('#ctrl-agent');
    const bcShown = await page.locator('#ctrl-broadcast-line').isVisible();
    const phOff = await page.getAttribute('#ctrl-stdin', 'placeholder');
    const toggleOk = selOn.includes('zcode-dev') && bcHidden && phOn.startsWith('描述复合目标')
      && hintOn === selOn.split('/')[0]
      && selOff === selBefore && bcShown && phOff.startsWith('用自然语言描述');
    if (!toggleOk) e10note = `联动异常: on=${selOn} hint=${hintOn} ph=${String(phOn).slice(0, 12)}… off=${selOff}`;
    await page.check('#ctrl-prime');
    await page.fill('#ctrl-stdin', PRIME_GOAL);
    await page.click('button:has-text("运行巡检")');
    await page.waitForSelector('text=批次报告', { timeout: 30000 });
    await page.waitForSelector('[data-act="detail"]', { timeout: 20000 }); // 等待任务行出现（提交后自动刷新快照）
    const reportOk = (await page.textContent('body')).includes('已提交');
    e10ok = toggleOk && reportOk;
    record('E10', '总理模式：勾选联动+取消恢复，提交复合目标进入批次报告', e10ok, e10note);
  } else {
    // 网内无 zcode-dev 能力：回退普通巡检提交保持链路覆盖；E10b 相应标 PENDING
    await page.fill('#ctrl-stdin', 'E2E 自动化巡检验证：请回报节点名');
    await page.click('button:has-text("运行巡检")');
    await page.waitForSelector('text=批次报告', { timeout: 30000 });
    await page.waitForSelector('[data-act="detail"]', { timeout: 20000 });
    e10ok = (await page.textContent('body')).includes('已提交');
    record('E10', '总理模式提交（回退普通巡检：网内无 zcode-dev 能力）', e10ok, 'PENDING');
  }

  // TC-E10b 总理模式任务入参：经任务详情 API 断言 args[0] 以 PRIME 提示词开头、
  // 含「# 本次目标」段与用户输入的目标文本
  const primeTaskId = await page.locator('[data-act="detail"]').first().getAttribute('data-id');
  const primeTask = await page.evaluate(async id => {
    const r = await fetch('/api/v1/tasks/' + encodeURIComponent(id), { credentials: 'same-origin' });
    return r.status === 200 ? r.json() : null;
  }, primeTaskId);
  const a0 = primeTask?.input?.args?.[0] || '';
  const primeArgsOk = a0.startsWith('# SwarmMind 总理智能体（Prime）')
    && a0.includes('# 本次目标') && a0.includes(PRIME_GOAL);
  record('E10b', '总理模式任务 args[0] 含 PRIME 提示词特征与目标文本（任务详情 API）', primeArgsOk,
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
  await page.click('aside button:has-text("任务监管")');
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
    record('E19', '失败任务详情：stderr 前置展示', true, '当前无带 stderr 的失败任务（跳过）');
  }

  // ---------- 策略容量页 ----------
  await page.click('aside button:has-text("策略容量")');
  await page.waitForSelector('text=信箱条数', { timeout: 15000 }); // 策略页独有首行
  const policyRows = await page.locator('table tbody tr').count();
  record('E12', '策略容量：额度表渲染', policyRows >= 8, `行数 ${policyRows}`);

  // ---------- 设置页：账户信息 + 令牌 ----------
  await page.click('button:has-text("设置")');
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

  await browser.close();

  // ---------- 汇总 ----------
  const pass = results.filter(r => r.ok).length;
  console.log(`\n========== E2E 汇总：${pass}/${results.length} 通过 ==========`);
  if (pass !== results.length) {
    console.log('FAIL 明细：');
    results.filter(r => !r.ok).forEach(r => console.log(`  ${r.id} ${r.name} ${r.note}`));
    process.exit(1);
  }
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
