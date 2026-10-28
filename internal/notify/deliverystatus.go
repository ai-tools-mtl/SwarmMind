// 渠道最近投递状态（FE-POLISH P0-3「告警通道失效不告警」）：投递失败此前只进
// 日志，唯一告警通道失效用户无从发现。本文件给每渠道记录「最近成功投递时刻 /
// 最近失败时刻+原因摘要」两个事实（内存态），随渠道列表响应经既有端点暴露，
// 双端渠道卡据此展示并在失败时前置修复入口。
//
// 如实边界：内存态自本进程启动起记，重启清零，不重建投递历史、不做分析页
//（FE-POLISH P0-3 裁决口径）；原因经 sanitizeErrMsg 脱敏（只到 scheme://host
// 与状态码/底层原因，path/query/userinfo 不外带），与日志同一红线。
package notify

import (
	"time"
)

// DeliveryStatus 单渠道最近投递事实（零值=尚无投递记录，不冒充成功）。
type DeliveryStatus struct {
	LastOKAt       time.Time
	LastFailAt     time.Time
	LastFailReason string
}

// ChannelView 渠道列表响应体单条：脱敏投影（Channel.Masked）+ 最近投递状态。
// hub /api/v1/notify/channels 与 node /api/v1/local/notify/channels 共用同一
// 响应形态（双端渠道卡同构展示）。指针字段 omitempty：无记录时字段整体缺席
//（time.Time 零值不会被 omitempty 省略，必须用指针）。
type ChannelView struct {
	Channel
	LastOKAt   *time.Time `json:"last_ok_at,omitempty"`
	LastFailAt *time.Time `json:"last_fail_at,omitempty"`
	// LastFailReason 最近失败原因摘要（已脱敏，同日志口径）。
	LastFailReason string `json:"last_fail_reason,omitempty"`
}

// deliveryOK 记录一次投递成功（含渠道「发测试」成功——测试成功同样是「这条
// 渠道此刻可达」的证据）。并发安全：投递在独立 goroutine 进行。
func (d *Dispatcher) deliveryOK(id string) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	if d.deliv == nil {
		d.deliv = map[string]DeliveryStatus{}
	}
	st := d.deliv[id]
	st.LastOKAt = time.Now().UTC()
	d.deliv[id] = st
}

// deliveryFail 记录一次投递最终失败（重试穷尽或单次测试失败），reason 为脱敏
// 后的人话摘要。
func (d *Dispatcher) deliveryFail(id, reason string) {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	if d.deliv == nil {
		d.deliv = map[string]DeliveryStatus{}
	}
	st := d.deliv[id]
	st.LastFailAt = time.Now().UTC()
	st.LastFailReason = reason
	d.deliv[id] = st
}

// DeliveryStatuses 全渠道投递状态快照（副本，调用方可自由读取）。
func (d *Dispatcher) DeliveryStatuses() map[string]DeliveryStatus {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	out := make(map[string]DeliveryStatus, len(d.deliv))
	for k, v := range d.deliv {
		out[k] = v
	}
	return out
}

// ListView 渠道列表（脱敏）与最近投递状态合并视图：管理面渠道列表端点的响应体
// 数据源。能复用既有列表响应就不新增端点（FE-POLISH P0-3 实施约束）。
func (d *Dispatcher) ListView() []ChannelView {
	chs := d.store.List()
	statuses := d.DeliveryStatuses()
	out := make([]ChannelView, len(chs))
	for i, c := range chs {
		out[i] = ChannelView{Channel: c.Masked()}
		st, ok := statuses[c.ID]
		if !ok {
			continue
		}
		if !st.LastOKAt.IsZero() {
			t := st.LastOKAt
			out[i].LastOKAt = &t
		}
		if !st.LastFailAt.IsZero() {
			t := st.LastFailAt
			out[i].LastFailAt = &t
			out[i].LastFailReason = st.LastFailReason
		}
	}
	return out
}
