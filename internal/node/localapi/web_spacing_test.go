package localapi

import (
	"io"
	"net/http"
	"net/http/httptest"
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
		`<section class="card spaced"><h2>本机统筹 · 形态</h2>`,                    // setup 分支
		`<section class="card spaced"><div class="row"><h2>本机统筹 · 形态</h2>`,   // 常态 head
		`<section class="card spaced"><div class="row"><h2>最近任务（本机派出的）</h2>`, // recentTasksCard
		// 三卡内部节奏统一：执行账本不再用 spaced 下推（mini-stat 由样式给 9px 0，与 .metric 同）
		`<h3>执行账本（三态）</h3><div class="mini-stat">`,
		// 其他页同类漏挂（要求 4：一次修完，只动顶层区块间距）
		// 注意：下一行是 Go 反引号原始串（不插值），${on} 是逐字匹配 app.js 里的
		// JS 模板字面量源码——拼接在测试内 on 分支完成，勿当 Go 插值「修」掉。
		`card svc-card spaced ${on ? '' : 'off'}`,                                                               // svcCard
		`<section class="card spaced"><div class="row"><div><h2>能力配置</h2></div>`,                                // 未加载态
		`<section class="card spaced"><h2>能力配置</h2>`,                                                            // 不可用态
		`<section class="card spaced"><div class="row"><div><h2>能力配置 <span class="badge">node.yaml</span></h2>`, // 编辑态
		`<section class="card spaced"><div class="row"><h2>我的任务</h2>`,                                           // myTasksCard
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js 应含 %q（顶层区块 spaced 节奏回归）", want)
		}
	}
	// 反向：三卡 grid 不得再出现无 spaced 形态
	if strings.Contains(js, `class="grid cols3">`) {
		t.Error("app.js 不应再有未挂 spaced 的 grid cols3（区块零间距回归）")
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
