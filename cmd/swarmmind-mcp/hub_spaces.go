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
