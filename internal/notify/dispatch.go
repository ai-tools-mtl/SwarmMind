// Dispatcher 任务终态通知分发器（P4③ 控制台化升级，替代单 webhook Client）：
// 每次终态事件取渠道存储最新快照，路由到全部 enabled 且订阅该事件的渠道；
// 各渠道独立投递、独立重试，互不阻塞；渠道级失败只记日志（日志不含凭证与
// webhook URL query），绝不影响任务终态与主流程。
//
// 可靠性参数沿用 P4③ 设计定稿：单次请求 5s 超时；失败重试 3 次指数退避
// （1s/2s/4s）；发送在独立 goroutine，不阻塞状态机；同一任务只会被调用一次
// （去重挂在 core 终态转换点）。
package notify

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// Dispatcher 任务终态通知分发器。结构化实现 hub core.TerminalNotifier 与
// node service.TerminalNotifier（同一 TaskFinal 形状，两侧接口各自声明）。
type Dispatcher struct {
	store *ChannelStore
	inbox *Inbox

	hc          *http.Client
	backoff     []time.Duration // 长度即重试次数
	smtpTimeout time.Duration   // SMTP 全链路 deadline（net/smtp 无原生超时）
	wg          sync.WaitGroup  // 在途投递计数（WaitIdle 用）

	dmu   sync.Mutex                // deliv 锁（投递在独立 goroutine，读写并发）
	deliv map[string]DeliveryStatus // 渠道 ID → 最近投递事实（内存态，deliverystatus.go）
}

// DispatcherOption Dispatcher 可选参数（测试注入短超时/短退避）。
type DispatcherOption func(*Dispatcher)

// WithTimeout 单次 HTTP 请求超时（0 = 默认 5s）。
func WithTimeout(d time.Duration) DispatcherOption {
	return func(disp *Dispatcher) {
		if d > 0 {
			disp.hc.Timeout = d
		}
	}
}

// WithSMTPTimeout SMTP 全链路 deadline（连接/认证/发送/退出整体；0 = 默认 5s）。
// 与 HTTP 超时独立：net/smtp 无每请求超时，靠该 deadline 兜底挂起的服务器。
func WithSMTPTimeout(d time.Duration) DispatcherOption {
	return func(disp *Dispatcher) {
		if d > 0 {
			disp.smtpTimeout = d
		}
	}
}

// WithBackoff 重试退避序列（长度即重试次数；nil/空 = 默认 1s/2s/4s）。
func WithBackoff(seq []time.Duration) DispatcherOption {
	return func(disp *Dispatcher) {
		if len(seq) > 0 {
			disp.backoff = append([]time.Duration(nil), seq...)
		}
	}
}

// NewDispatcher 构造分发器。store 与 inbox 由装配方提供（inbox 非 nil 时 inbox
// 渠道才生效）。
func NewDispatcher(store *ChannelStore, inbox *Inbox, opts ...DispatcherOption) *Dispatcher {
	d := &Dispatcher{
		store:       store,
		inbox:       inbox,
		hc:          &http.Client{Timeout: defaultTimeout},
		backoff:     defaultBackoff[:],
		smtpTimeout: defaultTimeout,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// TaskFinal 任务终态通知入口（core.TerminalNotifier）。快照渠道、过滤
// enabled+订阅，逐渠道异步投递后立即返回——状态机持久化不被阻塞。错误全部
// 自行消化（重试穷尽只记日志），绝不向状态机传回任何失败。
//
// inbox 聚合：站内列表全局一份，同一终态事件无论命中几个 inbox 渠道只 Push
// 一条（inbox 渠道只承担开关与事件过滤职责）；其余渠道照常各自投递。
func (d *Dispatcher) TaskFinal(t protocol.Task) {
	if d == nil {
		return
	}
	inboxPushed := false
	for _, ch := range d.store.List() {
		if !ch.Enabled || !subscribed(ch.Events, t.Status) {
			continue
		}
		if ch.Type == TypeInbox {
			if inboxPushed || d.inbox == nil {
				continue
			}
			inboxPushed = true
			d.wg.Add(1)
			go func(it InboxItem) {
				defer d.wg.Done()
				d.inbox.Push(it)
			}(inboxItemFor(t, false))
			continue
		}
		d.wg.Add(1)
		go func(ch Channel) {
			defer d.wg.Done()
			d.deliver(ch, t)
		}(ch)
	}
}

// WaitIdle 等待全部在途投递结束（测试与优雅关闭观察用；发送本身尽力而为，
// 不保证成功）。TaskFinal 在返回前完成全部 Add，故常规调用序下无 Add/Wait 竞态。
func (d *Dispatcher) WaitIdle() {
	if d == nil {
		return
	}
	d.wg.Wait()
}

// Store 渠道配置存储访问器（管理面 CRUD 的入口；Dispatcher 只读它做路由）。
func (d *Dispatcher) Store() *ChannelStore { return d.store }

// Inbox 站内通知表访问器（管理面拉取/标已读入口；未接线时 nil）。
func (d *Dispatcher) Inbox() *Inbox { return d.inbox }

// deliver 单渠道投递（非 inbox 类型；inbox 聚合在 TaskFinal）：按类型与消息
// 格式（webhook 的 Format 维度，2026-10-26 feishu 并入 webhook）构建载荷后带
// 重试发送。渠道级失败只记日志（渠道名+ID+脱敏后的原因），不打断其他渠道。
func (d *Dispatcher) deliver(ch Channel, t protocol.Task) {
	defer func() {
		if r := recover(); r != nil { // 单渠道 panic 不拖垮其余渠道（防御）
			log.Printf("[notify] 通知渠道 %s(%s) 投递异常（已跳过）: %v", ch.Name, ch.ID, r)
		}
	}()
	switch ch.Type {
	case TypeEmail:
		d.retry(ch, func() error { return d.sendEmail(ch.Secrets, t) })
	case TypeWebhook:
		if normalizeFormat(ch.Format) == FormatFeishu {
			// 飞书机器人格式：msg_type text 文本消息；无自定义头（飞书不读，见
			// validateSecrets——配置层已拒，此处 nil 兜底）。投递用原始完整地址
			//（query/userinfo 里可能就是认证凭证，脱敏会打断认证）；脱敏只发生在
			// 错误展示路径（retry→sanitizeErrMsg，只留域名与状态码）。
			body := mustJSON(feishuMessage{MsgType: "text", Content: feishuContent{Text: feishuText(t)}})
			d.retry(ch, func() error { return d.postHTTP(ch, ch.Secrets["url"], body, nil) })
			return
		}
		body := mustJSON(terminalPayloadFor(t))
		headers := headerMap(ch.Secrets)
		d.retry(ch, func() error { return d.postHTTP(ch, ch.Secrets["url"], body, headers) })
	default:
		log.Printf("[notify] 通知渠道 %s(%s) 类型 %q 无投递实现（跳过）", ch.Name, ch.ID, ch.Type)
	}
}

// retry 渠道级重试骨架：任一次成功即停；失败按退避序列重试（首次发送 +
// len(backoff) 次），穷尽后只记日志（渠道名 + 脱敏原因）。成功/穷尽两路都落
// 渠道投递状态（FE-POLISH P0-3：渠道卡「最近成功/最近失败」数据源）。
func (d *Dispatcher) retry(ch Channel, attempt func() error) {
	var lastErr error
	for i := 0; ; i++ {
		if i > 0 {
			time.Sleep(d.backoff[i-1])
		}
		err := attempt()
		if err == nil {
			d.deliveryOK(ch.ID)
			return
		}
		lastErr = err
		if i >= len(d.backoff) {
			break
		}
	}
	d.deliveryFail(ch.ID, sanitizeErrMsg(lastErr))
	log.Printf("[notify] 通知渠道 %s(%s) 投递最终失败（已重试 %d 次，放弃；不影响任务）: %s",
		ch.Name, ch.ID, len(d.backoff), sanitizeErrMsg(lastErr))
}

// TestChannel 渠道「发测试」按钮：单次尝试（不走重试——HTTP 请求控制在单次
// 超时内，管理面不等 27s），返回 (成功, 人话失败原因)。inbox 渠道写一条演示
// 站内通知恒成功；未知渠道 → ErrNotFound。email/webhook 两路落渠道投递状态
//（FE-POLISH P0-3：测试成功/失败与真实投递同为渠道可达性证据）；inbox 不落
//（站内投递恒成功，不承担外部告警通道观测职责）。
func (d *Dispatcher) TestChannel(id string) (bool, string) {
	ch, ok := d.store.Get(id)
	if !ok {
		return false, ErrNotFound.Error()
	}
	switch ch.Type {
	case TypeInbox:
		if d.inbox == nil {
			return false, "站内通知未启用"
		}
		d.inbox.Push(inboxItemFor(protocol.Task{ID: "t-test", Status: protocol.StatusCompleted}, true))
		return true, ""
	case TypeEmail:
		err := d.sendEmail(ch.Secrets, sampleTestTask())
		if err != nil {
			d.deliveryFail(ch.ID, sanitizeErrMsg(err))
			return false, sanitizeErrMsg(err)
		}
		d.deliveryOK(ch.ID)
		return true, ""
	case TypeWebhook:
		if normalizeFormat(ch.Format) == FormatFeishu {
			body := mustJSON(feishuMessage{MsgType: "text", Content: feishuContent{Text: testText(ch.Name)}})
			err := d.postHTTP(ch, ch.Secrets["url"], body, nil)
			if err != nil {
				d.deliveryFail(ch.ID, sanitizeErrMsg(err))
				return false, sanitizeErrMsg(err)
			}
			d.deliveryOK(ch.ID)
			return true, ""
		}
		body := mustJSON(testPayload(ch.Name))
		err := d.postHTTP(ch, ch.Secrets["url"], body, headerMap(ch.Secrets))
		if err != nil {
			d.deliveryFail(ch.ID, sanitizeErrMsg(err))
			return false, sanitizeErrMsg(err)
		}
		d.deliveryOK(ch.ID)
		return true, ""
	default:
		return false, "该类型暂无投递实现"
	}
}

// ---------- 载荷 ----------

// terminalPayloadFor raw 结构化载荷（事件字段表与 P4③ 设计定稿一致）。
func terminalPayloadFor(t protocol.Task) terminalPayload {
	return terminalPayload{
		Event:       "task_final",
		TaskID:      t.ID,
		Status:      string(t.Status),
		NodeID:      t.TargetNode,
		Agent:       t.TargetAgent,
		SubmittedBy: t.SubmittedBy,
		DurationSec: durationSec(t),
		Error:       resultError(t.Result),
		Summary:     summary(t.Result),
	}
}

// inboxItemFor 终态事件 → 站内通知条目；test=true 为渠道测试演示条目。
func inboxItemFor(t protocol.Task, test bool) InboxItem {
	return InboxItem{
		TaskID:  t.ID,
		Status:  string(t.Status),
		NodeID:  t.TargetNode,
		Agent:   t.TargetAgent,
		Summary: summary(t.Result),
		Error:   resultError(t.Result),
		Test:    test,
	}
}

// testPayload 渠道测试载荷（webhook 类型）。
func testPayload(name string) map[string]any {
	return map[string]any{"event": "test", "message": "SwarmMind 通知渠道「" + name + "」测试消息"}
}

// testText 渠道测试文案（webhook 飞书格式）。
func testText(name string) string {
	return "🔔 SwarmMind 测试消息：通知渠道「" + name + "」配置成功。"
}

// sampleTestTask 渠道测试按钮用的样例任务（正文人话、字段齐全即可，不落任务账本）。
func sampleTestTask() protocol.Task {
	return protocol.Task{ID: "t-test", Status: protocol.StatusCompleted}
}

// ---------- HTTP 投递（feishu / webhook 共用） ----------

// postHTTP 单次 POST：2xx 视为成功。headers 附加自定义头（webhook 渠道）。
func (d *Dispatcher) postHTTP(ch Channel, rawURL string, body []byte, headers map[string]string) error {
	if rawURL == "" {
		return errors.New("缺少推送地址")
	}
	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("推送地址非法: %w", err) // %w 保住 url.Error 链，供 sanitizeErrMsg 剥地址
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		return err // 调用方经 sanitizeErrMsg 脱敏（剥成 scheme://host，path/query/userinfo 全不外带）
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // 排干以复用连接
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook 返回 %s", resp.Status)
	}
	return nil
}

// sanitizeSecretURL 日志/错误信息脱敏用 URL：只留 scheme://host。飞书自定义
// 机器人把凭证放在 path（/bot/v2/hook/<token>），通用 webhook 常放 query 或
// userinfo——一律不外带，错误信息只出现域名（与状态码，由 postHTTP 拼接）。
// 解析失败原样返回空串由调用方报「地址非法」。
func sanitizeSecretURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	u.Path = ""
	u.RawQuery = ""
	u.User = nil
	return u.String()
}

// sanitizeErrMsg 错误信息脱敏：http.Client 的 url.Error 会把完整请求 URL
// （含 path/query/userinfo 凭证）嵌进消息——剥成 scheme://host 只留域名，
// 另拼底层原因。
func sanitizeErrMsg(err error) string {
	if err == nil {
		return ""
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Sprintf("POST %s: %v", sanitizeSecretURL(ue.URL), ue.Err)
	}
	return err.Error()
}

// headerMap 从 secrets 提取自定义头（header.<名> 键）。
func headerMap(secrets map[string]string) map[string]string {
	var out map[string]string
	for k, v := range secrets {
		if !isHeaderKey(k) || v == "" {
			continue
		}
		if name, ok := headerName(k); ok {
			if out == nil {
				out = map[string]string{}
			}
			out[name] = v
		}
	}
	return out
}

// ---------- 邮件投递（email 渠道，net/smtp 标准库实现，零新依赖） ----------

// sendEmail SMTP 投递。net/smtp 无原生超时——拨号用 DialTimeout，连接建立后
// 打一个绝对 deadline（SetDeadline），连接/TLS 握手/EHLO/认证/发送/退出全链路
// 都在 deadline 内返回（挂起不回话的服务器也兜得住）。端口 465 走隐式 TLS；
// 其余端口明文连接、服务器宣告 STARTTLS 时自动升级（smtp.SendMail 同语义，
// 因其无法纳入 deadline 替换为手动 client 流程）。明文端口上 smtp.PlainAuth
// 客户端会拒绝发送认证——宁可发不出，不在明文线上交出口令。
func (d *Dispatcher) sendEmail(sec map[string]string, t protocol.Task) error {
	host := strings.TrimSpace(sec["host"])
	port := strings.TrimSpace(sec["port"])
	user := sec["username"]
	pass := sec["password"]
	from := strings.TrimSpace(sec["from"])
	to := strings.TrimSpace(sec["to"])
	if host == "" || port == "" || from == "" || to == "" {
		return errors.New("email 渠道配置不完整（host/port/from/to）")
	}
	subject, body := emailContent(t)
	msg := buildMail(from, to, subject, body)
	addr := host + ":" + port
	auth := smtp.PlainAuth("", user, pass, host)
	var conn net.Conn
	var err error
	if port == "465" {
		conn, err = tls.DialWithDialer(&net.Dialer{Timeout: d.smtpTimeout}, "tcp", addr,
			&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = net.DialTimeout("tcp", addr, d.smtpTimeout)
	}
	if err != nil {
		return err
	}
	if err := conn.SetDeadline(time.Now().Add(d.smtpTimeout)); err != nil {
		_ = conn.Close()
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	return sendSMTPSession(c, conn, host, auth, from, []string{to}, msg)
}

// sendSMTPSession SMTP 会话主体（连接+deadline 已就位）：EHLO、明文连接按需
// STARTTLS、服务器宣告 AUTH 才认证（语义对齐 smtp.SendMail）、Mail/Rcpt/Data/
// Quit。任何一步失败关闭连接返回；整段受 conn 的 deadline 约束。
func sendSMTPSession(c *smtp.Client, conn net.Conn, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	defer func() { _ = c.Close() }() // Quit 成功后连接已关，Close 幂等兜底（错误忽略）
	if err := c.Hello("localhost"); err != nil {
		return err
	}
	if ok, _ := c.Extension("STARTTLS"); ok {
		if _, isTLS := conn.(*tls.Conn); !isTLS {
			if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return err
			}
		}
	}
	if ok, _ := c.Extension("AUTH"); ok {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err = w.Write(msg); err != nil {
		_ = w.Close()
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// emailContent 邮件主题与正文（人话口径与飞书文案一致）。
func emailContent(t protocol.Task) (string, string) {
	return feishuText(t), feishuText(t)
}

// buildMail 组装 RFC 5322 邮件（UTF-8；主题经 Q 编码支持中文）。
func buildMail(from, to, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(body)
	b.WriteString("\r\n")
	return []byte(b.String())
}
