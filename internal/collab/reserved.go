// reserved.go collab 全局回执表（Ops，全空间共享一表）的内部 opID 命名空间单源
// （W07-02 起 hub api 与 node localapi 共用——两端同表同清单，消除第二套保留词典
// 漂移；清单枚举锚 = internal/collab/ops.go 全部 .apply( 内部键形态，终审必修5）：
//   - "boot-"     start 后循环阶段引导（装配层 bootstrapCycleAfterStart，落
//     control:set_phase 族回执）
//   - "converge:" 派发意图收敛（collab ConvergeIntent，converge:<rid>:<outcome>）
//   - "attach:"   派发任务关联（collab AttachDispatchTask，attach:<rid>）
//   - "syspause:" 系统自动暂停（collab SystemPause，syspause:<space>:…:v<t>）
//   - "syspausex:"租约过期暂停（collab PauseExpired，syspausex:<space>:…）
//
// 客户端可控 request_id 的写入口（create/control/dispatch/messages/sugar）统一
// 过 RequestIDReserved 门：回执表跨空间共享，他空间撞号即可占住内部动作的回执键
// （create 携 boot-S-drafting → S 的 start 引导 conflict → 阶段空卡死）。命中即
// 400 reserved_request_id（人话列明保留清单），先于一切校验与事务（零副作用不占
// 回执）。
package collab

import "strings"

// ReservedRequestIDPrefixes 内部 opID 命名空间保留清单（单源；新增内部 apply 键
// 形态时同批补录——grep 实据枚举 ops.go）。
var ReservedRequestIDPrefixes = []string{"boot-", "converge:", "attach:", "syspause:", "syspausex:"}

// ReservedRequestIDMessage 保留前缀拒绝人话（列明清单，两端同文）。
const ReservedRequestIDMessage = "request_id 以系统保留前缀开头（内部命名空间：boot- / converge: / attach: / syspause: / syspausex:" +
	"——回执表全局共享，撞号会占住内部动作回执使其冲突卡死）：请换用其他请求标识"

// RequestIDReserved 客户端 request_id 是否撞内部保留命名空间（true=拒绝；调用方
// 在解码后、一切校验与事务之前先过本门）。
func RequestIDReserved(id string) bool {
	trimmed := strings.TrimSpace(id)
	for _, p := range ReservedRequestIDPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return false
}
