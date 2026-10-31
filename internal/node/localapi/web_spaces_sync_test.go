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

// TestWebSpacesDictSharedBlock 空间词典双端一致性锁（W04-03 P03/P04，SPEC 六.五
// 「双端 Web 展示一致性契约」第 1/3 条）：协作空间页面共用的九态/调度方式/完成
// 授权/意图/动作词/空态词典收进「空间词典 shared block」双端逐字节同文——hub 端
// 空间后端 W07 接线前为如实壳，词典先行同文防「接线时才发现词表漂移」；单端独改
// 即红。既有 shared block 零触碰（本文件只新增锁，不改 web_drawer_sync_test.go /
// web_md_render_test.go 任何断言）。
func TestWebSpacesDictSharedBlock(t *testing.T) {
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

	blk := sharedBlock(t, nodeJS, hubJSStr, "空间词典 shared block")

	// ---- 词典全集锚点：九态九键 / 调度方式 / 完成授权 / 意图四态 / 动作词 / 空态。 ----
	for _, want := range []string{
		"pending_config: '待配置', pending_leader: '待管家领取', ready: '可运行（待启动）',",
		"running: '运行中', paused: '已暂停', blocked: '受阻', needs_review: '待核对',",
		"completed: '已完成', abandoned: '已放弃',",
		"const SPACE_TERMINAL = new Set(['completed', 'abandoned']);",
		"const SPACE_MODES = { manual: '手动', builtin: '内置协作', external: '外部管家' };",
		"const SPACE_FINISH_AUTH = { human_only: '完成须主人定版', verified_write_review: '写审可自动完成' };",
		"const SPACE_INTENTS = { pending: '排队待投递', accepted: '执行中', converged: '已收束', cancelled: '已取消' };",
		"dispatch: '指名派发', configure_members: '配置成员', takeover: '接管',",
		"renew: '续约', finish: '申请完成', finalize_done: '定版（完成）', finalize_abandoned: '放弃（定版）',",
		"archive: '归档', budget: '调整预算', policy: '调整策略',",
		"const SPACE_EMPTY_LIST = '还没有协作空间';",
		"const SPACE_HUB_OFF = '中心端协作空间后端未接线（规划 W07）';",
		"const SPACE_ZONES = ['head', 'goal', 'members', 'sched', 'msgs', 'compose', 'w05', 'events', 'actions'];",
		"function spaceWaitReason(sp) {",
		"function spaceBadge(s) {",
	} {
		if !strings.Contains(blk, want) {
			t.Errorf("空间词典 shared block 应含 %q", want)
		}
	}

	// ---- 双端页面接线锚点（词典消费面；两端各自实现但挂点同词）。 ----
	// node：P03 导航入口 + P04 独立详情路由 + 操作词经词典。
	for _, want := range []string{
		"['spaces', '协作空间']",
		`data-act="space-back"`,
		`id="space-detail"`,
		"return S.spaceId ? spaceDetailPageView() : spaceListPageView();",
	} {
		if !strings.Contains(nodeJS, want) {
			t.Errorf("node app.js 应含 %q（P03/P04 接线）", want)
		}
	}
	// hub：壳如实不可用（无假入口）——无创建按钮、未接线声明可见。
	for _, want := range []string{
		"['spaces', '协作空间']",
		"SPACE_HUB_OFF",
	} {
		if !strings.Contains(hubJSStr, want) {
			t.Errorf("hub app.js 应含 %q（P03 壳接线）", want)
		}
	}
	for _, ban := range []string{
		`'space-new'`,    // 假创建入口禁令（hub 后端 W07 前不提供）
		`'space-send'`,   // 假发言入口禁令
		`'space-act:'`,   // 假控制动作入口禁令
		`'sp-submit'`,    // 假弹层提交禁令
	} {
		if strings.Contains(hubJSStr, ban) {
			t.Errorf("hub app.js 不应含 %q（后端未接线不得提供假操作入口）", ban)
		}
	}
	// node 端必须有真实操作入口（与 hub 禁令互为对照——防两端都抄成壳）。锚点为
	// button(act) 调用参数与动作分发字面量（data-act 属性由 button() 运行时拼装，
	// 不以渲染产物形态出现在源码）。
	for _, want := range []string{
		`button('新建空间', 'space-new', 'primary')`,
		`button('发送', 'space-send', 'primary', 'type="button"')`,
		`'space-act:' + act`,
		"case 'space-act':",
	} {
		if !strings.Contains(nodeJS, want) {
			t.Errorf("node app.js 应含 %q（本机空间完整操作面）", want)
		}
	}
}
