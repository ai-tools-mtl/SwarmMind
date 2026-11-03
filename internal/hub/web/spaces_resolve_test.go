// spaces_resolve_test.go 留待核对人工裁决页面接线源码锚（W09 工作块 B，docs/tasks/
// v3/W09-hardening.md 工作块 B「hub 页面」；同 spaces_poll_test.go 源码锚口径——
// 行为面由服务端契约测试 internal/hub/api spaces_resolve_test 承担，本文件锁
// hub 控制台空间详情页裁决入口的结构接线）：
//   - 调度条分区渲染留待核对意图行（数据源 = 服务端 diag.awaiting_review_intents
//     join 投影，前端不自算状态）；
//   - 行内 accept/reject 裁决按钮 + note 弹层（草稿只存弹层 DOM——D22 同款纪律）；
//   - 提交走 POST intents/{rid}/resolve，失败人话透出层保留；
//   - shared block 零触碰由 internal/node/localapi 逐字节锁测试承担（不在本锚）。
package web

import (
	"strings"
	"testing"
)

func TestConsoleSpaceResolveWiringSources(t *testing.T) {
	app := readRepo(t, "app.js")
	for _, want := range []string{
		`function spaceAwaitZone(pg) {`,   // 留待核对行分区渲染
		`pg.diag.awaiting_review_intents`, // 数据源=服务端 join 投影（零前端自算）
		`const SPACE_RESOLVE_WORDS = { accept: '核验通过（收束）', reject: '否决（取消收束）' };`, // 裁决按钮词
		`SPACE_RESOLVE_REASONS[r.reason]`, // 形态词随服务端 reason
		`data-verdict="accept"`,           // 裁决词形传参
		`data-verdict="reject"`,
		`'space-resolve': b => spaceIntentResolve(`,            // 点击接线
		`'space-resolve-confirm': b => spaceResolveConfirm(b)`, // 确认提交接线
		`/intents/${encodeURIComponent(rid)}/resolve`,          // REST 端点对齐
		`textarea id="resolve-note"`,                           // note 弹层（草稿存弹层 DOM）
		`同裁决幂等命中，未重复变更`,                                        // dup 门呈现
		`主人管理会话专属`,                                             // 授权边界如实注记
	} {
		if !strings.Contains(app, want) {
			t.Fatalf("空间详情裁决接线缺源码锚: %s", want)
		}
	}
	// node 页零改动锚：node 端 app.js 不得出现裁决入口（hub 管理动作——SPEC 六.五
	// 白名单差异，随批登记）。
	nodeApp := readRepo(t, "../../node/web/app.js")
	for _, ban := range []string{"space-resolve", "awaiting_review_intents", "SPACE_RESOLVE_WORDS"} {
		if strings.Contains(nodeApp, ban) {
			t.Fatalf("node 页不得携带裁决入口（hub 管理动作）: %s", ban)
		}
	}
}
