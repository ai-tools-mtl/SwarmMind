package localapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/transport"
)

// TestWebOverviewSpacing 总览页区块间距类断言（t20261023-f2da，三层走查之功能层）：
// .spaced 是区块间距唯一载体（原 .card+.card 相邻选择器已废除，与 hub 同口径），
// node 端顶层区块漏挂 spaced = 与相邻区块零间距挤在一起（伦哥 2026-10-23 实测抓出）。
// 对 embed 服务出的 /app.js、/style.css 做类断言（任务书认可口径），锁住：
//   - 总览三统计卡 grid、本机统筹卡两分支、最近任务卡；
//   - 同类漏挂一次修完的服务页（svc 卡、能力配置三态）与任务页（我的任务卡）；
//   - 设计系统载体 .spaced{margin-top:22px} 与 mini-stat 卡内 9px 节奏不被改坏。
//
// computed margin-top 的实机核验由 headless 走查覆盖（docs/TASK-overview-spacing.md）。
func TestWebOverviewSpacing(t *testing.T) {
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
	js, css := fetch("/app.js"), fetch("/style.css")

	for _, want := range []string{
		// 总览：顶层区块间 22px 节奏（资产级类断言——SPA 渲染 DOM 断言需 headless，
		// 由任务书三层走查的整页截图环节覆盖，本测只锁 embed 资产的类标记）
		`<div class="grid cols3 spaced">`,
		`<section class="card spaced"><h2>本机管家 · 形态</h2>`,                    // setup 分支
		`<section class="card spaced"><div class="row"><h2>本机管家 · 形态</h2>`,   // 常态 head
		`<section class="card spaced"><div class="row"><h2>最近任务（本机执行过的）</h2>`, // recentTasksCard（2026-10-26 账本全量驱动随批改词）
		// 三卡内部节奏统一：恢复卡账本计数条（t20261026-webfix：旧「诊断：账本计数」
		// details 折叠+裸大数字改为一行中文标签计数条，伦哥 2026-10-26 反馈换展示形式；
		// 卡内节奏与 .metric 同 9px，账本三态常显但人话标签小字形态不上主面重心）
		`<h3>重启恢复</h3>`,
		`<div class="ledger-line">账本 · 受理 <b>`,
		// 其他页同类漏挂（要求 4：一次修完，只动顶层区块间距）
		// 注意：下一行是 Go 反引号原始串（不插值），${on} 是逐字匹配 app.js 里的
		// JS 模板字面量源码——拼接在测试内 on 分支完成，勿当 Go 插值「修」掉。
		`card svc-card spaced ${on ? '' : 'off'}`,                                                               // svcCard
		`<section class="card spaced"><div class="row"><div><h2>能力配置</h2></div>`,                                // 未加载态
		`<section class="card spaced"><h2>能力配置</h2>`,                                                            // 不可用态
		`<section class="card spaced"><div class="row"><div><h2>能力配置 <span class="badge">node.yaml</span></h2>`, // 编辑态
		`<section class="card spaced" id="my-tasks-card"><div class="row"><h2>我的任务</h2>`,                                // myTasksCard（P1-03 槽位 id，锁测试随实现同步）
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js 应含 %q（顶层区块 spaced 节奏回归）", want)
		}
	}
	// 反向：三卡 grid 不得再出现无 spaced 形态
	if strings.Contains(js, `class="grid cols3">`) {
		t.Error("app.js 不应再有未挂 spaced 的 grid cols3（区块零间距回归）")
	}
	// 文案契约（codex 复审 ①②，2026-10-25）：三态裸词上主面与技术态前置不得回潮——
	// 人话在前，形态代号只进悬停（title="形态代号：…"）；账本三态改计数条常显
	// （t20261026-webfix）后，旧「诊断：账本计数」折叠形态不得回潮。
	for _, banned := range []string{
		`执行账本（三态）`,
		`standalone（本机自治，不连 hub）`,
		`setup（未连接中心端）`,
		`诊断：账本计数`,
		`<small>accepted</small>`,
		`<small>started</small>`,
		`<small>finished</small>`,
	} {
		if strings.Contains(js, banned) {
			t.Errorf("app.js 不应再有 %q（人话在前，技术态进诊断/悬停）", banned)
		}
	}
	if !strings.Contains(js, `title="形态代号：`) {
		t.Error(`app.js 应含 title="形态代号：…"（形态技术态悬停可查）`)
	}
	// 结构性防回潮（codex 终审，2026-10-25）：上面的正反向 Contains 只能挡逐字节
	// 复刻，挡不住变体（字段挪回主面、旧文案微调）——改为解析式断言，锁结构而非
	// 字符串：
	//   ① 账本计数条（t20261026-webfix：替换旧「诊断：账本计数」折叠，伦哥反馈换
	//      展示形式）：定位 <div class="ledger-line"> 块，锁「中文标签 + b 数字 +
	//      esc(led.<字段> ?? 0) 数据链」完整形态，且三字段引用全文件各恰一次——
	//      改裸英文标签、换回 details 折叠、字段引用断裂即红；
	//   ② 形态三态配对：standalone/setup/networked 三态各验——modeTxt 链把形态
	//      映射到人话（可见面）、modeTech 链映射到代号本身（悬停面），modeStat
	//      模板把 title="形态代号：…" 与 esc(modeTxt) 绑在同一 <b> 上；三段咬合
	//      才有「任一形态下人话可见 + 代号只进悬停」的配对语义。
	ledgerRe := regexp.MustCompile(`(?s)<div class="ledger-line">(.*?)</div>`)
	ledgerMatches := ledgerRe.FindAllStringSubmatch(js, -1)
	if len(ledgerMatches) != 1 {
		t.Fatalf("app.js 应恰含一个 ledger-line 计数条，got %d", len(ledgerMatches))
	}
	ledgerBody := ledgerMatches[0][1]
	if !strings.HasPrefix(ledgerBody, `账本 · 受理 <b>${esc(led.accepted ?? 0)}</b> · 进行中 <b>${esc(led.started ?? 0)}</b> · 已结束 <b>${esc(led.finished ?? 0)}</b>`) {
		t.Errorf("ledger-line 计数条结构被改：应锁「账本 · 受理/进行中/已结束 + esc(led.<字段> ?? 0)」完整中文标签+数据链形态，got %q", ledgerBody)
	}
	assign := func(name string) string {
		m := regexp.MustCompile(`(?s)const ` + name + ` = (.*?);`).FindStringSubmatch(js)
		if m == nil {
			t.Fatalf("app.js 应含 const %s = …（形态三态渲染结构被改）", name)
		}
		return m[1]
	}
	txtChain, techChain, statTpl := assign("modeTxt"), assign("modeTech"), assign("modeStat")
	for _, form := range []struct {
		code, human string
		fallback    bool // networked 走 modeTxt 链兜底分支（无显式 === 比较）
	}{
		{code: "standalone", human: "本机自治，不连中心端"},
		{code: "setup", human: "未连接中心端"},
		{code: "networked", human: "组网在线", fallback: true},
	} {
		if form.fallback {
			if !regexp.MustCompile(`\s*:\s*'` + regexp.QuoteMeta(form.human) + `'\s*$`).MatchString(txtChain) {
				t.Errorf("modeTxt 链应以兜底人话 %q 收尾（%s 可见面须为人话）", form.human, form.code)
			}
		} else if !regexp.MustCompile(`st\?\.mode === '` + form.code + `'\s*\?\s*'` + regexp.QuoteMeta(form.human) + `'`).MatchString(txtChain) {
			t.Errorf("modeTxt 应把形态 %s 映射为人话 %q（可见面不得裸用技术代号）", form.code, form.human)
		}
		if !regexp.MustCompile(`st\?\.mode === '` + form.code + `'\s*\?\s*'` + form.code + `'`).MatchString(techChain) {
			t.Errorf("modeTech 应把形态 %s 映射到代号本身（悬停 title=形态代号：%s）", form.code, form.code)
		}
	}
	// 配对落点：title 与可见文本必须绑在同一 <b> 元素上（分家即失去
	// 「人话在前、代号进悬停」的形态语义）。
	pairRe := regexp.MustCompile("<b\\$\\{modeTech \\? ` title=\"形态代号：\\$\\{modeTech\\}\"` : ''\\}>\\$\\{esc\\(modeTxt\\)\\}</b><small>当前形态</small>")
	if !pairRe.MatchString(statTpl) {
		t.Error(`modeStat 应把 title="形态代号：${modeTech}" 与 ${esc(modeTxt)} 绑在同一 <b>（人话与形态代号配对，不得分家）`)
	}
	// 设计系统载体与 mini-stat 卡内节奏（含 .spaced 组合保序：统筹卡 22px 下推不被单类盖掉）
	for _, want := range []string{
		`.spaced{margin-top:22px}`,
		`.mini-stat{display:flex;gap:18px;margin:9px 0}`,
		`.mini-stat.spaced{margin-top:22px}`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css 应含 %q", want)
		}
	}
}
