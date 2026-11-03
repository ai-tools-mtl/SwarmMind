package localapi

// MCP 接入 P07 双端六分区页结构锁（W03-04，docs/tasks/v3/W03-04-rfc.md §2/§3/§4）。
// 结构面源码锚点（先例：web_nav_filter_test.go）；双端提示词逐字节同文锁；输出面
// 禁词金丝（与 mcpverify_test.go verifyBannedExternalClaims 同源清单——自动点亮路径
// 绝不出现「外部已验证」类冒认措辞）；hub 侧零自动写锁（不得出现 apply/revert 端点
// 调用）；node 侧点亮纪律锚（V1/V3/V4/V5 仅 confirm 端点可点亮）。

import (
	"os"
	"strings"
	"testing"
)

// mcpOnboardAssets 双端 app.js 源码（hub 直读文件；node 直读文件——结构锚点为源码
// 级，embed 一致性由既有 fetchAsset 家族测试另行覆盖）。
func mcpOnboardAssets(t *testing.T) (string, string) {
	t.Helper()
	node, err := os.ReadFile("../../node/web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	hub, err := os.ReadFile("../../hub/web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	return string(node), string(hub)
}

// TestWebMcpOnboardPromptsIdentical 复制提示词双端逐字节同文（V4 §12.4 原句；V5
// 消耗模型额度默认不运行形态）——文案层同源，任何一端独改即漂移 FAIL。
func TestWebMcpOnboardPromptsIdentical(t *testing.T) {
	nodeJS, hubJS := mcpOnboardAssets(t)
	const v4 = "请通过我配置的 SwarmMind MCP 列出当前目标的机器与 agent，明确它是本机还是中心范围；只查询，不创建任务、不启动模型工作。若工具不可用，报告缺少的工具或错误。"
	const v5 = "请通过我配置的 SwarmMind MCP 向测试 agent「在此填写能力名」提交一条低影响验证任务：使用独立工作区，只做只读观察并报告结果，不修改项目、不执行发布。注意：执行验证会消耗模型额度——请先向我复述你将提交的内容，等我确认后再提交；完成后按四类结局如实报告：任务被接受 / 执行失败 / 工具权限拒绝 / 工作失败。"
	for name, src := range map[string]string{"node": nodeJS, "hub": hubJS} {
		for name2, want := range map[string]string{"V4": v4, "V5": v5} {
			if !strings.Contains(src, "const MCP_PROMPT_"+name2+" = '"+want+"';") {
				t.Errorf("%s app.js 缺 %s 提示词（或与定案文本不一字）——双端提示词必须逐字节同文（W03-04-rfc §4）", name, name2)
			}
		}
	}
}

// TestWebMcpOnboardForbiddenClaims 输出面禁词金丝（mcpverify_test 金丝清单同源）：
// 双端 app.js 的自动点亮路径绝不出现「外部已验证」类措辞；「服务端视角」「用户确认」
// 分流词必须在位。
func TestWebMcpOnboardForbiddenClaims(t *testing.T) {
	nodeJS, hubJS := mcpOnboardAssets(t)
	for _, banned := range verifyBannedExternalClaims {
		for name, src := range map[string]string{"node": nodeJS, "hub": hubJS} {
			if strings.Contains(src, banned) {
				t.Errorf("%s app.js 含禁词 %q（自动路径不得冒认外部验证）", name, banned)
			}
		}
	}
	for name, src := range map[string]string{"node": nodeJS, "hub": hubJS} {
		if !strings.Contains(src, "服务端视角") || !strings.Contains(src, "用户确认") {
			t.Errorf("%s app.js 缺「服务端视角/用户确认」分流词（检查视角必须可见）", name)
		}
	}
}

// TestWebMcpOnboardNodeStructure node 侧 #/mcp 子页结构锚点：六分区定序 + 居中
// modal + 右抽屉 + seq 失效守卫 + 点亮纪律（外部层仅 confirm 端点）+ 五端点消费。
func TestWebMcpOnboardNodeStructure(t *testing.T) {
	nodeJS, _ := mcpOnboardAssets(t)
	for _, want := range []string{
		"ph === 'mcp'", // 子页路由（不占主导航）
		`const MCP_ZONES = ['scope', 'env', 'auth', 'gen', 'verify', 'maint'];`, // 六分区定序
		`data-zone="${z}"`,    // 分区容器（drawerZonesApply 消费）
		`'#/mcp'`,             // 入口深链
		`id="mcp-modal"`,      // 居中表单 modal（形态表硬规）
		`id="mcp-drawer"`,     // 诊断明细右抽屉（形态表硬规）
		"let mcpBoardSeq = 0", // 换范围先失效旧响应：发送前自增
		"if (seq !== mcpBoardSeq || S.sessionEpoch !== epoch) return", // 迟到应答丢弃
		"mcpFingerprint(m.draft) !== fp",                              // 弹层预览/应用表单指纹守卫
		"'/api/v1/local/mcp/verify'",                                  // 板面 GET
		"'/api/v1/local/mcp/verify/v0'",                               // V0 服务端检查
		"'/api/v1/local/mcp/verify/v2'",                               // V2 服务端视角探测
		"'/api/v1/local/mcp/verify/confirm'",                          // 用户确认登记（外部层唯一点亮路径）
		"'/api/v1/local/mcp/verify/config-version'",                   // apply/revert 后版本锚推进
		"'/api/v1/local/mcp/config/preview'",                          // W03-02 既有端点消费
		"'/api/v1/local/mcp/config/apply'",
		"'/api/v1/local/mcp/config/revert'",
		"mcpConfirmLayer", // 外部层确认入口
		"V2 服务端视角探测",      // V2 恒标服务端视角（按钮词）
		"不推断",             // 分层边界句在位
	} {
		if !strings.Contains(nodeJS, want) {
			t.Errorf("node app.js 应含 %q（P07 六分区页结构锁）", want)
		}
	}
	// 外部层点亮唯一入口=confirm：登记按钮不得出现在自动路径（V1/V3/V4/V5 无服务端
	// 检查调用——node 侧不得对 v1/v3/v4/v5 发起 verify/v0|v2 之外的点亮请求）。
	if strings.Contains(nodeJS, "verify/v0\", { layer") || strings.Contains(nodeJS, "layer: 'v1'") && strings.Contains(nodeJS, "verify/v0") {
		t.Errorf("node app.js 外部层不得经 v0/v2 端点点亮（仅 confirm）")
	}
}

// TestWebMcpOnboardHubStructure hub 侧设置页 sec-mcp 分区结构锚点：零自动写（不得
// 出现 apply/revert 端点）、远端不探测（不得出现 fetch/XMLHttpRequest 到任意地址——
// 模板生成为纯前端）、页面校验恒标、页内跳转导航、用户确认仅本页会话。
func TestWebMcpOnboardHubStructure(t *testing.T) {
	_, hubJS := mcpOnboardAssets(t)
	for _, want := range []string{
		`id="sec-mcp"`,          // 设置页分区锚（jump-to 目标）
		`data-target="sec-mcp"`, // 页内跳转按钮
		"mcpOnboardSection",     // 六分区挂载
		"'mcpob-open'",          // 配置生成弹层（居中 modal 形态）
		"'mcpob-gen'",
		"页面校验（非服务端验证）", // V0 恒标限定
		"不代证",          // V2 如实边界
		"仅本页会话",        // 用户确认存储边界如实
		"不写任何外部机器文件",   // 零自动写口径（形态表硬规的人话面）
		"SWARM_TARGET", // env 契约变量名（绝不重命名）
		"SWARM_HUB_URL",
		"SWARM_HUB_TOKEN",
		"mcpServers",    // 顶层布局（与节点端模板同形）
		"[mcp_servers.", // codex TOML 形态
		// W07-03 装配注记与动作解析修复锚点：
		"tools/list 实际发现为准", // 工具面随版本演进如实（不硬编码清单/数量、不宣称两面完全对等）
		"不提供本机 agent/管家安装", // hub 无 agent/管家也可运行（页面不要求 hub 本机装 CLI）
		`'mcpob-ask-confirm', 'link', 'data-layer="'`, // 用户确认按钮经 data-layer 传参（旧冒号词形 data-act="mcpob-ask-confirm:v1" 在 hub 精确分发表下永不命中——死按钮修复的回归锚）
	} {
		if !strings.Contains(hubJS, want) {
			t.Errorf("hub app.js 应含 %q（P07 hub 形态结构锁）", want)
		}
	}
	// 死按钮禁令：冒号词形 act 在 hub 精确分发（ACT[dataset.act] 全键匹配）下不可达。
	if strings.Contains(hubJS, `'mcpob-ask-confirm:' + l`) {
		t.Error("hub app.js 不应再生成冒号词形 mcpob-ask-confirm:<layer>（精确分发表不可达=死按钮）")
	}
	// 零自动写硬规：hub 页不得调用宿主配置写端点（写宿主配置属调用机本机动作）。
	for _, ban := range []string{
		"'/api/v1/local/mcp/config/apply'",
		"'/api/v1/local/mcp/config/revert'",
		"'/api/v1/local/mcp/config/preview'",
		"'/api/v1/local/mcp/verify",
	} {
		if strings.Contains(hubJS, ban) {
			t.Errorf("hub app.js 不应调用 %q（hub 形态=只复制/下载不自动应用；中心零新端点消费）", ban)
		}
	}
}

// TestWebMcpOnboardEntryLinks node 侧入口接线：MCP 授权卡与管家卡指路升格（W06-03
// 「随后续版本接入」限定词随 W03-04 交付退役）。
func TestWebMcpOnboardEntryLinks(t *testing.T) {
	nodeJS, _ := mcpOnboardAssets(t)
	if !strings.Contains(nodeJS, "function mcpPageLink(") {
		t.Fatal("node app.js 缺 mcpPageLink 入口助手")
	}
	if strings.Count(nodeJS, "mcpPageLink(") < 4 {
		t.Errorf("mcpPageLink 入口链接过少（授权卡+管家卡两处文案×2 形态），got %d", strings.Count(nodeJS, "mcpPageLink("))
	}
	// 旧「随后续版本接入」限定退役（W03-04 已交付页面，措辞不得承诺不存在的能力）。
	if strings.Contains(nodeJS, "随后续版本接入") {
		t.Error("node app.js 不应再含「随后续版本接入」（W03-04 已交付，限定词退役）")
	}
	if !strings.Contains(nodeJS, "MCP 接入引导") {
		t.Error("node app.js 授权卡应带「MCP 接入引导 →」入口按钮")
	}
}
