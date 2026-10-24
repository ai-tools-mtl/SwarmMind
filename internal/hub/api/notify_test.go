package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mtl802/swarmmind/internal/hub/core"
	"github.com/mtl802/swarmmind/internal/notify"
	"github.com/mtl802/swarmmind/internal/protocol"
)

// notifyFixture 通知面测试基座：fixture + 接线 notify.Dispatcher（渠道存储落
// TempDir，测试可直读磁盘核对凭证保留语义）。
type notifyFixture struct {
	*fixture
	disp    *notify.Dispatcher
	dataDir string
}

func newNotifyFixture(t *testing.T) *notifyFixture {
	f := newFixture(t)
	dataDir := t.TempDir()
	channels, err := notify.OpenChannelStore(dataDir)
	if err != nil {
		t.Fatalf("打开渠道存储: %v", err)
	}
	disp := notify.NewDispatcher(channels, &notify.Inbox{})
	srv := httptest.NewServer(New(Config{
		Core: f.core, AdminToken: "test-admin-token", DataDir: dataDir, Notify: disp,
	}).Handler())
	t.Cleanup(srv.Close)
	f.srv = srv
	return &notifyFixture{fixture: f, disp: disp, dataDir: dataDir}
}

// storedChannelsFile 直读磁盘快照（核对脱敏底下的真实凭证保留语义）。
func (nf *notifyFixture) storedChannelsFile(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(nf.dataDir, "notify_channels.json"))
	if err != nil {
		t.Fatalf("读渠道快照: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("快照解析: %v", err)
	}
	return raw
}

// TestNotifyEndpointsAuthGate 通知端点全部走鉴权：未登录一律 401。
func TestNotifyEndpointsAuthGate(t *testing.T) {
	f := newNotifyFixture(t)
	hc := f.client(false)
	for _, tc := range [][2]string{
		{http.MethodGet, "/api/v1/notify/channels"},
		{http.MethodPost, "/api/v1/notify/channels"},
		{http.MethodPut, "/api/v1/notify/channels/c-x"},
		{http.MethodDelete, "/api/v1/notify/channels/c-x"},
		{http.MethodPost, "/api/v1/notify/channels/c-x/test"},
		{http.MethodGet, "/api/v1/notify/inbox"},
		{http.MethodPost, "/api/v1/notify/inbox/read"},
	} {
		if code, _ := f.do(hc, tc[0], tc[1], `{}`); code != http.StatusUnauthorized {
			t.Errorf("%s %s 未登录应 401, got %d", tc[0], tc[1], code)
		}
	}
}

// TestNotifyChannelCRUDAndMasking CRUD 全链 + 凭证脱敏：创建请求里的明文
// password 在响应/列表中恒为 ******；快照磁盘上保留明文（投递要用）。
func TestNotifyChannelCRUDAndMasking(t *testing.T) {
	f := newNotifyFixture(t)
	hc := f.client(true)

	// 创建 email 渠道
	code, body := f.do(hc, http.MethodPost, "/api/v1/notify/channels", `{
		"type":"email","name":"告警邮箱",
		"events":["task_failed"],
		"secrets":{"host":"smtp.x.com","port":"465","username":"u","password":"real-pass","from":"a@x.com","to":"b@x.com"}
	}`)
	if code != http.StatusCreated {
		t.Fatalf("创建应 201, got %d: %s", code, body)
	}
	var created struct {
		Channel struct {
			ID      string            `json:"id"`
			Type    string            `json:"type"`
			Name    string            `json:"name"`
			Enabled bool              `json:"enabled"`
			Events  []string          `json:"events"`
			Secrets map[string]string `json:"secrets"`
		} `json:"channel"`
	}
	// 响应体是渠道对象本身
	if err := json.Unmarshal(body, &created.Channel); err != nil {
		t.Fatalf("响应解析: %v", err)
	}
	ch := created.Channel
	if !strings.HasPrefix(ch.ID, "c-") || !ch.Enabled || ch.Name != "告警邮箱" {
		t.Fatalf("创建响应形态不符: %+v", ch)
	}
	if len(ch.Events) != 1 || ch.Events[0] != "task_failed" {
		t.Errorf("events 应透传: %v", ch.Events)
	}
	if ch.Secrets["password"] != "******" {
		t.Errorf("响应 password 应脱敏, got %q", ch.Secrets["password"])
	}
	if ch.Secrets["username"] != "u" || ch.Secrets["host"] != "smtp.x.com" {
		t.Errorf("非敏感字段应明文: %v", ch.Secrets)
	}

	// 列表同样脱敏
	code, body = f.do(hc, http.MethodGet, "/api/v1/notify/channels", "")
	if code != 200 {
		t.Fatalf("列表应 200, got %d", code)
	}
	if !strings.Contains(string(body), `"******"`) || strings.Contains(string(body), "real-pass") {
		t.Errorf("列表应含占位且不含明文: %s", body)
	}

	// PUT：password 传 ****** → 磁盘原值保留；name 更新生效
	code, body = f.do(hc, http.MethodPut, "/api/v1/notify/channels/"+ch.ID,
		`{"name":"告警邮箱2","secrets":{"password":"******"}}`)
	if code != 200 {
		t.Fatalf("更新应 200, got %d: %s", code, body)
	}
	if strings.Contains(string(body), "real-pass") {
		t.Error("更新响应不得回显明文凭证")
	}
	disk := f.storedChannelsFile(t)
	chans := disk["channels"].([]any)
	first := chans[0].(map[string]any)
	secrets := first["secrets"].(map[string]any)
	if secrets["password"] != "real-pass" {
		t.Errorf("****** 应保留磁盘原值, got %v", secrets["password"])
	}
	if first["name"] != "告警邮箱2" {
		t.Errorf("name 应更新, got %v", first["name"])
	}

	// 启用开关（部分更新）：只传 enabled
	code, _ = f.do(hc, http.MethodPut, "/api/v1/notify/channels/"+ch.ID, `{"enabled":false}`)
	if code != 200 {
		t.Fatalf("启用开关应 200, got %d", code)
	}
	disk = f.storedChannelsFile(t)
	first = disk["channels"].([]any)[0].(map[string]any)
	if first["enabled"] != false || first["secrets"].(map[string]any)["password"] != "real-pass" {
		t.Errorf("部分更新不得丢凭证: %v", first)
	}

	// DELETE
	code, _ = f.do(hc, http.MethodDelete, "/api/v1/notify/channels/"+ch.ID, "")
	if code != 200 {
		t.Fatalf("删除应 200, got %d", code)
	}
	code, _ = f.do(hc, http.MethodDelete, "/api/v1/notify/channels/"+ch.ID, "")
	if code != 404 {
		t.Fatalf("重复删除应 404, got %d", code)
	}
}

// TestNotifyChannelValidationErrors 校验失败路径：sms 占位拒绝、缺必填凭证、
// 非法事件，全部 400 且不落盘。
func TestNotifyChannelValidationErrors(t *testing.T) {
	f := newNotifyFixture(t)
	hc := f.client(true)
	cases := []string{
		`{"type":"sms","name":"短信"}`,
		`{"type":"feishu","name":"缺地址"}`,
		`{"type":"webhook","name":"坏地址","secrets":{"url":"not-a-url"}}`,
		`{"type":"inbox","name":"坏事件","events":["on_fire"]}`,
		`{"type":"email","name":"缺端口","secrets":{"host":"h","username":"u","password":"p","from":"a","to":"b"}}`,
		`{"name":"缺类型"}`,
	}
	for _, body := range cases {
		code, resp := f.do(hc, http.MethodPost, "/api/v1/notify/channels", body)
		if code != http.StatusBadRequest {
			t.Errorf("%s 应 400, got %d: %s", body, code, resp)
		}
	}
	code, resp := f.do(hc, http.MethodPost, "/api/v1/notify/channels", `{}`)
	if code != 400 || !strings.Contains(string(resp), "渠道名称必填") && !strings.Contains(string(resp), "未知渠道类型") {
		t.Logf("空体报错信息: %d %s", code, resp)
	}
	if n := len(f.storedChannelsFileOrEmpty(t)); n != 0 {
		t.Errorf("被拒绝的创建不得落盘, got %d", n)
	}
}

func (nf *notifyFixture) storedChannelsFileOrEmpty(t *testing.T) []any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(nf.dataDir, "notify_channels.json"))
	if err != nil {
		return nil
	}
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	chans, _ := raw["channels"].([]any)
	return chans
}

// TestNotifyWebhookHeaderMaskingAndDelete webhook 自定义头全链：创建/列表/编辑
// 三类读响应中头的值一律 ******（含 X-API-Key 等非 Authorization 名）；编辑
// 提交的 headers map 即权威——提交里消失的键真实删除，占位回传保留原值。
func TestNotifyWebhookHeaderMaskingAndDelete(t *testing.T) {
	f := newNotifyFixture(t)
	hc := f.client(true)

	// 创建 webhook 渠道：两个自定义头（X-API-Key 非敏感名也必须脱敏）
	code, body := f.do(hc, http.MethodPost, "/api/v1/notify/channels", `{
		"type":"webhook","name":"带头",
		"secrets":{"url":"https://hook/x","header.X-API-Key":"ak-123","header.X-Trace":"tr-9"}
	}`)
	if code != http.StatusCreated {
		t.Fatalf("创建应 201, got %d: %s", code, body)
	}
	if strings.Contains(string(body), "ak-123") || strings.Contains(string(body), "tr-9") {
		t.Errorf("创建响应头的值应一律脱敏: %s", body)
	}
	if !strings.Contains(string(body), "******") {
		t.Errorf("创建响应应含占位: %s", body)
	}
	id := gjsonGet(t, body, "id")

	// 列表：明文头值不得出现
	code, body = f.do(hc, http.MethodGet, "/api/v1/notify/channels", "")
	if code != 200 || strings.Contains(string(body), "ak-123") || strings.Contains(string(body), "tr-9") {
		t.Fatalf("列表响应不得含明文头值: %d %s", code, body)
	}

	// 编辑：X-Trace 行删除（提交 map 只剩 url+X-API-Key 占位）→ X-Trace 真实删除、
	// X-API-Key 原值保留
	code, body = f.do(hc, http.MethodPut, "/api/v1/notify/channels/"+id,
		`{"secrets":{"url":"https://hook/x","header.X-API-Key":"******"}}`)
	if code != 200 {
		t.Fatalf("编辑应 200, got %d: %s", code, body)
	}
	secrets := f.storedChannelsFile(t)["channels"].([]any)[0].(map[string]any)["secrets"].(map[string]any)
	if _, ok := secrets["header.X-Trace"]; ok {
		t.Errorf("提交里消失的头应删除: %v", secrets)
	}
	if secrets["header.X-API-Key"] != "ak-123" {
		t.Errorf("占位回传应保留原值, got %v", secrets["header.X-API-Key"])
	}

	// 启用开关（secrets 缺省）→ 头不受「提交即权威」影响
	code, _ = f.do(hc, http.MethodPut, "/api/v1/notify/channels/"+id, `{"enabled":false}`)
	if code != 200 {
		t.Fatalf("启用开关应 200, got %d", code)
	}
	secrets = f.storedChannelsFile(t)["channels"].([]any)[0].(map[string]any)["secrets"].(map[string]any)
	if secrets["header.X-API-Key"] != "ak-123" {
		t.Errorf("secrets 缺省不得碰任何凭证: %v", secrets)
	}
}

// TestNotifyChannelTestEndpoint 测试端点：可达 webhook 返回 ok=true；不可达
// 返回 ok=false 带原因（同为 200）；未知渠道 404。
func TestNotifyChannelTestEndpoint(t *testing.T) {
	f := newNotifyFixture(t)
	hc := f.client(true)

	hit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hit.Close)

	code, body := f.do(hc, http.MethodPost, "/api/v1/notify/channels", `{"type":"webhook","name":"能通","secrets":{"url":"`+hit.URL+`"}}`)
	ok1 := gjsonGet(t, body, "id")
	code, body = f.do(hc, http.MethodPost, "/api/v1/notify/channels/"+ok1+"/test", "")
	if code != 200 || !strings.Contains(string(body), `"ok":true`) {
		t.Errorf("可达渠道测试应 ok=true, got %d %s", code, body)
	}

	code, body = f.do(hc, http.MethodPost, "/api/v1/notify/channels", `{"type":"webhook","name":"不通","secrets":{"url":"http://127.0.0.1:1/nope"}}`)
	bad1 := gjsonGet(t, body, "id")
	code, body = f.do(hc, http.MethodPost, "/api/v1/notify/channels/"+bad1+"/test", "")
	if code != 200 || !strings.Contains(string(body), `"ok":false`) || !strings.Contains(string(body), "error") {
		t.Errorf("不可达渠道测试应 ok=false 带原因, got %d %s", code, body)
	}

	code, body = f.do(hc, http.MethodPost, "/api/v1/notify/channels/c-nonexist/test", "")
	if code != 404 {
		t.Errorf("未知渠道测试应 404, got %d %s", code, body)
	}
}

// TestNotifyInboxEndpoints 站内通知端点：终态事件驱动（Dispatcher.TaskFinal）
// → GET 可见未读 → 标单条已读 → 未读清零；空体=全部标已读；unknown id 404。
func TestNotifyInboxEndpoints(t *testing.T) {
	f := newNotifyFixture(t)
	hc := f.client(true)

	// 建一个 inbox 渠道（走 HTTP 创建链，验证端到端）
	code, body := f.do(hc, http.MethodPost, "/api/v1/notify/channels", `{"type":"inbox","name":"站内"}`)
	if code != 201 {
		t.Fatalf("创建 inbox 渠道应 201, got %d %s", code, body)
	}

	// GET 空态
	code, body = f.do(hc, http.MethodGet, "/api/v1/notify/inbox", "")
	if code != 200 || !strings.Contains(string(body), `"unread":0`) || !strings.Contains(string(body), `"persistent":false`) {
		t.Fatalf("空站内应 200 未读 0 且标注非持久, got %d %s", code, body)
	}

	// 终态事件 → 站内通知
	f.disp.TaskFinal(protocol.Task{
		ID: "t-inbox1", Status: protocol.StatusCompleted, TargetNode: "mac-mini",
		Result: &protocol.TaskResult{FinalAnswer: "好了"},
	})
	f.disp.WaitIdle()
	code, body = f.do(hc, http.MethodGet, "/api/v1/notify/inbox", "")
	if !strings.Contains(string(body), `"unread":1`) || !strings.Contains(string(body), `"task_id":"t-inbox1"`) {
		t.Fatalf("终态事件应产生未读站内通知: %s", body)
	}
	var got struct {
		Notifications []struct {
			ID string `json:"id"`
		} `json:"notifications"`
	}
	_ = json.Unmarshal([]byte(body), &got)
	id := got.Notifications[0].ID

	// 标单条已读
	code, body = f.do(hc, http.MethodPost, "/api/v1/notify/inbox/read", `{"id":"`+id+`"}`)
	if code != 200 || !strings.Contains(string(body), `"marked":1`) {
		t.Fatalf("标已读应 marked=1, got %d %s", code, body)
	}
	code, body = f.do(hc, http.MethodGet, "/api/v1/notify/inbox", "")
	if !strings.Contains(string(body), `"unread":0`) {
		t.Fatalf("标已读后未读应清零: %s", body)
	}

	// 再来两条 → 空体全读
	f.disp.TaskFinal(protocol.Task{ID: "t-inbox2", Status: protocol.StatusFailed})
	f.disp.TaskFinal(protocol.Task{ID: "t-inbox3", Status: protocol.StatusNeedsReview})
	f.disp.WaitIdle()
	code, body = f.do(hc, http.MethodPost, "/api/v1/notify/inbox/read", "")
	if code != 200 || !strings.Contains(string(body), `"marked":2`) {
		t.Fatalf("全部标已读应 marked=2, got %d %s", code, body)
	}

	// unknown id 404
	code, _ = f.do(hc, http.MethodPost, "/api/v1/notify/inbox/read", `{"id":"n-nope"}`)
	if code != 404 {
		t.Errorf("标记不存在的通知应 404, got %d", code)
	}
}

// TestNotifyEndpoints503WhenUnwired Dispatcher 未接线（旧构造方）→ 503 降级。
func TestNotifyEndpoints503WhenUnwired(t *testing.T) {
	f := newFixture(t) // 不带 Notify
	hc := f.client(true)
	for _, tc := range [][2]string{
		{http.MethodGet, "/api/v1/notify/channels"},
		{http.MethodPost, "/api/v1/notify/channels/c-x/test"},
		{http.MethodGet, "/api/v1/notify/inbox"},
	} {
		if code, _ := f.do(hc, tc[0], tc[1], ""); code != http.StatusServiceUnavailable {
			t.Errorf("%s %s 未接线应 503, got %d", tc[0], tc[1], code)
		}
	}
}

// gjsonGet 从单层 JSON 对象取字符串字段（测试小工具，不引依赖）。
func gjsonGet(t *testing.T, body []byte, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("解析 %s: %v", body, err)
	}
	v, _ := m[key].(string)
	return v
}

// 编译期防呆：core.Notifier 接口由 Dispatcher 实现（装配方口径回归锚）。
var _ core.TerminalNotifier = (*notify.Dispatcher)(nil)
