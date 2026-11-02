// httpstatus.go collab 错误码 → HTTP 状态映射单源（W07-02）：node localapi 与
// hub api 的 spaces REST 面共用同一投影表——两端「同码即同状态」由本函数锁定，
// 消除第二套映射词典漂移。未知码归 500（未知错误不冒充业务拒绝）。
package collab

import "net/http"

// HTTPStatus collab 业务错误码 → HTTP 状态（writeSpaceErr 两端共用的唯一真相；
// 判定按 Code 字符串——调用方以 errors.As 取 *Error 后传 Code，或直接传码）。
//   - 400 invalid_request（参数/词形非法）；
//   - 404 space_not_found；
//   - 403 forbidden（逐动作授权拒绝）；
//   - 409 状态机/版本/CAS/租约/在途/预算/绑定/成员族——可补齐的业务拒绝，非服务
//     端故障（completion_not_satisfied 同族：判据未满足=可补齐，W04-01 RFC §四）；
//   - 503 finish_policy_unavailable（判据策略未装配——装配缺失诚实降级）。
func HTTPStatus(code string) int {
	switch code {
	case CodeInvalid:
		return http.StatusBadRequest
	case CodeNotFound:
		return http.StatusNotFound
	case CodeForbidden:
		return http.StatusForbidden
	case CodeIllegalTransition, CodeVersionConflict, CodeRequestConflict,
		CodeLeaseExpired, CodeTermConflict, CodeMemberInFlight,
		CodeExecutionInFlight, CodeBudgetExhausted,
		CodeSessionBoundElsewhere, CodeSpaceDispatchRequired,
		CodeMemberNotFound, CodeNoMembers,
		CodeCompletionNotSatisfied:
		// completion_not_satisfied 409 语义（W04-01 RFC §四）：判据未满足=可补齐的
		// 业务拒绝（缺口清单人话透出，非服务端故障）。
		return http.StatusConflict
	case CodeFinishPolicyUnavailable:
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}
