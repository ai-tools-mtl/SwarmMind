package localapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/transport"
)

// TestWebDrawerSharedBlocks 抽屉组装层双端一致性锁（t20261025-drawer-sync，SPEC 六.五
// 「双端 Web 展示一致性契约」第一条）：md 渲染器之外的抽屉共用件——状态词典、时间
// 工具、事件流、分区更新机制——shared block 双端逐字节同文。不锁整个 drawerHtml/
// taskDrawerZones 字节（两端分区内容有合理差异白名单，SPEC 六.五 第六条），只锁
// 「单端独改即漂移」的共用件与关键行。
func TestWebDrawerSharedBlocks(t *testing.T) {
	bus := transport.NewBus()
	svc := newService(t, bus.Endpoint("nodeA"))
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc,
		Agents:     newState(t, t.TempDir(), nil),
		LocalToken: "tok-local",
	}).Handler())
	t.Cleanup(srv.Close)

	fetch := func(path string) string {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s 应 200, got %d", path, resp.StatusCode)
		}
		return string(b)
	}
	nodeJS := fetch("/app.js")
	hubJS, err := os.ReadFile("../../hub/web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	hubJSStr := string(hubJS)

	// ---- 五个抽屉共用 shared block 双端逐字节一致 ----
	// （分区更新机制的逐字节锁同为裸比对：机制实现不锁关键行——阶段 D 终审必须修 6，
	// 实现字符串断言会把某一版实现锁成必需；行为面由 headless harness 覆盖。）
	dict := sharedBlock(t, nodeJS, hubJSStr, "抽屉展示词典 shared block")
	timeBlk := sharedBlock(t, nodeJS, hubJSStr, "抽屉时间工具 shared block")
	evBlk := sharedBlock(t, nodeJS, hubJSStr, "抽屉事件与错误 shared block")
	logBlk := sharedBlock(t, nodeJS, hubJSStr, "抽屉答复与日志 shared block")
	sharedBlock(t, nodeJS, hubJSStr, "抽屉分区更新 shared block")

	// 词典：cancelled 系双端必备（潜伏漂移项：node 路由打通即暴露）+ 终态集含 cancelled
	for _, want := range []string{
		`cancelled: '已取消',`,
		`user_cancelled: '管理员手动取消了此任务，进程已整组终止',`,
		`const TERMINAL = new Set(['completed', 'failed', 'needs_review', 'cancelled']);`,
		`const REVIEW_ACTIONS = { ack_review: '确认结果有效', mark_failed: '标记失败' };`,
	} {
		if !strings.Contains(dict, want) {
			t.Errorf("抽屉展示词典 shared block 应含 %q", want)
		}
	}
	// 时间工具：零值守卫（D16）+ 解析转 UTC（D17 契约禁切片硬贴后缀）
	for _, want := range []string{
		`!String(s).startsWith('0001-01-01')`,
		`!Number.isNaN(new Date(s).getTime())`,
		"function fmtUTC(s) {",
		"d.getUTCFullYear()",
		"padStart(2, '0')",
		"} UTC`;",
	} {
		if !strings.Contains(timeBlk, want) {
			t.Errorf("抽屉时间工具 shared block 应含 %q", want)
		}
	}
	if strings.Contains(timeBlk, "slice(0, 19)") || strings.Contains(timeBlk, "slice(0, 16)") {
		t.Error("抽屉时间工具 shared block 不得用 ISO 串切片硬贴 UTC 后缀（D17 契约）")
	}
	// 事件与错误：零值不生成事件 + ol.timeline + needs_review 唯一热点（D15/D16/D20）
	// + 组合错误码首段查词典（D5 口径统一）；阶段 D 终审补全：受理/终态/核对三处
	// 一律完整有效时间守卫（无证据时刻连事件骨架一起不生成）
	for _, want := range []string{
		"function drawerTimelineHtml(t) {",
		"if (realTime(t.created_at)) ev.push(['任务已受理'",
		"realTime(t.result?.started_at)",
		"if (fin) ev.push([",
		"t.review && realTime(t.review.at)",
		"t.status === 'needs_review'",
		"`<ol class=\"timeline\">${",
		` class="hl"`,
		"function taskErrorNotice(r, needs) {",
		"String(r.error).split(';')[0]",
		`|| '执行中断'`,
		"typeof r.exit_code === 'number'",
	} {
		if !strings.Contains(evBlk, want) {
			t.Errorf("抽屉事件与错误 shared block 应含 %q", want)
		}
	}
	// 答复与日志：mdAnswer 白底主角+（空）兜底；诊断日志恒两段+空段（空）+单 pre 双段原文
	// （D8/D10/D11/D12 统一口径全部锁在本块内，两端同文即同行为）；drawerAnswerHtml
	// D9 状态优先——终态判定先行，cancelled+result=null（后端合法态）不得落「尚未到终态」；
	// promptZoneHtml 任务指令分区（2026-10-26 notify-unify 改动四提升双端同文；
	// 2026-10-26 prompt-args-md 重构：args 形态判据分流——任一参数含换行或总长 >300
	// 字符⇒提示词文本形态 md 渲染+原文切换，否则短参数逐参 code 胶囊；stdin+args 并存
	// 双小节标题共享一个原文切换；单源不显示小节标题；皆空省略。判据行为边界由
	// TestWebPromptZoneShapeBehavior 经 node 实测，此处只锁双端同文的文案/挂点行）
	for _, want := range []string{
		"function mdAnswer(text) {",
		"function drawerAnswerHtml(t, res, needs) {",
		"TERMINAL.has(t.status)",
		"function drawerLogInner(res, raw) {",
		"'当前为原始输出。'",
		"'默认按 Markdown 渲染；空格对齐的进程输出请切原文。'",
		"<pre>stdout:\\n${esc(res.stdout_tail || '（空）')}\\n\\nstderr:\\n${esc(res.stderr_tail || '（空）')}</pre>",
		"sec('stdout', res.stdout_tail) + sec('stderr', res.stderr_tail)",
		"'<p class=\"muted\">（空）</p>'",
		"function promptZoneHtml(t) {",
		"t.input?.stdin",
		"t.input?.args",
		"'prompt-raw'",
		"本任务以命令行参数下达（逐参数展示，保留边界）：",
		"任务指令",
		"任务说明（stdin）",
		"命令行参数（args · ${args.length} 段）",
		"'当前为原始输入。'",
	} {
		if !strings.Contains(logBlk, want) {
			t.Errorf("抽屉答复与日志 shared block 应含 %q", want)
		}
	}

	// ---- 防回潮反向禁令（D22，旧形态变异验证可拦）：刷新路径禁止整抽屉重建 ----
	// 分区接线与迟到防护的「正向」行为面（节点身份/草稿/busy/迟到应答/乱序）不锁
	// 源码字符串——阶段 D 终审必须修 6：源码字符串断言会把某一版实现锁成必需，
	// 行为面由 headless harness 覆盖（docs/prototypes/shoot-drawer-sync-c3.mjs：
	// 节点身份/在途 busy/迟到应答/列表乱序/焦点滚动/无变化零写入）。
	for _, ban := range []string{
		"$('#drawer').innerHTML = taskDrawerHtml(",
		"function taskDrawerHtml(",
		"$('#layer').addEventListener('click', e =>",
		"dr.innerHTML = drawerHtml(",
		"function drawerHtml(",
	} {
		if strings.Contains(nodeJS, ban) {
			t.Errorf("node app.js 不应再含 %q（D22：刷新禁止整层重建；遮罩关闭须具名去重）", ban)
		}
		if strings.Contains(hubJSStr, ban) {
			t.Errorf("hub app.js 不应再含 %q（D22：轮询同步禁止整抽屉重建）", ban)
		}
	}
	// 终态集单一来源：端侧终态判定一律经 shared block 词典 TERMINAL（含 cancelled），
	// 不得私建终态集造成双端分叉
	if !strings.Contains(hubJSStr, "function terminal(st) { return TERMINAL.has(st); }") {
		t.Error("hub app.js 终态判定应经抽屉展示词典 TERMINAL（单一来源）")
	}
	if !strings.Contains(nodeJS, "TERMINAL.has(t.status)") {
		t.Error("node app.js 终态判定应经抽屉展示词典 TERMINAL（单一来源）")
	}
	// 核对记录时间口径（阶段 D 必须修 5）：抽屉面一律 fmtUTC（解析转 UTC），禁回潮
	// 旧 fmtFull ISO 串切片
	if strings.Contains(hubJSStr, "fmtFull(t.review.at)") {
		t.Error("hub app.js 核对记录时间应经 fmtUTC（D17 契约），不得用 fmtFull 切片")
	}
}

// TestWebNotifyFormSharedBlock 通知渠道编辑面双端一致性锁（t20261026-notify-polish，
// SPEC 六.五「双端 Web 展示一致性契约」第一条 + 2026-10-26 三层范围裁决）：通知表单
// 骨架、类型/消息格式词典、modalHead 与配套局部重绘收进「通知表单与词典 shared
// block」双端逐字节同文；操作逻辑层（居中 modal 形态/字段恒显恒位/卡片位置）不进
// shared block 锁，锁关键行；node 无 inbox 的如实口径以 NOTIFY_TYPE_KEYS 参数体现。
func TestWebNotifyFormSharedBlock(t *testing.T) {
	bus := transport.NewBus()
	svc := newService(t, bus.Endpoint("nodeA"))
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc,
		Agents:     newState(t, t.TempDir(), nil),
		LocalToken: "tok-local",
	}).Handler())
	t.Cleanup(srv.Close)

	fetch := func(path string) string {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s 应 200, got %d", path, resp.StatusCode)
		}
		return string(b)
	}
	nodeJS, nodeCSS := fetch("/app.js"), fetch("/style.css")
	hubJS, err := os.ReadFile("../../hub/web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	hubCSS, err := os.ReadFile("../../hub/web/style.css")
	if err != nil {
		t.Fatal(err)
	}
	hubJSStr, hubCSSStr := string(hubJS), string(hubCSS)

	// ---- shared block 双端逐字节一致 + 关键行（词典/恒定骨架/参数化面） ----
	blk := sharedBlock(t, nodeJS, hubJSStr, "通知表单与词典 shared block")
	for _, want := range []string{
		"const NOTIFY_TYPES = {",
		"const NOTIFY_FORMATS = { generic: '通用 JSON', feishu: '飞书机器人' };",
		"const NOTIFY_EVENT_OPTS = [",
		"function modalHead(title) {",
		// modalHead × 按钮内联字面量（双端 button() 签名不同，带 aria-label 无法同文经助手）
		`<button type="button" data-act="close" class="close" aria-label="关闭弹层">×</button>`,
		`<h2 id="layer-title">`,
		"function notifyTypeOptionsHtml(type, typeKeys) {",
		"function notifyHintHtml(type, format) {",
		"function notifyFormHtml(ch, typeKeys) {",
		// 恒定骨架：消息格式/推送地址/自定义请求头三件恒显恒位，切格式仅 label 与 disabled 变化
		`<label>消息格式<select id="nf-format">`,
		`id="nf-url"`,
		`id="nf-headers" rows="2"`,
		`' disabled'`,
		"飞书机器人协议不支持自定义请求头",
		`id="nf-hint"`,
		"function nfRepaint(type, format, sec) {",
		"function nfSecFromDom() {",
		"function collectNotifyForm() {",
	} {
		if !strings.Contains(blk, want) {
			t.Errorf("通知表单与词典 shared block 应含 %q", want)
		}
	}

	// ---- node 端形态：类型键参数化（无 inbox 如实口径）+ 居中 modal + 直清家族 ----
	for _, want := range []string{
		"const NOTIFY_TYPE_KEYS = ['email', 'webhook', 'sms'];",
		"notifyFormHtml(ch, NOTIFY_TYPE_KEYS)",
		// 居中 modal（与 hub openLayerPlain 同形态），无障碍对齐：role/aria-modal/aria-labelledby
		`<section class="modal" id="notify-modal" role="dialog" aria-modal="true" aria-labelledby="layer-title" tabindex="-1">`,
		"$('#layer [role=dialog]')?.focus();",
		// shared modalHead ×/取消同词关闭动作（守护防误清其他层）
		"case 'close':",
		"if ($('#notify-modal')) closeNotifyForm();",
		// 切换只重绘两槽（D2 局部刷新），格式切换经 nfSecFromDom 回填不丢
		"nfRepaint(e.target.value, 'generic', {});",
		"nfRepaint($('#nf-type')?.value || 'webhook', e.target.value, nfSecFromDom());",
		// 改动四：通知卡上移页头后第一位（head 之后 notifyCard 紧随，能力配置沉底）
		"return head + notifyCard() + empty + S.agents.map(a => svcCard(a)).join('') +",
	} {
		if !strings.Contains(nodeJS, want) {
			t.Errorf("node app.js 应含 %q（通知编辑 modal 化/类型键参数化/通知卡上移）", want)
		}
	}
	for _, ban := range []string{
		`id="notify-drawer"`,           // 旧右侧抽屉形态退役（改动三）
		"case 'nf-close':",             // 旧关闭动作词退役（与 hub 同词 close）
		"cfgSection() + notifyCard()", // 旧卡片沉底次序退役（改动四）
	} {
		if strings.Contains(nodeJS, ban) {
			t.Errorf("node app.js 不应再含 %q（通知编辑 modal 化/卡片上移后旧形态退役）", ban)
		}
	}

	// ---- hub 端形态：全集类型键（不传 typeKeys）+ 切换走 shared nfRepaint ----
	for _, want := range []string{
		"openLayerPlain(notifyFormHtml(null))",
		"nfRepaint($('#nf-type')?.value || 'webhook', e.target.value, nfSecFromDom());",
	} {
		if !strings.Contains(hubJSStr, want) {
			t.Errorf("hub app.js 应含 %q（通知表单接入 shared 块函数）", want)
		}
	}

	// ---- 样式：modal 形态同文 / 恒定骨架 grid / 说明槽去边距 / 铃铛几何根因 ----
	for _, css := range []struct{ side, src string }{{"node", nodeCSS}, {"hub", hubCSSStr}} {
		for _, want := range []string{
			// .overlay 遮罩淡入 + .modal 尺寸/圆角/阴影/入场动效（node 自 hub 同文同步）
			".overlay{position:fixed;inset:0;background:var(--scrim);z-index:50;display:flex;align-items:center;justify-content:center;padding:24px;opacity:0;transition:opacity var(--dur-scrim) ease}",
			".overlay.open .modal{opacity:1;transform:none}",
			".modal{width:min(920px,94vw);max-height:86vh;overflow:auto;background:var(--surface);padding:28px;border-radius:var(--r-sheet);box-shadow:var(--sh-overlay);",
			// 事件字段集与说明槽（改动二双端同补）
			".nf-events{border:1px solid var(--hairline);border-radius:var(--r-thumb);padding:12px 14px;margin:14px 0 4px;display:grid;gap:8px}",
			"#nf-hint{margin:0}",
		} {
			if !strings.Contains(css.src, want) {
				t.Errorf("%s style.css 应含 %q（通知表单/modal 双端同文样式）", css.side, want)
			}
		}
	}
	// hub 专属：铃铛 padding:0 重置（全局 button padding 顶偏图标的根因修复，改动一）
	if !strings.Contains(hubCSSStr, ".bell-btn{position:relative;padding:0;") {
		t.Error("hub style.css .bell-btn 应显式 padding:0（铃铛图标居中，DoD 几何断言 <1px）")
	}
	// node 专属：通知表单 grid 骨架在 node 是全局 .fields 覆盖（既有），hub 以 #notify-form 同值收齐
	if !strings.Contains(nodeCSS, ".fields{display:grid;gap:8px}") {
		t.Error("node style.css 应含 .fields grid 覆盖（通知表单纵向堆叠基准）")
	}
	if !strings.Contains(hubCSSStr, "#notify-form.fields{display:grid;gap:8px}") {
		t.Error("hub style.css 应含 #notify-form.fields grid 覆盖（与 node 表单骨架同构）")
	}
}

// TestWebDrawerComponentStyles 抽屉共用组件样式双端一致锁（SPEC 六.五 第一/二条）：
// .timeline 事件流（左轨圆点+needs_review 热点）与 .artifact 产物行分隔线为双端同文
// 规则——node 补齐后抽壳比对，任一端私改即红。
func TestWebDrawerComponentStyles(t *testing.T) {
	bus := transport.NewBus()
	svc := newService(t, bus.Endpoint("nodeA"))
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc,
		Agents:     newState(t, t.TempDir(), nil),
		LocalToken: "tok-local",
	}).Handler())
	t.Cleanup(srv.Close)

	fetch := func(path string) string {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s 应 200, got %d", path, resp.StatusCode)
		}
		return string(b)
	}
	nodeCSS := fetch("/style.css")
	hubCSS, err := os.ReadFile("../../hub/web/style.css")
	if err != nil {
		t.Fatal(err)
	}
	hubCSSStr := string(hubCSS)

	rules := []string{
		".timeline{list-style:none;padding:0;margin:14px 0}",
		".timeline li{padding:0 0 18px 22px;border-left:1px solid var(--hairline);position:relative}",
		".timeline li:before{content:'';position:absolute;left:-5px;top:6px;width:8px;height:8px;border-radius:50%;background:var(--faint)}",
		".timeline li.hl:before{background:var(--heat)}",
		".artifact{padding:12px 0;border-bottom:1px solid var(--hairline)}",
	}
	for _, r := range rules {
		if !strings.Contains(nodeCSS, r) {
			t.Errorf("node style.css 应含 %q（抽屉组件样式双端同文）", r)
		}
		if !strings.Contains(hubCSSStr, r) {
			t.Errorf("hub style.css 应含 %q（抽屉组件样式双端同文）", r)
		}
	}
}

// TestWebDrawerUnifiedCopyStructure 抽屉结构/文案/材质统一锁（t20261025-drawer-sync
// 阶段 C3，SPEC 六.五 第二/四/五条）：不锁整个分区构建函数字节（两端有白名单差异），
// 只锁跨端必须同文同构的落点与不得回潮的旧形态。
func TestWebDrawerUnifiedCopyStructure(t *testing.T) {
	bus := transport.NewBus()
	svc := newService(t, bus.Endpoint("nodeA"))
	srv := httptest.NewServer(New(Config{
		NodeID: "nodeA", Service: svc,
		Agents:     newState(t, t.TempDir(), nil),
		LocalToken: "tok-local",
	}).Handler())
	t.Cleanup(srv.Close)

	fetch := func(path string) string {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s 应 200, got %d", path, resp.StatusCode)
		}
		return string(b)
	}
	nodeJS := fetch("/app.js")
	hubJS, err := os.ReadFile("../../hub/web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	hubJSStr := string(hubJS)

	// ---- 跨端同文文案（D18 主文案 / 状态介绍 / 诊断日志标题 / 保留期 / 归档说明 /
	//      标准词「查看全部任务」/ 空答复与产物空态兜底） ----
	for _, want := range []string{
		"这个任务结果拿不准，等你确认：执行到一半机器重启了，请核对后登记结论，不要重新提交同一任务。",
		"任务已完成：可阅读最终答复并下载产物文件。",
		"本次执行未完成：先看下方说明，再决定是否重新派任务。",
		"任务已被取消：执行进程已整组终止（或尚未执行），不会自动重跑。",
		"任务仍在进行，抽屉每 5 秒自动刷新。",
		"诊断日志（stdout / stderr 尾部）",
		"最多保留各 64KiB。",
		"任务已归档前保留 24 小时。",
		"查看全部任务",
		"结果还没确认——这个任务结果拿不准，等你核对。",
		"该任务没有单独的文本答复，输出内容见下方诊断日志与产物。",
		"该任务没有文本答复（例如等待类任务）；如有文件输出，见下方产物列表。",
		"暂无已登记产物。中断任务不推断存在可交付产物。",
		"产物文件",
		"任务详情 · ",
	} {
		if !strings.Contains(nodeJS, want) {
			t.Errorf("node app.js 应含跨端同文文案 %q", want)
		}
		if !strings.Contains(hubJSStr, want) {
			t.Errorf("hub app.js 应含跨端同文文案 %q", want)
		}
	}

	// ---- node 端能力如实（D19）与修正后形态 ----
	for _, want := range []string{
		"本控制台暂不支持登记核对结论，请人工核查实际结果，并到 hub 管理台登记。",
		"核对结论请到 hub 管理台登记。",
	} {
		if !strings.Contains(nodeJS, want) {
			t.Errorf("node app.js 应含 %q（D19 核对能力如实指路）", want)
		}
	}
	for _, ban := range []string{
		"核对结论由本机主人在本控制台确认）", // 旧 reviewNote 模板串（D19：承诺不存在的能力；注释引用不带右括号不误伤）
		"终态自带下一步",                // 归档保留说明统一后旧下一步行退役（D21）
		"<ul class=\"timeline\"",      // 旧两行拼接时间线（D15：对齐 hub ol.timeline 事件流）
		"任务号 <code>${esc(t.id)}</code> · 追踪", // 旧抽屉元信息行（D2 头部同构：任务号入标题行）
	} {
		if strings.Contains(nodeJS, ban) {
			t.Errorf("node app.js 不应再含 %q（旧形态已按阶段 B 裁决统一）", ban)
		}
	}

	// ---- hub 端旧形态退役 ----
	for _, ban := range []string{
		">运行日志</summary>",   // D10：更名诊断日志（stdout / stderr 尾部）
		"返回任务列表",           // D21：标准词「查看全部任务」
		"内部标记：",            // D5：错误条统一为 taskErrorNotice（人话在前·原码同行）
		"<h3 class=\"spaced\">任务输入</h3>", // D7：任务输入收 details 折叠沉底
	} {
		if strings.Contains(hubJSStr, ban) {
			t.Errorf("hub app.js 不应再含 %q（旧形态已按阶段 B 裁决统一）", ban)
		}
	}
	for _, want := range []string{
		`<details class="spaced"><summary>任务输入（原样 JSON）</summary><pre>`, // D7 折叠+沉底灰块
		"taskErrorNotice(r, needs)", // D5 hub 接入统一错误条
		`${badge(t.status)}</h2></div>`, // D2 头部同构：badge 入 agent@node 行
	} {
		if !strings.Contains(hubJSStr, want) {
			t.Errorf("hub app.js 应含 %q", want)
		}
	}

	// ---- node 端 D4 介绍条 / D22 壳层无障碍 / 归档区接线 ----
	for _, want := range []string{
		"taskErrorNotice(res, t.status === 'needs_review')", // D5 node 接入统一错误条
		`role="dialog" aria-modal="true" aria-labelledby="drawer-title"`, // D1 对齐 hub 壳层基线
		"任务已归档前保留 24 小时。${button('查看全部任务 →', 'close-drawer', 'link')}",
		"button('回表单重新派单', 'reopen-form', 'link')", // node 独有白名单动作保留
	} {
		if !strings.Contains(nodeJS, want) {
			t.Errorf("node app.js 应含 %q", want)
		}
	}
}
