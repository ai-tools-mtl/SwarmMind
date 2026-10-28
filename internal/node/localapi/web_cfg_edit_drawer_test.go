package localapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/transport"
)

// TestWebCfgEditDrawer 能力配置编辑抽屉化断言（t20261023-a4b1，三层走查之功能层；
// 接替 t20261023-9546 的 TestWebCfgEditStyle——行内 details 展开已被本任务整体
// 拆除，旧标记锁随之作废）。锁住：
//   - app.js：表格行内只剩「编辑」pill（button data-act="cfg-open:<i>"，落
//     .table-action button 默认档），details/summary 行内展开标记与 DOM 真相源
//     检测（syncCfgEditing/S.cfg.editing）全部移除；
//   - app.js：抽屉基建复用任务详情抽屉家族（#layer + .overlay.drawer-overlay +
//     .drawer#cfg-drawer），标题「编辑 agent · <name>」，底部操作条三键
//     （取消 ghost / 删除此 agent off-btn / 保存 primary「保存（校验+写盘）」），
//     右上 × 关闭；
//   - app.js：单行取消还原语义——打开时 JSON 深拷贝快照，取消/遮罩/× 还原
//     S.cfg.agents[idx]；保存在途（cfgSaving）暂拒关抽屉（防取消还原与在途
//     PUT 竞态分叉）；
//   - app.js：轮询挂起守卫迁移到新状态位（cfgEdit 非空 = 表格 DOM 不重绘，
//     数据照常刷）；抽屉保存走同一 cfgSave PUT 整表协议（label 参数区分文案，
//     返回成功位决定关抽屉）；
//   - style.css：.cfg-edit 行内样式全拆；抽屉表单两列栅格（窄抽屉回退单列）、
//     复选框跨满两列、底部操作条 sticky 满宽；.table-action button 默认档、
//     抽屉基建（.drawer-overlay/.drawer 700px）、.cfg-check 同行形态、
//     .cfg-table td 贴行顶、全局 details/summary 区块语义原样保留。
//
// computed 形态与交互（打开/取消还原/保存/被拒/轮询保护）的实机核验由 headless
// 走查覆盖（docs/TASK-cfg-drawer.md）。
func TestWebCfgEditDrawer(t *testing.T) {
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
		// 行内「编辑」pill：data-act 锁（cfg-open:<i>），样式落 .table-action button 默认档
		`button('编辑', 'cfg-open:' + i)`,
		// 编辑会话状态位（轮询挂起守卫迁移目标；锁代码本体，注释可自由润色）
		`let cfgEdit = null;`,
		`if (cfgEdit) return;`,
		// 单行取消还原：打开时深拷贝快照
		`cfgEdit = { idx, snap: JSON.parse(JSON.stringify(a)) };`,
		`if (cfgEdit && restore && S.cfg.agents[cfgEdit.idx]) S.cfg.agents[cfgEdit.idx] = cfgEdit.snap;`,
		// 保存在途暂拒关抽屉（取消还原 vs 在途 PUT 竞态守卫）
		`let cfgSaving = false;`,
		`if (cfgSaving) return;`,
		// 删除在途禁删（splice 会让 idx 错位写串行）+ 遮罩空守卫（均为单行锁，勿跨行匹配）
		`case 'cfg-del': {`,
		`if (cfgSaving) return; // 保存 PUT 在途禁删`,
		`if (!cfgEdit) return;`,
		// 抽屉基建复用任务详情抽屉家族（同一 #layer/overlay/drawer 结构）
		"<div class=\"overlay drawer-overlay\"><div class=\"drawer\" id=\"cfg-drawer\">",
		// 标题口径
		`编辑 agent · `,
		// 底部操作条三键 + 右上 ×：取消（ghost/默认档）｜删除此 agent（danger 危险
		// 描边——FE-POLISH B5：off-btn 脱离 toggle 语境无样式，删除动作统一危险形态）｜保存（primary）
		`button('取消', 'cfg-cancel', '')`,
		"button('删除此 agent', `cfg-del:${i}`, 'danger')",
		`button('保存（校验+写盘）', 'cfg-save-row', 'primary')`,
		`button('×', 'cfg-close', 'close')`,
		// 保存走同一 PUT 整表协议（FE-POLISH B6：恢复文案由 busyBtn 记忆 dataset.label，
		// label 参数退役——原参数只为失败恢复文案服务）
		`cfgSave(btn)`,
		// 表单字段集迁移进抽屉（cfg-form-cols 栅格容器）
		`<div class="cfg-form-cols">`,
		// 复选框行标记契约：input 在文字前（.cfg-check flex 同行修复的依赖结构）
		`<label class="cfg-check"><input type="checkbox"`,
		// 遮罩点击关闭 = 取消语义（还原单行）
		`if (e.target.classList.contains('overlay')) closeCfgDrawer(true);`,
		// 抽屉内删除：行移除即关抽屉（沿用 cfg-del 暂存语义，保存全部才落盘）
		`if (cfgEdit) closeCfgDrawer(false); // 抽屉内删除：行已移除即关抽屉（closeCfgDrawer 内含 render）`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js 应含 %q（编辑抽屉化回归）", want)
		}
	}
	for _, ban := range []string{
		// 行内 details 展开旧标记：整体拆除
		`<details class="cfg-edit">`,
		`<summary>编辑</summary>`,
		`class="cfg-form"`,
		// DOM 真相源编辑态检测（details toggle 检测）：随 details 一起移除
		`syncCfgEditing`,
		`S.cfg.editing`,
	} {
		if strings.Contains(js, ban) {
			t.Errorf("app.js 不应再含 %q（行内 details 编辑回归）", ban)
		}
	}
	for _, want := range []string{
		// 抽屉表单两列栅格（700px 抽屉内）+ 复选框跨满两列
		`.cfg-form-cols{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:2px 18px}`,
		`.cfg-form-cols .cfg-check{grid-column:1/-1}`,
		// 底部操作条 sticky 贴抽屉底 + 负 margin 满宽（滚动内容不两侧漏出）
		`.cfg-drawer-foot{position:sticky;bottom:0;display:flex;align-items:center;justify-content:space-between;gap:14px;margin:24px -32px 0;padding:14px 32px 10px;background:var(--surface);border-top:1px solid var(--hairline)}`,
		// 窄抽屉回退单列 + 操作条随抽屉 padding（680px 媒体查询内）
		`.cfg-form-cols{grid-template-columns:1fr}`,
		`.cfg-drawer-foot{margin-left:-22px;margin-right:-22px;padding-left:22px;padding-right:22px}`,
		// 「编辑」pill 现落 .table-action button 默认档（t20261023-9546 的小 pill 参数，保留锁）
		`.table-action{white-space:nowrap}.table-action button{font-size:12.5px;height:auto;padding:4px 10px}`,
		// 抽屉基建不动（任务详情抽屉同家族：右贴边 700px 白面板）
		`.drawer-overlay{padding:0;justify-content:flex-end;align-items:stretch}`,
		// 2026-10-25 抽屉统一（D1）：node 抽屉壳对齐 hub 入场动效（opacity/transform
		// 过渡 + .open 终态；cfg 编辑抽屉同壳同动效家族），断言随样式演进同步更新
		`.drawer{width:min(700px,96vw);background:var(--surface);overflow:auto;padding:28px 32px;box-shadow:var(--sh-overlay);opacity:0;transform:translateX(48px);transition:transform var(--dur-enter) var(--ease-spring),opacity var(--dur-enter) var(--ease-spring)}`,
		`.overlay.open .drawer{transform:none;opacity:1}`,
		// 复选框行常规形态保留（抽屉表单沿用）
		`.cfg-check{display:flex;align-items:center;gap:8px}`,
		`.cfg-check input{width:auto;flex:none;margin:0}`,
		// 能力配置表名称列带描述时贴行顶（命名空间只罩该表）
		`.cfg-table td{vertical-align:top}`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css 应含 %q（编辑抽屉样式回归）", want)
		}
	}
	if strings.Contains(css, `.cfg-edit`) {
		t.Error("style.css 不应再含 .cfg-edit（行内展开样式随 details 整体拆除）")
	}
	// 全局 details/summary 区块展开语义不动（「编辑字段说明」「配置详情」等区块用）
	for _, want := range []string{
		`details{border-top:1px solid var(--hairline);padding-top:15px;margin:20px 0}summary{cursor:pointer;font-weight:600`,
		`details[open] summary{margin-bottom:18px}`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css 应保留全局 details/summary 区块样式 %q（抽屉化不得动全局）", want)
		}
	}
}
