// spaces.go 协作空间 REST 最小面（W04-01，docs/tasks/v3/W04-01-rfc.md §六）：
//   - POST /api/v1/spaces                创建（request_id 缺省自动生成）
//   - GET  /api/v1/spaces                列表
//   - GET  /api/v1/spaces/{id}           详情（全量视图 + 在途投影）
//   - POST /api/v1/spaces/{id}/control   控制骨架（逐动作授权表：pause/resume/
//     ready/start/configure_members/set_budget/
//     policy/takeover/renew/finish/finalize/archive）
//
// 鉴权：本批全部 authLocal（本机主人面；prime_token 不放行——编排主体派发/
// 消息面随 W04-02 dispatch/messages 同批开线，模型层 driver/orchestrate 判定
// 已由 collab 单测覆盖）。主体固定 owner（local_token 即本机主人，服务端门已
// 验证，不采信请求自报）。messages/dispatches/budget/policy/finalize/archive
// 独立端点暂不落：budget/policy/finalize/archive 能力已并入 control 骨架动作；
// messages/dispatches 归 W04-02（RFC §六范围裁定）。MCP 工具按豁免⑯随 W04-03。
//
// 错误投影：collab Error.Code → HTTP（见 writeSpaceErr）；存储未装配 503
// spaces_unavailable（与 sessions 降级同形态，诚实不虚构）。
package localapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mtl802/swarmmind/internal/collab"
)

// spaceReady collab 存储装配守卫（未装配 503 诚实降级）。
func (s *Server) spaceReady(w http.ResponseWriter) bool {
	if s.cfg.Collab == nil {
		writeErr(w, http.StatusServiceUnavailable, "spaces_unavailable",
			"空间协作存储未装配（本机未启用 spaces；升级或装配后可用）")
		return false
	}
	return true
}

// spaceOwnerSubject 本机主人主体（authLocal 门已验证 local_token；collab 不读
// HTTP，主体由调用方传入——AGENTS 审计主体纪律）。
func spaceOwnerSubject() collab.Subject {
	return collab.Subject{Name: "owner", Class: collab.ClassOwner}
}

// handleSpaceCreate POST /api/v1/spaces：创建空间。request_id 缺省自动生成
// （自动生成不承担重放回执）；显式提供时同键同文 200+dup 返回原空间、异文
// 409 request_conflict（与 hub 提交幂等同心智）。
func (s *Server) handleSpaceCreate(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) {
		return
	}
	var req collab.CreateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 CreateRequest JSON: "+err.Error())
		return
	}
	sp, dup, err := s.cfg.Collab.CreateSpace(req, spaceOwnerSubject())
	if err != nil {
		writeSpaceErr(w, err)
		return
	}
	code := http.StatusCreated
	if dup {
		code = http.StatusOK
	}
	writeJSON(w, code, map[string]any{"space": sp, "dup": dup})
}

// handleSpaceList GET /api/v1/spaces：全量列表（创建序）。
func (s *Server) handleSpaceList(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) {
		return
	}
	spaces := s.cfg.Collab.Spaces()
	writeJSON(w, http.StatusOK, map[string]any{"spaces": spaces, "total": len(spaces)})
}

// handleSpaceGet GET /api/v1/spaces/{id}：详情（视图含成员/负责人/预算用量/
// 策略/在途投影）。
func (s *Server) handleSpaceGet(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) {
		return
	}
	view, ok := s.cfg.Collab.SpaceView(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, collab.CodeNotFound, "空间不存在: "+r.PathValue("id"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"space": view})
}

// handleSpaceControl POST /api/v1/spaces/{id}/control：控制骨架（逐动作授权表）。
// request_id 必填（写操作稳定请求标识，重放返回原回执）；expected_version 必填
// （乐观锁；takeover 另有 expected_term CAS，term 裁决先于版本复核）。
func (s *Server) handleSpaceControl(w http.ResponseWriter, r *http.Request) {
	if !s.spaceReady(w) {
		return
	}
	var req collab.ControlRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request", "请求体须为 ControlRequest JSON: "+err.Error())
		return
	}
	res, dup, err := s.cfg.Collab.Control(r.PathValue("id"), req, spaceOwnerSubject())
	if err != nil {
		writeSpaceErr(w, err)
		return
	}
	// 控制动作统一 200（语义回执在体：applied=是否实际变更、dup=操作 ID 重放命中）。
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "dup": dup})
}

// writeSpaceErr collab 错误 → HTTP 投影（RFC §一错误码表；未知码归 500）。
func writeSpaceErr(w http.ResponseWriter, err error) {
	var ce *collab.Error
	if !errors.As(err, &ce) {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	code := http.StatusInternalServerError
	switch ce.Code {
	case collab.CodeInvalid:
		code = http.StatusBadRequest
	case collab.CodeNotFound:
		code = http.StatusNotFound
	case collab.CodeForbidden:
		code = http.StatusForbidden
	case collab.CodeIllegalTransition, collab.CodeVersionConflict, collab.CodeRequestConflict,
		collab.CodeLeaseExpired, collab.CodeTermConflict, collab.CodeMemberInFlight,
		collab.CodeExecutionInFlight, collab.CodeBudgetExhausted,
		collab.CodeSessionBoundElsewhere, collab.CodeSpaceDispatchRequired,
		collab.CodeMemberNotFound, collab.CodeNoMembers:
		code = http.StatusConflict
	case collab.CodeFinishPolicyUnavailable:
		code = http.StatusServiceUnavailable
	}
	writeErr(w, code, ce.Code, stripCollabPrefix(err.Error()))
}

// stripCollabPrefix 错误文案去包前缀（collab: 已由错误码承担；人话正文直接透出）。
func stripCollabPrefix(msg string) string {
	for _, p := range []string{"collab: "} {
		if len(msg) > len(p) && msg[:len(p)] == p {
			return msg[len(p):]
		}
	}
	return msg
}
