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

// TestWebMdRender 任务结果 Markdown 渲染断言（t20261023-969e，三层走查之功能层）。
// 锁住：
//   - 双端一致性锁（比标记行更硬）：node/hub 两端 app.js 的「md 渲染器 shared
//     block」与两端 style.css 的「md 渲染样式 shared block」逐字节一致——
//     escape-first 渲染器与配套样式必须两端同改同验，抽块比对一锁全收；
//   - 关键解析行（在 shared block 内，一致性成立即两端同有）：escape-first 铁律、
//     围栏代码（语言徽标、内部不解析行内）、h1-h6、引用（escape-first 后行首
//     &gt;）、分隔线、两层嵌套列表、表格列数不匹配按段落降级、段内换行 <br>、
//     `code` 占位保护、粗体/斜体紧贴非空白才配对、链接 url 白名单（http(s)://
//     与 / 相对路径，其余降级纯文本）+ target=_blank + noopener noreferrer；
//   - 接入行：node 抽屉最终答复 mdAnswer + 诊断日志 #log-wrap + log-raw 切换
//     （旧 pre 纯文本形态移除）；hub 抽屉最终答复 mdAnswer + 失败原因
//     #drawer-fail/fail-raw + 运行日志 #drawer-runlog/log-raw；
//   - 样式载体：.md-body 分级字号/md 表格收紧、.md-lang 语言徽标、.md-quote、
//     .md-log-head/.md-log-label 日志切换行与流标签。
//
// 渲染行为（恶意输入字面化、原文切换、表格/嵌套列表效果、空兜底）由 headless
// 三层走查覆盖（docs/TASK-md-render.md）。hub 端 embed 变量私有（internal/hub/web
// 不导出），此处直读其源文件比对（go test CWD=包目录，路径稳定；go:embed 保证
// 源文件即二进制发布内容；node 端仍经 embed HTTP 服务取证）。
func TestWebMdRender(t *testing.T) {
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

	// ---- 双端一致性锁：渲染器/样式 shared block 抽块逐字节比对 ----
	jsBlock := sharedBlock(t, nodeJS, hubJSStr, "md 渲染器 shared block")
	cssBlock := sharedBlock(t, nodeCSS, hubCSSStr, "md 渲染样式 shared block")

	// ---- 关键解析行（抽自 shared block；块一致 + 块内含关键行 = 两端都在） ----
	for _, want := range []string{
		`function mdToHtml(text) {`,
		// escape-first 铁律：全文先转义（& < > " '）再解析
		"const escaped = src.replace(/[&<>\"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','\"':'&quot;',\"'\":'&#39;'}[c]));",
		// 围栏代码块（可带语言标注；未闭合到文末按已闭合处理）
		"const fence = line.match(/^```([\\w#+.-]*)\\s*$/);",
		// h1-h6 标题
		"const h = line.match(/^(#{1,6})\\s+(.+?)\\s*#*\\s*$/);",
		// 引用（escape-first 后行首 > 为 &gt;）
		"if (/^\\s*&gt;/.test(line)) {",
		// 分隔线单独成行
		"if (/^\\s*(-{3,}|\\*{3,})\\s*$/.test(line)) { out.push('<hr>'); i++; continue; }",
		// 两层缩进嵌套（行首 ≥2 空格为二级）
		"items.push({ d: m[1].length >= 2 ? 1 : 0, mk: m[2], tx: m[3].trim() });",
		// 表格：头/分隔列数不匹配按段落降级的口径
		"if (head.length >= 2 && head.length === sep.length) {",
		// 段落：连续行合并，段内换行 <br>
		"out.push(`<p>${buf.map(mdInline).join('<br>')}</p>`);",
		// 行内 `code` 占位保护（内部不参与其他行内规则）
		"s = s.replace(/`([^`\\n]+)`/g, (_, c) => `\\x00${codes.push(c) - 1}\\x00`);",
		// 粗体/斜体：紧贴非空白才配对，不配对字面保留
		"s = s.replace(/\\*\\*([^*\\s](?:[^*]*[^*\\s])?)\\*\\*/g, '<strong>$1</strong>');",
		"s = s.replace(/\\*([^*\\s](?:[^*]*[^*\\s])?)\\*/g, '<em>$1</em>');",
		// 链接 url 白名单 + 外链安全属性（javascript: 等不匹配白名单 → 原样降级纯文本）
		"/^(https?:\\/\\/|\\/(?!\\/|\\\\))/i.test(url)",
		"? `<a href=\"${url}\" target=\"_blank\" rel=\"noopener noreferrer\">${label}</a>`",
		// 审查 R1：引用递归深度封顶 32 + mdToHtml try/catch 降级原文（抽屉永不崩）
		"function mdBlocks(lines, depth) {",
		"depth >= 32 ? esc(buf.join('\\n')) : mdBlocks(buf, depth + 1)",
		"} catch (e) { return `<pre>${esc(text)}</pre>`; }",
		// 审查 R3：NUL 伪造占位越界原样保留
		"return s.replace(/\\x00(\\d+)\\x00/g, (m, k) => { const c = codes[+k]; return c === undefined ? m : `<code>${c}</code>`; });",
	} {
		if !strings.Contains(jsBlock, want) {
			t.Errorf("md 渲染器 shared block 应含关键解析行 %q", want)
		}
	}
	for _, want := range []string{
		`.md-body h1{font-size:17px;letter-spacing:-.01em}`,
		`.md-body table{width:auto;min-width:55%}`,
		// 容器化有界表面：md 内容与抽屉文本流分层；最终答复 accent 焦点竖线；
		// 日志容器底色对齐 pre 的 .04（渲染/原文切换不跳变）；容器内代码块白底描边
		`.md-body{line-height:1.7;background:rgba(0,0,0,.025);border:1px solid var(--hairline);border-radius:var(--r-thumb);padding:14px 18px}`,
		`.md-answer{border-left:3px solid var(--accent)}`,
		`.md-body.md-log{background:rgba(0,0,0,.04)}`,
		`.md-body .md-code pre{background:var(--surface);border:1px solid var(--hairline)}`,
		// th 显式 static：hub 全局 th 吸顶（sticky;top:84px）泄进 md 表会把表头
		// 钉在 .md-table 滚动容器顶 +84px 处盖住数据行（走查实测），此行回移即回归
		`.md-body th{position:static;font-weight:600;color:var(--ink-2);border-bottom:1px solid var(--faint)}`,
		`.md-lang{position:absolute;top:7px;right:9px;`,
		`.md-quote{margin:0 0 10px;padding:8px 14px;border-left:3px solid var(--faint);color:var(--ink-2)}`,
		`.md-log-head{display:flex;align-items:center;justify-content:space-between;gap:10px;margin-bottom:8px}`,
		`.md-log-label{display:inline-block;font-size:11px;font-family:var(--mono);color:var(--ink-3);background:rgba(0,0,0,.05);border-radius:var(--r-chip);padding:3px 10px;margin:12px 0 8px}`,
	} {
		if !strings.Contains(cssBlock, want) {
			t.Errorf("md 渲染样式 shared block 应含 %q", want)
		}
	}

	// ---- node 抽屉接入行 + 旧形态移除 ----
	for _, want := range []string{
		`mdAnswer(res.final_answer)`,
		`<details><summary>诊断日志（stdout / stderr 尾部）</summary><div id="log-wrap">${logSectionHtml(res)}</div></details>`,
		`case 'log-raw': {`,
		`S.drawer.logRaw = !S.drawer.logRaw;`,
		`if (wrap) wrap.innerHTML = logSectionHtml(S.drawer.task.result);`,
		`button(raw ? '查看渲染' : '查看原文', 'log-raw', 'link')`,
	} {
		if !strings.Contains(nodeJS, want) {
			t.Errorf("node app.js 应含 %q（诊断日志/最终答复 md 渲染接入）", want)
		}
	}
	for _, ban := range []string{
		`<h3>最终答复 · FinalAnswer</h3><pre>`, // 旧纯文本最终答复
		// 旧「details 直挂 pre」默认形态（原文视图经 logSectionHtml 的 pre 属预期，不在此列）
		`<summary>诊断日志（stdout / stderr 尾部）</summary><pre>`,
	} {
		if strings.Contains(nodeJS, ban) {
			t.Errorf("node app.js 不应再含 %q（最终答复/诊断日志已改 md 渲染 + 原文切换）", ban)
		}
	}

	// ---- hub 抽屉接入行 + 旧形态移除 ----
	for _, want := range []string{
		`mdAnswer(r.final_answer)`,
		`<div id="drawer-fail">${failInner(t)}</div>`,
		`<div id="drawer-runlog">${runLogInner(t)}</div>`,
		`'log-raw': () => {`,
		`'fail-raw': () => {`,
		`S.drawer.failRaw = !S.drawer.failRaw;`,
		`if (w) w.innerHTML = runLogInner(S.drawer.task);`,
		`if (w) w.innerHTML = failInner(S.drawer.task);`,
		`button(raw ? '查看渲染' : '查看原文', 'fail-raw', 'link')`,
	} {
		if !strings.Contains(hubJSStr, want) {
			t.Errorf("hub app.js 应含 %q（失败原因/运行日志/最终答复 md 渲染接入）", want)
		}
	}
	for _, ban := range []string{
		`<p style="white-space:pre-wrap">${esc(r.final_answer)}</p>`,   // 旧纯文本最终答复
		`<pre style="white-space:pre-wrap">${esc(r.stderr_tail)}</pre>`, // 旧纯文本失败原因
	} {
		if strings.Contains(hubJSStr, ban) {
			t.Errorf("hub app.js 不应再含 %q（已改 md 渲染 + 原文切换）", ban)
		}
	}
}

// sharedBlock 抽取「===== <name> begin … end ===== */」标记之间的双端同步块
//（含标记行），并断言两份逐字节一致；返回 node 侧块供关键行断言复用。
func sharedBlock(t *testing.T, nodeSrc, hubSrc, name string) string {
	t.Helper()
	begin := "===== " + name + " begin"
	end := "===== " + name + " end ===== */"
	cut := func(src, side string) string {
		i := strings.Index(src, begin)
		if i < 0 {
			t.Fatalf("%s 应含起始标记 %q", side, begin)
		}
		j := strings.Index(src, end)
		if j < 0 || j < i {
			t.Fatalf("%s 应含结束标记 %q", side, end)
		}
		return src[i : j+len(end)]
	}
	nodeBlock, hubBlock := cut(nodeSrc, "node"), cut(hubSrc, "hub")
	if nodeBlock != hubBlock {
		t.Errorf("%s 两端不一致（双端同步维护块必须逐字节相同；两端 diff 排查）\nnode %d 字节 / hub %d 字节", name, len(nodeBlock), len(hubBlock))
	}
	return nodeBlock
}
