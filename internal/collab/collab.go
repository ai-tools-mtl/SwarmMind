// Package collab 实现空间协作内核模型（W04-01，docs/tasks/v3/W04-01-rfc.md）：
// Space/Member/DispatchIntent/Leader 模型与九态状态机、Owner/term CAS 接管、
// 负责人租约、成员一原生线程绑定、执行意图 journal/outbox 存储、预算三分离
// 扣账、逐动作授权纯判定与 W05-01 策略接口面。
//
// 定位（W04 任务书「抽出无模型、动作封闭的协作服务」）：本包只含模型与确定性
// 程序——不读 HTTP、不执行 agent、不注册任何终态回调自动推进；执行/调度接线
// 归 W04-02（Node 本地 adapter）与 W07（Hub 远程 adapter）。旧 Group/Work
// （internal/hub/group、internal/hub/core）保持原语义独立运行，与本包零耦合
// （兼容映射见 RFC §五，不迁移不冒认）。
//
// 存储（RFC §二）：单文件原子快照 spaces.json（0600，独占 tmp + fsync + rename
// + 目录 fsync 尽力而为，时间一律 UTC），操作回执表内嵌快照承担 journal（操作
// ID 幂等：同 ID 同文回放原回执零副作用、同 ID 异文冲突），pending 意图承担
// outbox（先于执行持久化；进程恢复按实物对账，绝不凭回调自动重跑）。加载失败
// 拒绝带病启动（同 W02-01 mcp_access 口径）。
//
// 授权（RFC §三）：主体（Subject）由调用方传入服务端已验证身份，本包不读
// HTTP、不采信请求自报；逐动作授权表为纯判定函数。所有写操作携 request_id +
// expected_version，不因角色跳过状态/版本校验。
package collab

import (
	"fmt"
	"time"
)

// 常量（RFC §一/§二）。
const (
	// SpaceIDPrefix 空间 ID 前缀（旧群 g- 独立命名空间，互不引用）。
	SpaceIDPrefix = "sp-"

	// fileName 空间状态快照文件名（相对数据目录）。
	fileName = "spaces.json"

	// stateVersion 快照结构版本（读到未知版本拒绝启动，宁拒启不瞎门）。
	stateVersion = 1

	// DefaultLeaseTTL 负责人租约默认时长（external/builtin；manual 不设租约）。
	DefaultLeaseTTL = 5 * time.Minute
	// MaxLeaseSeconds 单次授予/续约的租约时长上限（秒）——防误配置把租约拉成
	// 永不过期（ fencing 语义要求租约可过期）。
	MaxLeaseSeconds = 86400
	// DefaultMaxReceipts 操作回执滚动上限：超限按 RecordedAt 淘汰最旧。淘汰只
	// 影响「极旧请求的重放命中」，不影响在途意图（意图独立持久）。
	DefaultMaxReceipts = 4096
	// MaxEventsPerSpace 单空间审计事件滚动上限（超限丢最旧；审计主链在意图与
	// 空间字段，事件表是解释面）。
	MaxEventsPerSpace = 200
	// MaxMessagesPerSpace 单空间消息滚动上限（超限丢最旧；增量读响应标注裁剪
	// 位置 pruned_before_seq，同旧群信箱约定——since 游标只对窗口内消息有效）。
	MaxMessagesPerSpace = 1000
	// MaxMessageText 单条消息正文上限（字节，截断；防快照膨胀）。
	MaxMessageText = 16 << 10
	// MaxBodyNote 控制备注/原因字段上限（字节），防快照膨胀。
	MaxNoteBytes = 4 << 10
)

// 错误码（哨兵变量见下；HTTP 投影：localapi spaces.go）。
const (
	CodeInvalid                 = "invalid_request"
	CodeNotFound                = "space_not_found"
	CodeIllegalTransition       = "illegal_transition"
	CodeVersionConflict         = "version_conflict"
	CodeRequestConflict         = "request_conflict"
	CodeForbidden               = "forbidden"
	CodeLeaseExpired            = "lease_expired"
	CodeTermConflict            = "term_conflict"
	CodeMemberInFlight          = "member_in_flight"
	CodeExecutionInFlight       = "execution_in_flight"
	CodeBudgetExhausted         = "budget_exhausted"
	CodeFinishPolicyUnavailable = "finish_policy_unavailable"
	CodeCompletionNotSatisfied  = "completion_not_satisfied"
	CodeSessionBoundElsewhere   = "session_bound_elsewhere"
	CodeSpaceDispatchRequired   = "space_dispatch_required"
	CodeMemberNotFound          = "member_not_found"
	CodeNoMembers               = "no_members"
	CodeStoreClosed             = "store_closed"
)

// 错误哨兵（errors.Is 判定；同码即同类——调用方按 Code 投影 HTTP）。
var (
	// ErrInvalid 参数非法（缺字段/越界/取值非法）。
	ErrInvalid = &Error{Code: CodeInvalid, msg: "参数非法"}
	// ErrNotFound 空间不存在。
	ErrNotFound = &Error{Code: CodeNotFound, msg: "空间不存在"}
	// ErrIllegalTransition 状态机不允许的转换（表外路径一律不存在）。
	ErrIllegalTransition = &Error{Code: CodeIllegalTransition, msg: "非法状态转换"}
	// ErrVersionConflict expected_version 与空间现版本不符（读档已过期，刷新重试）。
	ErrVersionConflict = &Error{Code: CodeVersionConflict, msg: "空间版本不符（预期版本与现版本不一致）"}
	// ErrRequestConflict 同 request_id 已被不同内容占用（同键异文）。
	ErrRequestConflict = &Error{Code: CodeRequestConflict, msg: "request_id 已被不同内容占用"}
	// ErrForbidden 主体无权执行该动作（逐动作授权表拒绝）。
	ErrForbidden = &Error{Code: CodeForbidden, msg: "主体无权执行该动作"}
	// ErrLeaseExpired 租约已过期：不能 renew 复活，接管由主人显式确认。
	ErrLeaseExpired = &Error{Code: CodeLeaseExpired, msg: "租约已过期（过期不能续约复活；接管由主人显式确认）"}
	// ErrTermConflict 任期 CAS 不符：同刻仅一方接管成功 / 旧任期写入被拒。
	ErrTermConflict = &Error{Code: CodeTermConflict, msg: "任期不符（expected_term 与当前任期不一致）"}
	// ErrMemberInFlight 涉及成员存在在途执行（先收束或核对）。
	ErrMemberInFlight = &Error{Code: CodeMemberInFlight, msg: "涉及成员存在在途执行（先收束或核对）"}
	// ErrExecutionInFlight 空间存在未收束执行（finish/finalize/archive 前置）。
	ErrExecutionInFlight = &Error{Code: CodeExecutionInFlight, msg: "空间存在未收束执行（先等待收束或核对）"}
	// ErrBudgetExhausted 对应维度预算已用尽（三计数分离，各自拒绝）。
	ErrBudgetExhausted = &Error{Code: CodeBudgetExhausted, msg: "预算已用尽"}
	// ErrFinishPolicyUnavailable 完成判据策略未装配（hooks 为 nil）：完成类动作
	// 一律拒绝，W04 只开放主人 finalize→abandoned。
	ErrFinishPolicyUnavailable = &Error{Code: CodeFinishPolicyUnavailable, msg: "完成判据策略未装配（完成须通过 W05 策略验证；本阶段仅支持主人放弃定版）"}
	// ErrCompletionNotSatisfied 策略判据未满足（等待人工裁决或材料补齐）。
	ErrCompletionNotSatisfied = &Error{Code: CodeCompletionNotSatisfied, msg: "完成判据未满足（等待人工裁决或材料补齐）"}
	// ErrSessionBoundElsewhere 会话已绑定其他空间（一原生线程唯一归属）。
	ErrSessionBoundElsewhere = &Error{Code: CodeSessionBoundElsewhere, msg: "会话已绑定其他协作空间（一原生线程唯一归属空间）"}
	// ErrSpaceDispatchRequired 会话已绑定协作空间：普通续轮必须改经空间派发
	// （W04-01 固定契约方案 A，RFC §1.4；W04-02 接线 sessionop）。
	ErrSpaceDispatchRequired = &Error{Code: CodeSpaceDispatchRequired, msg: "该会话已绑定协作空间，请经空间派发（space dispatch）推进，普通续轮被拒绝"}
	// ErrMemberNotFound 成员不在空间内。
	ErrMemberNotFound = &Error{Code: CodeMemberNotFound, msg: "成员不在空间内"}
	// ErrNoMembers 空间未配置成员（start/dispatch 前置）。
	ErrNoMembers = &Error{Code: CodeNoMembers, msg: "空间未配置成员（先 configure_members）"}
	// ErrClosed 存储已关闭。
	ErrClosed = &Error{Code: CodeStoreClosed, msg: "存储已关闭"}
)

// Error collab 业务错误（Code 供 HTTP 投影；同码哨兵 errors.Is 判定成立）。
type Error struct {
	Code string
	msg  string
}

func (e *Error) Error() string { return "collab: " + e.msg }

// Is 同码即同类（errors.Is(err, ErrXxx) 按 Code 判定，支持包装错误）。
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t != nil && t.Code == e.Code
}

// errf 构造带上下文的业务错误（保留哨兵 Code）。
func errf(sentinel *Error, format string, args ...any) *Error {
	e := *sentinel
	if format != "" {
		e.msg = sentinel.msg + ": " + fmt.Sprintf(format, args...)
	}
	return &e
}

// Logger 运维日志接口（*log.Logger 直接兼容）。
type Logger interface {
	Printf(format string, args ...any)
}

// nopLogger 未装配日志时的静默兜底。
type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

// nowUTC 快照默认时钟（测试经 Options.Now 注入 fake 时钟）。
func nowUTC() time.Time { return time.Now().UTC() }
