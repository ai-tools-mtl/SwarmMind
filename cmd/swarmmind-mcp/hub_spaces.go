// hub_spaces.go hub 模式空间工具（W07-02，docs/tasks/v3/W07-hub-routing.md
// REST/MCP映射表「同一组 space_* 工具Hub装配」行）：swarmmind_space_* 九件在 hub
// 模式装配——create/list/status/dispatch/control/messages/record/records/strategy。
// 与 node 面同一组共享实现（node_spaces.go spaceToolXxx——同一参数解析与状态词典
// 单源，禁止第二套词典）；差异仅面注记（spaceFaceHub：scope=hub:<id>、中心/跨机
// 语义）。
//
// 主人专属动作同 node 面拒收：control 工具对 set_budget/policy/finalize/archive
// 显式拒绝（spaceControlOwnerOnly 同一映射表——豁免⑯收窄保留面，hub 模式映射到
// 主人管理会话/控制台）；候选人定版/否决无工具面（豁免⑯，主人 hub 控制台）。
// 鉴权：工具经 hub 既有凭据（SWARM_HUB_TOKEN——PAT 编排主体或 admin_token 登录会
// 话均可，服务端 authSpaceSubject/authSpaceOwner 分层裁决；admin_token 经本 stdio
// 客户端以 Bearer 直调时 hub 服务端按 PAT 通道核验会 401——编排面如实按 PAT 语义，
// 主人动作归 hub 控制台）。
package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// hubSpaceAPI hub 面调用面（SWARM_HUB_URL + SWARM_HUB_TOKEN，与既有 hub 工具同一
// hubAPI 通道）。
func hubSpaceAPI() spaceAPICaller {
	return func(method, path string, body any) (int, []byte, error) {
		return hubAPI(method, path, body)
	}
}

// hubSpaceToolDefs hub 模式空间工具清单（共享定义 + 中心空间注记；tools/list 装配）。
func hubSpaceToolDefs() []toolDef { return spaceToolDefs(spaceFaceHub) }

// hub 空间工具实现（共享实现 + hub 面；一行装配点，词典零分叉）。
func toolHubSpaceCreate(args map[string]any) (string, error) {
	return spaceToolCreate(hubSpaceAPI(), spaceFaceHub, args)
}

func toolHubSpaceList(args map[string]any) (string, error) {
	return spaceToolList(hubSpaceAPI(), spaceFaceHub, args)
}

func toolHubSpaceStatus(args map[string]any) (string, error) {
	return spaceToolStatus(hubSpaceAPI(), spaceFaceHub, args)
}

func toolHubSpaceDispatch(args map[string]any) (string, error) {
	return spaceToolDispatch(hubSpaceAPI(), spaceFaceHub, args)
}

func toolHubSpaceControl(args map[string]any) (string, error) {
	return spaceToolControl(hubSpaceAPI(), spaceFaceHub, args)
}

func toolHubSpaceMessages(args map[string]any) (string, error) {
	return spaceToolMessages(hubSpaceAPI(), spaceFaceHub, args)
}

func toolHubSpaceRecord(args map[string]any) (string, error) {
	return spaceToolRecord(hubSpaceAPI(), spaceFaceHub, args)
}

func toolHubSpaceRecords(args map[string]any) (string, error) {
	return spaceToolRecords(hubSpaceAPI(), spaceFaceHub, args)
}

func toolHubSpaceStrategy(args map[string]any) (string, error) {
	return spaceToolStrategy(hubSpaceAPI(), spaceFaceHub, args)
}

// hubSpaceIntentResolveToolDef 留待核对人工裁决工具定义（W09-B；hub 模式专属——
// node 本地面无意图裁决端点，不入 spaceToolDefs 共享九件清单，词典零分叉）。
func hubSpaceIntentResolveToolDef() toolDef {
	return toolDef{
		Name:        "swarmmind_space_intent_resolve",
		Description: "人工裁决 hub 中心空间的留待核对意图（成员轮终不确定 needs_review，或执行证据缺失任务已清理——按纪律不自动收敛，同空间串行调度因此滞留）。verdict=accept 表示人工核验通过、该轮按完成收束（串行门解锁，排队中的下一棒照常推进）；verdict=reject 表示人工否决、该轮取消收束（不重发不转投，重派请再显式 dispatch）；note 可选裁决说明落意图注记。仅主人专属人工动作：PAT 编排主体调用服务端 403 指路 hub 控制台（agent 不能代人工核验或自证不确定轮次的结果）。仅留待核对态可裁决：任务仍在运行中或意图排队中返回 409；已收束意图同裁决重放幂等返回、异裁决 409 不可改写",
		InputSchema: toolObj(map[string]any{
			"space_id":   toolStr("空间 ID"),
			"request_id": toolStr("留待核对意图的受理请求 ID（GET dispatches/{request_id} 或空间详情 diag 可查）"),
			"verdict":    toolStr("裁决：accept（人工核验通过收束）| reject（人工否决取消收束）"),
			"note":       toolStr("裁决说明（可选；落意图注记——审计事件按既有留痕形态记服务端主体，收束结论以意图终态承载）"),
		}, "space_id", "request_id", "verdict"),
	}
}

// toolHubSpaceIntentResolve swarmmind_space_intent_resolve（hub 面装配点）：留待
// 核对意图人工裁决（主人专属——PAT 调用服务端 403 原话透传，工具面零第二套权限）。
func toolHubSpaceIntentResolve(args map[string]any) (string, error) {
	sid := strings.TrimSpace(argString(args["space_id"], args["space_id"] != nil))
	rid := strings.TrimSpace(argString(args["request_id"], args["request_id"] != nil))
	verdict := strings.TrimSpace(argString(args["verdict"], args["verdict"] != nil))
	if sid == "" || rid == "" || verdict == "" {
		return "", fmt.Errorf("space_id、request_id 与 verdict 均必填（verdict：accept=人工核验通过收束 | reject=人工否决取消收束）")
	}
	switch verdict {
	case "accept", "reject":
	default:
		return "", fmt.Errorf("verdict 须为 accept|reject，得 %q", verdict)
	}
	req := map[string]any{"verdict": verdict}
	if note := argString(args["note"], args["note"] != nil); strings.TrimSpace(note) != "" {
		req["note"] = note
	}
	code, data, err := hubSpaceAPI()("POST", "/api/v1/spaces/"+sid+"/intents/"+rid+"/resolve", req)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", nodeAPIErr(code, data)
	}
	var out struct {
		Dispatch spaceIntentView `json:"dispatch"`
		Dup      bool            `json:"dup"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	it := out.Dispatch
	if out.Dup {
		return fmt.Sprintf("该意图此前已按同裁决收束（幂等命中，零重复变更）：%s（成员 %s、状态 %s、注记 %s）。",
			it.RequestID, it.Member, it.Status, it.Note), nil
	}
	what := "人工核验通过，该轮已按完成收束（converged）"
	if verdict == "reject" {
		what = "人工否决收束（cancelled，不重发不转投）"
	}
	return fmt.Sprintf("留待核对意图已裁决收束：%s（成员 %s）——%s。\n串行门已解锁：同空间排队中的下一棒照常推进（结果落账不自动产生新派发——继续推进请显式 dispatch）。\n裁决主体为服务端解析的管理会话主体（审计留痕，非请求自报）。",
		it.RequestID, it.Member, what), nil
}
