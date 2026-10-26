// SwarmMind 控制台 E2E 测试（Playwright，真实浏览器交互）
// 运行：node tests/e2e-console.mjs
//   hub 模式（默认）：E2E_BASE 指向 hub 管理面，账户密码登录，跑 E01-E20 系列。
//   node 模式（终审必修4）：E2E_BASE 指向 node local API 且设置 local_token 环境变量
//     （本机管理令牌，令牌门登录）→ 跑 N01-N03/E-NAV3/E-N1~N3 系列：来源筛选行恒在、
//     轮询保持筛选、翻页叠加筛选、登出后 S.tf 重置。
// 覆盖：首次初始化/密码登录/总览/节点/任务/运维控制（含总理模式派发）/策略/设置（账户+令牌）/退出
// 语义：needs_review 提示、抽屉详情、批次报告、令牌明文一次性展示
// 汇总：PASS / SKIP / FAIL 三态分开计（终审必修4：跳过不再冒充 PASS）；仅 FAIL 判败退出。

import { chromium } from 'playwright';
import fs from 'node:fs';

const BASE = process.env.E2E_BASE || 'http://1.13.158.180:8444';
const USER = process.env.E2E_USER || 'admin';
const PASS = process.env.E2E_PASS || 'Swarm@2026';
const NODE_TOKEN = process.env.local_token || '';
const NODE_MODE = !!NODE_TOKEN;

const results = [];
const p_sleep = ms => new Promise(r => setTimeout(r, ms));
function record(id, name, ok, note = '', skip = false) {
  results.push({ id, name, ok: skip ? false : ok, skip, note });
  console.log(`${skip ? '⏭️ SKIP' : ok ? '✅ PASS' : '❌ FAIL'}  ${id}  ${name}${note ? '  — ' + note : ''}`);
}
function summarize() {
  const pass = results.filter(r => r.ok && !r.skip).length;
  const skip = results.filter(r => r.skip).length;
  const fail = results.filter(r => !r.ok && !r.skip).length;
  console.log(`\n========== E2E 汇总：PASS ${pass} / SKIP ${skip} / FAIL ${fail}（共 ${results.length} 条）==========`);
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

async function main() {
  const browser = await chromium.launch();
  if (NODE_MODE) {
    await nodeFlow(browser);
    await browser.close();
    summarize();
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
  const HUB_NAV = ['总览', '机器', '任务', '派发', '工作单', '群聊', '策略容量', '设置'];
  const hubNav = (await page.locator('aside nav button').allTextContents())
    .map(s => s.replace(/^0\d/, '').replace(/\s*\(\d+\)$/, '').trim());
  record('E-NAV', 'hub 导航词表：8 项名称与顺序', JSON.stringify(hubNav) === JSON.stringify(HUB_NAV), hubNav.join('/'));
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
