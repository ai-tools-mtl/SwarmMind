package notify

import (
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mtl802/swarmmind/internal/protocol"
)

// ---------- 事件订阅判定（路由核心） ----------

// TestSubscribedTruthTable 事件订阅真值表：空=全订阅；task_final 收全部终态；
// task_failed 仅失败；task_needs_review 仅等你确认；未知事件永不匹配。
func TestSubscribedTruthTable(t *testing.T) {
	cases := []struct {
		events []string
		status protocol.TaskStatus
		want   bool
	}{
		{nil, protocol.StatusCompleted, true},
		{nil, protocol.StatusFailed, true},
		{nil, protocol.StatusNeedsReview, true},
		{nil, protocol.StatusCancelled, true},
		{[]string{EventTaskFinal}, protocol.StatusCompleted, true},
		{[]string{EventTaskFinal}, protocol.StatusFailed, true},
		{[]string{EventTaskFinal}, protocol.StatusCancelled, true},
		{[]string{EventTaskFailed}, protocol.StatusFailed, true},
		{[]string{EventTaskFailed}, protocol.StatusCompleted, false},
		{[]string{EventTaskFailed}, protocol.StatusCancelled, false},
		{[]string{EventNeedsReview}, protocol.StatusNeedsReview, true},
		{[]string{EventNeedsReview}, protocol.StatusCompleted, false},
		// 三选多：失败+等你确认的渠道收不到 completed
		{[]string{EventTaskFailed, EventNeedsReview}, protocol.StatusFailed, true},
		{[]string{EventTaskFailed, EventNeedsReview}, protocol.StatusNeedsReview, true},
		{[]string{EventTaskFailed, EventNeedsReview}, protocol.StatusCompleted, false},
		{[]string{"bogus_event"}, protocol.StatusFailed, false},
	}
	for _, tc := range cases {
		if got := subscribed(tc.events, tc.status); got != tc.want {
			t.Errorf("subscribed(%v, %s) = %v, want %v", tc.events, tc.status, got, tc.want)
		}
	}
}

// ---------- Dispatcher 路由 ----------

// TestDispatcherRoutesToAllEnabledChannels 多渠道广播：两个启用渠道各收一份，
// 载荷按类型各归各（webhook 收 raw JSON、feishu 收文本消息包装）。
func TestDispatcherRoutesToAllEnabledChannels(t *testing.T) {
	wh := newHookServer(t, 0)
	fe := newHookServer(t, 0)
	s := newTestStore(t)
	mustCreate(t, s, TypeWebhook, "通用", nil, map[string]string{"url": wh.srv.URL})
	mustCreate(t, s, TypeFeishu, "飞书", nil, map[string]string{"webhook_url": fe.srv.URL})
	d, _ := newTestDispatcher(t, s)

	d.TaskFinal(testTask(protocol.StatusCompleted, &protocol.TaskResult{FinalAnswer: "done"}))
	d.WaitIdle()

	if got := wh.count(); got != 1 {
		t.Fatalf("webhook 渠道应收到 1 次, got %d", got)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(wh.lastBody()), &p); err != nil {
		t.Fatalf("webhook 载荷应为 JSON: %v", err)
	}
	if p["event"] != "task_final" || p["task_id"] != "t-abc123" || p["status"] != "completed" {
		t.Errorf("webhook 载荷字段不符: %v", p)
	}
	if got := fe.count(); got != 1 {
		t.Fatalf("feishu 渠道应收到 1 次, got %d", got)
	}
	var m feishuMessage
	if err := json.Unmarshal([]byte(fe.lastBody()), &m); err != nil {
		t.Fatalf("feishu 载荷解析: %v", err)
	}
	if m.MsgType != "text" || m.Content.Text != "✅ 任务完成 t-abc123 · mac-mini/coder · 耗时 3m20s" {
		t.Errorf("feishu 文案不符: %+v", m)
	}
}

// TestDispatcherSkipsDisabledAndUnsubscribed 停用渠道与未订阅渠道不收；
// 改 enabled 后即时生效（运行时可改，无需重建 Dispatcher）。
func TestDispatcherSkipsDisabledAndUnsubscribed(t *testing.T) {
	wh := newHookServer(t, 0)
	s := newTestStore(t)
	disabled := mustCreate(t, s, TypeWebhook, "停用中", nil, map[string]string{"url": wh.srv.URL})
	off := false
	if _, err := s.Update(disabled.ID, "", "", &off, nil, false, nil); err != nil {
		t.Fatalf("停用渠道: %v", err)
	}
	filtered := mustCreate(t, s, TypeWebhook, "只订失败", []string{EventTaskFailed}, map[string]string{"url": wh.srv.URL})
	d, _ := newTestDispatcher(t, s)

	// completed：停用不收；只订失败的不收
	_ = disabled
	_ = filtered
	d.TaskFinal(testTask(protocol.StatusCompleted, nil))
	d.WaitIdle()
	if got := wh.count(); got != 0 {
		t.Fatalf("completed 不应触达停用/只订失败渠道, got %d", got)
	}

	// failed：只订失败的渠道收
	d.TaskFinal(testTask(protocol.StatusFailed, &protocol.TaskResult{Error: protocol.CodeTimeout}))
	d.WaitIdle()
	if got := wh.count(); got != 1 {
		t.Fatalf("failed 应触达只订失败渠道 1 次, got %d", got)
	}

	// 运行时启用「停用中」渠道 → completed 也触达
	toggle := true
	if _, err := s.Update(disabled.ID, "", "", &toggle, nil, false, nil); err != nil {
		t.Fatalf("更新 enabled: %v", err)
	}
	d.TaskFinal(testTask(protocol.StatusCompleted, nil))
	d.WaitIdle()
	if got := wh.count(); got != 2 {
		t.Fatalf("启用后 completed 应触达 2 次（只订失败+新启用）, got %d", got)
	}
}

// TestDispatcherPerChannelIndependence 渠道级隔离：A 恒 500 重试穷尽，
// B 一次成功——A 的失败不影响 B 的投递（各渠道独立 goroutine+独立重试）。
func TestDispatcherPerChannelIndependence(t *testing.T) {
	bad := newHookServer(t, 1<<30) // 恒 500
	good := newHookServer(t, 0)
	s := newTestStore(t)
	mustCreate(t, s, TypeWebhook, "坏渠道", nil, map[string]string{"url": bad.srv.URL})
	mustCreate(t, s, TypeWebhook, "好渠道", nil, map[string]string{"url": good.srv.URL})
	d, _ := newTestDispatcher(t, s)

	d.TaskFinal(testTask(protocol.StatusCompleted, nil))
	d.WaitIdle()

	if got := bad.count(); got != 1+len(shortBackoff) {
		t.Errorf("坏渠道应首发+%d 重试=%d 次, got %d", len(shortBackoff), 1+len(shortBackoff), got)
	}
	if got := good.count(); got != 1 {
		t.Errorf("好渠道应恰好 1 次, got %d", got)
	}
}

// TestDispatcherWebhookCustomHeaders webhook 渠道自定义头随请求携带。
func TestDispatcherWebhookCustomHeaders(t *testing.T) {
	wh := newHookServer(t, 0)
	s := newTestStore(t)
	mustCreate(t, s, TypeWebhook, "带头", nil, map[string]string{
		"url":                 wh.srv.URL,
		"header.X-Swarm-Token": "abc123",
	})
	d, _ := newTestDispatcher(t, s)
	d.TaskFinal(testTask(protocol.StatusCompleted, nil))
	d.WaitIdle()
	if got := wh.lastHeader().Get("X-Swarm-Token"); got != "abc123" {
		t.Errorf("自定义头应随请求携带, got %q", got)
	}
	if ct := wh.lastHeader().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type 应为 application/json, got %q", ct)
	}
}

// TestDispatcherInboxRoute inbox 渠道：终态事件写站内通知（未读）；事件不匹配
// 不写；多条 inbox 渠道并存同一终态只投一条（站内列表全局一份，渠道只承担
// 开关与事件过滤职责）；其余类型渠道照常各自投递。
func TestDispatcherInboxRoute(t *testing.T) {
	s := newTestStore(t)
	mustCreate(t, s, TypeInbox, "站内", nil, nil)
	d, ib := newTestDispatcher(t, s)

	d.TaskFinal(testTask(protocol.StatusCompleted, &protocol.TaskResult{FinalAnswer: "搞定了"}))
	d.WaitIdle()
	items, unread := ib.Recent(20)
	if unread != 1 || len(items) != 1 {
		t.Fatalf("应有 1 条未读站内通知, got %d 条/%d 未读", len(items), unread)
	}
	if items[0].TaskID != "t-abc123" || items[0].Status != "completed" || items[0].Summary != "搞定了" {
		t.Errorf("站内通知字段不符: %+v", items[0])
	}

	// 事件过滤：只订失败的 inbox 渠道对 completed 不写
	s2 := newTestStore(t)
	mustCreate(t, s2, TypeInbox, "只订失败", []string{EventTaskFailed}, nil)
	d2, ib2 := newTestDispatcher(t, s2)
	d2.TaskFinal(testTask(protocol.StatusCompleted, nil))
	d2.WaitIdle()
	if n := ib2.Unread(); n != 0 {
		t.Errorf("completed 不应写只订失败的站内渠道, got %d 未读", n)
	}
}

// TestDispatcherInboxAggregation 同一终态命中多个 inbox 渠道（全订阅+只订失败+
// 停用）→ 站内只 Push 一条；并存的 webhook 渠道照常投递。
func TestDispatcherInboxAggregation(t *testing.T) {
	wh := newHookServer(t, 0)
	s := newTestStore(t)
	mustCreate(t, s, TypeInbox, "全订阅", nil, nil)
	mustCreate(t, s, TypeInbox, "只订失败", []string{EventTaskFailed}, nil)
	off := mustCreate(t, s, TypeInbox, "停用", nil, nil)
	en := false
	if _, err := s.Update(off.ID, "", "", &en, nil, false, nil); err != nil {
		t.Fatalf("停用渠道: %v", err)
	}
	mustCreate(t, s, TypeWebhook, "webhook 并存", nil, map[string]string{"url": wh.srv.URL})
	d, ib := newTestDispatcher(t, s)

	d.TaskFinal(testTask(protocol.StatusCompleted, nil))
	d.WaitIdle()
	if _, unread := ib.Recent(20); unread != 1 {
		t.Fatalf("三个 inbox 渠道（两启用一停用）命中同一 completed 终态应只投 1 条, got %d 未读", unread)
	}
	if got := wh.count(); got != 1 {
		t.Errorf("并存 webhook 渠道应照常投递 1 次, got %d", got)
	}

	// failed 再次命中（全订阅+只订失败都命中）→ 仍只累计 1 条新通知
	d.TaskFinal(testTask(protocol.StatusFailed, &protocol.TaskResult{Error: "x"}))
	d.WaitIdle()
	items, unread := ib.Recent(20)
	if unread != 2 || len(items) != 2 {
		t.Fatalf("failed 事件应再投 1 条（共 2 条未读）, got %d 条/%d 未读", len(items), unread)
	}
	if got := wh.count(); got != 2 {
		t.Errorf("webhook 应两次各投 1 次, got %d", got)
	}
}

// TestDispatcherDeliversFullURL 投递用原始完整地址：query/userinfo 里的凭证
// 是认证的一部分，必须原样到达（脱敏只发生在错误/日志展示路径）。
func TestDispatcherDeliversFullURL(t *testing.T) {
	wh := newHookServer(t, 0)
	s := newTestStore(t)
	mustCreate(t, s, TypeWebhook, "带凭证地址", nil,
		map[string]string{"url": wh.srv.URL + "/open-apis/bot/v2/hook/sekret-token?x-sign=topsecret"})
	d, _ := newTestDispatcher(t, s)
	d.TaskFinal(testTask(protocol.StatusCompleted, nil))
	d.WaitIdle()

	if got := wh.count(); got != 1 {
		t.Fatalf("应送达 1 次, got %d", got)
	}
	uri := wh.lastURI()
	if !strings.Contains(uri, "/open-apis/bot/v2/hook/sekret-token") || !strings.Contains(uri, "x-sign=topsecret") {
		t.Errorf("path 与 query 凭证应随原始地址完整送达（认证依赖它）, got %q", uri)
	}
}

// TestErrorMessagesLeakNoCredentials 错误展示路径脱敏：测试按钮/重试穷尽的
// 人话原因只留 scheme://host 域名与状态码——path（飞书 hook token）、query、
// userinfo 凭证一律不外带。
func TestErrorMessagesLeakNoCredentials(t *testing.T) {
	// 单元口径：sanitizeSecretURL 只留域名
	if got := sanitizeSecretURL("https://user:pw@open.feishu.cn/open-apis/bot/v2/hook/TOK123?k=v"); got != "https://open.feishu.cn" {
		t.Errorf("脱敏地址应只留 scheme://host, got %q", got)
	}
	// 端到端口径：不可达渠道测试按钮的失败原因不含 path/query 凭证
	s := newTestStore(t)
	ch := mustCreate(t, s, TypeWebhook, "带凭证不可达", nil,
		map[string]string{"url": "http://127.0.0.1:1/hook/sekret-path?token=topsecret-query"})
	d, _ := newTestDispatcher(t, s)
	ok, msg := d.TestChannel(ch.ID)
	if ok || msg == "" {
		t.Fatalf("不可达渠道测试应失败并带原因, got %v %q", ok, msg)
	}
	for _, secret := range []string{"sekret-path", "topsecret-query"} {
		if strings.Contains(msg, secret) {
			t.Errorf("失败原因不得外带凭证 %s: %q", secret, msg)
		}
	}
	if !strings.Contains(msg, "127.0.0.1") {
		t.Errorf("失败原因应保留域名供排查: %q", msg)
	}
}

// ---------- SMTP deadline（挂起服务器兜底） ----------

// hangServer 挂起 TCP 服务器：接受连接后不读不写不关（模拟黑洞服务器，
// net/smtp 无原生超时的回归场景）。固定端口绑定失败（权限/占用）由调用方跳过。
func hangServer(t *testing.T, network, addr string) string {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Fatalf("监听 %s: %v", addr, err)
	}
	done := make(chan struct{})
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				close(done)
				return
			}
			mu.Lock()
			conns = append(conns, c) // 悬着：不握手不应答
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
	})
	return ln.Addr().String()
}

// mustHangServer 挂起服务器（随机端口）；fixedPort 非 nil 时绑指定端口，
// 绑不上则跳过（端口 465 用例在无权限/被占用的环境让路）。
func mustHangServer(t *testing.T, fixedPort string) string {
	t.Helper()
	addr := "127.0.0.1:0"
	if fixedPort != "" {
		ln, err := net.Listen("tcp", "127.0.0.1:"+fixedPort)
		if err != nil {
			t.Skipf("端口 %s 不可用（%v），跳过该分支", fixedPort, err)
		}
		_ = ln.Close()
		addr = "127.0.0.1:" + fixedPort
	}
	return hangServer(t, "tcp", addr)
}

// TestSMTPDeadlineHangsServer SMTP 全链路 deadline：挂起的 SMTP 服务器（明文
// 端口不回 greeting、465 端口不完成 TLS 握手）都必须在 deadline 内返回，
// 不许无限挂起。
func TestSMTPDeadlineHangsServer(t *testing.T) {
	cases := []struct {
		name string
		port string // ""=随机端口走明文分支；"465"走隐式 TLS 分支
	}{
		{"明文端口 greeting 挂起", ""},
		{"465 隐式 TLS 握手挂起", "465"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr := mustHangServer(t, tc.port)
			host, port, _ := net.SplitHostPort(addr)
			s := newTestStore(t)
			ch := mustCreate(t, s, TypeEmail, "挂起服务器", nil, map[string]string{
				"host": host, "port": port, "username": "u", "password": "p", "from": "a@x", "to": "b@x",
			})
			d := NewDispatcher(s, &Inbox{}, WithSMTPTimeout(300*time.Millisecond))
			start := time.Now()
			ok, msg := d.TestChannel(ch.ID)
			elapsed := time.Since(start)
			if ok {
				t.Fatalf("挂起服务器应投递失败, msg=%q", msg)
			}
			if elapsed >= 5*time.Second {
				t.Fatalf("挂起服务器必须在 5s 内返回（deadline=300ms）, 耗时 %v", elapsed)
			}
			if elapsed >= 3*time.Second {
				t.Fatalf("注入 300ms deadline 应远快于上限返回, 耗时 %v", elapsed)
			}
		})
	}
}

// ---------- 载荷格式（P4③ 字段表回归） ----------

// TestWebhookPayloadFieldTable raw 载荷字段齐全性与缺失字段省略。
func TestWebhookPayloadFieldTable(t *testing.T) {
	h := newHookServer(t, 0)
	s := newTestStore(t)
	mustCreate(t, s, TypeWebhook, "全量", nil, map[string]string{"url": h.srv.URL})
	d, _ := newTestDispatcher(t, s)
	d.TaskFinal(testTask(protocol.StatusCompleted, &protocol.TaskResult{
		Status:      protocol.ResultCompleted,
		ExitCode:    0,
		FinalAnswer: "done",
	}))
	d.WaitIdle()

	var p map[string]any
	if err := json.Unmarshal([]byte(h.lastBody()), &p); err != nil {
		t.Fatalf("载荷应为合法 JSON: %v", err)
	}
	want := map[string]any{
		"event":        "task_final",
		"task_id":      "t-abc123",
		"status":       "completed",
		"node_id":      "mac-mini",
		"agent":        "coder",
		"submitted_by": "lun-guo",
		"duration_sec": float64(200), // 3m20s
		"summary":      "done",
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("字段 %s = %v, want %v", k, p[k], v)
		}
	}
	for _, absent := range []string{"error"} { // 缺失字段必须省略而非空串
		if _, ok := p[absent]; ok {
			t.Errorf("缺失字段 %s 不应出现", absent)
		}
	}
}

// TestWebhookPayloadSummaryTruncation summary 取 FinalAnswer 前 200 字符（rune 计）。
func TestWebhookPayloadSummaryTruncation(t *testing.T) {
	h := newHookServer(t, 0)
	s := newTestStore(t)
	mustCreate(t, s, TypeWebhook, "截断", nil, map[string]string{"url": h.srv.URL})
	d, _ := newTestDispatcher(t, s)
	long := ""
	for i := 0; i < 300; i++ {
		long += "汉"
	}
	d.TaskFinal(testTask(protocol.StatusCompleted, &protocol.TaskResult{FinalAnswer: long}))
	d.WaitIdle()

	var p struct{ Summary string }
	if err := json.Unmarshal([]byte(h.lastBody()), &p); err != nil {
		t.Fatalf("载荷解析: %v", err)
	}
	if n := len([]rune(p.Summary)); n != summaryMaxLen {
		t.Fatalf("summary 应截断到 %d 字符, got %d", summaryMaxLen, n)
	}
}

// TestFeishuPayloadFourStatuses 飞书文本消息：四终态人话文案。
func TestFeishuPayloadFourStatuses(t *testing.T) {
	cases := []struct {
		name   string
		status protocol.TaskStatus
		result *protocol.TaskResult
		want   string
	}{
		{"completed", protocol.StatusCompleted, &protocol.TaskResult{Status: protocol.ResultCompleted},
			"✅ 任务完成 t-abc123 · mac-mini/coder · 耗时 3m20s"},
		{"failed", protocol.StatusFailed, &protocol.TaskResult{Status: protocol.ResultFailed, Error: protocol.CodeTimeout},
			"❌ 任务失败 t-abc123 · mac-mini/coder · 耗时 3m20s（原因：timeout）"},
		{"needs_review", protocol.StatusNeedsReview, &protocol.TaskResult{Status: protocol.ResultNeedsReview},
			"⚠️ 任务结果拿不准，等你确认 t-abc123 · mac-mini/coder · 耗时 3m20s"},
		{"cancelled", protocol.StatusCancelled, nil,
			"🚫 任务已取消 t-abc123 · mac-mini/coder · 耗时 3m20s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHookServer(t, 0)
			s := newTestStore(t)
			mustCreate(t, s, TypeFeishu, "飞书", nil, map[string]string{"webhook_url": h.srv.URL})
			d, _ := newTestDispatcher(t, s)
			d.TaskFinal(testTask(tc.status, tc.result))
			d.WaitIdle()

			var m feishuMessage
			if err := json.Unmarshal([]byte(h.lastBody()), &m); err != nil {
				t.Fatalf("载荷解析: %v", err)
			}
			if m.MsgType != "text" {
				t.Errorf("msg_type = %q, want text", m.MsgType)
			}
			if m.Content.Text != tc.want {
				t.Errorf("文案 = %q, want %q", m.Content.Text, tc.want)
			}
		})
	}
}

// TestPerAttemptTimeout 单次请求超时按 Timeout 计：服务端拖过超时即判失败并重试。
func TestPerAttemptTimeout(t *testing.T) {
	h := newHookServer(t, 0)
	h.mu.Lock()
	h.sleep = 200 * time.Millisecond
	h.mu.Unlock()
	s := newTestStore(t)
	mustCreate(t, s, TypeWebhook, "慢", nil, map[string]string{"url": h.srv.URL})
	d, _ := newTestDispatcher(t, s)
	d.hc.Timeout = 20 * time.Millisecond
	d.TaskFinal(testTask(protocol.StatusCompleted, nil))
	d.WaitIdle()

	if got := h.count(); got != 1+len(shortBackoff) {
		t.Fatalf("超时应触发全部重试共 %d 次请求, got %d", 1+len(shortBackoff), got)
	}
}

// ---------- 渠道「发测试」按钮 ----------

// TestChannelTestButton 测试端点：成功渠道返回成功；失败渠道返回人话错误；
// 未知渠道报不存在；inbox 渠道写演示条目。
func TestChannelTestButton(t *testing.T) {
	h := newHookServer(t, 0)
	s := newTestStore(t)
	ok := mustCreate(t, s, TypeWebhook, "能通", nil, map[string]string{"url": h.srv.URL})
	bad := mustCreate(t, s, TypeWebhook, "不通", nil, map[string]string{"url": "http://127.0.0.1:1/nope"})
	ibCh := mustCreate(t, s, TypeInbox, "站内", nil, nil)
	d, ib := newTestDispatcher(t, s)

	if okK, msg := d.TestChannel(ok.ID); !okK || msg != "" {
		t.Errorf("可达渠道测试应成功, got %v %q", okK, msg)
	}
	if okK, msg := d.TestChannel(bad.ID); okK || msg == "" {
		t.Errorf("不可达渠道测试应失败并带原因, got %v %q", okK, msg)
	}
	if okK, _ := d.TestChannel("c-nonexist"); okK {
		t.Errorf("未知渠道测试应失败")
	}
	// 失败重试不参与：坏渠道只打一次（单次尝试语义）
	if got := h.count(); got != 1 {
		t.Errorf("测试按钮应单次尝试, got %d", got)
	}
	// inbox 渠道测试：写一条 Test 标记的演示通知
	if okK, msg := d.TestChannel(ibCh.ID); !okK || msg != "" {
		t.Fatalf("inbox 渠道测试应成功, got %v %q", okK, msg)
	}
	items, unread := ib.Recent(20)
	if unread != 1 || len(items) != 1 || !items[0].Test {
		t.Errorf("inbox 测试应写演示条目, got %+v unread=%d", items, unread)
	}
}
