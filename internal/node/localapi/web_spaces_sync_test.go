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
		"const SPACE_HUB_OFF = '中心空间服务未装配';",
		"const SPACE_ZONES = ['head', 'goal', 'members', 'sched', 'msgs', 'compose', 'w05', 'events', 'actions'];",
		"function spaceWaitReason(sp) {",
		"function spaceBadge(s) {",
		// W05-04 P04 策略区词典：记录态/证据四层/自报红线（双端同词）。
		"const SPACE_W05_NOTE = '本区展示协作策略（W05）循环事实：候选定版态、版本与证据四层、问题重开态与完成判据缺口；不以讨论内容冒充判据。';",
		"const SPACE_W05_EMPTY = '本空间暂无策略循环数据：候选/版本/问题均未登记（讨论发言与普通派发不产生循环事实）。';",
		"const SPACE_W05_OFF = '策略投影暂不可达（本机策略面未装配或读取失败）：数据未加载，不以讨论内容冒充判据。';",
		"const SPACE_CAND_STATES = { pending: '待定版', finalized: '已定版', dismissed: '已否决' };",
		"const SPACE_CAND_ACTIONS = { finalize: '定版', dismiss: '否决' };",
		"const SPACE_CAND_CONFIRM = { finalize: '确认定版', dismiss: '确认否决' };",
		"const SPACE_ISSUE_STATES = { open: '待复审', confirmed_fixed: '已修复确认', still_open: '仍未解', stale: '已失效（版本变更）' };",
		"const SPACE_SEVERITIES = { must: '必须修', should: '建议修', info: '可不修' };",
		"const SPACE_EVIDENCE_LAYERS = { cli_exit: 'CLI 退出0', protocol_parse: '协议解析', round_complete: '轮完成', work_accept: '工作验收' };",
		"const SPACE_EVIDENCE_SOURCES = { agent_self_report: 'agent 自报', executor_receipt: '执行回执', protocol_receipt: '协议回执', host_observation: '服务端事实', human_confirmation: '人工核对' };",
	} {
		if !strings.Contains(blk, want) {
			t.Errorf("空间词典 shared block 应含 %q", want)
		}
	}

	// ---- W05-04 策略区接线锚点：node 真渲染 + hub 真渲染（W07-03 起双端同面）。
	// node：w05 区投影渲染（分区 diff 机制既有）+ strategy 投影拉取 + 候选定版/否决
	// 主人产品面（W05-04 裁决修复：pending 行按钮走 SPACE_CAND_ACTIONS 词典，主人
	// 专属端点，编排主体服务端 403 人话指路）。
	for _, want := range []string{
		"function spaceW05Zone(st) {",
		"let spacePageReqSeq = 0",
		"seqAtReq !== spacePageReqSeq || S.sessionEpoch !== epochAtReq",   // 必修2：node 端守卫同构源码锚（node web 无 JS 测试包，行为反例由 hub 侧同构 harness 覆盖）
		"w05: spaceW05Zone(pg.strategy),",
		"'/strategy'",
		"SPACE_EVIDENCE_SOURCES[ev.source]",
		"不冒充已交付",
		"button(SPACE_CAND_ACTIONS.finalize, 'space-cand:finalize:' + esc(c.id), 'primary', 'type=\"button\"')",
		"button(SPACE_CAND_ACTIONS.dismiss, 'space-cand:dismiss:' + esc(c.id), 'danger', 'type=\"button\"')",
		"case 'space-cand': spaceCandAct(arg); return;",
		"function spaceCandAct(arg) {",
	} {
		if !strings.Contains(nodeJS, want) {
			t.Errorf("node app.js 应含 %q（P04 策略区接线）", want)
		}
	}
	// hub：w05 真渲染同表头（候选/版本/问题三表）+ 策略投影拉取（W07-03 可用化：
	// 数据源 GET /spaces/{id}/strategy；候选定版/否决主人产品面 hub 同构，参数经
	// data-cverb/data-cid——hub act 词形约定：键名精确+参数走 data-*）。
	for _, want := range []string{
		"<th>候选</th><th>提出者</th><th>轮次</th><th>状态</th><th>重申</th>",
		"<th>问题</th><th>级别</th><th>文件:行</th><th>有效状态</th><th>重报/修复</th>",
		"SPACE_EVIDENCE_LAYERS.cli_exit",
		"function spaceW05Zone(st) {",
		"'/strategy'",
		"function spaceCandAct(arg) {",
		"'space-cand'",
		"data-cverb=\"finalize\"",
		"data-cid=\"' + esc(c.id) + '\"",
	} {
		if !strings.Contains(hubJSStr, want) {
			t.Errorf("hub app.js 应含 %q（w05 真渲染接线 W07-03）", want)
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
	// hub：W07-03 可用化——真实数据面接线锚点（列表/详情/增量/策略三源 + 503
	// 未装配专态词典兜底 + 中心范围如实注记）。
	for _, want := range []string{
		"['spaces', '协作空间']",
		"SPACE_HUB_OFF",
		"loadResource('spaces', '/api/v1/spaces'",   // 必修1：列表经 loadAll 统一轮询链（独立 loadSpaces 退役）
		"async function fetchSpacePage() {",
		"spacePageReqSeq",   // 必修2：详情取数请求序号守卫（双端同构；hub 行为反例见 spaces_poll_test.go）
		"let spacePageReqSeq = 0",
		"/messages?since=",
		"'POST', `/api/v1/spaces/${encodeURIComponent(id)}/control`",
		"/candidates/${encodeURIComponent(cid)}/",
		"return S.spaceId ? spaceDetailPageView() : spaceListPageView();",
		`id="space-detail"`,
		`data-act="space-back"`,
		// 中心范围如实注记（范围差异：scope 固定 hub:<id>；成员线程=hub 注册表会话；
		// 操作主体=hub 管理会话 owner:admin；PAT 主人专属动作 403 如实）。
		"' · 范围 ' + esc(pg.scope)",
		"中心空间固定范围",
		"hub 注册表会话",
		"owner:admin",
		"403",
	} {
		if !strings.Contains(hubJSStr, want) {
			t.Errorf("hub app.js 应含 %q（P03/P04 可用化接线 W07-03）", want)
		}
	}
	// node 端必须有真实操作入口（与 hub 真实入口互为对照——防任何一端抄成壳）。
	// 锚点为 button(act) 调用参数与动作分发字面量（data-act 属性由 button() 运行时
	// 拼装，不以渲染产物形态出现在源码）。
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
