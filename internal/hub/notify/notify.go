// Package notify 任务终态 IM 通知（PRODUCT-DIRECTION v2 P4③）：任务首次落终态
// 时向 notify.webhook_url 异步 POST 一条通知——raw=结构化 JSON，feishu=飞书
// 自定义机器人文本消息。
//
// 可靠性边界（设计定稿）：发送在独立 goroutine 进行，不阻塞状态机持久化；
// 单次 5s 超时；失败重试 3 次指数退避（1s/2s/4s）；最终失败只记日志，绝不
// 影响任务终态与主流程。去重不在本包——触发点（core 终态旁路）只在非终态→
// 终态首次转换时调用一次（重复结果/冲突/清扫重扫均不触发），Sweep/重放天然
// 不产生重复通知。
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// 可靠性参数（设计定稿）：单次请求 5s 超时；失败重试 3 次，退避 1s/2s/4s。
const (
	defaultTimeout = 5 * time.Second
	retryBackoff   = 3
)

var defaultBackoff = [retryBackoff]time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// summaryMaxLen 通知摘要截取长度（FinalAnswer 前 200 字符，按 rune 计）。
const summaryMaxLen = 200

// Format 取值（notify.format）。raw=结构化 JSON；feishu=飞书自定义机器人文本消息。
const (
	FormatRaw    = "raw"
	FormatFeishu = "feishu"
)

// Config 客户端构造参数。
type Config struct {
	// WebhookURL 通知目标（飞书自定义机器人地址或任意 HTTP 端点）；空=禁用。
	WebhookURL string
	// Format 通知格式：raw（默认，空同 raw）| feishu；其他值构造报错。
	Format string
	// Timeout 单次 HTTP 请求超时；0=默认 5s（测试注入短值）。
	Timeout time.Duration
	// Backoff 重试退避序列（长度即重试次数）；nil=默认 1s/2s/4s（测试注入短值）。
	Backoff []time.Duration
}

// Client 任务终态 webhook 通知客户端。实现 core.TerminalNotifier（TaskFinal）。
// 零值不可用，经 NewClient 构造；WebhookURL 为空的客户端 TaskFinal 直接返回。
type Client struct {
	url     string
	feishu  bool
	hc      *http.Client
	backoff []time.Duration
	wg      sync.WaitGroup // 在途发送计数（WaitIdle 用）
}

// NewClient 构造通知客户端；仅 Format 非法时报错（启动即失败，不静默回落）。
func NewClient(cfg Config) (*Client, error) {
	switch cfg.Format {
	case "", FormatRaw, FormatFeishu:
	default:
		return nil, fmt.Errorf("notify: 未知格式 %q（可选 raw|feishu）", cfg.Format)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	backoff := cfg.Backoff
	if len(backoff) == 0 {
		backoff = defaultBackoff[:]
	}
	return &Client{
		url:     cfg.WebhookURL,
		feishu:  cfg.Format == FormatFeishu,
		hc:      &http.Client{Timeout: timeout},
		backoff: backoff,
	}, nil
}

// TaskFinal 任务终态通知入口。异步发送、立即返回——状态机持久化不被阻塞；
// 同一任务只会被调用一次（去重挂在 core 终态转换点，见包注释）。错误全部
// 自行消化（重试穷尽只记日志），绝不向状态机传回任何失败。
func (c *Client) TaskFinal(t protocol.Task) {
	if c == nil || c.url == "" {
		return
	}
	body := c.render(t)
	if body == nil {
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.post(body)
	}()
}

// WaitIdle 等待全部在途通知发送结束（测试与优雅关闭观察用；发送本身尽力而为，
// 不保证成功）。TaskFinal 在返回前完成 Add，故常规调用序下无 Add/Wait 竞态。
func (c *Client) WaitIdle() {
	if c == nil {
		return
	}
	c.wg.Wait()
}

// post 带重试的 webhook 投递：任一次 2xx 即成功；失败按退避序列重试
// （首次发送 + len(backoff) 次重试），穷尽后只记日志。
func (c *Client) post(body []byte) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			time.Sleep(c.backoff[attempt-1])
		}
		err := c.postOnce(body)
		if err == nil {
			return
		}
		lastErr = err
		if attempt >= len(c.backoff) {
			break
		}
	}
	log.Printf("[hub] 任务终态通知最终失败（已重试 %d 次，放弃；不影响任务终态）: %v", len(c.backoff), lastErr)
}

// postOnce 单次投递：2xx 视为成功，其余状态码与传输错误同为可重试失败。
func (c *Client) postOnce(body []byte) error {
	resp, err := c.hc.Post(c.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // 排干以复用连接
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook 返回 %s", resp.Status)
	}
	return nil
}

// render 按格式渲染通知体；序列化不可失败（纯静态结构），nil 仅作防御。
func (c *Client) render(t protocol.Task) []byte {
	if c.feishu {
		return mustJSON(feishuMessage{MsgType: "text", Content: feishuContent{Text: feishuText(t)}})
	}
	return mustJSON(terminalPayload{
		Event:       "task_final",
		TaskID:      t.ID,
		Status:      string(t.Status),
		NodeID:      t.TargetNode,
		Agent:       t.TargetAgent,
		SubmittedBy: t.SubmittedBy,
		DurationSec: durationSec(t),
		Error:       resultError(t.Result),
		Summary:     summary(t.Result),
	})
}

// terminalPayload raw 格式通知载荷（设计定稿字段表；缺失字段 omitempty 省略）。
type terminalPayload struct {
	Event       string `json:"event"`
	TaskID      string `json:"task_id"`
	Status      string `json:"status"`
	NodeID      string `json:"node_id,omitempty"`
	Agent       string `json:"agent,omitempty"`
	SubmittedBy string `json:"submitted_by,omitempty"`
	DurationSec *int64 `json:"duration_sec,omitempty"`
	Error       string `json:"error,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

// durationSec 任务耗时秒数（UpdatedAt-CreatedAt；CreatedAt 缺失不可计=nil 省略）。
func durationSec(t protocol.Task) *int64 {
	if t.CreatedAt.IsZero() {
		return nil
	}
	d := int64(t.UpdatedAt.Sub(t.CreatedAt).Seconds())
	return &d
}

func resultError(r *protocol.TaskResult) string {
	if r == nil {
		return ""
	}
	return r.Error
}

// summary FinalAnswer 前 200 字符（rune 计，不切断多字节字符）；无结果=省略。
func summary(r *protocol.TaskResult) string {
	if r == nil || r.FinalAnswer == "" {
		return ""
	}
	runes := []rune(r.FinalAnswer)
	if len(runes) > summaryMaxLen {
		runes = runes[:summaryMaxLen]
	}
	return string(runes)
}

// feishuMessage 飞书自定义机器人文本消息格式（设计定稿）。
type feishuMessage struct {
	MsgType string        `json:"msg_type"`
	Content feishuContent `json:"content"`
}

type feishuContent struct {
	Text string `json:"text"`
}

// feishuText 一行人话摘要（设计文案）：完成「✅ 任务完成 t-x · node/agent ·
// 耗时 3m20s」；失败附原因；needs_review 等人工确认；取消同口径。字段缺失
// （无节点/无耗时/无原因）对应片段整体省略。
func feishuText(t protocol.Task) string {
	var b strings.Builder
	switch t.Status {
	case protocol.StatusCompleted:
		b.WriteString("✅ 任务完成 ")
	case protocol.StatusFailed:
		b.WriteString("❌ 任务失败 ")
	case protocol.StatusNeedsReview:
		b.WriteString("⚠️ 任务结果拿不准，等你确认 ")
	case protocol.StatusCancelled:
		b.WriteString("🚫 任务已取消 ")
	default:
		b.WriteString("任务终态 ")
	}
	b.WriteString(t.ID)
	if t.TargetNode != "" || t.TargetAgent != "" {
		b.WriteString(" · " + t.TargetNode + "/" + t.TargetAgent)
	}
	if !t.CreatedAt.IsZero() {
		b.WriteString(" · 耗时 " + durationText(t))
	}
	if t.Status == protocol.StatusFailed && t.Result != nil && t.Result.Error != "" {
		b.WriteString("（原因：" + t.Result.Error + "）")
	}
	return b.String()
}

// durationText 人话时长（Truncate 到秒：3m20s，不出小数）。
func durationText(t protocol.Task) string {
	return t.UpdatedAt.Sub(t.CreatedAt).Truncate(time.Second).String()
}

// mustJSON 静态结构序列化兜底（不可达分支防御，返回 nil 由调用方跳过发送）。
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("[hub] 任务终态通知载荷序列化失败（跳过发送）: %v", err)
		return nil
	}
	return b
}
