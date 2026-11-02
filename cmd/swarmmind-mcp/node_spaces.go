// node_spaces.go node 空间工具（W04-02 六件 + W05-04 策略面三件，docs/tasks/v3/
// W04-spaces.md + W05-strategies.md + SPEC 六.六豁免⑯收窄兑现）：投影本机空间面
// /api/v1/spaces*——create/list/status/dispatch/control/messages 六件 + record/
// records/strategy 策略循环记录与投影三件。与 hub 面的边界（任务书既定豁免意图）：
//   - 预算/策略/定版/归档（set_budget/policy/finalize/archive）为主人专属（豁免
//     保留面）：control 工具对这四个动作显式拒绝（指路主人 REST/控制台），绝不
//     静默透传一个必然 403 的调用；
//   - dispatch 是编排主体唯一的执行入口（模型层逐动作授权：须当前驱动者形态——
//     领取 external 空间后持任期；主人 manual 空间 owner 形态）；
//   - messages 只读（发言归属须指名成员；主人发言经主人 REST 面）——发言零唤醒，
//     查询/发言不代表获得调度权（N01）。
//
// 全部工具经共享编排凭据 SWARM_NODE_PRIME_TOKEN 调本机 REST（服务端逐动作授权，
// 工具面不做第二套权限——只做参数校验与人话渲染，错误原话透传 nodeAPIErr）。
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// argNum 数值参数归一（进程内调用传 int/float64，线上 JSON 恒 float64——两者都收，
// 拒绝非数值）。
func argNum(args map[string]any, key string) (float64, bool) {
	switch v := args[key].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

// spaceView 空间 REST 视图解码（collab.SpaceView 形态 + 在途投影）。
type spaceView struct {
	ID           string `json:"id"`
	Goal         string `json:"goal"`
	Acceptance   string `json:"acceptance,omitempty"`
	Mode         string `json:"mode"`
	Status       string `json:"status"`
	Phase        string `json:"phase,omitempty"`
	Version      int64  `json:"version"`
	Archived     bool   `json:"archived"`
	PausedReason string `json:"paused_reason,omitempty"`
	WaitReason   string `json:"wait_reason,omitempty"`
	InFlight     int    `json:"in_flight"`
	Leader       struct {
		Kind       string `json:"kind"`
		Principal  string `json:"principal"`
		Term       uint64 `json:"term"`
		LeaseUntil string `json:"lease_until,omitempty"`
	} `json:"leader"`
	Budget struct {
		MaxMessages       int `json:"max_messages"`
		MaxDispatchRounds int `json:"max_dispatch_rounds"`
		MaxWallSeconds    int `json:"max_wall_seconds"`
	} `json:"budget"`
	Used struct {
		Messages       int   `json:"messages"`
		DispatchRounds int   `json:"dispatch_rounds"`
		WallSeconds    int64 `json:"wall_seconds"`
	} `json:"used"`
	Members []struct {
		Name      string `json:"name"`
		Role      string `json:"role,omitempty"`
		SessionID string `json:"session_id,omitempty"`
	} `json:"members"`
}

// spaceStatusLine 空间单行人话（list/status 共用；调度条核心字段：
// 状态/主体/任期/暂停原因——任务书「调度条/接管」投影）。
func spaceStatusLine(sp spaceView) string {
	line := fmt.Sprintf("%s：[%s] %s · 版本 %d · 在途 %d", sp.ID, sp.Status, sp.Goal, sp.Version, sp.InFlight)
	if sp.Leader.Term > 0 {
		line += fmt.Sprintf(" · 负责人 %s/%s 任期 %d", sp.Leader.Kind, sp.Leader.Principal, sp.Leader.Term)
	} else {
		line += " · 待领取（无负责人）"
	}
	if sp.PausedReason != "" {
		line += " · 暂停原因：" + sp.PausedReason
	}
	return line
}

// toolSpaceCreate swarmmind_space_create（node 面）：创建协作空间。编排主体仅可
// 建 external 待领取空间（成员可空）——AC14 顺序：领取→配置成员→显式启动；主人
// local_token 可建任意方式（经主人 REST 面，MCP 工具按编排主体语义描述）。request_id
// 可选幂等（同键同文 200+dup 返原空间、异文 409）。
func (b nodeBackend) toolSpaceCreate(args map[string]any) (string, error) {
	goal := strings.TrimSpace(argString(args["goal"], args["goal"] != nil))
	mode := strings.TrimSpace(argString(args["mode"], args["mode"] != nil))
	if goal == "" || mode == "" {
		return "", fmt.Errorf("goal 与 mode 均必填（mode：编排主体仅 external 待领取空间）")
	}
	req := map[string]any{"goal": goal, "mode": mode}
	if v := strings.TrimSpace(argString(args["acceptance"], args["acceptance"] != nil)); v != "" {
		req["acceptance"] = v
	}
	if rid := strings.TrimSpace(argString(args["request_id"], args["request_id"] != nil)); rid != "" {
		req["request_id"] = rid
	}
	if raw, ok := args["budget"].(map[string]any); ok {
		budget := map[string]any{}
		for _, k := range []string{"max_messages", "max_dispatch_rounds", "max_wall_seconds"} {
			if n, ok := argNum(raw, k); ok && n >= 0 {
				budget[k] = int64(n)
			}
		}
		if len(budget) > 0 {
			req["budget"] = budget
		}
	}
	// 完成授权字段显式拒绝（编排主体不能自授 verified_write_review——模型层同规
	// 403，工具面提前拒绝给人话；伪造授权字段拒绝，绝不静默丢弃）。
	if raw, ok := args["policy"].(map[string]any); ok {
		if len(raw) > 0 {
			return "", fmt.Errorf("policy（完成授权/领取约束）为主人专属配置：编排主体创建 external 空间使用默认 human_only，改配由主人管理面完成")
		}
	}
	code, data, err := b.nodeAPI("POST", "/api/v1/spaces", req, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 201 && code != 200 {
		return "", nodeAPIErr(code, data)
	}
	var out struct {
		Space spaceView `json:"space"`
		Dup   bool      `json:"dup"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	sp := out.Space
	dupNote := ""
	if out.Dup || code == 200 {
		dupNote = "（request_id 幂等命中：同键同文返回原空间，未重复创建）\n"
	}
	return fmt.Sprintf("空间已创建：%s\n目标：%s\n%s方式：%s · 状态：%s（版本 %d）\n后续：领取（takeover）→ 配置成员（configure_members）→ 显式启动（start）→ 指名派发（swarmmind_space_dispatch）。",
		sp.ID, sp.Goal, dupNote, sp.Mode, sp.Status, sp.Version), nil
}

// toolSpaceList swarmmind_space_list（node 面）：空间列表（创建序）。
func (b nodeBackend) toolSpaceList() (string, error) {
	code, data, err := b.nodeAPI("GET", "/api/v1/spaces", nil, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", nodeAPIErr(code, data)
	}
	var out struct {
		Spaces []spaceView `json:"spaces"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	if len(out.Spaces) == 0 {
		return "本机还没有协作空间（swarmmind_space_create 创建；一次性任务不需要空间，直接 swarmmind_submit）。", nil
	}
	var bl strings.Builder
	fmt.Fprintf(&bl, "本机协作空间列表：共 %d 个（创建时刻升序）", len(out.Spaces))
	for _, sp := range out.Spaces {
		bl.WriteString("\n" + spaceStatusLine(sp))
	}
	bl.WriteString("\n详情与成员/意图用 swarmmind_space_status；派发用 swarmmind_space_dispatch。")
	return bl.String(), nil
}

// spaceIntentView 意图投影（GET /dispatches/{request_id} 与 status 聚合共用）。
type spaceIntentView struct {
	RequestID string `json:"request_id"`
	Member    string `json:"member"`
	SessionID string `json:"session_id,omitempty"`
	TurnSeq   int64  `json:"turn_seq"`
	TaskID    string `json:"task_id,omitempty"`
	Term      uint64 `json:"term"`
	Status    string `json:"status"`
	Note      string `json:"note,omitempty"`
}

// toolSpaceStatus swarmmind_space_status（node 面）：空间详情——状态/负责人/任期/
// 租约/预算用量/成员绑定与待核对提示（unverified 同词表强制提醒随轮终投影）。
func (b nodeBackend) toolSpaceStatus(args map[string]any) (string, error) {
	sid := strings.TrimSpace(argString(args["space_id"], args["space_id"] != nil))
	if sid == "" {
		return "", fmt.Errorf("space_id 必填（swarmmind_space_list 可查）")
	}
	code, data, err := b.nodeAPI("GET", "/api/v1/spaces/"+sid, nil, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", nodeAPIErr(code, data)
	}
	var out struct {
		Space spaceView `json:"space"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	sp := out.Space
	var bl strings.Builder
	bl.WriteString(spaceStatusLine(sp) + "\n")
	fmt.Fprintf(&bl, "目标：%s\n", sp.Goal)
	if sp.Acceptance != "" {
		fmt.Fprintf(&bl, "验收：%s\n", sp.Acceptance)
	}
	fmt.Fprintf(&bl, "预算：消息 %d/%s · 派发 %d/%s · 墙钟 %ds/%s（已用/上限，0 上限=不限）\n",
		sp.Used.Messages, intOrUnlimited(sp.Budget.MaxMessages),
		sp.Used.DispatchRounds, intOrUnlimited(sp.Budget.MaxDispatchRounds),
		sp.Used.WallSeconds, intOrUnlimited(sp.Budget.MaxWallSeconds))
	if len(sp.Members) == 0 {
		bl.WriteString("成员：（未配置——领取后 configure_members 配置）\n")
	} else {
		bl.WriteString("成员：")
		for i, m := range sp.Members {
			if i > 0 {
				bl.WriteString("、")
			}
			fmt.Fprintf(&bl, "%s（%s）", m.Name, m.Role)
		}
		bl.WriteString("\n")
	}
	switch {
	case sp.Status == "pending_leader":
		bl.WriteString("\n待领取：takeover（编排主体领取须空间处于待领取且领取约束允许；任期 CAS 原子）。\n")
	case sp.Status == "paused":
		bl.WriteString("\n已暂停（" + sp.PausedReason + "）：暂停只停新增派发，在途照常收敛；恢复 resume 由当前负责人或主人执行——在途未清时须显式确认。\n")
	case sp.Status == "needs_review":
		bl.WriteString("\n待核对：存在不确定的执行事实（未收束意图）——核对前不要基于既有轮次下结论，恢复须显式 resume。\n")
	case sp.Status == "running":
		bl.WriteString("\n运行中：swarmmind_space_dispatch 指名派发（串行：同空间一次一棒）。\n")
	}
	if sp.InFlight > 0 {
		bl.WriteString(fmt.Sprintf("在途/排队意图 %d 条——结果落账不自动触发下一轮（结果回流靠显式派发）。\n", sp.InFlight))
	}
	return bl.String(), nil
}

// intOrUnlimited 预算上限人话（0=不限）。
func intOrUnlimited(n int) string {
	if n == 0 {
		return "不限"
	}
	return fmt.Sprintf("%d", n)
}

// toolSpaceDispatch swarmmind_space_dispatch（node 面）：指名派发（编排主体唯一
// 执行入口；须当前驱动者形态——领取后持任期；派发按空间串行执行：同空间一次一
// 棒，跨空间并行）。202 受理即回意图引用（不阻塞等待）；request_id 可选幂等（同
// 键同文返原意图，重放不重复扣预算——N02）。轮终经 status 或 messages 观察；
// 结果落账不自动触发下一轮。
func (b nodeBackend) toolSpaceDispatch(args map[string]any) (string, error) {
	sid := strings.TrimSpace(argString(args["space_id"], args["space_id"] != nil))
	member := strings.TrimSpace(argString(args["member"], args["member"] != nil))
	if sid == "" || member == "" {
		return "", fmt.Errorf("space_id 与 member 均必填（指名成员派发）")
	}
	ev, ok := argNum(args, "expected_version")
	if !ok || ev <= 0 {
		return "", fmt.Errorf("expected_version 必填（读档版本乐观锁；swarmmind_space_status 可查现版本）")
	}
	req := map[string]any{
		"member":           member,
		"expected_version": int64(ev),
	}
	if rid := strings.TrimSpace(argString(args["request_id"], args["request_id"] != nil)); rid != "" {
		req["request_id"] = rid
	} else {
		req["request_id"] = "disp-" + protocol.NewTraceID() // 自动生成（缺省不承担重放回执）
	}
	if term, ok := argNum(args, "term"); ok && term > 0 {
		req["term"] = int64(term)
	}
	if note := argString(args["note"], args["note"] != nil); note != "" {
		req["note"] = note
	}
	code, data, err := b.nodeAPI("POST", "/api/v1/spaces/"+sid+"/dispatches", req, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 202 && code != 200 {
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
		return fmt.Sprintf("该派发此前已受理（同 request_id 幂等命中，未重复执行未重复扣预算）：意图 %s（成员 %s、状态 %s）。\n用 swarmmind_space_status 或 GET dispatches 查询进度。", it.RequestID, it.Member, it.Status), nil
	}
	return fmt.Sprintf("派发已受理：意图 %s（成员 %s、任期 %d、状态 %s；空间串行调度中——同空间一次一棒）。\n结果落账不自动触发下一轮：继续推进请再次显式派发；进度用 swarmmind_space_status 查看。",
		it.RequestID, it.Member, it.Term, it.Status), nil
}

// spaceControlOwnerOnly control 工具显式拒绝的主人专属动作（SPEC 六.六豁免⑯收窄
// 保留面：预算/策略/定版/归档归主人，agent 不能经 control 代调或伪造人已授权）。
var spaceControlOwnerOnly = map[string]bool{
	"set_budget": true, "policy": true, "finalize": true, "archive": true,
}

// toolSpaceControl swarmmind_space_control（node 面）：有限控制——pause/resume/
// ready/start/configure_members/takeover/renew（白名单内；驱动者/主人形态由服务端
// 逐动作授权裁决）。set_budget/policy/finalize/archive 为主人专属（豁免保留面），
// 工具面显式拒绝指路主人；无任意 HTTP/脚本执行能力（任务书红线）。
func (b nodeBackend) toolSpaceControl(args map[string]any) (string, error) {
	sid := strings.TrimSpace(argString(args["space_id"], args["space_id"] != nil))
	action := strings.TrimSpace(argString(args["action"], args["action"] != nil))
	if sid == "" || action == "" {
		return "", fmt.Errorf("space_id 与 action 均必填（pause/resume/ready/start/configure_members/takeover/renew）")
	}
	if spaceControlOwnerOnly[action] {
		return "", fmt.Errorf("动作 %s 为主人专属（预算/策略/定版/归档不开放编排面——agent 不能代调或伪造人已授权）：请由本机主人经主人 REST/控制台执行", action)
	}
	switch action {
	case "pause", "resume", "ready", "start", "configure_members", "takeover", "renew":
	default:
		return "", fmt.Errorf("action 须为 pause|resume|ready|start|configure_members|takeover|renew（set_budget/policy/finalize/archive 主人专属；dispatch 经 swarmmind_space_dispatch），得 %q", action)
	}
	ev, ok := argNum(args, "expected_version")
	if !ok || ev <= 0 {
		return "", fmt.Errorf("expected_version 必填（读档版本乐观锁；swarmmind_space_status 可查现版本）")
	}
	req := map[string]any{"action": action, "expected_version": int64(ev)}
	if rid := strings.TrimSpace(argString(args["request_id"], args["request_id"] != nil)); rid != "" {
		req["request_id"] = rid
	} else {
		req["request_id"] = "spctl-" + protocol.NewTraceID() // 自动生成（缺省不承担重放回执）
	}
	if term, ok := argNum(args, "term"); ok && term > 0 {
		req["term"] = int64(term)
	}
	if et, ok := argNum(args, "expected_term"); ok {
		req["expected_term"] = int64(et)
	}
	if confirm, ok := args["confirm"].(bool); ok && confirm {
		req["confirm"] = true
	}
	if kind := strings.TrimSpace(argString(args["takeover_kind"], args["takeover_kind"] != nil)); kind != "" {
		req["takeover_kind"] = kind
	}
	if ls, ok := argNum(args, "lease_seconds"); ok && ls > 0 {
		req["lease_seconds"] = int64(ls)
	}
	if reason := argString(args["reason"], args["reason"] != nil); reason != "" {
		req["reason"] = reason
	}
	if note := argString(args["note"], args["note"] != nil); note != "" {
		req["note"] = note
	}
	if raw, ok := args["members"].([]any); ok && len(raw) > 0 {
		members := make([]map[string]any, 0, len(raw))
		for _, v := range raw {
			m, ok := v.(map[string]any)
			if !ok {
				return "", fmt.Errorf("members 须为对象数组（name 必填）")
			}
			mm := map[string]any{}
			name := strings.TrimSpace(argString(m["name"], m["name"] != nil))
			if name == "" {
				return "", fmt.Errorf("members[].name 必填")
			}
			mm["name"] = name
			for _, k := range []string{"role", "workspace", "session_id"} {
				if s := strings.TrimSpace(argString(m[k], m[k] != nil)); s != "" {
					mm[k] = s
				}
			}
			members = append(members, mm)
		}
		req["members"] = members
	}
	code, data, err := b.nodeAPI("POST", "/api/v1/spaces/"+sid+"/control", req, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", nodeAPIErr(code, data)
	}
	var out struct {
		Result struct {
			Space   spaceView `json:"space"`
			Action  string    `json:"action"`
			Dup     bool      `json:"dup,omitempty"`
			Applied bool      `json:"applied"`
		} `json:"result"`
		Dup bool `json:"dup"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	sp := out.Result.Space
	note := fmt.Sprintf("（空间现态 [%s] 版本 %d 在途 %d）", sp.Status, sp.Version, sp.InFlight)
	switch {
	case out.Dup:
		return "该控制动作此前已执行（request_id 幂等命中，返回原回执）" + note, nil
	case !out.Result.Applied && action == "pause":
		return "暂停动作幂等达成（空间本就非运行态）" + note, nil
	case !out.Result.Applied:
		return "控制动作幂等达成（无实际变更）" + note, nil
	}
	switch action {
	case "takeover":
		if sp.Leader.Term > 0 {
			return fmt.Sprintf("接管成功：%s/%s 任期 %d%s\n后续：configure_members 配置成员 → start 显式启动 → swarmmind_space_dispatch 派发。",
				sp.Leader.Kind, sp.Leader.Principal, sp.Leader.Term, note), nil
		}
		return "已开放外部领取（旧任期失效，空间转待领取）" + note + "\n编排主体可 takeover 领取（符合领取约束时）。", nil
	case "resume":
		return "恢复运行（继续由当前负责人显式驱动；排队意图按串行调度继续）" + note, nil
	default:
		return fmt.Sprintf("控制动作 %s 已执行%s", action, note), nil
	}
}

// spaceMessageView 消息账本条目解码。
type spaceMessageView struct {
	Seq    int64  `json:"seq"`
	Member string `json:"member,omitempty"`
	Text   string `json:"text"`
	By     string `json:"by"`
}

// toolSpaceMessages swarmmind_space_messages（node 面）：空间消息增量读（since=
// 已见最大 seq，只读不唤醒——发言与游标绝不触发任何执行，N01）。滚动窗口外的更早
// 消息以 pruned_before_seq 标注。发言归主人/成员面（编排主体经主人 REST 代转），
// 本工具只读。
func (b nodeBackend) toolSpaceMessages(args map[string]any) (string, error) {
	sid := strings.TrimSpace(argString(args["space_id"], args["space_id"] != nil))
	if sid == "" {
		return "", fmt.Errorf("space_id 必填（swarmmind_space_list 可查）")
	}
	path := "/api/v1/spaces/" + sid + "/messages"
	if since, ok := argNum(args, "since"); ok && since > 0 {
		path += fmt.Sprintf("?since=%d", int64(since))
	}
	code, data, err := b.nodeAPI("GET", path, nil, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", nodeAPIErr(code, data)
	}
	var out struct {
		Messages     []spaceMessageView `json:"messages"`
		PrunedBefore int64              `json:"pruned_before_seq"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	if len(out.Messages) == 0 {
		return fmt.Sprintf("空间 %s 无新消息（since 之后；窗口裁剪位置 seq<%d）。", sid, out.PrunedBefore+1), nil
	}
	var bl strings.Builder
	fmt.Fprintf(&bl, "空间 %s 消息（since 之后 %d 条，升序）：", sid, len(out.Messages))
	for _, m := range out.Messages {
		who := "主人"
		if m.Member != "" {
			who = "成员 " + m.Member
		}
		fmt.Fprintf(&bl, "\n#%d [%s] %s", m.Seq, who, truncateForStatus(m.Text))
	}
	if out.PrunedBefore > 0 {
		fmt.Fprintf(&bl, "\n（更早消息已滚动裁剪：seq ≤ %d 不可再拉）", out.PrunedBefore)
	}
	bl.WriteString("\n消息只读不唤醒（发言与游标不触发执行）；推进执行用 swarmmind_space_dispatch。")
	return bl.String(), nil
}

// spaceRecordToolArgs swarmmind_space_record 参数装配：kind 判别联合逐族映射
// REST records 字段（端点层零第二套校验——参数缺失/词形错误由服务端 400 人话
// 原话透传 nodeAPIErr）。kind 间同名不冲突：服务端按 kind 取用，冗余字段无害。
func spaceRecordToolArgs(args map[string]any) map[string]any {
	req := map[string]any{"kind": strings.TrimSpace(argString(args["kind"], args["kind"] != nil))}
	for _, k := range []string{"member", "body", "op", "baseline", "content_sha256", "patch_sha256",
		"author", "issue_id", "reviewer", "against_hash", "result", "version_hash", "note",
		"severity", "file", "title", "found_in_version", "version", "layer", "source", "ref"} {
		if v := strings.TrimSpace(argString(args[k], args[k] != nil)); v != "" {
			req[k] = v
		}
	}
	for _, k := range []string{"round", "author_round", "found_in_round", "line"} {
		if n, ok := argNum(args, k); ok {
			req[k] = int64(n)
		}
	}
	if v, ok := args["verified"].(bool); ok {
		req["verified"] = v
	}
	if raw, ok := args["dependencies"].([]any); ok && len(raw) > 0 {
		deps := make([]map[string]any, 0, len(raw))
		for _, v := range raw {
			d, ok := v.(map[string]any)
			if !ok {
				continue
			}
			dd := map[string]any{}
			for _, k := range []string{"kind", "ref", "sha256", "note"} {
				if s := strings.TrimSpace(argString(d[k], d[k] != nil)); s != "" {
					dd[k] = s
				}
			}
			deps = append(deps, dd)
		}
		req["dependencies"] = deps
	}
	return req
}

// toolSpaceRecord swarmmind_space_record（node 面，W05-04）：策略循环记录登记
// （candidate/review/issue/evidence 四族）。数据写入只是事实登记——不派发不推进
// 循环不触发任何执行（执行唯一入口 swarmmind_space_dispatch）；校验在服务端引擎
// （成员资格/hash 格式/自报不证实/发现版本须已登记），非法拒绝零登记。
func (b nodeBackend) toolSpaceRecord(args map[string]any) (string, error) {
	sid := strings.TrimSpace(argString(args["space_id"], args["space_id"] != nil))
	if sid == "" {
		return "", fmt.Errorf("space_id 必填（swarmmind_space_list 可查）")
	}
	req := spaceRecordToolArgs(args)
	kind, _ := req["kind"].(string)
	if kind == "" {
		return "", fmt.Errorf("kind 必填：candidate|review|issue|evidence（review 另需 op=revision|rereview|clean_review|claim_fix|reply）")
	}
	code, data, err := b.nodeAPI("POST", "/api/v1/spaces/"+sid+"/records", req, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 201 && code != 200 {
		return "", nodeAPIErr(code, data)
	}
	var out struct {
		Candidate *struct {
			ID       string `json:"id"`
			Proposer string `json:"proposer"`
			Summary  string `json:"summary"`
			Status   string `json:"status"`
		} `json:"candidate,omitempty"`
		Revision *struct {
			ID            string `json:"id"`
			ContentSHA256 string `json:"content_sha256"`
		} `json:"revision,omitempty"`
		Issue *struct {
			ID   string `json:"id"`
			File string `json:"file"`
			Line int    `json:"line"`
		} `json:"issue,omitempty"`
		Created bool `json:"created"`
		Dup     bool `json:"dup,omitempty"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	dupNote := ""
	if code == 200 {
		dupNote = "（幂等命中既有事实：候选同文去重计数 / 版本同 hash 同 ID，未产生第二份记录）\n"
	}
	switch {
	case out.Candidate != nil:
		// 提出者从应答体取真实值（终审建议3：服务端归属/去重后的权威提出者——
		// args["member"] 回显会在参数缺省/服务端规整时渲染空值或失真值）。
		return fmt.Sprintf("候选已登记：%s\n提出者：%s · 摘要：%s · 状态：%s（待定版——FINAL 登记不等于交付，定版由主人显式动作）\n%s数据写入只是事实登记，不触发执行；循环推进仍经显式派发。",
			out.Candidate.ID, out.Candidate.Proposer, out.Candidate.Summary, out.Candidate.Status, dupNote), nil
	case out.Revision != nil:
		return fmt.Sprintf("版本已登记：%s（内容 %s…）\n新版本登记使旧复审结论失效（版本变更留痕）；证据须另行逐层登记（四层分离）。\n%s数据写入只是事实登记，不触发执行。",
			out.Revision.ID, tailHash(out.Revision.ContentSHA256), dupNote), nil
	case out.Issue != nil:
		return fmt.Sprintf("问题已登记：%s（%s:%d）\n同 key 重报不换 ID；必须修未清零将阻塞完成判据。\n数据写入只是事实登记，不触发执行。", out.Issue.ID, out.Issue.File, out.Issue.Line), nil
	default:
		return fmt.Sprintf("记录已登记（%s）：事实入循环账，不触发执行、不推进阶段。\n%s", kind, dupNote), nil
	}
}

// tailHash hash 尾段展示（12 位）。
func tailHash(h string) string {
	if len(h) > 12 {
		return h[len(h)-12:]
	}
	return h
}

// toolSpaceRecords swarmmind_space_records（node 面，W05-04）：策略循环记录增量读
//（since=已见最大 seq，只读不唤醒）。记录四族 candidate/review/issue/evidence；
// 滚动裁剪区间以 pruned_before_seq 标注。循环状态全量投影见 swarmmind_space_strategy。
func (b nodeBackend) toolSpaceRecords(args map[string]any) (string, error) {
	sid := strings.TrimSpace(argString(args["space_id"], args["space_id"] != nil))
	if sid == "" {
		return "", fmt.Errorf("space_id 必填（swarmmind_space_list 可查）")
	}
	path := "/api/v1/spaces/" + sid + "/records"
	if since, ok := argNum(args, "since"); ok && since > 0 {
		path += fmt.Sprintf("?since=%d", int64(since))
	}
	code, data, err := b.nodeAPI("GET", path, nil, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", nodeAPIErr(code, data)
	}
	var out struct {
		Records []struct {
			Seq  int64  `json:"seq"`
			Kind string `json:"kind"`
			Type string `json:"type,omitempty"`
			Note string `json:"note"`
		} `json:"records"`
		PrunedBefore int64 `json:"pruned_before_seq"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	if len(out.Records) == 0 {
		return fmt.Sprintf("空间 %s 无新记录（since 之后；裁剪上界 seq ≤ %d）。", sid, out.PrunedBefore), nil
	}
	var bl strings.Builder
	fmt.Fprintf(&bl, "空间 %s 循环记录（since 之后 %d 条，升序）：", sid, len(out.Records))
	for _, r := range out.Records {
		family := r.Type
		if family == "" {
			family = "control"
		}
		fmt.Fprintf(&bl, "\n#%d [%s/%s] %s", r.Seq, family, r.Kind, truncateForStatus(r.Note))
	}
	if out.PrunedBefore > 0 {
		fmt.Fprintf(&bl, "\n（更早记录已滚动裁剪：seq ≤ %d 不可再拉）", out.PrunedBefore)
	}
	bl.WriteString("\n记录只读不唤醒（读游标不触发执行）；候选/版本/问题全量状态用 swarmmind_space_strategy 查看。")
	return bl.String(), nil
}

// spaceStrategyIssueLine 单问题人话行（strategy 工具渲染）。
func spaceStrategyIssueLine(id, severity, file string, line int, state string, blocking bool) string {
	sev := map[string]string{"must": "必须修", "should": "建议修", "info": "可不修"}[severity]
	if sev == "" {
		sev = severity
	}
	st := map[string]string{"open": "待复审", "confirmed_fixed": "已修复确认", "still_open": "仍未解", "stale": "已失效（版本变更）"}[state]
	if st == "" {
		st = state
	}
	mark := ""
	if blocking {
		mark = " ·阻塞"
	}
	return fmt.Sprintf("%s [%s] %s:%d · %s%s", id, sev, file, line, st, mark)
}

// toolSpaceStrategy swarmmind_space_strategy（node 面，W05-04）：策略投影只读——
// 循环阶段/候选及定版态/问题及重开态/修订与证据缺口/停止建议。无策略数据的空间
// 如实空态不编造。完成判据缺口如实展示（数据面事实，不等于可自动完成——完成授权
// 仍归逐动作表）。
func (b nodeBackend) toolSpaceStrategy(args map[string]any) (string, error) {
	sid := strings.TrimSpace(argString(args["space_id"], args["space_id"] != nil))
	if sid == "" {
		return "", fmt.Errorf("space_id 必填（swarmmind_space_list 可查）")
	}
	code, data, err := b.nodeAPI("GET", "/api/v1/spaces/"+sid+"/strategy", nil, b.primeToken)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", nodeAPIErr(code, data)
	}
	var out struct {
		Strategy struct {
			HasLoop         bool   `json:"has_loop"`
			Phase           string `json:"phase"`
			PhaseValid      bool   `json:"phase_valid"`
			PausedFrom      string `json:"paused_from,omitempty"`
			CurrentRevision string `json:"current_revision,omitempty"`
			Candidates      []struct {
				ID      string `json:"id"`
				Summary string `json:"summary"`
				Status  string `json:"status"`
			} `json:"candidates,omitempty"`
			Revisions []struct {
				ID            string `json:"id"`
				ContentSHA256 string `json:"content_sha256"`
			} `json:"revisions,omitempty"`
			Issues []struct {
				ID       string `json:"id"`
				Severity string `json:"severity"`
				File     string `json:"file"`
				Line     int    `json:"line"`
				State    string `json:"state"`
				Blocking bool   `json:"blocking"`
			} `json:"issues,omitempty"`
			Gaps []struct {
				Field  string `json:"field"`
				Detail string `json:"detail"`
			} `json:"gaps,omitempty"`
			Stop struct {
				ShouldPause bool `json:"should_pause"`
				Triggers    []struct {
					Kind   string `json:"kind"`
					Detail string `json:"detail"`
				} `json:"triggers,omitempty"`
			} `json:"stop"`
		} `json:"strategy"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("应答解析失败：%v", err)
	}
	sp := out.Strategy
	if !sp.HasLoop {
		return fmt.Sprintf("空间 %s 暂无策略循环数据（候选/版本/问题均未登记——如实空态）：讨论发言经 swarmmind_space_messages，候选登记经 swarmmind_space_record。", sid), nil
	}
	var bl strings.Builder
	phase := sp.Phase
	if !sp.PhaseValid {
		phase += "（未知词）"
	}
	fmt.Fprintf(&bl, "空间 %s 策略循环：阶段 %s", sid, phase)
	if sp.PausedFrom != "" {
		fmt.Fprintf(&bl, "（暂停前 %s，恢复须回该阶段）", sp.PausedFrom)
	}
	if sp.CurrentRevision != "" {
		fmt.Fprintf(&bl, " · 当前版本 %s…", tailHash(sp.CurrentRevision))
	}
	bl.WriteString("\n")
	if n := len(sp.Candidates); n > 0 {
		fmt.Fprintf(&bl, "候选 %d 条：", n)
		for i, c := range sp.Candidates {
			if i > 0 {
				bl.WriteString("、")
			}
			fmt.Fprintf(&bl, "%s[%s] %s", c.ID, c.Status, truncateForStatus(c.Summary))
		}
		bl.WriteString("\n（FINAL 登记不等于已交付——定版态如上，交付判定归完成判据）\n")
	}
	if n := len(sp.Revisions); n > 0 {
		fmt.Fprintf(&bl, "版本 %d 个（末位=当前）：", n)
		for i, r := range sp.Revisions {
			if i > 0 {
				bl.WriteString("、")
			}
			fmt.Fprintf(&bl, "%s(%s…)", r.ID, tailHash(r.ContentSHA256))
		}
		bl.WriteString("\n（证据四层逐版见 REST GET strategy；自报来源不算已证实）\n")
	}
	if n := len(sp.Issues); n > 0 {
		fmt.Fprintf(&bl, "问题 %d 条：\n", n)
		for _, is := range sp.Issues {
			fmt.Fprintf(&bl, "  %s\n", spaceStrategyIssueLine(is.ID, is.Severity, is.File, is.Line, is.State, is.Blocking))
		}
	}
	if n := len(sp.Gaps); n > 0 {
		fmt.Fprintf(&bl, "完成判据缺口 %d 项（如实展示；完成授权仍归逐动作表，不因缺口清零自动完成）：\n", n)
		for _, g := range sp.Gaps {
			fmt.Fprintf(&bl, "  [%s] %s\n", g.Field, truncateForStatus(g.Detail))
		}
	} else {
		bl.WriteString("完成判据缺口：无（判据面全满足；完成仍须显式 finish/finalize）\n")
	}
	if sp.Stop.ShouldPause {
		bl.WriteString("停止建议：建议暂停保留进度（")
		for i, tg := range sp.Stop.Triggers {
			if i > 0 {
				bl.WriteString("；")
			}
			fmt.Fprintf(&bl, "%s: %s", tg.Kind, truncateForStatus(tg.Detail))
		}
		bl.WriteString("）——落账归控制面（swarmmind_space_control pause），本投影不自动暂停。\n")
	}
	return bl.String(), nil
}

// spaceToolDefs node 空间工具清单（tools/list 装配；描述面向 agent 写「能做什么」，
// 与 REST 面同语义——差异如实写进描述）。
func spaceToolDefs() []toolDef {
	return []toolDef{
		{Name: "swarmmind_space_create", Description: "创建协作空间（编排主体仅 external 待领取空间，成员可空）：围绕一个目标建立固定成员线程的持续协作单元。返回空间 ID；后续流程：takeover 领取 → configure_members 配置成员 → start 显式启动 → swarmmind_space_dispatch 指名派发。request_id 可选幂等（同键同文返原空间）。一次性任务不需要空间，直接 swarmmind_submit", InputSchema: toolObj(map[string]any{
			"goal":       toolStr("空间目标（必填）"),
			"mode":       toolStr("调度方式（必填；编排主体仅 external——待领取外部空间；manual/builtin 为主人面）"),
			"acceptance": toolStr("可检查验收条件（可选）"),
			"budget":     toolObj(map[string]any{"max_messages": toolNum("消息条数上限（0=不限）"), "max_dispatch_rounds": toolNum("派发轮次上限（0=不限）"), "max_wall_seconds": toolNum("运行墙钟上限秒（0=不限）")}, "预算三分离定义（可选；缺省全不限）"),
			"request_id": toolStr("稳定创建请求 ID（可选）：同键同文返回原空间、异文 409"),
		}, "goal", "mode")},
		{Name: "swarmmind_space_list", Description: "列出本机协作空间：ID、目标、状态（待配置/待领取/可运行/运行/暂停/受阻/待核对/终态）、负责人与任期、在途数、暂停原因。发现可领取（待领取）或可续派的空间", InputSchema: toolObj(map[string]any{})},
		{Name: "swarmmind_space_status", Description: "查看协作空间详情：状态机位置、负责人/任期/租约、预算三分离用量、成员绑定、在途意图数与暂停/等待原因（调度条投影）。派发前读档拿 expected_version；待核对态禁止基于既有轮次下结论", InputSchema: toolObj(map[string]any{
			"space_id": toolStr("空间 ID（swarmmind_space_list 可查）"),
		}, "space_id")},
		{Name: "swarmmind_space_dispatch", Description: "向协作空间指名成员派发一轮执行（编排主体唯一执行入口；须当前驱动者形态——领取空间后持任期）。返回意图受理回执（202 异步，空间串行调度：同空间一次一棒，跨空间并行）；request_id 可选幂等（重放不重复执行不重复扣预算）。结果落账不自动触发下一轮——继续推进须再次显式派发", InputSchema: toolObj(map[string]any{
			"space_id":         toolStr("空间 ID"),
			"member":           toolStr("指名成员（须在空间内且已绑定原生会话）"),
			"expected_version": toolNum("读档空间版本（乐观锁；swarmmind_space_status 可查）"),
			"term":             toolNum("当前任期（驱动者形态；领取后 status 可查）"),
			"note":             toolStr("本轮派发说明（作为成员轮输入正文）"),
			"request_id":       toolStr("稳定受理请求 ID（可选）：同键同文回原意图（响应丢失重试安全）"),
		}, "space_id", "member", "expected_version")},
		{Name: "swarmmind_space_control", Description: "协作空间有限控制：pause（暂停新增派发）/resume（恢复）/ready（配置就绪）/start（显式启动）/configure_members（成员改组）/takeover（接管或领取待领取空间，expected_term 原子 CAS）/renew（续租约）。仅当前驱动者或主人形态可调（服务端逐动作授权裁决）；set_budget/policy/finalize/archive 为主人专属不受理；无任意命令/脚本执行能力", InputSchema: toolObj(map[string]any{
			"space_id":         toolStr("空间 ID"),
			"action":           toolStr("pause|resume|ready|start|configure_members|takeover|renew"),
			"expected_version": toolNum("读档空间版本（乐观锁）"),
			"term":             toolNum("当前任期（驱动者形态）"),
			"expected_term":    toolNum("takeover CAS 读档任期（未建立=0；同刻仅一方成功）"),
			"takeover_kind":    toolStr("takeover 目标形态（编排主体领取固定 external，可省略）"),
			"lease_seconds":    toolNum("租约时长秒（takeover/renew；0=默认 TTL）"),
			"members":          toolArr("configure_members 成员表（全量替换；元素 name/role/workspace/session_id）"),
			"confirm":          toolBool("resume 在在途未清时的显式确认（true 才放行续跑队列）"),
			"reason":           toolStr("原因/备注（可选，落审计）"),
			"request_id":       toolStr("稳定请求 ID（可选；重放返回原回执）"),
		}, "space_id", "action", "expected_version")},
		{Name: "swarmmind_space_messages", Description: "拉取协作空间消息（增量读）：since 传已见最大 seq 只返回之后的新消息，适合轮询补齐。消息只读不唤醒——发言与游标绝不触发任何执行；滚动窗口外更早消息不可再拉（响应标注裁剪位置）。派发结果与进度经 swarmmind_space_status 查看", InputSchema: toolObj(map[string]any{
			"space_id": toolStr("空间 ID"),
			"since":    toolNum("已见最大消息 seq（缺省 0=从头）"),
		}, "space_id")},
		{Name: "swarmmind_space_record", Description: "登记协作空间策略循环记录（数据写入只是事实登记，绝不等于执行——不派发不推进循环）：kind=candidate 登记成员 FINAL: 前缀候选结论（提出者须为空间成员，非法/越权拒绝不静默换人）；kind=review 按 op 登记版本（revision，内容/补丁 SHA-256）、复审结论（rereview：confirmed_fixed|still_open）、零发现复审（clean_review）、修复声明（claim_fix）、问题回应（reply）；kind=issue 登记审查问题（级别 must/should/info、文件:行、发现版本须已登记）；kind=evidence 登记测试执行证据（四层 cli_exit/protocol_parse/round_complete/work_accept；agent 自报来源标已证实直接拒绝；verified=true 编排面 403 拒绝——已证实标记只能由本机主人登记，人工核对证据走主人面）。新版本登记使旧复审结论失效", InputSchema: toolObj(map[string]any{
			"space_id":         toolStr("空间 ID"),
			"kind":             toolStr("记录族：candidate|review|issue|evidence"),
			"op":               toolStr("review 族操作：revision|rereview|clean_review|claim_fix|reply"),
			"member":           toolStr("candidate：提出者（空间成员名）"),
			"body":             toolStr("candidate：FINAL: 前缀发言原文"),
			"round":            toolNum("candidate：提出轮次"),
			"baseline":         toolStr("review/revision：代码基线（commit/工作区锚）"),
			"content_sha256":   toolStr("review/revision：内容 SHA-256 hex64"),
			"patch_sha256":     toolStr("review/revision：补丁 SHA-256 hex64（整树交付可省）"),
			"author":           toolStr("review/revision：编写成员"),
			"author_round":     toolNum("review/revision、claim_fix：编写/返工轮次"),
			"dependencies":     toolArr("review/revision：依赖产物引用（元素 kind/ref/sha256/note）"),
			"issue_id":         toolStr("review/rereview|claim_fix|reply：目标问题 ID"),
			"reviewer":         toolStr("review/rereview|clean_review、issue：审查/复审者（空间成员）"),
			"against_hash":     toolStr("review/rereview|clean_review：复审针对版本 SHA-256 hex64"),
			"result":           toolStr("review/rereview：confirmed_fixed|still_open"),
			"version_hash":     toolStr("review/claim_fix：修复声明版本 SHA-256 hex64"),
			"severity":         toolStr("issue：must|should|info"),
			"file":             toolStr("issue：文件"),
			"line":             toolNum("issue：行号"),
			"title":            toolStr("issue：标题（同 key 重报不换 ID）"),
			"found_in_version": toolStr("issue：发现版本 SHA-256 hex64（须已登记）"),
			"found_in_round":   toolNum("issue：发现时编写轮次"),
			"version":          toolStr("evidence：证据绑定版本 SHA-256 hex64（须已登记）"),
			"layer":            toolStr("evidence：cli_exit|protocol_parse|round_complete|work_accept"),
			"source":           toolStr("evidence：来源（agent_self_report 永不可证实；executor_receipt/protocol_receipt/host_observation/human_confirmation 可证实）"),
			"verified":         toolBool("evidence：已证实标记（自报来源传 true 即拒绝）"),
			"ref":              toolStr("evidence：回执号/命令 hash/人工主体"),
			"note":             toolStr("备注（可选）"),
		}, "space_id", "kind")},
		{Name: "swarmmind_space_records", Description: "拉取协作空间策略循环记录（增量读）：since 传已见最大 seq 只返回之后的新记录，适合轮询补齐。记录四族 candidate/review/issue/evidence（控制留痕如阶段变更如实标注 control）；只读不唤醒——读游标绝不触发执行；滚动裁剪区间以 pruned_before_seq 标注。候选/版本/问题全量状态用 swarmmind_space_strategy", InputSchema: toolObj(map[string]any{
			"space_id": toolStr("空间 ID"),
			"since":    toolNum("已见最大记录 seq（缺省 0=从头）"),
		}, "space_id")},
		{Name: "swarmmind_space_strategy", Description: "查看协作空间策略循环投影（只读）：循环阶段与暂停前阶段、候选及定版态（FINAL 登记不等于已交付）、问题及重开态（重报已确认问题即回归重开）、当前版本与完成判据缺口清单（如实展示，不等于可自动完成）、停止建议（预算耗尽/修复阈值/验收争议——落账归控制面）。无策略数据的空间返回如实空态", InputSchema: toolObj(map[string]any{
			"space_id": toolStr("空间 ID（swarmmind_space_list 可查）"),
		}, "space_id")},
	}
}
