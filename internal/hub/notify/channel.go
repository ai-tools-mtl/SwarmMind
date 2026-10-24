// 通知渠道（P4③ 控制台化升级）：类型枚举、事件订阅模型、凭证脱敏与准入校验。
//
// 四类首批渠道：feishu（飞书自定义机器人）、email（SMTP）、webhook（通用
// JSON POST）、inbox（站内=控制台通知中心）；sms 在枚举中占位但暂不开放
// （需接入服务商，API 与 UI 均拒绝）。
//
// 事件订阅（events，三选多，默认全订阅）：task_final=全部终态；
// task_failed=仅失败；task_needs_review=仅等你确认。空列表=全订阅。
package notify

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// 渠道类型（首批四类 + sms 占位）。
const (
	TypeFeishu  = "feishu"
	TypeEmail   = "email"
	TypeWebhook = "webhook"
	TypeInbox   = "inbox"
	// TypeSMS 占位类型：UI 禁用灰显（「需接入服务商，暂未开放」），API 创建/
	// 更新一律拒绝——宁可明确报错，不收一条永远发不出去的渠道配置。
	TypeSMS = "sms"
)

// 事件订阅值（events 三选多；空=全订阅）。
const (
	EventTaskFinal    = "task_final"        // 全部终态（完成/失败/等你确认/取消）
	EventTaskFailed   = "task_failed"       // 仅失败
	EventNeedsReview  = "task_needs_review" // 仅等你确认
)

// ChannelTypes 类型枚举（含 sms 占位；UI 据此渲染下拉并对 sms 禁用）。
var ChannelTypes = []string{TypeFeishu, TypeEmail, TypeWebhook, TypeInbox, TypeSMS}

// EventValues 合法事件订阅值。
var EventValues = []string{EventTaskFinal, EventTaskFailed, EventNeedsReview}

// MaskPlaceholder 凭证脱敏占位：GET 响应中敏感字段替换为该值；PUT 更新若某
// 字段传回该值则保留原值不覆盖（GitHub 式「留空即不改」语义）。
const MaskPlaceholder = "******"

// Channel 一条通知渠道配置（持久化形态，notify_channels.json）。
type Channel struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Name      string            `json:"name"`
	Enabled   bool              `json:"enabled"`
	Events    []string          `json:"events"`            // 空=全订阅
	Secrets   map[string]string `json:"secrets"`           // 按类型：见 ValidateSecrets
	CreatedAt time.Time         `json:"created_at"`
}

// ChannelIDPrefix 渠道 ID 前缀（自生成 c-xxxx，用户不可指定）。
const ChannelIDPrefix = "c-"

// newChannelID 生成 64bit 随机渠道 ID（hex）。rand 失败 panic 级罕见，返回
// 错误由调用方决定（创建路径 500）。
func newChannelID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return ChannelIDPrefix + hex.EncodeToString(b[:]), nil
}

// subscribed 事件订阅判定：空列表=全订阅；task_final 收全部终态；task_failed
// 仅 failed；task_needs_review 仅 needs_review。未知事件值不匹配任何状态
// （准入校验已拒，防御性收窄）。
func subscribed(events []string, status protocol.TaskStatus) bool {
	if len(events) == 0 {
		return true
	}
	for _, e := range events {
		switch e {
		case EventTaskFinal:
			return true
		case EventTaskFailed:
			if status == protocol.StatusFailed {
				return true
			}
		case EventNeedsReview:
			if status == protocol.StatusNeedsReview {
				return true
			}
		}
	}
	return false
}

// normalizeEvents 事件列表规范化：TrimSpace、去重、保序；返回空切片表示全订阅。
// 非法值报错（准入校验，不静默丢弃——拼错事件名应尽快失败）。
func normalizeEvents(events []string) ([]string, error) {
	out := make([]string, 0, len(events))
	seen := make(map[string]bool, len(events))
	for _, e := range events {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if seen[e] {
			continue
		}
		valid := false
		for _, v := range EventValues {
			if e == v {
				valid = true
				break
			}
		}
		if !valid {
			return nil, fmt.Errorf("未知事件订阅 %q（可选 task_final|task_failed|task_needs_review，留空=全部）", e)
		}
		seen[e] = true
		out = append(out, e)
	}
	return out, nil
}

// validateChannel 渠道准入校验（创建与更新共用）：类型枚举、名称、事件订阅、
// 按类型必填凭证。name 上限 64 字符（rune 计）防滥用。
func validateChannel(typ, name string, events []string, secrets map[string]string) error {
	switch typ {
	case TypeFeishu, TypeEmail, TypeWebhook, TypeInbox:
	case TypeSMS:
		return fmt.Errorf("sms 渠道需接入服务商，暂未开放")
	default:
		return fmt.Errorf("未知渠道类型 %q", typ)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("渠道名称必填")
	}
	if len([]rune(name)) > 64 {
		return fmt.Errorf("渠道名称过长（上限 64 字符）")
	}
	if _, err := normalizeEvents(events); err != nil {
		return err
	}
	return validateSecrets(typ, secrets)
}

// validateSecrets 按类型校验必填凭证：
//   - feishu：webhook_url 必填且须为合法 http(s) URL（飞书机器人地址）；
//   - email：host/port/username/password/from/to 六件套必填，port 为 1-65535 数字；
//   - webhook：url 必填且须为合法 http(s) URL；headers 可选（header.<名> 键）；
//   - inbox：无凭证。
func validateSecrets(typ string, secrets map[string]string) error {
	switch typ {
	case TypeFeishu:
		return requireHTTPURL(secrets, "webhook_url")
	case TypeWebhook:
		if err := requireHTTPURL(secrets, "url"); err != nil {
			return err
		}
		for k, v := range secrets {
			if !isHeaderKey(k) || v == "" {
				continue
			}
			name, ok := headerName(k)
			if !ok || strings.ContainsAny(name, " \t\r\n:") {
				return fmt.Errorf("header 键 %q 非法（应为 header.<HTTP头名>）", k)
			}
			if strings.ContainsAny(v, "\r\n") {
				// HTTP 头值本就禁止换行；放进来会在编辑抽屉 textarea 里裂成多行、
				// 回存时裂出幽灵头
				return fmt.Errorf("header %s 的值含非法换行符", name)
			}
		}
	case TypeEmail:
		for _, k := range []string{"host", "port", "username", "password", "from", "to"} {
			if strings.TrimSpace(secrets[k]) == "" {
				return fmt.Errorf("email 渠道缺少必填项 %s", k)
			}
		}
		port := strings.TrimSpace(secrets["port"])
		n := 0
		for _, r := range port {
			if r < '0' || r > '9' {
				return fmt.Errorf("email 渠道 port 须为数字（1-65535），got %q", port)
			}
			n = n*10 + int(r-'0')
			if n > 65535 {
				return fmt.Errorf("email 渠道 port 超出范围（1-65535）")
			}
		}
		if n < 1 {
			return fmt.Errorf("email 渠道 port 超出范围（1-65535）")
		}
	case TypeInbox:
		// 无凭证
	}
	return nil
}

// requireHTTPURL 校验指定键存在且为合法 http(s) URL。
func requireHTTPURL(secrets map[string]string, key string) error {
	raw := strings.TrimSpace(secrets[key])
	if raw == "" {
		return fmt.Errorf("缺少必填项 %s", key)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s 须为合法 http(s) 地址", key)
	}
	return nil
}

// isHeaderKey 报告 secrets 键是否为 webhook 自定义头（header.<名> 形态）。
func isHeaderKey(k string) bool { return strings.HasPrefix(k, "header.") }

// headerName 取 header.<名> 中的 HTTP 头名。
func headerName(k string) (string, bool) {
	name := strings.TrimPrefix(k, "header.")
	return name, name != ""
}

// sensitiveSecretKey 报告凭证键是否口令类（GET 脱敏对象）。自定义头不走此判定
// （头的值一律视为凭证脱敏，见 Masked）；url/webhook_url 是配置地址，按设计稿
// 口径明文展示（编辑抽屉需可见可核对），内嵌登录凭证时由 Masked 整体打码。
func sensitiveSecretKey(k string) bool {
	lk := strings.ToLower(k)
	return strings.Contains(lk, "password") || strings.Contains(lk, "passwd") ||
		strings.Contains(lk, "secret")
}

// secretURLHasUserinfo 报告地址是否内嵌登录凭证（user:pass@host）。这类地址
// 无法「只打码密码部分还保证 PUT 回传可用」，读响应中整体替换为占位，走既有
// 「传回 ****** 即不改」通道。
func secretURLHasUserinfo(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.User != nil
}

// Masked 返回脱敏副本（列表/创建/编辑三类读响应统一出口，明文凭证不出管理面
// 读路径）：
//   - 口令类字段（password/passwd/secret）→ ******；
//   - 自定义头的值一律 → ******（头即凭证：Authorization、X-API-Key、签名……
//     无法按头名穷举）；
//   - 地址内嵌登录凭证（user:pass@host）→ 整串 ******。
//
// PUT 传回占位即保留原值（GitHub 式语义，见 mergeSecrets）。原对象不被改动。
func (c Channel) Masked() Channel {
	cp := c.clone()
	for k, v := range cp.Secrets {
		if v == "" {
			continue
		}
		if isHeaderKey(k) || sensitiveSecretKey(k) || secretURLHasUserinfo(v) {
			cp.Secrets[k] = MaskPlaceholder
		}
	}
	return cp
}

// clone 深拷贝（Events/Secrets 独立底层数组，不与已发布状态共享）。
func (c Channel) clone() Channel {
	cp := c
	cp.Events = append([]string(nil), c.Events...)
	cp.Secrets = make(map[string]string, len(c.Secrets))
	for k, v := range c.Secrets {
		cp.Secrets[k] = v
	}
	return cp
}

// applyUpdate 把更新请求合并进现有渠道（PUT 语义核心）：
//   - name 非空（Trim 后）才更新；type 非空且变化才切换（凭证按合并结果重校验）；
//   - enabled/events 由指针区分「未提供（保留）」与「提供（覆盖，events 可为
//     空数组=全订阅）」；
//   - secrets 未提供（nil）→ 全部保留（部分更新如「启用开关」只传 enabled）；
//     提供时 webhook 自定义头以提交内容为准（提交里消失的键=删除，前端删头行
//     即生效），其余键 merge：值为 ****** 占位 → 保留原值（原值不存在则丢弃
//     该键——占位无原值可保，等于未填，交由校验报缺）；空串 → 显式清除该键；
//     其余 → 覆盖。
func applyUpdate(cur *Channel, typ, name string, enabled *bool, events []string, haveEvents bool, secrets map[string]string) error {
	newTyp := cur.Type
	if strings.TrimSpace(typ) != "" {
		newTyp = strings.TrimSpace(typ)
	}
	newName := cur.Name
	if strings.TrimSpace(name) != "" {
		newName = strings.TrimSpace(name)
	}
	newEnabled := cur.Enabled
	if enabled != nil {
		newEnabled = *enabled
	}
	newEvents := cur.Events
	if haveEvents {
		norm, err := normalizeEvents(events)
		if err != nil {
			return err
		}
		newEvents = norm
	}
	newSecrets := mergeSecrets(cur, newTyp, secrets)
	if err := validateChannel(newTyp, newName, newEvents, newSecrets); err != nil {
		return err
	}
	cur.Type, cur.Name, cur.Enabled, cur.Events, cur.Secrets = newTyp, newName, newEnabled, newEvents, newSecrets
	return nil
}

// mergeSecrets 凭证合并（占位保留语义见 applyUpdate 注释）。
//   - secrets 未提供（nil）→ 全部保留（启用开关等部分更新不碰凭证）；
//   - secrets 提供时，webhook 自定义头（header.*）以提交内容为准：提交里消失
//     的键即删除（前端删头行必须真实生效，不保留旧值）；其余键维持 merge
//     （占位保留原值/空串显式清除/新值覆盖），保证编辑单字段不丢其余凭证。
//
// 类型切换时旧凭证全部作废：占位键无处可保即丢弃，新类型缺什么校验会明确报出来。
func mergeSecrets(cur *Channel, newTyp string, updates map[string]string) map[string]string {
	base := map[string]string{}
	sameType := newTyp == cur.Type
	if sameType {
		for k, v := range cur.Secrets {
			base[k] = v
		}
	}
	if updates == nil {
		return base
	}
	if sameType {
		for k := range base {
			if isHeaderKey(k) {
				if _, ok := updates[k]; !ok {
					delete(base, k) // 提交里消失的 header 键 = 删除
				}
			}
		}
	}
	for k, v := range updates {
		switch {
		case v == MaskPlaceholder:
			if old, ok := base[k]; ok && old != "" {
				base[k] = old // 占位：保留原值
			} else {
				delete(base, k) // 无原值可保：丢弃，校验按缺项报错
			}
		default:
			if strings.TrimSpace(v) == "" {
				delete(base, k) // 空串：显式清除
			} else {
				base[k] = v
			}
		}
	}
	return base
}
